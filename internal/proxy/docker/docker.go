// Package docker defines a proxy for the Docker API.
//
// Summary: Change the internal container state transparently while providing
// an otherwise unmodified view from the external API.
//
// patch for:
//   - /start
//   - /restart
//   - /unpause
//
// unpatch for:
//
//   - /export
//   - /commit
//   - /changes  // TODO: Unimplemented
//   - /archive  // TODO: Unimplemented
//
// ignore:
//
//   - RestartPolicy -> no need to re-patch UNLESS if fail+restart during unpatch
//   - /exec/start -> no need to re-patch since container must already be started
package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	re "regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/oss-rebuild/internal/proxy/certfmt"
	"github.com/google/oss-rebuild/internal/proxy/dockerfs"
	"github.com/pkg/errors"
)

const (
	// Path at which the proxy cert is created in the container execution
	// environment.
	// NOTE: /var/cache is chosen since it exists by default on almost every
	// distro and since it, by its semantics, can be emptied without having a
	// functional impact on running applications.
	proxyCertPath    = "/var/cache/proxy.crt"
	proxyCertJKSPath = "/var/cache/proxy.crt.jks"
	// Official interface for providing additional args to JVMs.
	javaEnvVar = "JAVA_TOOL_OPTIONS"
	// Env var to which docker requests will be sent by the docker CLI.
	dockerEnvVar = "DOCKER_HOST"
	// The path to the docker proxy that can be bound within a container to make docker calls.
	proxySocketPath = "/var/cache/proxy.sock"
)

func readChunks(r io.Reader) chan []byte {
	c := make(chan []byte, 2)
	go func() {
		b := make([]byte, 1024)
		for {
			n, err := r.Read(b)
			if n > 0 {
				cpy := make([]byte, n)
				copy(cpy, b[:n])
				c <- cpy
			}
			if err != nil {
				c <- nil
				break
			}
		}
	}()
	return c
}

type containerNotFoundError struct{ string }

func (e containerNotFoundError) Error() string {
	return "No such container: " + e.string
}

func resolveContainerID(c *UDSHTTPClient, id string) (string, error) {
	log.Printf("Resolving container ID: %s", id)
	resp, err := c.Get("/containers/" + id + "/json")
	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", containerNotFoundError{id}
	case http.StatusInternalServerError:
		log.Printf("Failed to resolve ID for %s: %s", id, err)
		return "", errors.New("Internal server error")
	default:
		defer resp.Body.Close()
		container := struct {
			ID string `json:"Id"`
		}{}
		d := json.NewDecoder(resp.Body)
		if err := d.Decode(&container); err != nil {
			return "", errors.New("Failed to parse container")
		}
		return container.ID, nil
	}
}

func truststoreCertPatch(fs dockerfs.Filesystem, container string, cert []byte) (*patch, error) {
	truststore, err := locateTruststore(&fs)
	if err != nil {
		return nil, errors.New("Unable to locate truststore for " + container + ": " + err.Error())
	}
	old := *truststore
	truststore.Contents = append(truststore.Contents[:], cert...)
	return newPatch(&old, truststore)
}

func createFile(fs dockerfs.Filesystem, container string, content []byte, path string) error {
	_, err := fs.Stat(path)
	if err != os.ErrNotExist {
		return errors.New("file already exists")
	}
	name := filepath.Base(path)
	hdr, err := tar.FileInfoHeader(dockerfs.NewFileInfo(name, int64(len(content)), os.FileMode(0664), time.Now(), ""), "")
	if err != nil {
		return err
	}
	f := dockerfs.File{Path: path, Metadata: *hdr, Contents: content}
	return fs.WriteFile(&f)
}

func addBinding(imageSpec []byte, from, to, mode string) (newSpec []byte, err error) {
	img := make(map[string]any)
	if err = json.Unmarshal(imageSpec, &img); err != nil {
		err = errors.Errorf("failed to unmarshal json: %s\nBody: %s", err, string(imageSpec))
		return
	}
	hostConfig, ok := img["HostConfig"].(map[string]any)
	if !ok {
		err = errors.Errorf("unexpected type of HostConfig\nBody: %s", string(imageSpec))
		return
	}
	newBinding := strings.Join([]string{from, to, mode}, ":")
	bindsObj, ok := hostConfig["Binds"]
	if !ok || bindsObj == nil {
		hostConfig["Binds"] = []any{newBinding}
	} else {
		binds, ok := bindsObj.([]any)
		if !ok {
			err = errors.Errorf("unexpected type of HostConfig.Binds\nBody: %s", string(imageSpec))
			return
		}
		hostConfig["Binds"] = append(binds, newBinding)
	}
	newSpec, err = json.Marshal(img)
	if err != nil {
		err = errors.Errorf("failed to re-marshal json: %s\nStruct: %s", err, img)
		return
	}
	return
}

func getEnvVar(imageSpec []byte, avar string) (val string, err error) {
	img := make(map[string]any)
	if err = json.Unmarshal(imageSpec, &img); err != nil {
		err = errors.Errorf("failed to unmarshal json: %s\nBody: %s", err, string(imageSpec))
		return
	}
	raw, ok := img["Env"]
	var envs []any
	if ok && raw != nil {
		envs, ok = img["Env"].([]any)
		if !ok {
			err = errors.Errorf("unexpected type of Env\nBody: %s", string(imageSpec))
			return
		}
	}
	// NOTE: Last one wins!
	var found bool
	for _, env := range envs {
		if e, ok := env.(string); ok && strings.HasPrefix(e, avar+"=") {
			val = strings.TrimPrefix(e, avar+"=")
			found = true
		}
	}
	if !found {
		err = os.ErrNotExist
	}
	return
}

func addEnvVars(imageSpec []byte, newVars []string) (newSpec []byte, err error) {
	img := make(map[string]any)
	if err = json.Unmarshal(imageSpec, &img); err != nil {
		err = errors.Errorf("failed to unmarshal json: %s\nBody: %s", err, string(imageSpec))
		return
	}
	raw, ok := img["Env"]
	var envs []any
	if ok && raw != nil {
		envs, ok = img["Env"].([]any)
		if !ok {
			err = errors.Errorf("unexpected type of Env\nBody: %s", string(imageSpec))
			return
		}
	}
	var patched []any
	for _, newVar := range newVars {
		patched = append(patched, newVar)
	}
	envs = append(envs, patched...)
	img["Env"] = envs
	newSpec, err = json.Marshal(img)
	if err != nil {
		err = errors.Errorf("failed to re-marshal json: %s\nStruct: %s", err, img)
		return
	}
	return
}

func removeEnvVars(imageSpec []byte, varNames []string) (newSpec []byte, err error) {
	img := make(map[string]any)
	if err = json.Unmarshal(imageSpec, &img); err != nil {
		err = errors.Errorf("failed to unmarshal json: %s\nBody: %s", err, string(imageSpec))
		return
	}
	raw, ok := img["Env"]
	if !ok || raw == nil {
		newSpec = imageSpec
		return
	}
	envs, ok := img["Env"].([]any)
	if !ok {
		err = errors.Errorf("unexpected type of Env\nBody: %s", string(imageSpec))
		return
	}
	stripped := make([]string, 0)
	for i := range envs {
		env, ok := envs[len(envs)-i-1].(string)
		if !ok {
			err = errors.Errorf("unexpected type of Env #%d\nBody: %s", i, string(imageSpec))
			return
		}
		var j int
		var varName string
		for j, varName = range varNames {
			if strings.HasPrefix(env, varName+"=") {
				goto skip
			}
		}
		stripped = append(stripped, env)
		continue
	skip:
		varNames[j] = varNames[len(varNames)-1]
		varNames = varNames[:len(varNames)-1]
	}
	// Reverse list.
	for i := range stripped[:(len(stripped)+1)/2] {
		stripped[i], stripped[len(stripped)-i-1] = stripped[len(stripped)-i-1], stripped[i]
	}
	img["Env"] = stripped
	newSpec, err = json.Marshal(img)
	if err != nil {
		err = errors.Errorf("failed to re-marshal json: %s\nStruct: %s", err, img)
		return
	}
	return
}

func trimQuotes(in string) string {
	for len(in) > 1 && in[0] == in[len(in)-1] && (in[0] == '\'' || in[0] == '"') {
		in = in[1 : len(in)-1]
	}
	return in
}

type actionType int

const (
	noAction actionType = iota
	patchEnvVarsDuring
	patchTruststoreBefore
	unpatchTruststoreDuring
	unpatchTruststoreAndEnvVarsDuring
)

var (
	containerCreatePattern  = re.MustCompile(`(/v[^/]+)?/containers/create`)
	containerStartPattern   = re.MustCompile(`(/v[^/]+)?/containers/([^/]+)/start`)
	containerExportPattern  = re.MustCompile(`(/v[^/]+)?/containers/([^/]+)/export`)
	containerRestartPattern = re.MustCompile(`(/v[^/]+)?/containers/([^/]+)/restart`)
	containerUnpausePattern = re.MustCompile(`(/v[^/]+)?/containers/([^/]+)/unpause`)
	commitPattern           = re.MustCompile(`(/v[^/]+)?/commit`)
)

// getActionType determines the actionType that should be taken along with the container ID on which the action should be taken.
func getActionType(req *http.Request) (actionType, string) {
	path := req.URL.Path
	switch {
	case containerCreatePattern.MatchString(path): // URI: /containers/create
		return patchEnvVarsDuring, ""
	case containerStartPattern.MatchString(path): // URI: /containers/<id>/start
		return patchTruststoreBefore, containerStartPattern.FindStringSubmatch(path)[2]
	case containerRestartPattern.MatchString(path): // URI: /containers/<id>/restart
		return patchTruststoreBefore, containerRestartPattern.FindStringSubmatch(path)[2]
	case containerUnpausePattern.MatchString(path): // URI: /containers/<id>/unpause
		return patchTruststoreBefore, containerUnpausePattern.FindStringSubmatch(path)[2]
	case containerExportPattern.MatchString(path): // URI: /containers/<id>/export
		return unpatchTruststoreDuring, containerExportPattern.FindStringSubmatch(path)[2]
	case commitPattern.MatchString(path): // URI: /commit?container=<id>
		return unpatchTruststoreAndEnvVarsDuring, req.URL.Query().Get("container")
	default:
		return noAction, ""
	}
}

var (
	httpInternalServerErrorResponse = []byte("HTTP/1.1 500 Internal Server Error\r\n\r\n")
	httpNotFoundResponse            = []byte("HTTP/1.1 404 Not Found\r\n\r\n")
	nullJSONBody                    = []byte("null\n")
)

type patch struct {
	Before *dockerfs.File
	After  *dockerfs.File
}

func newPatch(before, after *dockerfs.File) (*patch, error) {
	switch {
	case before == nil:
		// TODO: Implement file add.
		return nil, errors.New("file addition is unsupported")
	case after == nil:
		// TODO: Implement file delete.
		return nil, errors.New("file deletion is unsupported")
	case before.Metadata.Typeflag != after.Metadata.Typeflag:
		return nil, errors.New("file type modification is unsupported")
	}
	p := patch{before, after}
	if !filepath.IsAbs(*p.Path()) {
		return nil, errors.New("patched path must be absolute")
	}
	return &p, nil
}

// Apply writes the After file to the target container.
func (p patch) Apply(fs *dockerfs.Filesystem) error {
	return fs.WriteFile(p.After)
}

// Revert writes the Before file to the target container, validating After is the current state.
func (p patch) Revert(fs *dockerfs.Filesystem) error {
	f, err := fs.Open(*p.Path())
	if err != nil {
		return err
	}
	// TODO: Either remove and support smart rollback or make check more robust.
	if !bytes.Equal(f.Contents, p.After.Contents) {
		return errors.New("out of band change to patched file: " + *p.Path())
	}
	if err := fs.WriteFile(p.Before); err != nil {
		return err
	}
	return nil
}

// Path returns the file path associated with patch.
func (p patch) Path() *string {
	if p.Before == nil {
		return &p.After.Path
	}
	return &p.Before.Path
}

type patchSet struct {
	*sync.Mutex
	Patches []patch
}

// ContainerTruststorePatcher provides a Docker API proxy that patches the container truststore while running.
type ContainerTruststorePatcher struct {
	Cert        x509.Certificate
	EnvVars     []string
	JavaEnvVar  bool
	proxySocket string
	patchMap    map[string]*patchSet
	m           sync.Mutex
	created     atomic.Uint32
}

// NewContainerTruststorePatcher creates a new ContainerTruststorePatcher for the provided root certificate.
func NewContainerTruststorePatcher(cert x509.Certificate, truststoreEnvVars []string, javaEnvVar bool, recursiveProxy bool) *ContainerTruststorePatcher {
	var sockName string
	if recursiveProxy {
		file, err := os.CreateTemp("/tmp", "proxy-*.sock")
		if err != nil {
			log.Fatalf("failed to create temp file: %s", err)
		}
		sockName = file.Name()
		if err := os.Remove(file.Name()); err != nil {
			log.Fatalf("failed to configure temp file: %s", err)
		}
	}
	return &ContainerTruststorePatcher{Cert: cert, EnvVars: truststoreEnvVars, JavaEnvVar: javaEnvVar, proxySocket: sockName, patchMap: make(map[string]*patchSet)}
}

// leasePatchSet locks and returns the provided container's patchSet.
// NOTE: The patchSet is returned locked and it is the responsibility of the
// caller to unlock it when complete.
func (d *ContainerTruststorePatcher) leasePatchSet(container string) *patchSet {
	d.m.Lock()
	defer d.m.Unlock()
	p, ok := d.patchMap[container]
	if !ok {
		p = new(patchSet)
		p.Mutex = new(sync.Mutex)
		d.patchMap[container] = p
	}
	p.Lock()
	return p
}

// Proxy serves the Docker API while patching the container truststore.
func (d *ContainerTruststorePatcher) Proxy(srvAddr, dockerAddr string) {
	tcpChan := make(chan net.Conn, 1)
	udsChan := make(chan net.Conn, 1)
	tl, err := net.Listen("tcp", srvAddr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				log.Printf("Failed to establish connection: %s", err)
				continue
			}
			tcpChan <- c
		}
	}()
	if d.proxySocket != "" {
		ul, err := net.Listen("unix", d.proxySocket)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.Chmod(ul.Addr().String(), fs.ModeSocket|0660); err != nil {
			log.Fatal(err)
		}
		go func() {
			for {
				c, err := ul.Accept()
				if err != nil {
					log.Printf("Failed to establish connection: %s", err)
					continue
				}
				udsChan <- c
			}
		}()
	}
	for {
		var c net.Conn
		select {
		case c = <-tcpChan:
		case c = <-udsChan:
		}
		s, err := net.Dial("unix", dockerAddr)
		if err != nil {
			log.Printf("Failed to establish connection: %s", err)
			continue
		}
		go d.proxyRequest(c, s)
	}
}

func (d *ContainerTruststorePatcher) proxyRequest(clientConn, serverConn net.Conn) {
	defer clientConn.Close()
	defer serverConn.Close()
	// Read request from client.
	req, err := http.ReadRequest(bufio.NewReader(clientConn))
	if err != nil {
		log.Printf("Failed to read client request from %s: %s", clientConn.RemoteAddr(), err)
		return
	}
	log.Printf("Processing request: %s %s", req.Method, req.URL.RequestURI())
	serverClient := NewUDSHTTPClient(serverConn.RemoteAddr().String())
	action, id := getActionType(req)
	switch action {
	case patchEnvVarsDuring:
		body, err := io.ReadAll(req.Body)
		if err != nil {
			log.Fatalf("Failed to read body for request %s: %s", req.URL.Path, err)
		}
		newBody := body
		// NOTE: This binding overlaps with the cert file we write at start time.
		// Due to Docker's fs layering behavior, this allows us to ensure export
		// and commit operations on the container won't pick up any new files or
		// directories written to the dir during its execution.
		volName := fmt.Sprintf("proxy-vol%d", d.created.Add(1))
		newBody, err = addBinding(newBody, volName, filepath.Dir(proxyCertPath), "rw")
		if err != nil {
			log.Fatalf("Failed to add volume for request %s: %s", req.URL.Path, err)
		}
		var vars []string
		for _, v := range d.EnvVars {
			vars = append(vars, v+"="+proxyCertPath)
		}
		if d.JavaEnvVar {
			// NOTE: Since other user-provided values can be set in JAVA_TOOL_OPTIONS,
			// we merge the proxy-specific arg into the existing value, if present.
			val, err := getEnvVar(newBody, javaEnvVar)
			if err != nil && !os.IsNotExist(err) {
				log.Fatalf("Failed to get env var for request %s: %s", req.URL.Path, err)
			}
			newVal := val
			if val != "" {
				newVal = trimQuotes(val) + " "
			}
			newVal += "-Djavax.net.ssl.trustStore=" + proxyCertJKSPath
			vars = append(vars, javaEnvVar+"="+newVal)
			log.Printf("Updated %s [old=%s, new=%s]", javaEnvVar, val, newVal)
		}
		if d.proxySocket != "" {
			newBody, err = addBinding(newBody, d.proxySocket, proxySocketPath, "rw")
			if err != nil {
				log.Fatalf("failed to add docker bindings for request %s: %s", req.URL.Path, err)
			}
			vars = append(vars, dockerEnvVar+"=unix://"+proxySocketPath)
			log.Printf("Bound %s to %s", d.proxySocket, proxySocketPath)
		}
		newBody, err = addEnvVars(newBody, vars)
		if err != nil {
			log.Fatalf("Failed to add env vars for request %s: %s", req.URL.Path, err)
		}
		req.ContentLength = int64(len(newBody))
		req.Body = io.NopCloser(bytes.NewReader(newBody))
	case patchTruststoreBefore:
		id, err = resolveContainerID(serverClient, id)
		if err != nil {
			log.Printf("Unable to resolve container ID: %s", err)
			switch err.(type) {
			case containerNotFoundError:
				clientConn.Write(httpNotFoundResponse)
			default:
				clientConn.Write(httpInternalServerErrorResponse)
			}
			return
		}
		fs := dockerfs.Filesystem{Client: serverClient, Container: id}
		certBytes := certfmt.ToPEM(&d.Cert)
		// NOTE: This doesn't need to be cleaned up due to the enclosing volume
		// binding made at creation time.
		if err := createFile(fs, id, certBytes, proxyCertPath); err != nil {
			log.Println(err.Error())
			break
		}
		if d.JavaEnvVar {
			jks, err := certfmt.ToJKS(&d.Cert)
			if err != nil {
				log.Println(err.Error())
				break
			}
			if err := createFile(fs, id, jks, proxyCertJKSPath); err != nil {
				log.Println(err.Error())
				break
			}
		}
		patchset := d.leasePatchSet(id)
		if len(patchset.Patches) > 0 {
			log.Println("Active patches applied for " + id)
			patchset.Unlock()
			break
		}
		truststorePatch, err := truststoreCertPatch(fs, id, certBytes)
		if err != nil {
			log.Println(err.Error())
			patchset.Unlock()
			break
		}
		if err := truststorePatch.Apply(&fs); err != nil {
			log.Println("Unable to apply patch for " + id + ": " + err.Error())
			patchset.Unlock()
			break
		}
		patchset.Patches = append(patchset.Patches, *truststorePatch)
		patchset.Unlock()
	case unpatchTruststoreAndEnvVarsDuring:
		body, err := io.ReadAll(req.Body)
		if err != nil {
			log.Fatalf("failed to read body for request %s: %s", req.URL.Path, err)
		}
		var otherVars []string
		if d.JavaEnvVar {
			otherVars = append(otherVars, javaEnvVar)
		}
		if d.proxySocket != "" {
			otherVars = append(otherVars, dockerEnvVar)
		}
		allVars := append(otherVars, d.EnvVars...)
		var newBody []byte
		if !bytes.Equal(body, nullJSONBody) {
			newBody, err = removeEnvVars(body, allVars)
			if err != nil {
				log.Fatalf("failed to remove env vars for request %s: %s", req.URL.Path, err)
			}
		} else {
			// With a null body, the docker daemon will access the container
			// specifications internally. As such, we need to substitute this out for
			// out own specification.
			origID := id
			resp, err := serverClient.Get("/containers/" + id + "/json")
			if err != nil {
				log.Fatalf("failed to fetch container spec for %s: %s", id, err)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Fatalf("failed to read body for request %s: %s", resp.Request.URL.Path, err)
			}
			resp.Body.Close()
			cnt := make(map[string]any)
			if err = json.Unmarshal(body, &cnt); err != nil {
				log.Fatalf("bad container spec for %s: %s", id, string(body))
			}
			cfg, err := json.Marshal(cnt["Config"])
			if err != nil {
				log.Fatalf("failed to marshal config for %s: %s\n%s", id, err, cnt["Config"])
			}
			newCfg, err := removeEnvVars(cfg, allVars)
			if err != nil {
				log.Fatalf("failed to remove env vars for request %s: %s", req.URL.Path, err)
			}
			resp, err = serverClient.Post("/commit?container="+origID, "application/json", bytes.NewReader(newCfg))
			if err != nil {
				log.Fatalf("failed to commit container for %s: %s\n%s", id, err, string(newCfg))
			}
			img := struct {
				ID string `json:"Id"`
			}{}
			d := json.NewDecoder(resp.Body)
			if err := d.Decode(&img); err != nil {
				log.Fatalf("failed to decode temporary container response for %s: %s", id, err)
			}
			resp.Body.Close()
			resp, err = serverClient.Post("/containers/create", "application/json", bytes.NewReader(append([]byte(`{"Image": "`+img.ID+`",`), newCfg[1:]...)))
			if err != nil {
				log.Fatalf("failed to create temporary container for %s: %s", id, err)
			}
			container := struct {
				ID string `json:"Id"`
			}{}
			d = json.NewDecoder(resp.Body)
			if err := d.Decode(&container); err != nil {
				log.Fatalf("failed to decode temporary container response for %s: %s", id, err)
			}
			resp.Body.Close()
			newID := container.ID
			req.URL.RawQuery = strings.Replace(req.URL.RawQuery, "container="+origID, "container="+newID, -1)
			newBody = nullJSONBody
		}
		req.ContentLength = int64(len(newBody))
		req.Body = io.NopCloser(bytes.NewReader(newBody))
		fallthrough
	case unpatchTruststoreDuring:
		id, err = resolveContainerID(serverClient, id)
		if err != nil {
			log.Printf("Unable to resolve container ID: %s", err)
			switch err.(type) {
			case containerNotFoundError:
				clientConn.Write(httpNotFoundResponse)
			default:
				clientConn.Write(httpInternalServerErrorResponse)
			}
			return
		}
		fs := dockerfs.Filesystem{Client: serverClient, Container: id}
		patchset := d.leasePatchSet(id)
		defer patchset.Unlock()
		if len(patchset.Patches) == 0 {
			break
		}
		// TODO: /pause the container here to ensure container operations don't see unpatched changes.
		for _, p := range patchset.Patches {
			if err := p.Revert(&fs); err != nil {
				// XXX: This is a really bad situation. If we can't revert the
				// patches applied, it's better we crash and try to ensure the
				// corrupted state doesn't go silently unreported.
				log.Fatalf("Failed to revert patches for %s: %s", id, err)
			}
		}
		// Re-apply the patches once the command completes.
		defer func() {
			var applied []int
			for i, p := range patchset.Patches {
				if err := p.Apply(&fs); err != nil {
					log.Printf("Failed to re-apply patches for %s: %s", id, err)
				} else {
					applied = append(applied, i)
				}
			}
			if len(applied) == 0 {
				patchset.Patches = patchset.Patches[:0]
			} else if len(applied) != len(patchset.Patches) {
				log.Printf("Attempting to recover from patch application failure for %s", id)
				for _, idx := range applied {
					p := patchset.Patches[idx]
					if err := p.Revert(&fs); err != nil {
						// Rollback failed. We're in an inconsistent state so crashing
						// is the safest option.
						log.Fatalf("Failed to recover from repatch failure for %s: %s", id, err)
					}
				}
				patchset.Patches = patchset.Patches[:0]
			}
		}()
	}
	// Round-trip request to API server and complete request with client..
	// TODO: Use uds HTTPClient once Upgrade works.
	if err := req.Write(serverConn); err != nil {
		log.Printf("Failed to write client request: %s", err)
		clientConn.Write(httpInternalServerErrorResponse)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(serverConn), req)
	if err != nil {
		log.Printf("Failed to read server response: %s", err)
		clientConn.Write(httpInternalServerErrorResponse)
		return
	}
	// PERF: Ideally we'd support keep-alive but unconditional closure avoids
	// needing to implement connection management and ensures the client doesn't
	// try to reuse the connection that we're closing.
	// TODO: Use http.Server to get connection management.
	resp.Header.Set("Connection", "close")
	if err := resp.Write(clientConn); err != nil {
		log.Printf("Failed to write server response: %s", err)
		return
	}
	if req.Header.Get("Upgrade") != "" && resp.StatusCode == http.StatusSwitchingProtocols {
		log.Printf("Upgraded protocol to %s", req.Header.Get("Upgrade"))
		// Transparently proxy bytes between client and server.
		// Server dictates termination.
		clientChunks := readChunks(clientConn)
		serverChunks := readChunks(serverConn)
		for {
			select {
			case b := <-clientChunks:
				if b == nil {
					continue
				}
				serverConn.Write(b)
			case b := <-serverChunks:
				if b == nil {
					goto term
				}
				clientConn.Write(b)
			}
		}
	term:
	}
	log.Printf("Finished request: %s %s", req.Method, req.URL.RequestURI())
}
