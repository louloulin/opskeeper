package main

import (
	"os"
	"path/filepath"
	"testing"
)

// tree writes files into a temp dir and returns its path. Every test here
// is about what a name-based walk can and cannot conclude, so the inputs
// are small enough to read in one screen and the expectations are about
// classification, never about counts over the real tree.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func analyseTree(t *testing.T, dir string) *result {
	t.Helper()
	records, err := parseAll([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	return analyse(records)
}

func finding(res *result, path, name string) (verdict, bool) {
	for _, f := range res.files {
		if filepath.Base(f.path) != path {
			continue
		}
		for _, s := range f.findings {
			if s.name == name {
				return s.why, true
			}
		}
	}
	return live, false
}

// TestAFuncSomeOtherFileCallsIsLive is the base case, and it is the one a
// tool gets wrong first: the caller is in a different file, so counting
// references inside the declaring file would call this dead.
func TestAFuncSomeOtherFileCallsIsLive(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"a.go": "package p\n\nfunc Helper() int { return 1 }\n",
		"b.go": "package p\n\nfunc Use() int { return Helper() }\n",
	}))
	if v, ok := finding(res, "a.go", "Helper"); ok && v != live {
		t.Fatalf("Helper called from b.go reported as %v", v)
	}
}

// TestAFuncNothingCallsIsDead is the other base case.
func TestAFuncNothingCallsIsDead(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"a.go": "package p\n\nfunc Orphan() int { return 1 }\n",
		"b.go": "package p\n\nfunc Use() int { return 2 }\n",
	}))
	if v, ok := finding(res, "a.go", "Orphan"); !ok || v != dead {
		t.Fatalf("Orphan: got (%v, %v), want (dead, true)", v, ok)
	}
}

// TestAFuncOnlyATestCallsIsItsOwnTier pins the distinction the whole
// report rests on. "Nothing calls it" and "only a test calls it" are
// different findings: the second means someone already wrote down what
// the function is for, which is evidence it was meant to be wired up.
func TestAFuncOnlyATestCallsIsItsOwnTier(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"a.go":      "package p\n\nfunc OnlyTested() int { return 1 }\n",
		"a_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = OnlyTested() }\n",
	}))
	if v, ok := finding(res, "a.go", "OnlyTested"); !ok || v != testOnly {
		t.Fatalf("OnlyTested: got (%v, %v), want (test-only, true)", v, ok)
	}
}

// TestAMethodAnInterfaceAsksForIsNeverReported is the false positive that
// would make this tool a liability. A method reached only by being
// assigned to an interface variable is never named at the call site, so a
// walk that only counts names reports every implementation of every
// interface in the tree as dead.
func TestAMethodAnInterfaceAsksForIsNeverReported(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"iface.go": "package p\n\ntype Doer interface{ DoIt() error }\n",
		"impl.go":  "package p\n\ntype impl struct{}\n\nfunc (impl) DoIt() error { return nil }\n",
	}))
	if v, ok := finding(res, "impl.go", "DoIt"); ok {
		t.Fatalf("DoIt satisfies an interface but was reported as %v", v)
	}
}

// TestAMethodNoInterfaceAsksForIsReported is the other side of the same
// coin: the escape hatch must not be so wide that it excuses everything.
func TestAMethodNoInterfaceAsksForIsReported(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"a.go": "package p\n\ntype t struct{}\n\nfunc (t) Unasked() error { return nil }\n",
	}))
	if v, ok := finding(res, "a.go", "Unasked"); !ok || v != dead {
		t.Fatalf("Unasked: got (%v, %v), want (dead, true)", v, ok)
	}
}

// TestAStandardLibraryContractIsNeverReported covers the second escape
// hatch. Nothing in the tree calls MarshalJSON, and it is still called by
// every json.Marshal in the world.
func TestAStandardLibraryContractIsNeverReported(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"a.go": "package p\n\ntype doc struct{}\n\nfunc (doc) MarshalJSON() ([]byte, error) { return nil, nil }\n" +
			"func (doc) String() string { return \"\" }\n",
	}))
	for _, name := range []string{"MarshalJSON", "String"} {
		if v, ok := finding(res, "a.go", name); ok {
			t.Fatalf("%s is a standard contract but was reported as %v", name, v)
		}
	}
}

// TestMainAndInitAreReachedByTheRuntime: the two symbols nothing calls and
// everything depends on.
func TestMainAndInitAreReachedByTheRuntime(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"a.go": "package p\n\nfunc init() {}\n\nfunc main() {}\n",
	}))
	if len(res.files) != 0 {
		t.Fatalf("main/init were reported: %+v", res.files)
	}
}

// TestAWhollyUnreachableFileContributesItsLines is the number stage 3
// needs, so it is worth pinning: a file with one live symbol among three
// dead ones must not add its lines, because nobody can delete a partial
// file and claim the line saving.
func TestAWhollyUnreachableFileContributesItsLines(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"gone.go":  "package p\n\nfunc A() {}\n\nfunc B() {}\n",
		"stays.go": "package p\n\nfunc Live() {}\n\nfunc Use() { Live() }\n",
	}))
	if res.unreachableFiles != 1 || res.unreachableLines == 0 {
		t.Fatalf("unreachable = %d files / %d lines, want 1 file with lines",
			res.unreachableFiles, res.unreachableLines)
	}
	// stays.go has one dead symbol (Use, which nothing calls) among a live
	// one, so it is reported but must not contribute lines.
	for _, f := range res.files {
		if filepath.Base(f.path) == "stays.go" && f.unreachable {
			t.Fatalf("a partially live file was counted as unreachable: %+v", f)
		}
	}
}

// TestAFileWhoseEverySymbolIsTestOnlyStillCounts pins that the two tiers
// are not collapsed: a wholly-unreachable file that a test does exercise
// is the exact shape decision 116 had to find by hand.
func TestAFileWhoseEverySymbolIsTestOnlyStillCounts(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"window.go":      "package p\n\nfunc Migrate() error { return nil }\n",
		"window_test.go": "package p\n\nimport \"testing\"\n\nfunc TestMigrate(t *testing.T) { _ = Migrate() }\n",
	}))
	if res.testOnlyFiles != 1 {
		t.Fatalf("test-only files = %d, want 1 (dead=%d)", res.testOnlyFiles, res.deadFiles)
	}
	if res.deadFiles != 0 {
		t.Fatalf("a test-referenced file was counted as dead: %d", res.deadFiles)
	}
}

// TestADeclarationIsNotAReferenceToItself stops the walk from calling a
// function live because its own declaration mentions its own name.
func TestADeclarationIsNotAReferenceToItself(t *testing.T) {
	res := analyseTree(t, tree(t, map[string]string{
		"a.go": "package p\n\nfunc Self() {}\n",
	}))
	if v, ok := finding(res, "a.go", "Self"); !ok || v != dead {
		t.Fatalf("Self: got (%v, %v), want (dead, true)", v, ok)
	}
}

// TestOverlappingRootsReportEachFileOnce. Callers pass ".", "core" and
// "core/manager", and the first version of this tool reported every
// finding three times, which would have tripled the headline number.
func TestOverlappingRootsReportEachFileOnce(t *testing.T) {
	dir := tree(t, map[string]string{
		"core/manager/p/a.go": "package p\n\nfunc Orphan() {}\n",
	})
	records, err := parseAll([]string{dir, filepath.Join(dir, "core"), filepath.Join(dir, "core", "manager")})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("parsed %d files from three overlapping roots, want 1", len(records))
	}
}

// TestTheShippedTreeHasNothingTheToolCannotExplain is the weakest test in
// the file and it is here on purpose: it runs the real analysis over
// scripts/domaincheck — a small, real, self-contained tree — and only
// asserts that the tool runs and terminates with a bounded finding list.
// A deadness tool that hangs or explodes on a real tree is not a report,
// it is a hazard.
func TestTheShippedTreeHasNothingTheToolCannotExplain(t *testing.T) {
	dir := filepath.Join("..", "domaincheck")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("domaincheck not present: %v", err)
	}
	records, err := parseAll([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	res := analyse(records)
	if res.deadSymbols > 50 {
		t.Fatalf("%d unreachable symbols in a 400-line tool; the walk is probably wrong", res.deadSymbols)
	}
}
