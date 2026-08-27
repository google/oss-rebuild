// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package snapshot implements the snapshot database: a rollup engine that
// scans Firestore and writes a single SQLite database to a file:// or
// gs:// destination. There are no row structs anywhere: entity tables are
// docdb doc tables whose columns extract from the source documents, and
// the rollups are derived tables materialized from SQL over them, so the
// registry below is the entire schema.
package snapshot

import (
	"github.com/google/oss-rebuild/internal/docdb"
)

// SchemaVersion identifies the snapshot database layout. Bump it on any
// change that would mislead an older reader: dropped, renamed, or retyped
// columns, or key changes. Additive columns do not require one.
// TestSchemaShapeIsGolden enforces the rule against the committed golden
// for this version and walks an author through the bump, after which the
// versioned artifact names cut readers over by era rather than breaking
// them in place. Superseded goldens stay committed as the record of the
// eras still being consumed.
const SchemaVersion = 1

// Table names in the snapshot database.
const (
	TableAttempts         = "attempts"
	TableRuns             = "runs"
	TableAgentSessions    = "agent_sessions"
	TableAgentIterations  = "agent_iterations"
	TableScratchVMs       = "scratch_vms"
	TableScratchExecs     = "scratch_execs"
	TableRepoMetrics      = "repo_metrics"
	TablePackageSignals   = "package_signals"
	TableCostObservations = "cost_observations"
	TableEcosystemDaily   = "ecosystem_daily"
)

// Observation source discriminators for cost_observations rows.
const (
	ObservationSourceAttempt = "attempt"
	ObservationSourceSession = "agent_session"
	ObservationSourceScratch = "scratch_vm"
)

// costObservationsQuery normalizes measured resource usage into one row per
// completed attempt (timings and costs), per agent session with usage
// (tokens), and per deleted scratch VM (vm_seconds), the latter attributed
// to a target through its linked session. Measures a source does not carry
// read NULL.
const costObservationsQuery = `
SELECT ecosystem, package, version, artifact,
	'` + ObservationSourceAttempt + `' AS source, run_id, NULL AS session_id, NULL AS scratch_id,
	setup_seconds, source_seconds, deps_seconds, build_seconds, failed_in,
	cost_inference_seconds AS inference_seconds, cost_builder_seconds AS builder_seconds,
	cost_builder_pool AS builder_pool, cost_logs_bytes AS logs_bytes,
	cost_container_bytes AS container_bytes, cost_artifact_bytes AS artifact_bytes,
	NULL AS input_tokens, NULL AS cached_input_tokens, NULL AS output_tokens, NULL AS model,
	NULL AS vm_seconds, NULL AS machine_class, created AS timestamp
FROM attempts WHERE status != 'RUNNING'
UNION ALL
SELECT ecosystem, package, version, artifact,
	'` + ObservationSourceSession + `', NULL, session_id, NULL,
	NULL, NULL, NULL, NULL, NULL,
	NULL, NULL, NULL, NULL, NULL, NULL,
	input_tokens, cached_input_tokens, output_tokens, model,
	NULL, NULL, created
FROM agent_sessions WHERE json_extract(raw, '$.Usage') IS NOT NULL
UNION ALL
SELECT s.ecosystem, s.package, s.version, s.artifact,
	'` + ObservationSourceScratch + `', NULL, s.session_id, sc.scratch_id,
	NULL, NULL, NULL, NULL, NULL,
	NULL, NULL, NULL, NULL, NULL, NULL,
	NULL, NULL, NULL, NULL,
	sc.vm_seconds, sc.machine_class, sc.created
FROM scratch_vms sc LEFT JOIN agent_sessions s ON s.scratch_id = sc.scratch_id
WHERE sc.vm_seconds > 0`

// ecosystemDailyQuery aggregates completed attempts, agent-session tokens,
// and scratch VM-seconds into one row per (ecosystem, day). A row exists for
// any day any measure contributes. Documents without a created stamp
// aggregate under an empty day rather than vanishing on NULL join keys.
// first_time_successes counts packages whose earliest success fell on that
// day. Scratch VM-seconds attribute to a target's ecosystem through the
// linked session. Unlinked scratches are omitted (no ecosystem).
const ecosystemDailyQuery = `
WITH completed AS (SELECT * FROM attempts WHERE status != 'RUNNING'),
days AS (
	SELECT ecosystem, coalesce(substr(created, 1, 10), '') AS day, count(*) AS attempts,
		sum(success) AS successes, sum(status = 'ERROR') AS errors,
		count(DISTINCT nullif(package, '')) AS distinct_pkgs,
		sum(coalesce(cost_builder_seconds, 0)) AS builder_seconds
	FROM completed GROUP BY 1, 2),
firsts AS (
	SELECT ecosystem, coalesce(substr(min(created), 1, 10), '') AS day
	FROM completed WHERE success GROUP BY ecosystem, package),
first_days AS (SELECT ecosystem, day, count(*) AS n FROM firsts GROUP BY 1, 2),
tokens AS (
	SELECT ecosystem, coalesce(substr(created, 1, 10), '') AS day,
		sum(coalesce(input_tokens, 0) + coalesce(output_tokens, 0)) AS n
	FROM agent_sessions WHERE json_extract(raw, '$.Usage') IS NOT NULL GROUP BY 1, 2),
vm AS (
	SELECT s.ecosystem, coalesce(substr(sc.created, 1, 10), '') AS day, sum(sc.vm_seconds) AS n
	FROM scratch_vms sc JOIN agent_sessions s ON s.scratch_id = sc.scratch_id
	WHERE sc.vm_seconds > 0 GROUP BY 1, 2),
keys AS (
	SELECT ecosystem, day FROM days UNION SELECT ecosystem, day FROM first_days
	UNION SELECT ecosystem, day FROM tokens UNION SELECT ecosystem, day FROM vm)
SELECT k.ecosystem, k.day,
	coalesce(d.attempts, 0) AS attempts, coalesce(d.successes, 0) AS successes,
	coalesce(d.errors, 0) AS errors, coalesce(d.distinct_pkgs, 0) AS distinct_pkgs_attempted,
	coalesce(f.n, 0) AS first_time_successes, coalesce(t.n, 0) AS tokens,
	coalesce(d.builder_seconds, 0.0) AS builder_seconds, coalesce(v.n, 0.0) AS scratch_vm_seconds
FROM keys k
LEFT JOIN days d ON d.ecosystem = k.ecosystem AND d.day = k.day
LEFT JOIN first_days f ON f.ecosystem = k.ecosystem AND f.day = k.day
LEFT JOIN tokens t ON t.ecosystem = k.ecosystem AND t.day = k.day
LEFT JOIN vm v ON v.ecosystem = k.ecosystem AND v.day = k.day
ORDER BY 1, 2`

// Tables is the registry of snapshot tables and the single declaration of
// the snapshot schema, in build order: doc tables first, then the derived
// tables that query them. Nothing about an entity exists outside its raw
// document. The write statement extracts the key and clock columns, every
// other entity column is generated from raw, and adding one is one spec
// line that backfills for all history at the next rollup. Unmeasured values
// and absent parent objects (no costs, no provenance) read NULL through
// generated columns. Paths follow each document's JSON encoding: Go field
// names except where the schema structs carry json tags.
// NOTE: A path is associated with its column by nothing but position in
// this registry. A crossed pairing extracts plausible values that no test
// distinguishes, so check the pairing carefully when adding or changing
// columns.
func Tables() []docdb.TableDef {
	return []docdb.TableDef{
		{
			Name: TableAttempts,
			Cols: []docdb.Col{
				{Name: "ecosystem", Type: "TEXT", Expr: docdb.Doc("$.Ecosystem")},
				{Name: "package", Type: "TEXT", Expr: docdb.Doc("$.Package")},
				{Name: "version", Type: "TEXT", Expr: docdb.Doc("$.Version")},
				{Name: "artifact", Type: "TEXT", Expr: docdb.Doc("$.Artifact")},
				{Name: "run_id", Type: "TEXT", Expr: docdb.Doc("$.RunID")},
				{Name: "updated", Type: "TEXT", Expr: docdb.DocTime("$.Updated")},
				// The strategy oneof's json tags double as its labels. The
				// one divergent tag is renamed.
				{Name: "strategy_type", Type: "TEXT", Expr: "replace(" + docdb.OneofKey("$.Strategy") + ", 'rebuild_location_hint', 'location_hint')"},
			},
			PK: []string{"ecosystem", "package", "version", "artifact", "run_id"},
			GenCols: []docdb.GenCol{
				// status and success normalize legacy records that predate
				// the status field.
				{Name: "status", Type: "TEXT", Stored: true,
					Expr: "coalesce(nullif(" + docdb.Raw("$.Status") + ", ''), CASE WHEN " + docdb.Raw("$.Success") + " THEN 'SUCCESS' ELSE 'FAILURE' END)"},
				// coalesce keeps the column defined when both fields are
				// absent: NULL OR NULL propagates NULL, not false.
				{Name: "success", Type: "INTEGER", Stored: true,
					Expr: "coalesce(" + docdb.Raw("$.Success") + " OR " + docdb.Raw("$.Status") + " = 'SUCCESS', 0)"},
				// mechanism is how the strategy came to be: a definition
				// alone is verbatim, inference alone inferred, both hinted.
				{Name: "mechanism", Type: "TEXT",
					Expr: "CASE" +
						" WHEN " + docdb.Raw("$.Provenance.definition") + " IS NOT NULL AND " + docdb.Raw("$.Provenance.inference") + " IS NOT NULL THEN 'hinted'" +
						" WHEN " + docdb.Raw("$.Provenance.definition") + " IS NOT NULL THEN 'verbatim'" +
						" WHEN " + docdb.Raw("$.Provenance.inference") + " IS NOT NULL THEN 'inferred'" +
						" ELSE '' END"},
				{Name: "message", Type: "TEXT", Expr: docdb.Raw("$.Message"), Stored: true},
				{Name: "executor_version", Type: "TEXT", Expr: docdb.Raw("$.ExecutorVersion"), Stored: true},
				{Name: "build_id", Type: "TEXT", Expr: docdb.Raw("$.BuildID")},
				{Name: "oblivious_id", Type: "TEXT", Expr: docdb.Raw("$.ObliviousID")},
				{Name: "definition_repository", Type: "TEXT", Expr: docdb.Raw("$.Provenance.definition.repository")},
				{Name: "definition_ref", Type: "TEXT", Expr: docdb.Raw("$.Provenance.definition.ref")},
				{Name: "definition_path", Type: "TEXT", Expr: docdb.Raw("$.Provenance.definition.path")},
				{Name: "inference_version", Type: "TEXT", Expr: docdb.Raw("$.Provenance.inference.version")},
				{Name: "setup_seconds", Type: "REAL", Expr: docdb.RawSeconds("$.BuildTimings.Setup")},
				{Name: "source_seconds", Type: "REAL", Expr: docdb.RawSeconds("$.BuildTimings.Source")},
				{Name: "deps_seconds", Type: "REAL", Expr: docdb.RawSeconds("$.BuildTimings.Deps")},
				{Name: "build_seconds", Type: "REAL", Expr: docdb.RawSeconds("$.BuildTimings.Build")},
				{Name: "failed_in", Type: "TEXT", Expr: docdb.Raw("$.BuildTimings.FailedIn")},
				{Name: "has_costs", Type: "INTEGER", Expr: docdb.Raw("$.Costs") + " IS NOT NULL"},
				{Name: "cost_inference_seconds", Type: "REAL", Expr: docdb.Raw("$.Costs.inference_seconds")},
				{Name: "cost_builder_seconds", Type: "REAL", Expr: docdb.Raw("$.Costs.builder_seconds")},
				{Name: "cost_builder_pool", Type: "TEXT", Expr: docdb.Raw("$.Costs.builder_pool")},
				{Name: "cost_logs_bytes", Type: "INTEGER", Expr: docdb.Raw("$.Costs.logs_bytes")},
				{Name: "cost_container_bytes", Type: "INTEGER", Expr: docdb.Raw("$.Costs.container_bytes")},
				{Name: "cost_artifact_bytes", Type: "INTEGER", Expr: docdb.Raw("$.Costs.artifact_bytes")},
				{Name: "created", Type: "TEXT", Expr: docdb.RawTime("$.Created"), Stored: true},
				{Name: "started", Type: "TEXT", Expr: docdb.RawTime("$.Started")},
				{Name: "finished", Type: "TEXT", Expr: docdb.RawTime("$.Finished")},
			},
			Indexes: [][]string{{"run_id"}, {"created"}},
		},
		{
			Name: TableRuns,
			Cols: []docdb.Col{{Name: "run_id", Type: "TEXT", Expr: docdb.Doc("$.ID")}},
			PK:   []string{"run_id"},
			GenCols: []docdb.GenCol{
				{Name: "benchmark_name", Type: "TEXT", Expr: docdb.Raw("$.BenchmarkName")},
				{Name: "benchmark_hash", Type: "TEXT", Expr: docdb.Raw("$.BenchmarkHash"), Stored: true},
				{Name: "run_type", Type: "TEXT", Expr: docdb.Raw("$.Type")},
				{Name: "created", Type: "TEXT", Expr: docdb.RawTime("$.Created")},
			},
		},
		{
			Name: TableAgentSessions,
			Cols: []docdb.Col{
				{Name: "session_id", Type: "TEXT", Expr: docdb.Doc("$.ID")},
				{Name: "updated", Type: "TEXT", Expr: docdb.DocTime("$.Updated")},
			},
			PK: []string{"session_id"},
			GenCols: []docdb.GenCol{
				{Name: "run_id", Type: "TEXT", Expr: docdb.Raw("$.RunID")},
				{Name: "ecosystem", Type: "TEXT", Expr: docdb.Raw("$.Target.Ecosystem"), Stored: true},
				{Name: "package", Type: "TEXT", Expr: docdb.Raw("$.Target.Package"), Stored: true},
				{Name: "version", Type: "TEXT", Expr: docdb.Raw("$.Target.Version"), Stored: true},
				{Name: "artifact", Type: "TEXT", Expr: docdb.Raw("$.Target.Artifact")},
				{Name: "status", Type: "TEXT", Expr: docdb.Raw("$.Status"), Stored: true},
				{Name: "stop_reason", Type: "TEXT", Expr: docdb.Raw("$.StopReason"), Stored: true},
				{Name: "execution_mode", Type: "TEXT", Expr: docdb.Raw("$.ExecutionMode")},
				{Name: "scratch_id", Type: "TEXT", Expr: docdb.Raw("$.ScratchID")},
				{Name: "max_iterations", Type: "INTEGER", Expr: docdb.Raw("$.MaxIterations")},
				{Name: "success_iteration", Type: "TEXT", Expr: docdb.Raw("$.SuccessIteration")},
				{Name: "input_tokens", Type: "INTEGER", Expr: docdb.Raw("$.Usage.input")},
				{Name: "cached_input_tokens", Type: "INTEGER", Expr: docdb.Raw("$.Usage.cached_input")},
				{Name: "output_tokens", Type: "INTEGER", Expr: docdb.Raw("$.Usage.output")},
				{Name: "model", Type: "TEXT", Expr: docdb.Raw("$.Usage.model")},
				{Name: "created", Type: "TEXT", Expr: docdb.RawTime("$.Created"), Stored: true},
			},
			Indexes: [][]string{{"ecosystem", "package", "version"}, {"created"}},
		},
		{
			Name: TableAgentIterations,
			Cols: []docdb.Col{
				{Name: "session_id", Type: "TEXT", Expr: docdb.Doc("$.SessionID")},
				{Name: "number", Type: "INTEGER", Expr: docdb.Doc("$.Number")},
				{Name: "updated", Type: "TEXT", Expr: docdb.DocTime("$.Updated")},
			},
			PK: []string{"session_id", "number"},
			GenCols: []docdb.GenCol{
				{Name: "iteration_id", Type: "TEXT", Expr: docdb.Raw("$.ID")},
				{Name: "status", Type: "TEXT", Expr: docdb.Raw("$.Status")},
				{Name: "build_success", Type: "INTEGER", Expr: docdb.Raw("$.Result.build_success")},
				{Name: "error_message", Type: "TEXT", Expr: docdb.Raw("$.Result.error_message")},
				{Name: "input_tokens", Type: "INTEGER", Expr: docdb.Raw("$.Usage.input")},
				{Name: "cached_input_tokens", Type: "INTEGER", Expr: docdb.Raw("$.Usage.cached_input")},
				{Name: "output_tokens", Type: "INTEGER", Expr: docdb.Raw("$.Usage.output")},
				{Name: "model", Type: "TEXT", Expr: docdb.Raw("$.Usage.model")},
				{Name: "created", Type: "TEXT", Expr: docdb.RawTime("$.Created")},
			},
		},
		{
			Name: TableScratchVMs,
			Cols: []docdb.Col{
				{Name: "scratch_id", Type: "TEXT", Expr: docdb.Doc("$.id")},
				{Name: "updated", Type: "TEXT", Expr: docdb.DocTime("$.updated")},
			},
			PK: []string{"scratch_id"},
			GenCols: []docdb.GenCol{
				{Name: "machine_class", Type: "TEXT", Expr: docdb.Raw("$.machine_class")},
				{Name: "state", Type: "TEXT", Expr: docdb.Raw("$.state")},
				{Name: "build_id", Type: "TEXT", Expr: docdb.Raw("$.build_id")},
				{Name: "oblivious_id", Type: "TEXT", Expr: docdb.Raw("$.oblivious_id")},
				{Name: "zone", Type: "TEXT", Expr: docdb.Raw("$.zone")},
				{Name: "created", Type: "TEXT", Expr: docdb.RawTime("$.created")},
				// vm_seconds derives the VM lifetime from record timestamps:
				// every mutator guards on live states, so a deleted
				// scratch's updated stamp is the deletion time. Live or
				// malformed records yield 0. The scratch's target resolves
				// by joining agent_sessions on scratch_id.
				{Name: "vm_seconds", Type: "REAL", Stored: true,
					Expr: "CASE WHEN " + docdb.Raw("$.state") + " = 'deleted' AND created IS NOT NULL AND updated > created" +
						" THEN round((julianday(updated) - julianday(created)) * 86400.0, 3) ELSE 0.0 END"},
			},
		},
		{
			Name: TableScratchExecs,
			Cols: []docdb.Col{
				{Name: "exec_id", Type: "TEXT", Expr: docdb.Doc("$.id")},
				{Name: "updated", Type: "TEXT", Expr: docdb.DocTime("$.updated")},
			},
			PK: []string{"exec_id"},
			GenCols: []docdb.GenCol{
				{Name: "scratch_id", Type: "TEXT", Expr: docdb.Raw("$.scratch_id")},
				{Name: "state", Type: "TEXT", Expr: docdb.Raw("$.state")},
				{Name: "exit_code", Type: "INTEGER", Expr: docdb.Raw("$.exit_code")},
				{Name: "timeout_seconds", Type: "INTEGER", Expr: docdb.Raw("$.timeout_seconds")},
				{Name: "error_message", Type: "TEXT", Expr: docdb.Raw("$.error.message")},
				{Name: "created_at", Type: "TEXT", Expr: docdb.RawTime("$.created_at")},
				{Name: "started_at", Type: "TEXT", Expr: docdb.RawTime("$.started_at")},
				{Name: "finished_at", Type: "TEXT", Expr: docdb.RawTime("$.finished_at")},
				// exec_seconds is the worker-observed command span. Both ends
				// come from the worker clock, so the difference is meaningful.
				// Records without a measured span (pending, or lost before a
				// start was observed) yield NULL rather than 0.
				{Name: "exec_seconds", Type: "REAL", Stored: true,
					Expr: "CASE WHEN started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= started_at" +
						" THEN round((julianday(finished_at) - julianday(started_at)) * 86400.0, 3) END"},
			},
			Indexes: [][]string{{"scratch_id"}},
		},
		{
			Name: TableRepoMetrics,
			Cols: []docdb.Col{
				{Name: "uri", Type: "TEXT", Expr: docdb.Doc("$.uri")},
				{Name: "updated", Type: "TEXT", Expr: docdb.DocTime("$.updated")},
			},
			PK: []string{"uri"},
			GenCols: []docdb.GenCol{
				{Name: "bytes", Type: "INTEGER", Expr: docdb.Raw("$.bytes")},
				{Name: "commits", Type: "INTEGER", Expr: docdb.Raw("$.commits")},
				{Name: "head", Type: "TEXT", Expr: docdb.Raw("$.head")},
			},
		},
		{
			Name: TablePackageSignals,
			Cols: []docdb.Col{
				{Name: "ecosystem", Type: "TEXT", Expr: docdb.Doc("$.Ecosystem")},
				{Name: "package", Type: "TEXT", Expr: docdb.Doc("$.Package")},
			},
			PK: []string{"ecosystem", "package"},
			GenCols: []docdb.GenCol{
				{Name: "dependents", Type: "INTEGER", Expr: docdb.Raw("$.Dependents")},
				{Name: "prevalence", Type: "REAL", Expr: docdb.Raw("$.Prevalence")},
				{Name: "score", Type: "REAL", Expr: docdb.Raw("$.Score"), Stored: true},
			},
		},
		{Name: TableCostObservations, Query: costObservationsQuery, Indexes: [][]string{{"ecosystem", "package"}, {"source"}, {"timestamp"}}},
		{Name: TableEcosystemDaily, Query: ecosystemDailyQuery, Indexes: [][]string{{"ecosystem", "day"}}},
	}
}
