package pluginmanifest

import (
	"sort"
)

// The catalog index and the compatibility matrix.
//
// LoadCatalog has existed since the manifest loader did, and until this
// file it had no production caller: the control plane could validate a
// catalog and then had nothing to show it. An index that only tests can
// reach is a class in a library, and an operator asking "what can this fleet
// install" was answered by reading YAML.
//
// Two questions, kept apart because they have different answers:
//
//   - the INDEX: what is on offer. Name, version, targets, safety tier,
//     capabilities, scopes, tool count, install policy.
//   - the MATRIX: for a given node build, what can be installed HERE. The
//     same package is installable on one fleet and refused on another, and
//     the refusal names the component to upgrade.
//
// The matrix is computed with CheckVersions — the same function the node's
// own admission calls. Computing it here with a second implementation
// would make the control plane's pre-flight and the node's refusal able to
// disagree, which is the failure versionmatrix_test.go exists to prevent.

// Entry is one row of the index.
//
// The compatibility question — can THIS node host that package — is not
// answered here. It is answered by core/domains/service/plugin, which
// projects the decision across the real fleet and is what a release is
// gated on. This file answers the quieter question: what is on offer, and
// what does each package say about itself.
//
// That separation was measured rather than preferred. The first version of
// this file also built a grid over hypothetical (edge, pig) pairs, and it
// was a worse answer to a question the control plane already answers per
// node — a fleet's actual versions beat a grid of versions an operator
// invented to look at. The grid is gone; what stays is the index.
type Entry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Vendor  string `json:"vendor,omitempty"`
	// Targets are the planes the package asked to run on.
	Targets []string `json:"targets"`
	// SafetyLevel and Capability are the manifest's own claim about how
	// much damage the package can do, and what its worst tool can do.
	SafetyLevel string `json:"safety_level"`
	Capability  string `json:"capability"`
	// Scopes are the host capabilities the package needs injected.
	Scopes []string `json:"scopes,omitempty"`
	// ToolCount is what the package actually ships, counted from the
	// manifest rather than from a table maintained beside it.
	ToolCount int `json:"tool_count"`
	// Strategy is how the host is meant to roll it out.
	Strategy string `json:"install_strategy,omitempty"`
	// MinEdgeVersion and MinPigVersion are the two host floors. Empty is
	// "did not say", which the domain layer reads as no requirement — so
	// an empty floor is a real state, and UndeclaredFloors below says so
	// in the payload rather than leaving a reader to infer it from an
	// absent field.
	MinEdgeVersion string `json:"min_edge_version,omitempty"`
	MinPigVersion  string `json:"min_pig_version,omitempty"`
	// UndeclaredFloors names the axes this package said nothing about. A
	// row with one is a row the compatibility projection cannot rule on:
	// it will be admitted on a node too old to run it, every time.
	UndeclaredFloors []string `json:"undeclared_floors,omitempty"`
}

// Entries flattens the catalog into the index, sorted by name.
//
// Sorted because this feeds a UI table and an operator's diff between two
// snapshots; an index whose order depends on directory iteration is an index
// that produces spurious changes.
func (c Catalog) Entries() []Entry {
	out := make([]Entry, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		e := Entry{
			Name:           p.Name(),
			Version:        p.Manifest.Metadata.Version,
			Vendor:         p.Manifest.Metadata.Vendor,
			Targets:        targetNames(p.Targets()),
			SafetyLevel:    string(p.Manifest.Spec.SafetyLevel),
			Capability:     p.HighestCapability().String(),
			Scopes:         scopeNames(p.Manifest.Spec.RequiredScopes),
			ToolCount:      len(p.Manifest.Spec.Tools),
			Strategy:       p.Manifest.Spec.Install.Strategy,
			MinEdgeVersion: p.Manifest.Spec.Install.MinEdgeVersion,
			MinPigVersion:  p.Manifest.Spec.Install.MinPigVersion,
		}
		if e.MinEdgeVersion == "" {
			e.UndeclaredFloors = append(e.UndeclaredFloors, "min_edge_version")
		}
		if e.MinPigVersion == "" {
			e.UndeclaredFloors = append(e.UndeclaredFloors, "min_pig_version")
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
