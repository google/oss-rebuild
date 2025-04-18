package executor

import (
	"context"

	"github.com/go-git/go-billy/v5"
)

// Executor defines the interface for different execution strategies
type Executor interface {
	Setup(ctx context.Context, projectFs billy.Filesystem, artifact string) error
	Run(ctx context.Context, dockerfile []byte, pkgName, version string) error
	Cleanup() error
}
