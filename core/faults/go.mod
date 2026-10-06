// Module faults stages the failures the evaluation plane measures an agent
// against.
//
// It is a module of its own, and the reason is written down rather than
// discovered later. `core/harness` states this as an invariant:
//
//	harness depends on core, core/pig and the standard library.
//	Nothing else. It reaches no database, no HTTP client, and no
//	other Opskeeper module.
//
// An injector cannot live under that rule. A PG lock chain is a set of
// sessions holding row locks; a slow query is a `pg_sleep` on a live
// connection; a held transaction is a BEGIN that never commits. None of
// them is a description of a fault — they are faults, staged against a
// running database. Putting pgx inside the corpus module would have made
// "the golden cases are a portable corpus" false, and it would have done
// it quietly, one `go get` at a time.
//
// So the injector tree moved here. What it may reach:
//
//   - core/harness  for schema.InjectStep, the corpus format it stages from
//   - pgx           the one database client this module exists to hold
//   - the standard library
//
// Nothing else. In particular it reaches no control plane: a fault
// injector that could reach the manager could be talked into injecting
// into production through it.
module github.com/vincent-wuhan/opskeeper/core/faults

go 1.26.0

require (
	github.com/jackc/pgx/v5 v5.8.0
	github.com/vincent-wuhan/opskeeper/core/harness v0.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// Sibling modules resolve by path during development; the workspace covers
// this in a normal build, the replace keeps a bare module directory
// buildable in CI jobs that disable workspaces.
replace (
	github.com/vincent-wuhan/opskeeper/core => ../
	github.com/vincent-wuhan/opskeeper/core/harness => ../harness
	github.com/vincent-wuhan/opskeeper/core/pig => ../pig
)
