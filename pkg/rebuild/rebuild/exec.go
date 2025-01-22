package rebuild

import (
	"bytes"
	"context"
	"io"
	"log"
	"os/exec"
)

// ExecuteScript executes a single step of the strategy and returns the output regardless of error.
func ExecuteScript(ctx context.Context, dir string, script string) (string, error) {
	output := new(bytes.Buffer)
	outAndLog := io.MultiWriter(output, log.Default().Writer())
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Stdout = outAndLog
	cmd.Stderr = outAndLog
	// CD into the package's directory (which is where we cloned the repo.)
	cmd.Dir = dir
	log.Printf(`Executing build script: """%s"""`, cmd.String())
	err := cmd.Run()
	return output.String(), err
}
