# Local Development Workflow

This guide provides a comprehensive overview of how to run and develop OSS Rebuild locally using the `ctl` command-line tool. It covers benchmark files, local rebuild execution, and results analysis through the `ctl` CLI and the Terminal User Interface (`tui`).

## Benchmark Files

Benchmarks are the core unit of work for OSS Rebuild systems. They define specific sets of ecosystem, packages, versions, and artifacts to be rebuilt and verified. Taken together, these four values (ecosystem, package, version, artifact name) combine to represent a single rebuild goal, which OSS Rebuild refers to as a "Target".

Benchmark files themselves are JSON documents that provide a structured manifest which runners use to orchestrate builds. The structure is definined in (defined in tools/benchmark/benchmark.go)

For example, a benchmark with one package and version would look like this:

```json
{
  "Count": 1,
  "Updated": "2025-12-01T00:00:00.000000000-05:00",
  "Packages": [
    {
      "Ecosystem": "pypi",
      "Name": "absl_py",
      "Versions": ["2.0.0"]
    }
  ]
}
```

OSS Rebuild uses a handful of standard benchmarks to aid in development and comparison of rebuild improvements. These can be found under tools/benchmark/data/ (e.g., pypi_top_250_pure.json, debian_top_500.json). In most cases the code used to generate these benchmarks can be found in the benchmark/generate/main.go file.

## The `ctl` CLI tool

OSS Rebuild includes the `ctl` CLI tool (colloquially pronounced "cuttle") to assist in general development and evaluation work. To see the current set of commands supported, run `go run ./tools/ctl --help`.

### `run-bench` - Running Benchmarks Locally

To execute a benchmark on your local machine, use the `run-bench` command with the `--local` flag.

> [!NOTE]
> Some strategies depend on timewarp, which is served in production using a "bootstrap tools" bucket.
> When running locally, a bucket can be specified using `--bootstrap-bucket` and `--bootstrap-version`.
>
> Some example values would be `--bootstrap-bucket=google-rebuild-bootstrap-tools --bootstrap-version=v0.0.0-20250428204534-b35098b3c7b7`

```bash
cat <<EOF > /tmp/my-benchmark.json
{
  "Count": 1,
  "Updated": "2025-12-01T00:00:00.000000000-05:00",
  "Packages": [
    {
      "Ecosystem": "pypi",
      "Name": "absl_py",
      "Versions": [
        "2.0.0"
      ]
    }
  ]
}
EOF
go run ./tools/ctl run-bench attest --local --max-concurrency 1 /tmp/my-benchmark.json
```

This will run the pypi_absl_micro.json benchmark locally (as opposed to the hosted execution platform). The local runner spins up a separate Docker container for each build in the benchmark. This guarantees that every build runs in a clean, isolated environment.

If you have a large machine, you might want to increase the `--max-concurrency` beyond 1. One of the benefits of hosted (non-local) infrastructure is that extremely high concurrency is possible.

Metadata about the attempts, errors, build logs, and the rebuilt artifacts will all be written to the local asset store (/tmp/oss-rebuild).

For example, if you executed the above benchmark exactly once, you would have:

```
$ tree /tmp/oss-rebuild
/tmp/oss-rebuild
├── assets
│   └── pypi
│       └── absl_py
│           └── 2.0.0
│               └── absl_py-2.0.0-py3-none-any.whl
│                   └── 2025-12-01T23:46:16Z
│                       ├── absl_py-2.0.0-py3-none-any.whl
│                       └── logs
└── rundex
    ├── runs
    │   └── 2025-12-01T23:46:16Z
    │       └── pypi
    │           └── absl_py
    │               └── absl_py-2.0.0-py3-none-any.whl
    │                   └── firestore.json
    └── runs_metadata
        └── 2025-12-01T23:46:16Z.json
14 directories, 4 files
```

Metadata about the run as a whole is stored under /tmp/oss-rebuild/rundex/runs_metadata/2025-12-01T23:46:16Z.json

Target-specific metadata can be found under /tmp/oss-rebuild/rundex/runs/...

Finally, all the logs, rebuilt artifacts, and other assets will be located under /tmp/oss-rebuild/assets/...

For more details about where OSS Rebuild stores artifacts, see <TODO: storage.md>

### `get-results` - Querying Results

Once a local run is complete, you can query the outcomes programmatically using the get-results command. You'll need the run ID, which is logged at the start of `run-bench`. In the above example it was `2025-12-01T23:46:16Z`.

To query the results, run `get-results` and provide the run id. For example:

```bash
go run ./tools/ctl get-results --run <run-id>
```

By default, `get-results` will summarize the run's results, grouping targets by their final result message. Alternatively, you can format the output as a csv for analysis or visualization in your favorite spreadsheet editor:

```bash
go run ./tools/ctl get-results --run <run-id> --format=csv
```

If you want to filter the results based on their message, you can use the `--pattern` flag, providing a regex pattern to match results that are included in the output. These are the same messages used for grouping in the default `get-results` query. For example, you can select all targets that finished building but did not match the upstream:

```bash
go run ./tools/ctl get-results --run <run-id> --pattern='.*rebuild content mismatch.*' --format=csv
```

Additionally, by combining filtering with `--format=bench` you can make a new benchmark with only the failure case you're interested in. This is helpful if you fix a specific failure case and want to retry only those errors.

```bash
go run ./tools/ctl get-results --run <run-id> --pattern='.*rebuild content mismatch.*' --format=bench > /tmp/my-bench.json
```

### `tui` - Interactive Tools

For a powerful, interactive way to explore your local development results, use the terminal user interface, `tui`.

For the most features, run this command inside a tmux session, and on a machine that has Docker installed.

```bash
go run ./tools/ctl tui
```

Without any arguments, `tui` will automatically scan /tmp/oss-rebuild for all available run data.

The discovered rebuild results will be displayed in the left-hand side in a tree view, which is the primary visualization of `tui`. Nodes can be expanded in the tree by pressing the <enter> key. The data hierarchy is: benchmark > run ID > failure message (class of error) > target.

<!--- TODO: change tui to use a consistent color for expandable fields -->

<!--- TODO: change the target-specific commands to be in a more reasonable ordering -->

If you expand nodes all the way down to a single target, you can expand once more to reveal target-specific commands:

- `metadata`: View metadata about the attempt, such as the strategy, start time, etc. This will open a modal that can be closed by pressing `esc`.
- `logs`: View the logs captured during the build. This will open a new tmux window showing the logs using "less" to allow scrolling and searching.
- `diff`: View the diff between the rebuild and the upstream artifact. This also opens a tmux window similar to `logs`.
- `generate stabilizers from diff`: This creates new stabilizers to ignore the detected differences. This is helpful when the diff is unimportant and otherwise unaddressable.
- `run local`: Start a new local execution reusing the strategy from the attempt. Importantly, you can enter the build environment by pressing the `a` key to attach to the most recent container launched this way.
- `edit and run local`: This is similar to `run local` but allows adjustments to the strategy before a new container is launched. This can be used to evaluate potential changes to a strategy, either for contributing a manual strategy or to inform updates to the inference process. The edited strategy is stored by default in the /tmp/oss-rebuild directory, but a build def repo can be specified using the `--def-dir` flag.
