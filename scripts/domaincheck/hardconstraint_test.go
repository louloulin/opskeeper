package main

import (
	"strings"
	"testing"
)

// The hard-constraint table is the one place in this checker where a declared
// edge is promoted from "a seam somebody could hold open" to "a seam a process
// boundary may not cut". Everything below is about keeping that promotion
// honest: it is checked in both directions so it cannot rot, and it is reported
// by the cut pricer so a proposal that severs one is visibly wrong.

// TestTheShippedHardConstraintSetIsNotEmpty is the first line of defence
// against a table that has been emptied by accident. Every other test here
// builds its own set; this one looks at the shipped one, because a constraint
// table with nothing in it still reports a clean tree and still prices every
// split as though nothing were impossible.
func TestTheShippedHardConstraintSetIsNotEmpty(t *testing.T) {
	if len(hardConstraints) == 0 {
		t.Fatal("hardConstraints is empty; every check below still passes, because an empty " +
			"constraint set is indistinguishable from a tree with no uncuttable edges")
	}
	for e, why := range hardConstraints {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s -> %s is a hard constraint with no reason. A constraint nobody can "+
				"read the reasoning for is a constraint the next reader will delete", e.from, e.to)
		}
	}
	// The four are the audit chain, and the reason they are hard is the same
	// physical property in all four cases. If a fifth arrives, it needs the
	// same argument, so the test asks for the shape rather than the count.
	for e := range hardConstraints {
		if e.to != "audit" && e.from != "audit" {
			t.Errorf("%s -> %s is declared a hard process constraint but is not one of the "+
				"audit chain's edges. That is allowed, and it is also the point at which this "+
				"table stops being a transcription of a decision and starts being a new one — "+
				"so it has to be argued in the comment above, not only here",
				e.from, e.to)
		}
	}
}

// TestAHardConstraintOnAnUndeclaredEdgeIsAViolation covers the first drift
// direction. A constraint on an edge the table does not declare constrains
// nothing at all: the edge is not part of the boundary, so nothing checks it
// and nothing reports it when it moves.
func TestAHardConstraintOnAnUndeclaredEdgeIsAViolation(t *testing.T) {
	r := testRules()
	r.hard = map[edge]string{
		{from: "alpha", to: "beta"}: "these two write one ordered chain",
	}
	out := strings.Join(check(world(), r), "\n")
	if !strings.Contains(out, "alpha -> beta") {
		t.Errorf("a hard constraint on an undeclared edge is not reported:\n%s", out)
	}
	if !strings.Contains(out, "constrains nothing") {
		t.Errorf("the message does not say what is wrong with it:\n%s", out)
	}
}

// TestAHardConstraintWhoseEdgeNoLongerHappensIsAViolation covers the other
// direction, and it is the more dangerous of the two. A stale constraint is
// not inert: it keeps asserting something untrue, and the day somebody relies
// on it is the day a split is refused for a reason that no longer exists —
// which is how a boundary rule becomes something people work around.
func TestAHardConstraintWhoseEdgeNoLongerHappensIsAViolation(t *testing.T) {
	r, sources := withEdge(testRules(), "alpha", "beta")
	r.hard = map[edge]string{
		{from: "alpha", to: "beta"}: "these two write one ordered chain",
	}
	// The edge is declared and the constraint is on it, but the tree no longer
	// performs it, so the justification has expired with the dependency.
	out := strings.Join(check(sources[:0], r), "\n")
	if !strings.Contains(out, "no longer happens") {
		t.Errorf("a hard constraint on an edge that stopped happening is not reported:\n%s", out)
	}
	if !strings.Contains(out, "stale") {
		t.Errorf("the message does not say that the entry is the thing that is wrong:\n%s", out)
	}
}

// TestTheShippedHardConstraintsHoldInTheRealTree is the one test here that
// looks at the repository rather than a fixture. The other three prove the rule
// fires; this one proves the shipped table is actually true of the shipped
// tree, so the constraint list cannot describe a codebase that no longer exists.
//
// The gate itself already runs check() over the real tree on every commit, and
// that is the enforcement. This test exists so the failure names the constraint
// instead of arriving as one line inside a checker that also reports unrelated
// things.
func TestTheShippedHardConstraintsHoldInTheRealTree(t *testing.T) {
	sources, _, err := parseControlPlane("../..")
	if err != nil {
		t.Fatalf("parse the manager tree: %v", err)
	}
	for _, v := range check(sources, defaultRules()) {
		if strings.Contains(v, "hard process constraint") {
			t.Errorf("a shipped hard constraint does not match the shipped tree: %s", v)
		}
	}
}

// TestASeveredHardConstraintIsReportedAboveThePrice is the reporting half.
//
// The cut pricer is a report mode by design: a proposal that is wrong should
// be priced and argued about, not turned into a red build on the day it is
// written. That is the right call for a cost. It is the wrong call for an
// impossibility, because a cost and an impossibility are different in kind —
// a severed edge can be paid for with a seam, and a severed hard constraint
// cannot be paid for at all without giving up the property that made it hard.
func TestASeveredHardConstraintIsReportedAboveThePrice(t *testing.T) {
	r, sources := withEdge(testRules(), "alpha", "beta")
	hard := map[edge]string{
		{from: "alpha", to: "beta"}: "these two write one ordered chain",
	}
	g := buildGraph(sources, r)
	out := reportCutWithHard(t, g, "one = alpha\ntwo = beta\n", hard)

	if !strings.Contains(out, "HARD CONSTRAINT SEVERED") {
		t.Fatalf("a severed hard constraint is not reported at all:\n%s", out)
	}
	if !strings.Contains(out, "one ordered chain") {
		t.Errorf("the report does not carry the reason, so a reader cannot judge it:\n%s", out)
	}
	if !strings.Contains(out, "1 of 1 hard process constraints are cut") {
		t.Errorf("the report does not total the damage against the whole set, so a reader "+
			"cannot tell a complete answer from a partial one:\n%s", out)
	}
	// Above the price: the cut list is the part people skim, and a constraint
	// that appears below it is a constraint that gets skimmed past.
	severedAt := strings.Index(out, "HARD CONSTRAINT SEVERED")
	priceAt := strings.Index(out, "the edges this split severs")
	if priceAt < 0 {
		t.Fatalf("the price section is missing, so the ordering assertion is vacuous:\n%s", out)
	}
	if severedAt > priceAt {
		t.Errorf("the severed constraint is reported below the price, where the cost lives:\n%s", out)
	}
}

// TestAGroupingThatKeepsAHardConstraintWholeSaysNothing is the negative case,
// and it is the one that keeps the report from crying wolf. A pricer that
// shouts on every run is a pricer people learn to skip, and then it is worse
// than the thing it replaced.
func TestAGroupingThatKeepsAHardConstraintWholeSaysNothing(t *testing.T) {
	r, sources := withEdge(testRules(), "alpha", "beta")
	hard := map[edge]string{
		{from: "alpha", to: "beta"}: "these two write one ordered chain",
	}
	g := buildGraph(sources, r)
	out := reportCutWithHard(t, g, "one = alpha, beta\n", hard)

	if strings.Contains(out, "HARD CONSTRAINT SEVERED") {
		t.Errorf("a grouping that keeps the constraint whole is reported as severing it:\n%s", out)
	}
	if strings.Contains(out, "hard process constraints are cut") {
		t.Errorf("a clean grouping still prints a damage total:\n%s", out)
	}
	// The edge is real and declared, so the price section must still be there.
	// Silencing the constraint report must not silence the report.
	if !strings.Contains(out, "the edges this split severs") {
		t.Errorf("the price section disappeared with the constraint report:\n%s", out)
	}
}

// TestAConstraintOnAnIslandIsNotReported guards the shape of the rule itself.
// The pricer skips a cut it cannot price — an edge whose endpoints the grouping
// does not mention — so a hard constraint involving an unassigned domain would
// otherwise vanish silently, which is the one outcome worse than being wrong
// about it: the reader is told nothing at all.
func TestAConstraintOnAnIslandIsNotReported(t *testing.T) {
	r, sources := withEdge(testRules(), "alpha", "beta")
	hard := map[edge]string{
		{from: "alpha", to: "beta"}: "these two write one ordered chain",
	}
	g := buildGraph(sources, r)
	// beta is simply not mentioned, so the grouping has an unassigned domain
	// and the edge cannot be priced.
	out := reportCutWithHard(t, g, "one = alpha\n", hard)

	if !strings.Contains(out, "domain(s) the grouping does not mention") {
		t.Fatalf("the unassigned domain is not reported, so this test is not exercising the "+
			"case it claims to:\n%s", out)
	}
	if strings.Contains(out, "HARD CONSTRAINT SEVERED") {
		t.Errorf("an edge with an unassigned endpoint is reported as severed, which asserts a "+
			"cut the grouping never made:\n%s", out)
	}
}
