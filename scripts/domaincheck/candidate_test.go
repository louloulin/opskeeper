package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This file is about the two split proposals in docs/, and it is the half of
// the story that decision 211 deliberately left open.
//
// Decision 211 taught the cut pricer to *report* a severed hard constraint, and
// it found that docs/manager-split.proposed severs three of the four. It also
// made a deliberate choice: `-cut` stays a report mode, because a proposal that
// is wrong should be priced and argued about rather than turned into a red
// build on the day it is written — and a red build people turn off is worse
// than no gate at all.
//
// That choice is right about *writing* a proposal. It is wrong about *shipping
// one that has been corrected*, because a corrected candidate is a different
// kind of artifact: it is a claim about the current tree, and a claim about the
// current tree goes stale the moment the tree moves. So this file pins the
// corrected candidate the way the other twenty-three gates pin things — by
// recomputing, and failing when the file and the tree disagree.

// price is what the cut pricer says about one candidate file.
type price struct {
	internal   int
	crossing   int
	severed    int
	unassigned int
	// denominator is the hard-constraint count the file divided by. It comes
	// from the file rather than from the pricer, which is exactly why it needs
	// checking: nothing else in the table would notice it going stale.
	denominator int
}

// priceFile runs the real pricer over a real candidate file against the real
// tree. Nothing here is reimplemented: if the pricer changes what it counts,
// these numbers change with it, which is the point.
func priceFile(t *testing.T, rel string) price {
	t.Helper()
	root := filepath.Join("..", "..")
	sources, _, err := parseTree(filepath.Join(root, "core", "manager"), defaultRules())
	if err != nil {
		t.Fatalf("parse the tree: %v", err)
	}
	r := defaultRules()
	grouping, order, err := loadGrouping(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("load the grouping in %s: %v", rel, err)
	}
	var buf bytes.Buffer
	buildGraph(sources, r).printCut(&buf, grouping, order, r.hard)
	out := buf.String()

	p := price{
		severed:    strings.Count(out, "HARD CONSTRAINT SEVERED"),
		unassigned: countOf(t, out, `(\d+) domain\(s\) the grouping does not mention`),
	}
	m := regexp.MustCompile(`(\d+) import statements stay inside a group, (\d+) cross one`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the pricer printed no price line for %s:\n%s", rel, out)
	}
	p.internal, _ = strconv.Atoi(m[1])
	p.crossing, _ = strconv.Atoi(m[2])
	return p
}

func countOf(t *testing.T, out, pattern string) int {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse %q out of:\n%s", m[1], out)
	}
	return n
}

const constrainedCandidate = "docs/manager-split.constrained"
const correctedCandidate = "docs/manager-split.proposed"

// TestTheConstrainedCandidateSeversNoHardConstraint is the reason the other file
// exists. The four hard constraints all point at `audit`, so a legal grouping
// has to put {audit, aiops, chatdiagnose, frontierbound, middleware} in one
// group. The corrected candidate does. If a future edit moves one of them out,
// this goes red with the pricer saying which edge it cut.
func TestTheConstrainedCandidateSeversNoHardConstraint(t *testing.T) {
	p := priceFile(t, constrainedCandidate)
	if p.severed != 0 {
		t.Errorf("%s severs %d hard process constraints; the audit chain is one ordered "+
			"tamper-evident chain, and two processes writing it concurrently is a distributed "+
			"coordination problem whose bill is larger than the split saves",
			constrainedCandidate, p.severed)
	}
}

// TestTheConstrainedCandidateAssignsEveryDomain closes the second defect
// decision 211 recorded: both proposals named 57 of the 58 domains, leaving
// `federationchild` unassigned. A domain nobody assigned is a domain whose
// packages are invisible to the pricer, so it is also a domain whose imports
// are not counted on either side of the cut.
func TestTheConstrainedCandidateAssignsEveryDomain(t *testing.T) {
	p := priceFile(t, constrainedCandidate)
	if p.unassigned != 0 {
		t.Errorf("%s leaves %d domain(s) unassigned; an unassigned domain's packages are "+
			"counted on neither side of the cut, so the price understates the seam it opens",
			constrainedCandidate, p.unassigned)
	}
}

// TestTheConstrainedCandidateIsNoMoreExpensiveThanTheOneItCorrects is the
// finding that made the correction worth making. Moving three domains from apps
// into core was expected to cost more, because each of them has edges of its
// own. It cost less: those three were net *exporters* of crossing edges — they
// sat in apps while everything they depend on sat in core, so nearly every
// edge leaving them was a seam. Moving them turns most of those into internal
// edges and leaves `frontierbound -> metric` as the only new one.
//
// This test exists so the claim in the candidate file cannot rot into a
// slogan. If a later refactor makes the corrected grouping genuinely more
// expensive, that is a real finding and the file should be rewritten — not this
// threshold quietly relaxed.
func TestTheConstrainedCandidateIsNoMoreExpensiveThanTheOneItCorrects(t *testing.T) {
	before := priceFile(t, correctedCandidate)
	after := priceFile(t, constrainedCandidate)

	if after.crossing >= before.crossing {
		t.Errorf("the corrected candidate crosses %d import statements and the one it corrects "+
			"crosses %d; the correction was made because it is cheaper as well as legal, and if "+
			"that is no longer true then docs/manager-split.constrained is quoting a stale reason",
			after.crossing, before.crossing)
	}
	if after.internal <= before.internal {
		t.Errorf("the corrected candidate keeps %d import statements inside a group and the one "+
			"it corrects keeps %d; a correction that moves domains into a group without moving "+
			"any imports with them is a rename, not a regrouping", after.internal, before.internal)
	}
	// And the thing that makes the comparison worth reading at all: the
	// corrected candidate is not merely cheaper, it is the only one of the two
	// that is legal. If the original ever stops severing a constraint, this
	// test's premise needs re-examining rather than deleting.
	if before.severed == 0 {
		t.Logf("note: %s no longer severs a hard constraint, so it is no longer the "+
			"counter-example this file is written against", correctedCandidate)
	}
}

// TestThePriceQuotedInTheCandidateIsThePriceThePricerComputes is the anti-stale
// half. The candidate file states a table — 105/42/3 for the original, 116/31/0
// for the corrected one — in prose, in a comment, where nothing checks it. That
// is exactly the shape of number that goes stale: the tree moves, the pricer
// prints something new, and the document keeps asserting the old figure.
//
// So the table is parsed and compared. A drift here does not mean the grouping
// is wrong; it means the file is describing a tree that no longer exists, and a
// reader has no way to tell which of the two moved.
//
// Both rows are checked, and that is not redundancy. The first version of this
// test checked only the row describing this file, and the reverse verification
// immediately found the hole: rewriting 105 to 999 in the row describing the
// *other* proposal went green. A table that quotes a neighbour is a claim about
// that neighbour, and a gate that only watches its own column is a gate that
// lets the other column rot.
func TestThePriceQuotedInTheCandidateIsThePriceThePricerComputes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", constrainedCandidate))
	if err != nil {
		t.Fatalf("read the candidate: %v", err)
	}

	for _, row := range []struct{ file string }{
		{correctedCandidate},
		{constrainedCandidate},
	} {
		quoted := quotedRow(t, string(raw), row.file)

		if quoted.denominator != len(hardConstraints) {
			t.Errorf("the candidate quotes %d/%d hard constraints for %s, but the table this "+
				"repository ships declares %d; the denominator is the one number in the row that "+
				"does not come from the pricer, so it is the one that can rot silently",
				quoted.severed, quoted.denominator, row.file, len(hardConstraints))
		}

		got := priceFile(t, row.file)
		if quoted.internal != got.internal || quoted.crossing != got.crossing || quoted.severed != got.severed {
			t.Errorf("the candidate quotes %s as internal=%d crossing=%d severed=%d, and the "+
				"pricer computes internal=%d crossing=%d severed=%d. One of the two is describing "+
				"a tree that no longer exists, and a reader cannot tell which",
				row.file, quoted.internal, quoted.crossing, quoted.severed,
				got.internal, got.crossing, got.severed)
		}
	}
}

// quotedRow pulls one line of the price table out of a candidate file. The table
// is a comment block aligned with spaces rather than a markdown table, because
// it is read by a person pricing a split and by this test, and a pipe table
// would have meant either losing the alignment or parsing the pipes.
func quotedRow(t *testing.T, raw, file string) price {
	t.Helper()
	row := regexp.MustCompile(`(?m)^#\s+` + regexp.QuoteMeta(file) +
		`\s+(\d+)\s+(\d+)\s+(\d+)\s*/\s*(\d+)\s*$`).FindStringSubmatch(raw)
	if row == nil {
		t.Fatalf("%s has no price row for %s; the table is the one place the file makes a "+
			"claim about the current tree, so it has to be findable", constrainedCandidate, file)
	}
	return price{
		internal:    atoiOrFail(t, row[1]),
		crossing:    atoiOrFail(t, row[2]),
		severed:     atoiOrFail(t, row[3]),
		unassigned:  0,
		denominator: atoiOrFail(t, row[4]),
	}
}

func atoiOrFail(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}
