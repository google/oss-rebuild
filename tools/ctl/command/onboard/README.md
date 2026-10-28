# Onboarding

Bringing a package into oss-rebuild coverage means two things: deciding it is
worth the capacity, and then spending capacity until it reproduces.
`ctl onboard priority` answers the first, `ctl onboard enqueue|dispatch|status`
answers the second.

## Priority

There are far more packages than rebuild capacity, so something has to say
which ones matter. Priority is a per-package score combining independent
signals of importance, each computed by its own offline job and each
normalized per ecosystem into (0,1] so heavy tails and incomparable registry
scales stay out of the arithmetic.

| Signal     | What it measures                                          | Source                      |
| ---------- | --------------------------------------------------------- | --------------------------- |
| prevalence | distinct packages depending on this one, transitively too | deps.dev dependency graph   |
| prominence | how well known the package is by name                     | one Gemini call per package |

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
# Prevalence: read-only against public BigQuery data.
ctl onboard priority prevalence --project my-project --out prevalence.jsonl
ctl onboard priority prevalence --project my-project --out gs://my-analytics/priority/prevalence.jsonl

# Prominence: one cached model call per package. The corpus can be the export above.
ctl onboard priority prominence --project my-project --corpus prevalence.jsonl \
    --cache prom-cache.json --horizon 2025-06-01 --out gs://my-analytics/priority/prominence.jsonl
```

The two signals rank different things, and each fills the other's blind spot.
Prevalence sees blast radius, including plumbing nobody has heard of, and it
correctly downranks deprecated names that are still well-known. Prominence
represents public awareness, including the leaf applications not
well-represented in the dependency graph: an application like ripgrep has no
dependents in spite of having many installations and executions. The score
weights the two equally because there is no data yet to tune the weights, so a
package with only one of the signals scores at most 0.5.

> [!NOTE]
> Prevalence is not the OSSF Criticality Score, which scores repository activity.

## The queue

`enqueue` expands a package into one queue document per version, each a
campaign recording where that version sits on the escalation ladder. The
ladder's stages are replay, infer, and agent, each more expensive than the
last, and every version starts at infer (heuristic inference plus a build).

```sh
ctl onboard enqueue --project my-project --ecosystem npm --from-packages lodash,express \
    --signals-db gs://my-analytics/priority
ctl onboard enqueue --project my-project --ecosystem pypi --max-packages 200 \
    --signals-db gs://my-analytics/priority
ctl onboard status --project my-project
```

Candidates are derived from the signals export which contains both the package
and version ranking in addition to the registry metadata necessary to enqueue.
Without `--from-packages`, `enqueue` walks the database's packages in score
order, at most `--from-top` of them, and stops once `--max-packages` packages
have gained campaigns. The database holds only what the prevalence export kept,
so raise `--top` and `--top-versions` there and republish to reach further.

Since a package can have hundreds of ranked versions, we use `--max-versions`
to keep only the top few. It admits versions by the same ordering the queue is
drained by, so a version that would never reach the front never enters in the
first place.

Each campaign stores a score and a publication time rather than a precomputed
order. `Score` combines the exported scores, specialized to that version: the
version's own prevalence plus the package's prominence, which is identical
across versions. `DispatchOrder` multiplies it by a freshness boost derived
from the publication time at read time, so a recent release spikes and then
decays into the backlog while it waits, letting a fresh release of a mid-tier
package outrank a stale version of a critical one while keeping a fresh
version of a critical package above both.

State lives in Firestore so a run is resumable and mutable.

## Keeping coverage current

A tracked package's new releases enter the queue through `feed`, which takes
a batch of published releases and admits the newest stable one per package.
A package is tracked while the signal database ranks it, and the release
scores as its package does, so admission needs no registry listing. Each
campaign is held out of dispatch for `--settle` (default 15m) after
publication so a registry that has not finished propagating the release does
not fail the first attempt.

```sh
# One JSON feed.Event (internal/feed/feed.go) per line.
ctl onboard feed --project my-project --signals-db gs://my-analytics/priority --events releases.jsonl
```

The same pass runs inside the API at `POST /onboard/feed` with the batch in an
`events` form field, so a producer of release events needs no Firestore
access of its own.

## Dispatch

`dispatch` drains the queue in passes:

```sh
ctl onboard dispatch --project my-project --api https://api.example.run.app \
    --batch 200 --max-agent 20
```

Each pass observes what finished and launches what fits, so run it on a timer
until `status` reports the queue drained. Builds run wide, so `--batch` mostly
bounds how much work one pass creates. `--max-agent` is the real throttle
because agent sessions occupy scratch compute for their duration. A dispatch
whose build is lost is requeued after `--inflight-timeout` (default 24h). A
shorter value recovers faster from outages at the cost of duplicate builds
for anything that legitimately runs longer.

The same pass runs inside the API at `POST /onboard/dispatch`, bound to the
API's own rebuild and agent handlers, so a deployment drains its queue on a
Cloud Scheduler cadence without any route to the API from a workstation. Set
`onboard_dispatch_schedule` and `onboard_dispatch_params` in the deployment
variables. The parameters are the flags above with underscores (`max_agent`,
`agent_model`, `period=24h`). Omitted ones take the defaults, and a negative
count switches that cap off. Builds launch at `build_rate` per minute (default
40, under GCB's 60 creates per minute) so a large `batch` reaches the pool as
a stream, and `window` stops a pass launching before the next one fires. What
it leaves stays queued. With `refill` set, a scheduled run keeps re-observing
and launching every `refill` until `window` closes, so a slot freed mid-window
is reused within a tick rather than at the next cadence. Cloud Scheduler
skips a fire while the previous attempt is still running, and a pass can run
past the window by its observe and launch time, so keep `window` a pass
short of the cadence: `refill=30s` with `window=3m30s` under a five-minute
schedule is the shape.
