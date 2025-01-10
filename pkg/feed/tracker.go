package feed

import "github.com/google/oss-rebuild/pkg/rebuild/rebuild"

type Tracker interface {
	IsTracked(rebuild.Target) (bool, error)
}

type StaticListTracker struct {
	Tracked map[rebuild.Ecosystem]map[string]bool
}

func (tr *StaticListTracker) IsTracked(t rebuild.Target) (bool, error) {
	if _, ok := tr.Tracked[t.Ecosystem]; !ok {
		return false, nil
	}
	tracked, ok := tr.Tracked[t.Ecosystem][t.Package]
	return ok && tracked, nil
}

var _ Tracker = &StaticListTracker{}
