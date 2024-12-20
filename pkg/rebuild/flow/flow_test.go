package flow

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestResolveTemplate(t *testing.T) {
	tests := []struct {
		name     string
		template string
		data     interface{}
		want     string
		wantErr  bool
	}{
		{
			name:     "simple substitution",
			template: "Hello {{.Name}}",
			data:     struct{ Name string }{"World"},
			want:     "Hello World",
		},
		{
			name:     "invalid template syntax",
			template: "Hello {{.Name",
			data:     struct{ Name string }{"World"},
			want:     "",
			wantErr:  true,
		},
		{
			name:     "missing struct data field",
			template: "Hello {{.Name}}",
			data:     struct{ Foo string }{"World"},
			want:     "",
			wantErr:  true,
		},
		{
			name:     "missing map data field",
			template: "Hello {{.Name}}",
			data:     Data{"Foo": "World"},
			want:     "Hello <no value>",
		},
		{
			name:     "missing with field",
			template: "Hello {{.With.Name}}",
			data:     Data{"With": map[string]string{}},
			want:     "Hello ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			err := resolveTemplate(buf, tt.template, tt.data)
			if tt.wantErr != (err != nil) {
				t.Errorf("resolveTemplate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, buf.String()); !tt.wantErr && diff != "" {
				t.Errorf("template output mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStep_Resolve(t *testing.T) {
	tests := []struct {
		name    string
		step    Step
		with    map[string]string
		data    Data
		tools   []*Tool
		want    Fragment
		wantErr bool
	}{
		{
			name: "simple runs step with template",
			step: Step{
				Runs: "echo {{.With.message}}",
			},
			with: map[string]string{"message": "hello"},
			data: Data{},
			want: Fragment{
				Script: "echo hello",
			},
			wantErr: false,
		},
		{
			name: "basic tool usage",
			step: Step{
				Uses: "basic-tool",
				With: map[string]string{"message": "hello"},
			},
			with: map[string]string{},
			data: Data{},
			tools: []*Tool{
				{
					Name: "basic-tool",
					Steps: []Step{
						{Runs: "echo {{.With.message}}"},
					},
				},
			},
			want:    Fragment{Script: "echo hello"},
			wantErr: false,
		},
		{
			name: "nested tool resolution",
			step: Step{
				Uses: "outer-tool",
			},
			with: map[string]string{},
			data: Data{},
			tools: []*Tool{
				{
					Name: "inner-tool",
					Steps: []Step{
						{Runs: "echo inner-{{.With.message}}"},
					},
				},
				{
					Name: "middle-tool",
					Steps: []Step{
						{
							Uses: "inner-tool",
							With: map[string]string{
								"message": "from-middle",
							},
						},
						{Runs: "echo middle-{{.With.message}}"},
					},
				},
				{
					Name: "outer-tool",
					Steps: []Step{
						{
							Uses: "middle-tool",
							With: map[string]string{
								"message": "passthrough",
							},
						},
					},
				},
			},
			want: Fragment{
				Script: "echo inner-from-middle\necho middle-passthrough",
			},
			wantErr: false,
		},
		{
			name: "template expansion in 'with' values",
			step: Step{
				Uses: "middle-tool",
				With: map[string]string{
					"prefix":  "{{.With.custom_prefix}}",
					"message": "direct",
				},
			},
			with: map[string]string{
				"custom_prefix": "test",
			},
			data: Data{},
			tools: []*Tool{
				{
					Name: "inner-tool",
					Steps: []Step{
						{Runs: "echo inner-{{.With.message}}"},
					},
				},
				{
					Name: "middle-tool",
					Steps: []Step{
						{
							Uses: "inner-tool",
							With: map[string]string{
								"message": "from-{{.With.prefix}}",
							},
						},
						{
							Runs: "echo middle-{{.With.message}}",
						},
					},
				},
			},
			want: Fragment{
				Script: "echo inner-from-test\necho middle-direct",
			},
			wantErr: false,
		},
		{
			name: "with values don't leak between tools",
			step: Step{
				Uses: "outer-tool",
				With: map[string]string{
					"message": "should-not-appear",
				},
			},
			with: map[string]string{
				"extra": "should-not-appear",
			},
			data: Data{},
			tools: []*Tool{
				{
					Name: "inner-tool",
					Steps: []Step{
						{Runs: "echo inner-from-{{.With.message}}"},
						{Runs: `echo extra-is-{{.With.extra | or "empty"}}`},
					},
				},
				{
					Name: "outer-tool",
					Steps: []Step{
						{
							Uses: "inner-tool",
							With: map[string]string{
								"message": "outer",
							},
						},
					},
				},
			},
			want: Fragment{
				Script: "echo inner-from-outer\necho extra-is-empty",
			},
			wantErr: false,
		},
		{
			name: "invalid step - both runs and uses",
			step: Step{
				Runs: "echo hello",
				Uses: "basic-tool",
			},
			with: map[string]string{},
			data: Data{},
			tools: []*Tool{
				{
					Name: "basic-tool",
					Steps: []Step{
						{Runs: "echo {{.With.message}}"},
					},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Configure a fresh registry with required tools
			reg := newRegistry()
			for _, tool := range tt.tools {
				if err := reg.Register(tool); err != nil {
					t.Fatalf("failed to register tool %q: %v", tool.Name, err)
				}
			}
			Tools = reg

			got, err := tt.step.Resolve(tt.with, tt.data)

			if tt.wantErr != (err != nil) {
				t.Errorf("Step.Resolve() error = %v, wantErr %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Step.Resolve mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	r := newRegistry()

	t.Run("register and get tool", func(t *testing.T) {
		tool := &Tool{Name: "test"}
		err := r.Register(tool)
		if err != nil {
			t.Fatalf("unexpected error registering tool: %v", err)
		}

		got, err := r.Get("test")
		if err != nil {
			t.Fatalf("unexpected error getting tool: %v", err)
		}

		if diff := cmp.Diff(tool, got); diff != "" {
			t.Errorf("tool mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("register nil tool", func(t *testing.T) {
		err := r.Register(nil)
		if err == nil {
			t.Error("expected error registering nil tool but got none")
		}
	})

	t.Run("register duplicate tool", func(t *testing.T) {
		tool := &Tool{Name: "duplicate"}
		if err := r.Register(tool); err != nil {
			t.Fatalf("unexpected error registering first tool: %v", err)
		}

		if err := r.Register(tool); err == nil {
			t.Error("expected error registering duplicate tool but got none")
		}
	})

	t.Run("get non-existent tool", func(t *testing.T) {
		_, err := r.Get("non-existent")
		if err == nil {
			t.Error("expected error getting non-existent tool but got none")
		}
	})
}

func TestResolveSteps(t *testing.T) {
	steps := []Step{
		{
			Runs:  "echo {{.With.first}}",
			Needs: []string{"dep1"},
		},
		{
			Runs:  "echo {{.With.second}}",
			Needs: []string{"dep2"},
		},
	}
	with := map[string]string{
		"first":  "hello",
		"second": "world",
	}
	want := Fragment{
		Script: "echo hello\necho world",
		Needs:  []string{"dep1", "dep2"},
	}
	got, err := ResolveSteps(steps, with, Data{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ResolveSteps mismatch (-want +got):\n%s", diff)
	}
}

func TestFragment_Join(t *testing.T) {
	tests := []struct {
		name string
		f1   Fragment
		f2   Fragment
		want Fragment
	}{
		{
			name: "empty_commands",
			f1:   Fragment{},
			f2:   Fragment{},
			want: Fragment{},
		},
		{
			name: "one_empty_script",
			f1:   Fragment{Script: "echo foo"},
			f2:   Fragment{},
			want: Fragment{Script: "echo foo"},
		},
		{
			name: "merge_scripts_and_deps",
			f1: Fragment{
				Script: "echo first",
				Needs:  []string{"dep1"},
			},
			f2: Fragment{
				Script: "echo second",
				Needs:  []string{"dep2"},
			},
			want: Fragment{
				Script: "echo first\necho second",
				Needs:  []string{"dep1", "dep2"},
			},
		},
		{
			name: "duplicate_deps",
			f1: Fragment{
				Script: "echo first",
				Needs:  []string{"dep1", "dep2"},
			},
			f2: Fragment{
				Script: "echo second",
				Needs:  []string{"dep2", "dep3"},
			},
			want: Fragment{
				Script: "echo first\necho second",
				Needs:  []string{"dep1", "dep2", "dep2", "dep3"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.f1.Join(tt.f2)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Fragment.Join() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
