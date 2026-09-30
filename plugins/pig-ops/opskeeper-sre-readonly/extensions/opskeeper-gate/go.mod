// The courier, as the agent runtime builds it on a node.
//
// The canonical source is core/pig/extensions/opskeeper-gate. This copy is
// deliberate: the agent builds extensions from source on the node it runs
// on, and a node has no access to an unpublished OpsKeeper module. A
// package that referenced the canonical path would install and then fail to
// build, which is the worst time to find out.
//
// The two copies are kept identical by TestThePackagedCourierMatchesTheCanonicalSource.
// Editing one without the other fails a test rather than producing a node
// whose gate behaves differently from the one that was reviewed.
module github.com/vincent-wuhan/opskeeper/pig-ops/opskeeper-sre-readonly/extensions/opskeeper-gate

go 1.26.0

require (
	github.com/MichaelKinsy/PiG/extensions/sdk v0.3.0
	github.com/vincent-wuhan/opskeeper/core v0.0.0
)
