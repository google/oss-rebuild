# How to Contribute

We would love to accept your patches and contributions to this project.

## Before you begin

### Sign our Contributor License Agreement

Contributions to this project must be accompanied by a
[Contributor License Agreement](https://cla.developers.google.com/about) (CLA).
You (or your employer) retain the copyright to your contribution; this simply
gives us permission to use and redistribute your contributions as part of the
project.

If you or your current employer have already signed the Google CLA (even if it
was for a different project), you probably don't need to do it again.

Visit <https://cla.developers.google.com/> to see your current agreements or to
sign a new one.

### Review our Community Guidelines

This project follows
[Google's Open Source Community Guidelines](https://opensource.google/conduct/).

## Contribution process

### Development Workflow

After making code changes, ensure your environment is clean and all tests pass. We provide a pre-commit script to automate these checks.

1. Install the pre-commit hook (one-time setup):
   ```bash
   ./scripts/install_precommit.sh
   ```

2. The script will run automatically on `git commit`. You can also run it manually at any time:
   ```bash
   ./scripts/precommit.sh
   ```

The script performs the following:
- Updates dependencies (`go mod tidy`)
- Adds license headers (`./.hooks/addlicense`)
- Builds the project (`go build ./...`)
- Runs tests (`go test ./...`)
- Runs vet (`go vet ./...`)
- Formats code (`gofmt -s -w .` and `go run golang.org/x/tools/cmd/goimports -w .`)

### Code Reviews

All submissions, including submissions by project members, require review. We
use [GitHub pull requests](https://docs.github.com/articles/about-pull-requests)
for this purpose.

### Commit Structure

Pull Requests may use either Squash or Rebase PR merge strategies. For more
background, see
[the docs](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/about-merge-methods-on-github))
but to summarize:

- _Squash_: All PR commits are combined into one commit when merged.
- _Rebase_: All PR commits are merged as-is.

Feel free to cater your PR to whichever workflow you prefer.
