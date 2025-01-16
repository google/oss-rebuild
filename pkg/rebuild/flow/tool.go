package flow

import (
	"sync"

	"github.com/pkg/errors"
)

// Tool defines a task template with its system requirements
type Tool struct {
	Name  string
	Steps []Step
	// TODO: Add structured parameters to support defaults and required fields.
}

type Data map[string]any

// Generate executes the tool with the given context and parameters
func (t *Tool) Generate(with map[string]string, data Data) (Fragment, error) {
	// TODO: Validate structured params.
	frag, err := ResolveSteps(t.Steps, with, data)
	return frag, errors.Wrapf(err, "resolving tool '%q'", t.Name)
}

// registry manages the global set of available tools
type registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

// newRegistry creates a new tool registry
func newRegistry() *registry {
	return &registry{
		tools: make(map[string]*Tool),
	}
}

// Tools is the default global registry
var Tools = newRegistry()

// Register adds a tool to the registry
func (r *registry) Register(t *Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if t == nil {
		return errors.New("tool is nil")
	}
	if _, exists := r.tools[t.Name]; exists {
		return errors.Errorf("tool already registered: %q", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// Get retrieves a tool from the registry
func (r *registry) Get(name string) (*Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	if !ok {
		return nil, errors.Errorf("tool not found: %q", name)
	}
	return t, nil
}

// MustRegister is like Register but panics on error
func (r *registry) MustRegister(t *Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}
