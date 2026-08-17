# Onboarding

Bringing a package into oss-rebuild coverage means two things: deciding it is
worth the capacity, and then spending capacity until it reproduces.
`ctl onboard priority` answers the first, `ctl onboard enqueue` and its queue
answer the second.

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

## The queue

`enqueue` expands a package into one queue document per version, each a
campaign recording where that version sits on the escalation ladder. The
ladder's stages are replay, infer, and agent, each more expensive than the
last, and every version starts at infer (heuristic inference plus a build).

```sh
ctl onboard enqueue --project ssci-demos --ecosystem npm --from-packages lodash,express \
    --prevalence gs://my-analytics/priority/prevalence.jsonl
ctl onboard status --project ssci-demos
```

Candidates are derived from the signals export which contains both the package
and version ranking in addition to the registry metadata necessary to enqueue.

Since a package can have hundreds of ranked versions, we use `--max-versions`
to keep only the top few. It admits versions by the same ordering the queue is
drained by, so a version that would never reach the front never enters in the
first place.

Each campaign stores a score and a publication time rather than a precomputed
order. `Score` is the version's own prevalence from the export. `DispatchOrder`
multiplies it by a freshness boost derived from the publication time at read
time, so a recent release spikes and then decays into the backlog while it
waits, letting a fresh release of a mid-tier package outrank a stale version of
a critical one while keeping a fresh version of a critical package above both.

State lives in Firestore so a run is resumable and mutable.
