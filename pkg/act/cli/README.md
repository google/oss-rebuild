# ACT CLI Framework

The `pkg/act/cli` package provides utilities for building CLI commands using the
[act framework](../README.md), which promotes **transport-agnostic actions**
that can be exposed via CLI, HTTP, or other interfaces.

## Overview

The act framework separates business logic from transport concerns through three core concepts:

1. **Input** - Validated configuration/request data
2. **Action** - Pure business logic function
3. **Deps** - Dependency container (database, clients, IO, etc.)

The CLI package provides `RunE()`, which wires these components into Cobra command logic.

## Basic Pattern

### 1. Define Your Input (Config)

The Input holds all command configuration, typically from flags:

```go
type Config struct {
    Repository string
    Ref        string
}

func (c Config) Validate() error {
    if c.Repository == "" {
        return errors.New("repository is required")
    }
    if c.Ref == "" {
        return errors.New("ref is required")
    }
    return nil
}
```

**NOTE**: The `Validate()` method is called automatically by `RunE()`.

### 2. Define Your Dependencies

Dependencies should include the `cli.IO` struct for output streams:

```go
type Deps struct {
    IO cli.IO
    // Add other dependencies here (DB clients, HTTP clients, etc.)
}

```

If using `cli.RunE` to define the CLI entrypoint, a `Deps.SetIO` method is also
required:

```go
func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }
```

### 3. Write Your Action

The action contains pure business logic with no cli-specific logic:

```go
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
    // Business logic here
    fmt.Fprintf(deps.IO.Out, "Processing repository: %s\n", cfg.Repository)

    // Do work...

    return &act.NoOutput{}, nil
}
```

### 4. Create the Command Factory

Wire everything together:

```go
func Command() *cobra.Command {
    cfg := Config{}
    cmd := &cobra.Command{
        Use:   "my-command --repository <uri> --ref <ref>",
        Short: "Brief description",
        Long:  `Detailed description...`,
        Args:  cobra.NoArgs,
        RunE: cli.RunE(
            &cfg,
            cli.NoArgs[Config],
            act.InitDefault[*Deps],
            Handler,
        ),
    }
    cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
    return cmd
}

func flagSet(name string, cfg *Config) *flag.FlagSet {
    set := flag.NewFlagSet(name, flag.ContinueOnError)
    set.StringVar(&cfg.Repository, "repository", "", "repository URI")
    set.StringVar(&cfg.Ref, "ref", "", "git reference")
    return set
}
```

**NOTE**: The `flagSet` pattern keeps flag definitions separate from command wiring

## Testing Pattern

Commands are easy to test because actions are pure functions:

```go
func TestHandler(t *testing.T) {
    cfg := Config{
        Repository: "https://github.com/example/repo",
        Ref:        "main",
    }
    var outBuf, errBuf bytes.Buffer
    deps := &Deps{
        IO: cli.IO{
            Out: &outBuf,
            Err: &errBuf,
        },
    }
    _, err := Handler(context.Background(), cfg, deps)
    if err != nil {
        t.Errorf("Expected no error, got %v", err)
    }
    if !strings.Contains(outBuf.String(), "Processing repository") {
        t.Error("Expected output to contain 'Processing repository'")
    }
}
```
