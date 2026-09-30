// Module edge is the node plane: what runs on every managed host.
//
// Invariants:
//
//   - edge depends on core (contracts) and, where the agent runtime needs
//     adapting, on pig. It never reaches into another OpsKeeper module's
//     internals.
//   - edge holds no control-plane concern. Identity, approval, audit, and
//     tenancy live in the control plane and reach a node over the tunnel.
//     A node enforces what the control plane told it to enforce; it never
//     decides for itself.
//   - edge MAY import github.com/MichaelKinsy/PiG only through core/pig.
//     Direct PiG imports belong in the pig module, so a PiG upgrade lands
//     in one place.
//
// Subpackages:
//
//	pigsupervisor  the node agent process lifecycle: spawn, restart, health
module github.com/vincent-wuhan/opskeeper/core/edge

go 1.26.0

require github.com/vincent-wuhan/opskeeper/core v0.0.0

// Sibling modules resolve by path during development; the workspace covers
// this in a normal build, the replace keeps a bare module directory
// buildable in CI jobs that disable workspaces.
replace github.com/vincent-wuhan/opskeeper/core => ../
