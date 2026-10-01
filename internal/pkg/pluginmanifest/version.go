package pluginmanifest

import (
	"fmt"
	"strconv"
	"strings"
)

// Comparing the version a package needs against the version a node runs.
//
// This is the PiG × edge compatibility matrix, reduced to the one edge of
// it a node can check for itself. A package declares min_edge_version; the
// node knows what it is; the comparison happens on the node rather than in
// the manager, for the same reason the review does — the manager may ask
// for a package, but only the node can say whether *it* can run it.
//
// Dotted numeric comparison is deliberately not semver. The versions on
// this fleet come from build metadata: "0.8.0", "0.7.43", and on a
// developer machine "dev". A semver library would reject the last one and
// the honest answer for it is "unknown, so refuse" — not "assume it is
// old". Handling pre-release tags would add a rule that no version in this
// repository uses, and a rule nobody exercises is a rule nobody can trust.

// CompareVersions compares two dotted numeric versions.
//
// It returns -1 when a < b, 0 when they are equal, and 1 when a > b. The
// second return is false when either input is not a dotted numeric version
// — "dev", "", "0.8.0-rc1", "1.2.x". Callers must treat that as "cannot
// tell", which for an admission check means refuse: a node that guessed
// here would either run a package it cannot host or refuse one it can, and
// only one of those is recoverable.
//
// Missing components count as zero, so "0.8" and "0.8.0" are equal. That
// is what a person writing "0.8" means, and the alternative — treating a
// short version as unparseable — would refuse a package over a trailing
// zero.
func CompareVersions(a, b string) (int, bool) {
	as, ok := parseVersion(a)
	if !ok {
		return 0, false
	}
	bs, ok := parseVersion(b)
	if !ok {
		return 0, false
	}
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		switch {
		case av < bv:
			return -1, true
		case av > bv:
			return 1, true
		}
	}
	return 0, true
}

// parseVersion splits a dotted numeric version into its components.
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		// A leading "v" is how a git tag is spelled, and a package author
		// who wrote one meant the version. It is stripped on the first
		// component only, so "1.v2.3" stays unparseable.
		if len(out) == 0 {
			p = strings.TrimPrefix(p, "v")
		}
		if p == "" {
			return nil, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// MeetsMinEdgeVersion reports whether a node running nodeVersion may host a
// package that requires minEdgeVersion.
//
// An empty requirement means the package did not say, and the answer is
// yes: refusing every package that omits an optional field would make the
// field mandatory by accident. A version that cannot be compared — either
// side — is a refusal, and the returned reason says which side was
// unreadable so an operator is not sent to inspect a working node.
func MeetsMinEdgeVersion(minEdgeVersion, nodeVersion string) (bool, string) {
	minEdgeVersion = strings.TrimSpace(minEdgeVersion)
	if minEdgeVersion == "" {
		return true, ""
	}
	if _, ok := parseVersion(minEdgeVersion); !ok {
		return false, fmt.Sprintf("the package asks for edge version %q, which is not a version this node can compare", minEdgeVersion)
	}
	if _, ok := parseVersion(nodeVersion); !ok {
		// The node's own version is unknown. It might well be new enough;
		// nobody can say. Refusing names the real problem — the node
		// cannot state what it is — instead of pretending the package is
		// the thing at fault.
		return false, fmt.Sprintf("this node reports its agent version as %q, which is not a version, so it cannot tell whether it is new enough for a package needing %s",
			nodeVersion, minEdgeVersion)
	}
	cmp, ok := CompareVersions(nodeVersion, minEdgeVersion)
	if !ok {
		return false, fmt.Sprintf("cannot compare edge version %q with the required %s", nodeVersion, minEdgeVersion)
	}
	if cmp < 0 {
		return false, fmt.Sprintf("this node runs edge %s, but the package needs at least %s", nodeVersion, minEdgeVersion)
	}
	return true, ""
}

// MeetsMinPigVersion reports whether a node whose agent binary is pigVersion
// may host a package that requires minPigVersion.
//
// It is the second axis of the same matrix as MeetsMinEdgeVersion, and it
// is deliberately a separate function rather than a table-driven loop over
// two requirements: the two sides differ in what an unreadable value means.
// A node that cannot state its *edge* version has a provisioning problem;
// a node that cannot state its *agent* version has a different one, and the
// refusal has to name which, or an operator upgrades the wrong component.
//
// pigVersion is the version of the `pig` binary this node launches, not the
// version of the edge agent around it. They are reported by different
// things — the edge from its build metadata, the agent from whatever
// answers on its stdio — and a node that conflated them would enforce a
// requirement against a number that has nothing to do with it.
func MeetsMinPigVersion(minPigVersion, pigVersion string) (bool, string) {
	minPigVersion = strings.TrimSpace(minPigVersion)
	if minPigVersion == "" {
		// Same rule as the edge axis: an optional field left out is not a
		// requirement, and every package written before this field
		// existed must keep installing.
		return true, ""
	}
	if _, ok := parseVersion(minPigVersion); !ok {
		return false, fmt.Sprintf("the package asks for PiG version %q, which is not a version this node can compare", minPigVersion)
	}
	if _, ok := parseVersion(pigVersion); !ok {
		// The agent's own version is unknown, so the node cannot tell. The
		// message names the agent rather than the edge so nobody upgrades
		// the wrong binary chasing it.
		return false, fmt.Sprintf("this node reports its PiG agent version as %q, which is not a version, so it cannot tell whether it is new enough for a package needing %s",
			pigVersion, minPigVersion)
	}
	cmp, ok := CompareVersions(pigVersion, minPigVersion)
	if !ok {
		return false, fmt.Sprintf("cannot compare PiG version %q with the required %s", pigVersion, minPigVersion)
	}
	if cmp < 0 {
		return false, fmt.Sprintf("this node runs PiG %s, but the package needs at least %s", pigVersion, minPigVersion)
	}
	return true, ""
}
