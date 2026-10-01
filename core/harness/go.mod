// Module harness is the evaluation plane: the golden-case corpus, the
// injectors that stage a failure, the judges that score a response, and
// the loop that runs a whole incident end to end.
//
// Invariants:
//
//   - harness depends on core (contracts) and on the standard library.
//     Nothing else. It reaches no database, no HTTP client, no provider
//     SDK, and no other OpsKeeper module.
//   - The one capability it needs from outside — a model completion — is
//     taken as ports.Completer, injected by the caller. The harness never
//     learns how a provider is configured, and a rubric that could import
//     a provider client would drag that client's whole dependency graph
//     into every evaluation run.
//   - harness holds no production logic. Nothing in the control plane
//     imports it to make a decision; it is read by the eval command and by
//     tests.
//
// The zero-dependency property is the point, not a coincidence: it is what
// lets a third party fork the corpus and run it without resolving the
// server.
module github.com/vincent-wuhan/opskeeper/core/harness

go 1.26.0

require github.com/vincent-wuhan/opskeeper/core v0.0.0

// Sibling modules resolve by path during development; the workspace covers
// this in a normal build, the replace keeps a bare module directory
// buildable in CI jobs that disable workspaces.
replace github.com/vincent-wuhan/opskeeper/core => ../
