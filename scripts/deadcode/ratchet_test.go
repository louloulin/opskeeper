package main

import (
	"os"
	"path/filepath"
	"testing"
)

// unreachableBudget is the number this tree is allowed to have and no more.
//
// It is a ratchet rather than a threshold, and the difference is the whole
// point. A threshold says "823 is too many" and is therefore always false —
// the number has been near 800 for thirty decisions and the right answer about
// any given one of them was "leave it, it is accounted for". A ratchet says
// "823 today, and tomorrow you either go down or you say out loud why you
// went up". The second one is the only one that changes what happens, because
// the cost lands on the person adding the symbol rather than on whoever reads
// the report last.
//
// The two class counts are pinned too, and not because the gate needs them —
// the gate trips on the total, so that a symbol moving from dead to test-only
// (which happens the moment someone writes a test for it) does not read as
// growth. They are pinned because the finding that produced this file was in
// one of the two classes, and a total cannot tell a reader which one grew. A
// ratchet on a number nobody can decompose is a number nobody acts on.
const (
	unreachableBudget    = 823
	deadSymbolBudget     = 529
	testOnlySymbolBudget = 294
)

// TestTheUnreachableSymbolCountNeverGrows is the gate decision 199 declined to
// build, and it is built here for one narrow reason.
//
// Decision 199 measured that this walk cannot see six things: reflection,
// go:linkname, cgo //export, struct-tag codecs, embedded-method promotion and
// build tags. A symbol reachable only through one of those looks dead, so a
// gate on "is this symbol dead" would be a gate that cries wolf on a real but
// invisible path — and a gate that cries wolf teaches its reader to skip it.
// That reasoning is still correct and nothing here contradicts it.
//
// What the same reasoning does not cover is a gate on **growth**, because
// growth has to be justified by somebody either way. A symbol added with no
// production caller is a decision, and the decision was being made silently:
// the report grew, nobody read it, and three decisions later a documented
// control was still test-only while a boot log said it was loaded. That is
// the specific harm, it cost one ADR's worth of false confidence, and the
// cheapest thing standing between it and a repeat is making the growth
// require a sentence.
//
// The gate is deliberately one-sided. Lowering the numbers is not a failure —
// the budget is the ceiling, and a tree that deletes its way under it needs
// the pin moved down to keep meaning anything. So the constants above are
// edited in the same commit that brings the number down, and the failure
// message says so.
func TestTheUnreachableSymbolCountNeverGrows(t *testing.T) {
	// The root is located by a sibling this repository is known to have, not
	// by probing for go.work: the first version of this test used go.work as
	// its marker and scripts/modulecheck's TestTheRepositoryHasNoGoWorkProbes
	// rejected it, correctly — a file that asks whether a workspace is wired
	// up is a file that will not build in one. The marker here is
	// ../domaincheck, the same one the tree-smoke test above uses, so the two
	// tests in this package agree on where the repository is without either of
	// them caring how it is built.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "domaincheck")); err != nil {
		t.Skipf("not at the repository root: %v", err)
	}
	records, err := parseAll([]string{root})
	if err != nil {
		t.Fatalf("walking the shipped tree: %v", err)
	}
	res := analyse(records)

	if res.deadSymbols <= unreachableBudget &&
		res.deadOnlySymbols <= deadSymbolBudget &&
		res.testOnlySymbols <= testOnlySymbolBudget {
		return
	}
	t.Errorf("the tree now has %d unreachable symbols (%d dead, %d test-only), over the "+
		"ratchet of %d / %d / %d.\n"+
		"  Three ways out, and the first two are the point:\n"+
		"  1. the symbol is reachable and this walk cannot see it — say which of the six "+
		"invisible paths (reflection, go:linkname, cgo //export, struct-tag codecs, "+
		"embedded-method promotion, build tags) it uses, and add the fixture that proves it;\n"+
		"  2. the symbol is genuinely unreferenced — delete it, or say in "+
		"docs/opskeeper2-architecture.md why it is kept, with the same specificity the "+
		"decision log uses elsewhere;\n"+
		"  3. only then, if the growth is deliberate and recorded, raise the constants in "+
		"scripts/deadcode/ratchet_test.go in the same commit.\n"+
		"  Raising them silently is the one option that undoes this gate: the number only "+
		"means something while moving it costs a sentence.",
		res.deadSymbols, res.deadOnlySymbols, res.testOnlySymbols,
		unreachableBudget, deadSymbolBudget, testOnlySymbolBudget)
}
