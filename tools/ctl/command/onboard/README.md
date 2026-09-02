# Onboarding

Bringing a package into oss-rebuild coverage means two things: deciding it is
worth the capacity, and then spending capacity until it reproduces.
`ctl onboard priority` answers the first.

## Priority

There are far more packages than rebuild capacity, so something has to say
which ones matter. Priority is a per-package score combining independent
signals of importance, each computed by its own offline job and each
normalized per ecosystem into (0,1] so heavy tails and incomparable registry
scales stay out of the arithmetic.

| Signal     | What it measures                                            | Source                    |
| ---------- | ----------------------------------------------------------- | ------------------------- |
| prevalence | distinct packages depending on this one, transitively too   | deps.dev dependency graph |

Prevalence is computed at two granularities. The package number measures total
use and is a coarse estimation of overall value. The per-version number allows
us to better assess priority within a package's publications. Both are the
dependent count on a log scale relative to the ecosystem's most depended-on
package, so a version never outscores its package and the gap between them is
the share of the package's use that version carries: lodash sits near the top
of npm, lodash@4.17.21 just under it, and lodash@3.10.1 well below. The
package number says lodash is worth tracking at all. The version numbers say
which of its releases to rebuild first.

Each version row also carries the version's publish time from deps.dev and,
for PyPI, its pure wheel's filename from the registry's public file listing,
so a consumer can form a rebuild target and rank by age without a live
registry call. Other ecosystems derive the artifact name from the version.

Each job writes its ranked signal as a JSONL export, to a local path or a
gs:// object:

```sh
# Read-only against public BigQuery data.
ctl onboard priority prevalence --project my-project --out prevalence.jsonl
ctl onboard priority prevalence --project my-project --out gs://my-analytics/priority/prevalence.jsonl
```
