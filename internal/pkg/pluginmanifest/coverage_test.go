package pluginmanifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The coverage join is what lets a harness case say something about the
// plugin fleet. It is also the kind of table that rots silently: a tool is
// renamed, the map keeps its old key, and every case that exercised the
// tool starts scoring as uncovered with no error anywhere. These tests are
// the drift alarm.

func shippedPlugins(t *testing.T) []Plugin {
	t.Helper()
	base := filepath.Join(repoRoot(t), "plugins", "pig-ops")
	plugins, err := LoadAll(base)
	if err != nil {
		t.Fatalf("LoadAll %s: %v", base, err)
	}
	if len(plugins) == 0 {
		t.Fatalf("no packages under %s", base)
	}
	return plugins
}

func TestEveryShippedToolHasACapabilityFamily(t *testing.T) {
	// The load-bearing test. A first-party tool with no entry is a case
	// that would silently score as uncovered — and the failure mode is the
	// quiet one: the leaderboard dips, nobody knows why, and the answer is
	// a missing line in a map three directories away.
	//
	// Third-party packages are allowed unmapped tools; the shipped ones
	// are not, because their coverage is a number this repository reports.
	for _, p := range shippedPlugins(t) {
		if unmapped := p.UnmappedTools(); len(unmapped) > 0 {
			t.Errorf("package %s ships tools with no capability family: %s\n"+
				"an incident case exercising one would score as uncovered",
				p.Name(), strings.Join(unmapped, ", "))
		}
	}
}

func TestTheCapabilityTableHasNoDeadEntries(t *testing.T) {
	// The other direction, and the one that catches a rename: a table
	// entry for a tool no package ships is a stale key. It is harmless at
	// runtime and actively misleading during a review, because it makes
	// the table look like it covers more than it does.
	shipped := map[string]bool{}
	for _, p := range shippedPlugins(t) {
		for _, tool := range p.Manifest.Spec.Tools {
			shipped[tool.Name] = true
		}
	}
	for tool := range toolCapabilities {
		if !shipped[tool] {
			t.Errorf("the capability table maps %q, which no shipped package declares; "+
				"the tool was renamed or removed and the entry is now a lie", tool)
		}
	}
}

func TestTheReadOnlyPackageCoversTheHostProbeFamily(t *testing.T) {
	// A worked example of the join, asserted against the real package.
	// The CPU-spike case names host.host_load; the read-only package is
	// what serves it, and if it stopped, the case would become
	// unpassable on a fleet running the default bundle.
	plugins := shippedPlugins(t)
	var readonly Plugin
	for _, p := range plugins {
		if p.Name() == readOnlyProfile {
			readonly = p
		}
	}
	if readonly.Name() == "" {
		t.Fatalf("the read-only package is not among %d shipped packages", len(plugins))
	}
	caps := readonly.Capabilities()
	want := map[string]bool{CapHost: true, CapTopology: true, CapAlert: true}
	for _, c := range caps {
		delete(want, c)
	}
	if len(want) > 0 {
		t.Errorf("the read-only package is missing capability families %v; it has %v", want, caps)
	}
}

func TestAHostCaseIsCoveredByTheShippedPackages(t *testing.T) {
	// The end-to-end property: a golden case the fleet is supposed to
	// pass, scored against the fleet that ships. host/cpu-spike's
	// expectations are all host-family, and the read-only package serves
	// that family, so it must come out complete.
	plugins := shippedPlugins(t)
	cov := CoverageOf("host/cpu-spike",
		[]string{"host.host_load", "host.host_processes", "host.top_cpu_procs"}, plugins)
	if !cov.Complete() {
		t.Errorf("host/cpu-spike is not covered by the shipped fleet: uncovered=%v\n%s",
			cov.Uncovered, CoverageReason(cov.Uncovered[0]))
	}
	if len(cov.Packages) == 0 {
		t.Error("the case is complete but names no package that covers it")
	}
	for _, name := range cov.Packages {
		if name != readOnlyProfile && name != observabilityProfile {
			t.Errorf("unexpected package %q covers a host-family case", name)
		}
	}
}

func TestAMiddlewareCaseSaysWhyItIsUncoveredRatherThanJustBeingUncovered(t *testing.T) {
	// The gap this repository actually has. pg / redis / k8s / mq are
	// control-plane BaseTools, not plugin packages, so every case built
	// on them is structurally unpassable on a node. The report has to say
	// that, because "uncovered" alone reads as a missing package and sends
	// a reader looking for one that was never supposed to exist.
	plugins := shippedPlugins(t)
	cov := CoverageOf("pg/lock-waits", []string{"pg.lock_waits", "pg.active_sessions"}, plugins)
	if cov.Complete() {
		t.Fatal("a middleware case came out covered; the adapters are not packages")
	}
	if len(cov.Uncovered) != 2 {
		t.Errorf("uncovered = %v, want both expectations", cov.Uncovered)
	}
	reason := CoverageReason(cov.Uncovered[0])
	if !strings.Contains(reason, "control plane") {
		t.Errorf("reason %q does not explain that this family is not a package", reason)
	}
}

func TestAnUnknownFamilyIsReportedAsAMissingPackage(t *testing.T) {
	// Not everything uncovered is a known adapter. A case naming a family
	// nothing serves at all is a genuinely missing package, and the two
	// reasons must not be collapsed — one is "not packaged yet by design",
	// the other is "somebody wrote a case for a tool that does not exist".
	plugins := shippedPlugins(t)
	cov := CoverageOf("acme/mystery", []string{"quantum.entangle"}, plugins)
	if cov.Complete() {
		t.Fatal("an unknown family came out covered")
	}
	reason := CoverageReason(cov.Uncovered[0])
	if strings.Contains(reason, "control plane") {
		t.Errorf("reason %q blames an adapter for a family that is not one", reason)
	}
	if !strings.Contains(reason, "quantum") {
		t.Errorf("reason %q does not quote the family it could not place", reason)
	}
}

func TestCapabilityPrefixHandlesTheShapesCasesActuallyUse(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"pg.lock_waits", "pg"},
		{"host.host_load", "host"},
		{"git-artifact.LinkK8sImage", "git-artifact"},
		{"nodot", "nodot"},
		{"", ""},
		{".leading", ""},
	} {
		if got := CapabilityPrefix(tc.in); got != tc.want {
			t.Errorf("CapabilityPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCoverageIsDeterministic(t *testing.T) {
	// The report is read by a human comparing two runs, so the same input
	// must produce the same order. Map iteration would otherwise shuffle
	// it on every invocation.
	plugins := shippedPlugins(t)
	expectations := []string{"host.host_load", "pg.lock_waits", "alert.query_alert_rules"}
	first := CoverageOf("x", expectations, plugins)
	for i := 0; i < 8; i++ {
		again := CoverageOf("x", expectations, plugins)
		if strings.Join(first.Packages, ",") != strings.Join(again.Packages, ",") {
			t.Fatalf("packages order changed: %v vs %v", first.Packages, again.Packages)
		}
		if strings.Join(first.Uncovered, ",") != strings.Join(again.Uncovered, ",") {
			t.Fatalf("uncovered order changed: %v vs %v", first.Uncovered, again.Uncovered)
		}
	}
}

func TestTheCoverageOfARealCaseFileIsWhatTheFileSays(t *testing.T) {
	// Reads an actual case.yaml so the expectations under test are the
	// ones the harness will run, not a fixture that drifted from them.
	casesDir := filepath.Join(repoRoot(t), "core", "harness", "cases", "host", "cpu-spike")
	raw, err := os.ReadFile(filepath.Join(casesDir, "case.yaml"))
	if err != nil {
		t.Fatalf("read case: %v", err)
	}
	var expectations []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if strings.Contains(value, ".") && !strings.Contains(value, " ") {
			expectations = append(expectations, value)
		}
	}
	if len(expectations) < 3 {
		t.Fatalf("parsed only %v out of the case file; the extraction is wrong, not the case", expectations)
	}
	cov := CoverageOf("host/cpu-spike", expectations, shippedPlugins(t))
	if !cov.Complete() {
		t.Errorf("the shipped fleet cannot serve host/cpu-spike: uncovered=%v", cov.Uncovered)
	}
}

func TestEveryCaseFamilyIsEitherPackagedOrNamedAsADeliberateGap(t *testing.T) {
	// The report's whole value is that its gaps are *explained*. An
	// unexplained gap means somebody wrote a case for a family nobody has
	// decided about, and the honest failure is here rather than in a
	// leaderboard that quietly reports a zero.
	plugins := shippedPlugins(t)
	served := map[string]bool{}
	for _, p := range plugins {
		for _, cap := range p.Capabilities() {
			served[cap] = true
		}
	}
	casesDir := filepath.Join(repoRoot(t), "core", "harness", "cases")
	var unexplained []string
	err := filepath.Walk(casesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "case.yaml" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, family := range familiesIn(string(raw)) {
			if served[family] || IsMiddlewareFamily(family) || IsNonPackageFamily(family) {
				continue
			}
			unexplained = append(unexplained,
				fmt.Sprintf("%s: %s", family, CoverageReason(family+"._")))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cases: %v", err)
	}
	if len(unexplained) > 0 {
		t.Errorf("cases name families nothing serves and no list explains:\n  %s\n"+
			"either package them or add them to MiddlewareFamilies/NonPackageFamilies with a reason",
			strings.Join(unexplained, "\n  "))
	}
}

// familiesIn extracts the resource prefixes a case file's expectations use.
func familiesIn(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if strings.Contains(value, " ") {
			continue
		}
		// Only positions inside the expect block carry "<family>.<method>";
		// a tag or a prerequisite never has that shape with a single dot
		// and no spaces, which is what this filter is for.
		family := CapabilityPrefix(value)
		if family == value || family == "" || seen[family] {
			continue
		}
		seen[family] = true
		out = append(out, family)
	}
	return out
}

func TestTheMiddlewareListIsNotSilentlySwallowingEverything(t *testing.T) {
	// The gap list is an escape hatch, and an escape hatch that grows is a
	// coverage report that reports nothing. Every entry must correspond to
	// a real adapter family or a real non-package family, and the list
	// must stay small enough that its size is itself a signal.
	if len(MiddlewareFamilies) > 8 || len(NonPackageFamilies) > 4 {
		t.Errorf("the gap lists have grown to %d middleware and %d non-package families; "+
			"at this size they stop being exceptions and start being the answer",
			len(MiddlewareFamilies), len(NonPackageFamilies))
	}
	for _, f := range MiddlewareFamilies {
		if IsNonPackageFamily(f) {
			t.Errorf("%q is in both gap lists; the two claims are different", f)
		}
		if f == "" {
			t.Error("the middleware list contains an empty family")
		}
	}
}
