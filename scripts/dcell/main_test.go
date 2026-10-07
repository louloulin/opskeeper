package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The census is only worth printing if it can go red. Each test below builds
// the smallest tree that satisfies every predicate, then breaks exactly one
// thing and asserts the corresponding item reports a shortfall. A predicate
// that survives its own mutation is decoration.

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// routesFixture mirrors the six routes the control plane actually registers,
// one line per route, because the predicate counts registrations and a
// fixture that invented a seventh would be testing the fixture.
func routesFixture() string {
	lines := []string{
		`r.With(h.requireAdmin).Post("/v1/plugins/releases", h.start)`,
		`r.With(h.requireAdmin).Get("/v1/plugins/releases", h.list)`,
		`r.With(h.requireAdmin).Get("/v1/plugins/releases/{name}", h.status)`,
		`r.With(h.requireAdmin).Post("/v1/plugins/releases/{name}/advance", h.advance)`,
		`r.With(h.requireAdmin).Post("/v1/plugins/releases/{name}/halt", h.halt)`,
		`r.With(h.requireAdmin).Post("/v1/plugins/releases/{name}/rollback", h.rollback)`,
	}
	return strings.Join(lines, "\n") + "\n"
}

func pkgManifest(name, version, safety string, approval bool, tools ...[2]string) string {
	var b strings.Builder
	b.WriteString("apiVersion: opskeeper.io/v1\nkind: Plugin\nmetadata:\n  name: " + name + "\n  version: " + version + "\n  vendor: opskeeper\n")
	b.WriteString("spec:\n  targets: [edge]\n  safety_level: " + safety + "\n")
	if approval {
		b.WriteString("  approval:\n    required: true\n    blast_radius: pod\n")
	} else {
		b.WriteString("  approval:\n    required: false\n")
	}
	b.WriteString("  tools:\n")
	for _, t := range tools {
		b.WriteString("    - { name: " + t[0] + ", class: " + t[1] + " }\n")
	}
	return b.String()
}

// fixture writes a tree where every predicate holds, so a test only has to
// describe the one thing it breaks.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"opskeeper-sre-readonly", "opskeeper-sre-observability", "opskeeper-sre-middleware"} {
		write(t, root, filepath.Join("plugins", "pig-ops", name, "pig-ops.yaml"),
			pkgManifest(name, "0.1.0", "L1", false, [2]string{"probe_thing", "read"}))
	}
	write(t, root, "plugins/pig-ops/opskeeper-sre-repair/pig-ops.yaml",
		pkgManifest("opskeeper-sre-repair", "0.1.0", "L2", true, [2]string{"apply_change", "write"}))
	write(t, root, "plugins/pig-ops/opskeeper-sre-autonomy/pig-ops.yaml",
		pkgManifest("opskeeper-sre-autonomy", "0.1.0", "L3", true, [2]string{"recovery.execute", "destructive"}))

	write(t, root, "core/domains/server/plugin/http.go", routesFixture())
	write(t, root, "core/manager/biz/marketplace/signature.go", "package marketplace\n\nfunc VerifySignature(packPath string, expectedKey string) (SignatureState, error) { return 0, nil }\n")
	write(t, root, "core/edge/policygate/gate.go", "package policygate\n\nfunc (g *Gate) Admit(ctx context.Context, c Call) (Outcome, string, error) { return 0, \"\", nil }\n")
	write(t, root, "core/manager/server/marketplace/import.go", "package marketplace\n\nfunc (h *Handler) importContainer(w http.ResponseWriter, r *http.Request) {}\n")
	write(t, root, "core/manager/biz/pluginimport/importer.go", "package pluginimport\n\nfunc (i *Importer) Import(opts Options) (*Report, error) { return nil, nil }\n")
	for _, f := range []string{"manifest.go", "register.go", "negotiate.go"} {
		write(t, root, filepath.Join("sdk", f), "package sdk\n")
	}
	write(t, root, "core/floor/pluginmanifest/coverage.go", "package pluginmanifest\n\nvar DiagnosisGaps = []struct{ Reason, Searched string }{\n\t{Reason: \"r\", Searched: []string{\"core/edge\", \"core/floor\"}},\n}\n")
	write(t, root, "Makefile", "eval-gates:\npig-tool-scoping-check:\n")
	write(t, root, "scripts/sync-pig-ops.sh", "#!/bin/sh\n")
	return root
}

func byID(t *testing.T, id string) item {
	t.Helper()
	for _, it := range items() {
		if it.id == id {
			return it
		}
	}
	t.Fatalf("no item %s", id)
	return item{}
}

func assertHolds(t *testing.T, root, id string) {
	t.Helper()
	short, err := byID(t, id).check(root)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", id, err)
	}
	if short != "" {
		t.Fatalf("%s should hold on the fixture, got: %s", id, short)
	}
}

func assertBreaks(t *testing.T, root, id, rel, body string) {
	t.Helper()
	if rel != "" {
		write(t, root, rel, body)
	}
	short, err := byID(t, id).check(root)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", id, err)
	}
	if short == "" {
		t.Fatalf("%s still holds after breaking %s — the predicate is decoration", id, rel)
	}
}

func TestFixtureIsGreen(t *testing.T) {
	root := fixture(t)
	for _, it := range items() {
		assertHolds(t, root, it.id)
	}
}

func TestEveryPredicateCanGoRed(t *testing.T) {
	cases := []struct {
		id, rel, body string
	}{
		{"D1", "plugins/pig-ops/opskeeper-sre-readonly/pig-ops.yaml",
			"apiVersion: opskeeper.io/v2\nkind: Plugin\nmetadata:\n  name: opskeeper-sre-readonly\n  version: 0.1.0\nspec:\n  tools: []\n"},
		{"D2", "plugins/pig-ops/opskeeper-sre-observability/pig-ops.yaml",
			pkgManifest("opskeeper-sre-observability", "0.1.0", "L1", false, [2]string{"query_promql", "read"}, [2]string{"restart_thing", "write"})},
		{"D3", "plugins/pig-ops/opskeeper-sre-autonomy/pig-ops.yaml",
			pkgManifest("opskeeper-sre-autonomy", "0.1.0", "L2", true, [2]string{"recovery.execute", "write"})},
		{"D4", "plugins/pig-ops/opskeeper-sre-repair/pig-ops.yaml",
			pkgManifest("opskeeper-sre-repair", "0.1.0", "L2", false, [2]string{"apply_change", "write"})},
		{"D5", "core/domains/server/plugin/http.go",
			"\tr.With(h.requireAdmin).Post(\"/v1/plugins/releases\", h.start)\n"},
		{"D6", "core/manager/biz/marketplace/signature.go", "package marketplace\n"},
		{"D7", "core/edge/policygate/gate.go", "package policygate\n"},
		{"D8", "core/manager/server/marketplace/import.go", "package marketplace\n"},
		{"D9", "sdk/negotiate.go", ""},
		{"D10", "core/floor/pluginmanifest/coverage.go", "package pluginmanifest\n"},
		{"D11", "Makefile", "eval-gates:\n"},
		{"D12", "scripts/sync-pig-ops.sh", ""},
	}
	if len(cases) != len(items()) {
		t.Fatalf("%d mutation cases for %d items", len(cases), len(items()))
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			root := fixture(t)
			if tc.body == "" {
				if err := os.Remove(filepath.Join(root, tc.rel)); err != nil {
					t.Fatalf("remove %s: %v", tc.rel, err)
				}
				short, err := byID(t, tc.id).check(root)
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", tc.id, err)
				}
				if short == "" {
					t.Fatalf("%s still holds after deleting %s", tc.id, tc.rel)
				}
				return
			}
			assertBreaks(t, root, tc.id, tc.rel, tc.body)
		})
	}
}

// A package added to the roster is a claim about the ecosystem, and a claim
// nobody checked is the failure mode this file exists to end. So the census
// refuses to run when the directory and the roster disagree.
func TestRosterMatchesTheTree(t *testing.T) {
	entries, err := os.ReadDir("../../plugins/pig-ops")
	if err != nil {
		t.Fatalf("read plugins/pig-ops: %v", err)
	}
	found := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			found[e.Name()] = true
		}
	}
	for _, name := range roster {
		if !found[name] {
			t.Errorf("rostered package %s is not on disk", name)
		}
		delete(found, name)
	}
	var extra []string
	for name := range found {
		extra = append(extra, name)
	}
	if len(extra) > 0 {
		t.Errorf("packages on disk but not on the roster: %v — add them on purpose", extra)
	}
}

// The census is run against this repository as part of the suite, so a
// package deleted or a route removed fails the build rather than quietly
// lowering a number in a document nobody re-reads.
func TestCensusOfThisTreeIsGreen(t *testing.T) {
	root := "../.."
	for _, it := range items() {
		short, err := it.check(root)
		if err != nil {
			t.Errorf("%s %s: %v", it.id, it.subject, err)
			continue
		}
		if short != "" {
			t.Errorf("%s %s: %s", it.id, it.subject, short)
		}
	}
}
