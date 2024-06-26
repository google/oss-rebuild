package firestoretest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/pkg/errors"
)

func getPort() (*int, error) {
	var localhost [net.IPv6len]byte
	localhost[len(localhost)-1]++ // ::1
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP(localhost[:]), Port: 0})
	if err != nil {
		return nil, err
	}
	defer l.Close()
	return &l.Addr().(*net.TCPAddr).Port, nil
}

func waitForCmd(cmd *exec.Cmd) <-chan struct{} {
	cmdDone := make(chan struct{})
	go func() {
		cmd.Wait()
		close(cmdDone)
	}()
	return cmdDone
}

func StartEmulator(ctx context.Context, t *testing.T) <-chan error {
	t.Helper()
	port, err := getPort()
	if err != nil {
		t.Fatalf("getPort(): %v", err)
	}
	addr := fmt.Sprintf("localhost:%d", *port)
	t.Logf("starting firestore emulator... addr=%s", addr)

	cmd := exec.Command("gcloud", "emulators", "firestore", "start", "--host-port="+addr)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failure starting firestore emulator: %v", err)
	}

	if err := os.Setenv("FIRESTORE_EMULATOR_HOST", addr); err != nil {
		t.Fatalf("os.SetEnv(): %v", err)
	}

	result := make(chan error, 1)
	go func() {
		portReachable := make(chan struct{})
		go func() {
			for {
				c, err := net.DialTCP("tcp", nil, &net.TCPAddr{Port: *port})
				if err == nil {
					c.Close()
					close(portReachable)
					return
				}
				select {
				case <-time.After(300 * time.Millisecond):
					continue
				case <-ctx.Done():
					return
				}
			}
		}()
		select {
		case <-portReachable:
			t.Log("Firestore emulator is ready")
			result <- nil
		case <-waitForCmd(cmd):
			t.Log("Firestore emulator failed to start")
			result <- errors.Wrap(errors.New(cmd.ProcessState.String()), "firestore emulator exited")
		case <-ctx.Done():
			t.Log("Firestore emulator startup preempted by context")
			result <- ctx.Err()
		}
	}()
	t.Cleanup(func() {
		t.Log("Shutting down firestore emulator...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://%s/shutdown", addr), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("Error sending shutdown request: %v", err)
		} else {
			resp.Body.Close()
			t.Log("Shutdown request sent successfully")
		}
		select {
		case <-waitForCmd(cmd):
			t.Log("Firestore emulator shut down successfully")
		case <-time.After(5 * time.Second):
			t.Log("Timeout waiting for emulator to shut down, forcing termination")
			cmd.Process.Kill()
		}
	})
	return result
}
