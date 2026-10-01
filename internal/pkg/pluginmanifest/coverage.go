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
}

// Complete reports whether every expectation the case names is served by
// some package.
func (c CaseCoverage) Complete() bool { return len(c.Uncovered) == 0 }

// CapabilityPrefix extracts the resource family from a harness expectation
// like "pg.lock_waits" or "host.host_load".
//
// A case line with no dot is returned whole: it names no family, so
// nothing can be said about coverage and the caller sees a miss rather
// than a panic. A line whose family is not one a package can serve — "pg"
// and "k8s" today, because the middleware adapters are not plugin
// packages — is reported as uncovered, which is true.
func CapabilityPrefix(expectation string) string {
	if i := strings.IndexByte(expectation, '.'); i >= 0 {
		return expectation[:i]
	}
	return expectation
}

// CoverageOf joins one harness case's expectations against a set of
// packages.
//
// An expectation is covered when some package declares a tool whose
// capability family equals the expectation's prefix. That is a coarse
// join, and deliberately so: the alternative — matching method names
// across two vocabularies — would produce false negatives the moment a
// package renames a tool, and a coverage report that is wrong about what
// is missing is worse than one that is merely generous.
func CoverageOf(caseID string, expectations []string, plugins []Plugin) CaseCoverage {
	available := map[string]string{} // capability -> the package that provides it
	for _, p := range plugins {
		for _, cap := range p.Capabilities() {
			if _, taken := available[cap]; !taken {
				available[cap] = p.Name()
			}
		}
	}
	out := CaseCoverage{CaseID: caseID}
	pkgs := map[string]bool{}
	for _, want := range expectations {
		family := CapabilityPrefix(want)
		if name, ok := available[family]; ok {
			out.Covered = append(out.Covered, want)
			pkgs[name] = true
			continue
		}
		out.Uncovered = append(out.Uncovered, want)
	}
	for name := range pkgs {
		out.Packages = append(out.Packages, name)
	}
	sort.Strings(out.Packages)
	sort.Strings(out.Covered)
	sort.Strings(out.Uncovered)
	return out
}

// MiddlewareFamilies are the resource prefixes the golden cases use that
// live in the control plane rather than in a plugin package.
//
// The vocabulary is read from the adapter implementations
// (internal/middleware/adapter/<pkg>/<pkg>.go registers "<pkg>.method" tool
// names), not from the resource directory a case happens to sit in. The two
// differ: the k8s cases name k8s.* tools, but the mq cases name kafka.*
// and rabbitmq.* — the directory is the topic, the prefix is the system —
// so a list built from directory names would fail to place every Kafka and
// RabbitMQ expectation and report them as packages that were never
// supposed to exist.
//
// It is a named list rather than a comment because it is a decision, and
// because it makes the day one of these becomes a plugin package a
// one-line deletion rather than a hunt.
var MiddlewareFamilies = []string{"pg", "redis", "k8s", "mq", "kafka", "rabbitmq"}

// NonPackageFamilies are the prefixes the golden cases use that belong to
// neither a plugin package nor a middleware adapter.
//
// git-artifact is a Linker — a correlation between a deployment and the
// commit that produced it — not a tool family on any host or registry.
// Keeping it apart from MiddlewareFamilies keeps the two claims distinct:
// one says "served elsewhere in the control plane", the other says "not a
// tool family at all". A reader who conflated them would go looking for a
// git adapter to package.
var NonPackageFamilies = []string{"git-artifact"}

// IsMiddlewareFamily reports whether a family is served by a control-plane
// adapter rather than a plugin package.
func IsMiddlewareFamily(family string) bool {
	for _, f := range MiddlewareFamilies {
		if f == family {
			return true
		}
	}
	return false
}

// IsNonPackageFamily reports whether a family is not a tool family at all.
func IsNonPackageFamily(family string) bool {
	for _, f := range NonPackageFamilies {
		if f == family {
			return true
		}
	}
	return false
}

// CoverageReason explains one uncovered expectation, so the printed report
// names the cause rather than only the gap.
func CoverageReason(expectation string) string {
	family := CapabilityPrefix(expectation)
	switch {
	case IsMiddlewareFamily(family):
		return fmt.Sprintf("%s is served by the control plane's %s adapter, which is not a plugin package", expectation, family)
	case IsNonPackageFamily(family):
		return fmt.Sprintf("%s belongs to a control-plane correlation, not a tool family any package could serve", expectation)
	default:
		return fmt.Sprintf("no installed package declares a tool serving %q", family)
	}
}
