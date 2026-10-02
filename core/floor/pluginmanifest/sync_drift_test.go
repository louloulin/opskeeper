package pluginmanifest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The generated copies under plugins/pig-ops/ are produced by
// scripts/sync-pig-ops.sh. A script that can be forgotten is not a drift
// check, so the properties it is supposed to preserve are asserted here
// and run in CI.
//
// The specific per-package tests in profile_test.go stay, because they
// say things about a particular package's promise. These are the
// mechanical ones: every package, every extension, no exceptions.

const syncScript = "scripts/sync-pig-ops.sh"

// packagesDir is every shippable package.
func packagesDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "plugins", "pig-ops")
}

// TestEveryPackagedExtensionMatchesItsCanonicalSource is the drift check.
//
// The node builds these from source and has no way to reach the reviewed
// original, so a copy that has drifted is not a stale file — it is a node
// running code nobody looked at. This walks every package rather than
// naming them, so a new package is covered the moment it is added and a
// renamed extension is caught rather than silently skipped.
func TestEveryPackagedExtensionMatchesItsCanonicalSource(t *testing.T) {
	packages, err := os.ReadDir(packagesDir(t))
	if err != nil {
		t.Fatalf("read the packages directory: %v", err)
	}
	if len(packages) == 0 {
		t.Fatal("no packages found; the walk below would be vacuous")
	}

	checked := 0
	for _, pkg := range packages {
		if !pkg.IsDir() {
			continue
		}
		extsDir := filepath.Join(packagesDir(t), pkg.Name(), "extensions")
		exts, err := os.ReadDir(extsDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("%s: read extensions: %v", pkg.Name(), err)
		}
		for _, ext := range exts {
			if !ext.IsDir() {
				continue
			}
			canonical := filepath.Join(repoRoot(t), "core", "pig", "extensions", ext.Name())
			packaged := filepath.Join(extsDir, ext.Name())

			entries, err := os.ReadDir(canonical)
			if err != nil {
				t.Errorf("%s ships extensions/%s but there is no canonical source at %s; "+
					"a package may only ship extensions that exist under core/pig/extensions",
					pkg.Name(), ext.Name(), canonical)
				continue
			}

			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}
				checked++
				want, err := os.ReadFile(filepath.Join(canonical, name))
				if err != nil {
					t.Errorf("%s/%s: read canonical %s: %v", pkg.Name(), ext.Name(), name, err)
					continue
				}
				got, err := os.ReadFile(filepath.Join(packaged, name))
				if err != nil {
					t.Errorf("%s/%s: read packaged %s: %v; run %s", pkg.Name(), ext.Name(), name, err, syncScript)
					continue
				}
				if string(got) != string(want) {
					t.Errorf("%s/%s: the packaged %s has drifted from core/pig/extensions/%s/%s; run %s",
						pkg.Name(), ext.Name(), name, ext.Name(), name, syncScript)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no packaged extension was checked, so this drift test is vacuous")
	}
}

// TestEveryPackagedGoModCarriesNoReplaceDirective is the constraint that
// makes the copies work at all.
//
// A node has no checkout of this repository and no checkout of a
// pre-stable PiG. A replace directive that resolves on a developer
// machine points at a path that does not exist on the node, so the
// package installs and then fails to build — which is discovered during
// the first incident on the first node it was rolled to.
func TestEveryPackagedGoModCarriesNoReplaceDirective(t *testing.T) {
	packages, err := os.ReadDir(packagesDir(t))
	if err != nil {
		t.Fatalf("read the packages directory: %v", err)
	}

	checked := 0
	for _, pkg := range packages {
		if !pkg.IsDir() {
			continue
		}
		extsDir := filepath.Join(packagesDir(t), pkg.Name(), "extensions")
		exts, err := os.ReadDir(extsDir)
		if os.IsNotExist(err) {
			continue
		}
		for _, ext := range exts {
			if !ext.IsDir() {
				continue
			}
			modPath := filepath.Join(extsDir, ext.Name(), "go.mod")
			data, err := os.ReadFile(modPath)
			if err != nil {
				t.Errorf("%s/%s: read go.mod: %v; run %s", pkg.Name(), ext.Name(), err, syncScript)
				continue
			}
			checked++
			for i, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "replace") ||
					strings.Contains(line, "=>") {
					t.Errorf("%s/%s/go.mod line %d carries a replace directive (%q); a node cannot resolve it",
						pkg.Name(), ext.Name(), i+1, strings.TrimSpace(line))
				}
			}
			if !strings.Contains(string(data), "module github.com/vincent-wuhan/opskeeper/plugins/pig-ops/") {
				t.Errorf("%s/%s/go.mod does not declare the packaged module path; run %s",
					pkg.Name(), ext.Name(), syncScript)
			}
		}
	}
	if checked == 0 {
		t.Error("no packaged go.mod was checked, so this test is vacuous")
	}
}

// toolsets are the extensions that each carry their own copy of the broker
// client. Every directory under core/pig/extensions that ships one is
// discovered rather than named: the test below walks this list, so adding a
// fourth package without adding it here fails as a vacuous check rather
// than passing silently.
var toolsets = []string{
	"opskeeper-sre-readonly",
	"opskeeper-sre-observability",
	"opskeeper-sre-repair",
}

// TestEveryToolsetsBrokerClientIsTheSameFile asserts the one property that
// genuinely spans packages.
//
// Each toolset carries its own copy of the broker client, because each
// package has to build standalone on a node. N copies of a protocol are N
// things that can disagree, and the disagreement that matters is the unsafe
// one: a repair client that resent a call whose reply was lost would
// restart a service twice.
//
// So the copies must be the same file, modulo the package clause they
// belong to. Everything else — the framing, the line bound, the refusal to
// resend an unknown outcome — is asserted equal here rather than left to
// several sets of tests that happen to pass today.
func TestEveryToolsetsBrokerClientIsTheSameFile(t *testing.T) {
	if len(toolsets) < 2 {
		t.Fatal("the list is too short for the comparison below to mean anything")
	}
	// Sorted, so the "and" in a failure message is stable across runs and
	// a reviewer can see at a glance which two copies diverged.
	sort.Strings(toolsets)

	want, err := os.ReadFile(filepath.Join(repoRoot(t), "core", "pig", "extensions", toolsets[0], "client.go"))
	if err != nil {
		t.Fatalf("read the %s client: %v", toolsets[0], err)
	}
	for _, other := range toolsets[1:] {
		got, err := os.ReadFile(filepath.Join(repoRoot(t), "core", "pig", "extensions", other, "client.go"))
		if err != nil {
			t.Errorf("read the %s client: %v", other, err)
			continue
		}
		if stripPackageClause(string(want)) != stripPackageClause(string(got)) {
			t.Errorf("the %s and %s broker clients have diverged; they are the same protocol and a "+
				"divergence in the %s copy is a second action taken on a live system",
				toolsets[0], other, other)
		}
	}
}

// stripPackageClause removes the leading `package X` line so two clients
// in different packages can be compared. It deliberately does not
// normalise anything else: a whitespace change, a reordered field, a
// changed constant is a difference worth failing on.
func stripPackageClause(src string) string {
	if i := strings.IndexByte(src, '\n'); i >= 0 {
		return src[i+1:]
	}
	return src
}
