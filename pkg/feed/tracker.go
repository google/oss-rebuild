package feed

import (
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

type Tracker interface {
	IsTracked(schema.ReleaseEvent) (bool, error)
}

type funcTracker struct {
	isTracked func(schema.ReleaseEvent) (bool, error)
}

func (f *funcTracker) IsTracked(e schema.ReleaseEvent) (bool, error) {
	return f.isTracked(e)
}

func TrackerFromFunc(isTracked func(schema.ReleaseEvent) (bool, error)) Tracker {
	return &funcTracker{isTracked: isTracked}
}
