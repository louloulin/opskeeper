package pluginmanifest

import (
	"fmt"
	"sort"
	"strings"
)

// What a plugin package can actually do, for the purposes of regression
// scoring.
//
// The harness scores an incident case against the tools the agent called.
// A case's expectation is written as "<domain>.<method>" — pg.lock_waits,
// host.host_load — naming the *capability*, not the package that offers it.
// This file is the join between the two vocabularies: given a package's
// manifest, which capabilities does it add to a node.
//
// It exists because the join is not mechanical. A package's tools are
// named the way the model calls them (host_probe_tcp, query_promql), while
// a case names the middleware method behind them (host.host_probe_tcp? no —
// host.* is the host skill family), and the two vocabularies were written
// by different people at different times. Guessing the mapping would make
// the coverage report a description of the guess rather than of the
// packages, so the mapping is declared here, once, and drift between it
// and the shipped packages is a test failure rather than a wrong number.

// Capability tags a package's tools are grouped under.
//
// They are the resource prefixes the harness case files already use —
// "host", "pg", "k8s" — so a case's root_cause_lines can be matched
// against a package without a second translation table.
const (
	// CapHost is the local-host probe family: dmesg, lsof, journal, and
	// the rest of what a node learns about itself.
	CapHost = "host"
	// CapTopology is the control plane's graph.
	CapTopology = "topology"
	// CapAlert is alert-rule and incident history.
	CapAlert = "alert"
	// CapObservability is the metric / log / trace backends.
	CapObservability = "observability"
	// CapDatabase is the registered database sources.
	CapDatabase = "database"
	// CapSource is the code repository registry.
	CapSource = "source"
	// CapPostgres is the live PostgreSQL adapter: sessions, lock chains,
	// bloat, vacuum and replication state. It is separate from CapDatabase,
	// which is the *registered sources* view from exporter metrics — one is
	// asked of the instance, the other of what OpsKeeper recorded about it.
	CapPostgres = "pg"
	// CapRedis is the live Redis adapter: key census, memory shape, slow log.
	CapRedis = "redis"
	// CapK8s is the live Kubernetes adapter: pods, rollouts, nodes, events.
	CapK8s = "k8s"
	// CapMQ is the neutral broker adapter, for deployments wired through it
	// rather than through a vendor-specific one.
	CapMQ = "mq"
	// CapKafka and CapRabbitMQ are the two vendor adapters. They are their
	// own families because a case names the broker it is about — a
	// kafka.consumer_lag expectation is not answered by a RabbitMQ tool —
	// and the directory a case sits in is the topic, not the system.
	CapKafka    = "kafka"
	CapRabbitMQ = "rabbitmq"
	// CapGitArtifact is the git-artifact linker: a runtime symbol (a
	// PostgreSQL query, a Redis command, a Kubernetes image, an HTTP route)
	// resolved back to the commit and file:line that produced it.
	//
	// It is its own family because it is not "git" — the git adapter's other
	// tools read a repository, and this one reads a correlation index. It is
	// also the case that made the capability table's doc comment true: the
	// tool is git.find_runtime_link and the family is git-artifact, so a map
	// that guessed from the prefix would have filed it under "git" and
	// silently left the k8s/pod-oom case uncovered.
	CapGitArtifact = "git-artifact"

	// CapRecovery is the bounded-remediation dispatcher: a reserved,
	// approved action chosen from a fixed catalogue rather than composed
	// by the model. It is its own family because it is not "host" —
	// what it does depends on the action it was asked for — and folding
	// it into host would make every host case claim coverage by a tool
	// that may well have restarted something instead.
	CapRecovery = "recovery"
)

// toolCapabilities maps a deterministic tool name to the capability family
// it serves.
//
// The map is the honest part of this file. Each entry was read off the
// extension that registers the tool, not inferred from its name — which is
// why it is spelled out rather than derived. `analyze_database_status` is
// database, `list_metric_catalog` is observability, and a rule that guessed
// from the prefix would get `list_database_sources` right and
// `query_change_events` wrong.
var toolCapabilities = map[string]string{
	// --- opskeeper-sre-readonly ---
	"expand_topology":    CapTopology,
	"find_outlier_edges": CapTopology,
	"find_topology_node": CapTopology,
	"get_topology":       CapTopology,
	"query_alert_rules":  CapAlert,
	"host_dmesg":         CapHost,
	"host_grep_file":     CapHost,
	"host_lsof":          CapHost,
	"host_mtr":           CapHost,
	"host_netns_inspect": CapHost,
	"host_probe_dns":     CapHost,
	"host_probe_http":    CapHost,
	"host_probe_tcp":     CapHost,
	"host_read_journal":  CapHost,
	"host_sosreport":     CapHost,
	"host_strace":        CapHost,
	"host_tail_file":     CapHost,
	"host_traceroute":    CapHost,

	// --- opskeeper-sre-observability ---
	"analyze_database_status": CapDatabase,
	"get_edge_summary":        CapHost,
	"get_host_load":           CapHost,
	"grep_source":             CapSource,
	"list_database_sources":   CapDatabase,
	"list_metric_catalog":     CapObservability,
	"list_repo_sources":       CapSource,
	"query_change_events":     CapAlert,
	"query_logql":             CapObservability,
	"query_promql":            CapObservability,
	"query_traceql":           CapObservability,
	"read_source":             CapSource,

	// --- opskeeper-sre-middleware ---
	//
	// These names are the adapters' own, read off a live registration by
	// core/manager/middleware/toolset rather than inferred, and the entries are
	// written out for the reason the top of this file gives: the map is the
	// place a claim about a package is written down, and a rule derived from
	// the prefix would be a second, silent opinion about it.
	// TestMiddlewareFamiliesComeFromTheAdapters (core/manager/middleware/toolset)
	// and TestTheMiddlewareFamiliesMatchTheAdapters (cmd/opskeeper-eval,
	// which may import both sides) fail if these entries and the adapters'
	// prefixes disagree.
	"pg.active_sessions":        CapPostgres,
	"pg.connect":                CapPostgres,
	"pg.explain_query":          CapPostgres,
	"pg.index_usage":            CapPostgres,
	"pg.list_databases":         CapPostgres,
	"pg.list_schemas":           CapPostgres,
	"pg.list_tables":            CapPostgres,
	"pg.lock_waits":             CapPostgres,
	"pg.long_running_txns":      CapPostgres,
	"pg.replication_status":     CapPostgres,
	"pg.slow_log":               CapPostgres,
	"pg.table_bloat":            CapPostgres,
	"pg.top_queries_by_calls":   CapPostgres,
	"pg.top_queries_by_time":    CapPostgres,
	"pg.vacuum_status":          CapPostgres,
	"redis.big_keys":            CapRedis,
	"redis.blocked_clients":     CapRedis,
	"redis.client_list":         CapRedis,
	"redis.cluster_info":        CapRedis,
	"redis.config_get":          CapRedis,
	"redis.connect":             CapRedis,
	"redis.dbsize":              CapRedis,
	"redis.fragmentation_ratio": CapRedis,
	"redis.info":                CapRedis,
	"redis.key_space":           CapRedis,
	"redis.memory_usage":        CapRedis,
	"redis.slow_log":            CapRedis,
	"k8s.cluster_info":          CapK8s,
	"k8s.connect":               CapK8s,
	"k8s.deployment_status":     CapK8s,
	"k8s.events":                CapK8s,
	"k8s.node_list":             CapK8s,
	"k8s.pod_list":              CapK8s,
	"k8s.pod_logs":              CapK8s,
	"k8s.pvc_list":              CapK8s,
	"k8s.pvc_usage":             CapK8s,
	"k8s.rollout_history":       CapK8s,
	"k8s.rollout_status":        CapK8s,
	"k8s.top_nodes":             CapK8s,
	"k8s.top_pods":              CapK8s,
	"kafka.broker_skew":         CapKafka,
	"kafka.consumer_lag":        CapKafka,
	"kafka.partition_skew":      CapKafka,
	"kafka.topic_list":          CapKafka,
	"rabbitmq.cluster_info":     CapRabbitMQ,
	"rabbitmq.consumer_status":  CapRabbitMQ,
	"rabbitmq.queue_depth":      CapRabbitMQ,
	"rabbitmq.queue_list":       CapRabbitMQ,
	"mq.broker_status":          CapMQ,
	"mq.connect":                CapMQ,
	"mq.inspect_consumer_lag":   CapMQ,
	"mq.queue_list":             CapMQ,
	"git.find_runtime_link":     CapGitArtifact,

	// --- opskeeper-sre-repair ---
	"apply_config_change":  CapAlert,
	"draft_config_change":  CapAlert,
	"host_restart_service": CapHost,
	"verify_recovery":      CapHost,
	"recovery.execute":     CapRecovery,
}

// CapabilitiesOf returns the capability families a package's tools serve,
// sorted, with duplicates removed.
func (p Plugin) Capabilities() []string {
	seen := map[string]bool{}
	for _, t := range p.Manifest.Spec.Tools {
		if c, ok := toolCapabilities[t.Name]; ok {
			seen[c] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// UnmappedTools returns the tools of p that have no capability entry.
//
// This is deliberately not an error inside Capabilities: a third-party
// package will always have tools nobody wrote an entry for, and refusing
// its manifest over that would make the SDK unusable. But a *first-party*
// package with an unmapped tool is a real gap — a case that exercises it
// would score as uncovered — so the drift test asserts this list is empty
// for everything under plugins/pig-ops.
func (p Plugin) UnmappedTools() []string {
	var out []string
	for _, t := range p.Manifest.Spec.Tools {
		if _, ok := toolCapabilities[t.Name]; !ok {
			out = append(out, t.Name)
		}
	}
	return out
}

// CaseCoverage says which packages can serve one harness case, and which of
// the case's expectations nothing can serve.
//
// It is what turns the 20 golden cases from a set of scenarios into a
// coverage report: a case whose root_cause_lines name capabilities no
// installed package provides is a case the fleet is structurally unable to
// pass, and that is a fact worth failing a build over rather than
// discovering from a leaderboard score.
type CaseCoverage struct {
	CaseID string
	// Packages are the plugin names that provide at least one of the
	// capabilities the case names, sorted.
	Packages []string
	// Covered is the subset of the case's expectations some package can
	// serve.
	Covered []string
	// Uncovered is the rest.
	Uncovered []string
	// Reasons parallels Uncovered, one entry per uncovered expectation, and
	// is the only part of this report that can be acted on. "0/20 cases
	// covered" is a number somebody argues with; "pg.kill_session is not
	// packaged because every shipped package is read-only" is a package
	// somebody writes.
	Reasons []string
}

// Complete reports whether every expectation the case names is served by
// some package.
func (c CaseCoverage) Complete() bool { return len(c.Uncovered) == 0 }

// CapabilityPrefix extracts the resource family from a harness expectation
// like "pg.lock_waits" or "host.host_load".
//
// A case line with no dot is returned whole: it names no family, so
// nothing can be said about coverage and the caller sees a miss rather
// than a panic. A line whose family no package serves is reported as
// uncovered, which is true. Since the middleware package shipped, that set
// is small — "host" and "git", the two families still served only by the
// control plane — and each is explained in MiddlewareFamilies rather than
// left to read as an oversight.
func CapabilityPrefix(expectation string) string {
	if i := strings.IndexByte(expectation, '.'); i >= 0 {
		return expectation[:i]
	}
	return expectation
}

// ExpectationAliases maps a harness expectation onto the shipped tool that
// serves it, for the expectations that do not literally name a tool.
//
// Most expectations need no entry: the middleware package names its tools
// the way the cases name their capabilities (pg.lock_waits is both), so an
// exact match is the right answer and the overwhelming majority of the
// report is produced without consulting this table at all.
//
// An entry belongs here only when the capability genuinely ships under a
// different name, and only when the target is a tool a shipped package
// declares today. A rename between a case and an adapter that is not
// packaged yet is deliberately *not* an entry: the honest report line for
// redis.kill_client is that no package offers it, and aliasing it to the
// adapter's redis.client_kill would report a capability as covered by
// something that is not on any node.
//
// Each entry is a claim somebody can be wrong about, so both directions are
// tested: an alias whose target no package ships, and a shipped tool that
// two expectations both point at for different reasons, both fail.
var ExpectationAliases = map[string]string{
	// The observability package's fleet-wide host load is what a
	// host/cpu-spike case means by host.host_load. The adapter's own name
	// for the same read is host.load_average; the package offers the
	// control plane's, because the node has no business opening a
	// connection to a host-exporter to learn its own load.
	"host.host_load": "get_host_load",

	// The git-artifact linker is reached through one tool. The case names
	// the linker's API because that is the capability under test; the
	// package ships the adapter that carries it.
	"git-artifact.LinkK8sImage": "git.find_runtime_link",
}

// ToolServing reports the tool that satisfies an expectation, and the
// package that ships it.
//
// The expectation is taken literally first — that is the join for the
// middleware package and for every case whose vocabulary matches the tool
// names — and only then through ExpectationAliases. A literal match always
// wins, so an alias can never shadow a tool that actually exists under the
// name the case used.
func ToolServing(expectation string, byTool map[string]string) (tool, pkg string, ok bool) {
	if name, found := byTool[expectation]; found {
		return expectation, name, true
	}
	if alias, aliased := ExpectationAliases[expectation]; aliased {
		if name, found := byTool[alias]; found {
			return alias, name, true
		}
	}
	return "", "", false
}

// fleetIndex is the two joins the report needs: tool name to the package
// that ships it, and family to the packages that serve that family at all.
type fleetIndex struct {
	byTool   map[string]string
	byFamily map[string][]string
}

func indexFleet(plugins []Plugin) fleetIndex {
	idx := fleetIndex{byTool: map[string]string{}, byFamily: map[string][]string{}}
	for _, p := range plugins {
		for _, t := range p.Manifest.Spec.Tools {
			if _, taken := idx.byTool[t.Name]; !taken {
				idx.byTool[t.Name] = p.Name()
			}
			family := CapabilityPrefix(t.Name)
			if !contains(idx.byFamily[family], p.Name()) {
				idx.byFamily[family] = append(idx.byFamily[family], p.Name())
			}
		}
	}
	for family := range idx.byFamily {
		sort.Strings(idx.byFamily[family])
	}
	return idx
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// CoverageOf joins one harness case's expectations against a set of
// packages, at the method rather than the family.
//
// The join used to compare a case's family against the families its
// packages serve, and that reported the whole shipped fleet as covering all
// twenty cases while zero of them were actually coverable. The read-only
// packages ship pg.lock_waits, so pg.kill_session counted as covered; the
// same held for every other remediation expectation in the suite, which is
// to say for every write the fleet has deliberately not shipped yet. A
// report that cannot distinguish "we serve this" from "we serve something
// else in the same family" is worse than no report, because it is believed.
//
// So the join is on the tool name. An expectation is covered when a package
// declares a tool with that name, or a tool ExpectationAliases names as
// the one that serves it. Both are exact: nothing here infers a capability
// from a shared prefix.
func CoverageOf(caseID string, expectations []string, plugins []Plugin) CaseCoverage {
	idx := indexFleet(plugins)
	// Reasons is kept as a parallel slice, so the two have to be sorted as
	// one list. Sorting Uncovered on its own would leave every reason
	// describing a different expectation than the line it is printed under,
	// which is the one mistake this report cannot make: the reason is the
	// part a reader acts on, and a reason attached to the wrong gap sends
	// them to package the wrong tool.
	type gap struct {
		expectation string
		reason      string
	}
	var gaps []gap
	out := CaseCoverage{CaseID: caseID}
	pkgs := map[string]bool{}
	for _, want := range expectations {
		if _, name, ok := ToolServing(want, idx.byTool); ok {
			out.Covered = append(out.Covered, want)
			pkgs[name] = true
			continue
		}
		gaps = append(gaps, gap{expectation: want, reason: explainGap(want, idx)})
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].expectation < gaps[j].expectation })
	for _, g := range gaps {
		out.Uncovered = append(out.Uncovered, g.expectation)
		out.Reasons = append(out.Reasons, g.reason)
	}
	for name := range pkgs {
		out.Packages = append(out.Packages, name)
	}
	sort.Strings(out.Packages)
	sort.Strings(out.Covered)
	return out
}

// explainGap says why one expectation is not served.
//
// The three cases are kept apart deliberately. "A package serves this family
// but not this method" is a packaging decision to revisit. "This family is
// the control plane's adapter and no package offers it" is a decision
// somebody already made. "No package serves this family at all" is a case
// written for a tool that does not exist. Collapsing them — which is what
// the family-level join did to all three — leaves a reader with a number
// and no idea whether to write a package, change a manifest, or fix a case.
func explainGap(expectation string, idx fleetIndex) string {
	family := CapabilityPrefix(expectation)
	if serving := idx.byFamily[family]; len(serving) > 0 {
		return fmt.Sprintf("the %s family is served by %s, which ships no tool named %s",
			family, strings.Join(serving, ", "), expectation)
	}
	if IsMiddlewareFamily(family) {
		// True for the two families that remain: the adapter exists in the
		// control plane and no shipped package offers it. The sentence used
		// to end "which is not a plugin package" — that stopped being the
		// right explanation when the middleware adapters became one, so it
		// now says the narrower, still-true thing: nothing shipped offers
		// *this* name.
		return fmt.Sprintf("%s is served by the control plane's %s adapter, and no shipped package offers it", expectation, family)
	}
	return fmt.Sprintf("no installed package declares a tool serving %q", family)
}

// MiddlewareFamilies are the resource prefixes the golden cases use that
// still have no plugin package offering them.
//
// It used to be pg, redis, k8s, mq, kafka and rabbitmq. Those six became
// the opskeeper-sre-middleware package, and deleting them from this list is
// the one-line change this comment promised when it was first written: the
// adapter is still the implementation, but a package now offers it, so the
// report credits the package instead of explaining the gap.
//
// Two entries remain, and both are decisions rather than oversights:
//
//   - host — the host adapter executes as root on the machine OpsKeeper
//     exists to keep alive, over a local:// or ssh:// target. The `host`
//     family is already served by the read-only package's own probes and by
//     get_host_load, so its reads are a second route to an answer the fleet
//     already has, and its writes belong to the approval path.
//   - git — the git adapter's repository reads duplicate the observability
//     package's source family. Its one non-duplicate, the git-artifact
//     linker, is packaged and is its own family (see CapGitArtifact), which
//     is why "git" here does not mean "the git adapter is unpackaged".
//
// core/manager/middleware/toolset records the same exclusions next to the code
// that enforces them, and a test in cmd/opskeeper-eval — the only package
// that may import both sides — fails if the two lists stop agreeing.
//
// The vocabulary is read from the adapter implementations
// (core/manager/middleware/adapter/<pkg>/<pkg>.go registers "<pkg>.method" tool
// names), not from the resource directory a case happens to sit in. The two
// differ: the k8s cases name k8s.* tools, but the mq cases name kafka.*
// and rabbitmq.* — the directory is the topic, the prefix is the system —
// so a list built from directory names would fail to place every Kafka and
// RabbitMQ expectation and report them as packages that were never supposed
// to exist.
var MiddlewareFamilies = []string{string(CapHost), "git"}

// IsMiddlewareFamily reports whether a family is served by a control-plane
// adapter that no shipped package offers.
func IsMiddlewareFamily(family string) bool {
	for _, f := range MiddlewareFamilies {
		if f == family {
			return true
		}
	}
	return false
}

// CoverageReason explains one uncovered expectation by *family*, so a
// report built outside CoverageOf — the family drift test walks the case
// files directly — can still name the cause rather than only the gap.
//
// It is the family-level explanation and nothing more. The method-level
// reason, which is the one that can be acted on, is CaseCoverage.Reasons
// and is computed where the fleet is known.
func CoverageReason(expectation string) string {
	family := CapabilityPrefix(expectation)
	switch {
	case IsMiddlewareFamily(family):
		// True for the two families that remain: the adapter exists in the
		// control plane and no shipped package offers it. The sentence used
		// to end "which is not a plugin package" — that stopped being the
		// right explanation when the middleware adapters became one, so it
		// now says the narrower, still-true thing: nothing shipped offers
		// *this* name.
		return fmt.Sprintf("%s is served by the control plane's %s adapter, and no shipped package offers it", expectation, family)
	default:
		return fmt.Sprintf("no installed package declares a tool serving %q", family)
	}
}
