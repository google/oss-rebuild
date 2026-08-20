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

### Code Reviews

All submissions, including submissions by project members, require review. We
use [GitHub pull requests](https://docs.github.com/articles/about-pull-requests)
for this purpose.

Feedback on unfinished work can be solicited via a Draft PR.

### Pull Requests

One concern per PR. Split out unrelated fixes so they stay visible in the git
blame.

### Commit Structure

Pull Requests may use either Squash or Rebase PR merge strategies. For more
background, see
[the docs](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/about-merge-methods-on-github))
but to summarize:

- _Squash_: All PR commits are combined into one commit when merged.
- _Rebase_: All PR commits are merged as-is.

Feel free to cater your PR to whichever workflow you prefer.

## Style

Go code follows the [Google Go Style Guide](https://google.github.io/styleguide/go/).
The conventions below either go beyond that guide or deviate from it.

### Comments

- A comment must add something the code cannot: a rationale, a constraint, or a
  link. Comments that restate the code will be flagged in review.
- Magic values and non-obvious decisions get a comment citing the authoritative
  source (a spec, an RFC, an upstream issue) where one exists.
- If you find yourself explaining a decision in a PR thread, move that
  explanation into the code. The repo is the primary home for explanations, not
  issues or review threads.
- Write TODOs as `// TODO: Support multi-release JARs`, with no owner
  attribution. Mark constraints and caveats with `// NOTE: ...`.
- Prefer short trailing comments for struct fields, e.g.
  `Count int // retries remaining`. Use a preceding doc comment only when the
  semantics need more than a line.
- No commented-out code. Delete it and leave a TODO if the work remains.

### Prose

- No em-dashes and no semicolons, in comments, docs, and commit messages alike.
  Use periods and split sentences.
- Expand domain acronyms at first use, e.g. "Binary Non-Maintainer Upload
  (binNMU)".

### Errors

- Use `github.com/pkg/errors` rather than stdlib wrapping. It supports
  capturing stack traces.
- Phrase wrap messages in the present progressive: `errors.Wrap(err, "parsing
manifest")` formats as `parsing manifest: unexpected EOF`.
- Keep error chains linear. Avoid `errors.Join`.
- Wrap where the error is consumed rather than annotating every leaf. Return
  errors up the stack and log only at the top level. Library code never logs.
- Panic on invariant violations rather than handling impossible cases. Use a
  `Must` prefix for init-time helpers that crash on error.

### Code

- Avoid blank lines inside function bodies.
- Prefer compact forms: drop needless `else` branches, inline single-use
  variables, keep nesting minimal.

### Tests

- Use table-driven tests compared with `cmp.Diff`. Test case names are
  PascalCase.
- Prefer small synthetic fixtures that obviously exercise the behavior over
  realistic ones.
- Bug fixes include a regression test.
- Reuse the repo's test helpers: `archivetest`, `gitxtest`, `httpxtest`,
  `must()`, and `textwrap.Dedent`.
