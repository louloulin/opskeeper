package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestTheSameNameSeveralOwnersListIsReal is the assertion behind the report's
// most useful column.
//
// Four symbols in this tree are selected by more than one domain and declared
// by more than one domain: `Event`, `Rule`, `Usecase`, `Caller`. Each of those
// is two or more unrelated types that happen to share a name — alert.Event is
// not audit.Event, marketplace.Caller carries a TenantID that skill.Caller
// does not.
//
// That makes them the most dangerous entries in the shared-symbol list, and
// the reason a shared-symbol ranking cannot be acted on by sorting it. A move
// that carries "Caller" to a shared home without noticing it has several
// owners does not fail to compile; it compiles, and it silently changes what
// one of the owners means. Nothing downstream can see it.
//
// So the list is pinned. The assertion is that these are still ambiguous, so
// that the day someone gives one of them a single owner this test goes red and
// the name is promoted to a candidate — and, just as importantly, so nobody
// reads the current ranking and concludes the top of the list is safe to move.
//
// `ListFilter` was the fifth name here until decision 235 cut alert -> edge
// and systemhealth -> edge, which removed the only two consumers besides
// aiops. A symbol needs two or more *consuming* domains to be reported at
// all, so ListFilter left the report rather than becoming single-owner. That
// is a different end state from the one this test was written to catch, and
// the failure message below says which one happened — the earlier version
// reported both as "now has a single declaring domain", which was wrong for
// the case that actually occurred.
func TestTheSameNameSeveralOwnersListIsReal(t *testing.T) {
	ambiguous := map[string]bool{}
	reported := map[string]bool{}
	for _, r := range sharedRows(t) {
		reported[r.sym] = true
		if len(r.targets) > 1 {
			ambiguous[r.sym] = true
		}
	}
	// A mutation that made every one of these single-owner did NOT make this
	// test fail: it took a branch that skipped. The skip was meant for "the
	// trap column has nothing left to warn about", but that is exactly the
	// state where the names below have become move candidates, and skipping
	// there means the test passes while the thing it exists to watch has
	// silently changed. It fails instead, and says what changed.
	if len(ambiguous) == 0 {
		t.Fatalf("no symbol is declared by more than one domain any more, so the report's trap " +
			"column is empty; the four names below have all become single-owner and are now " +
			"move candidates. Fail on purpose so this is a decision, not a drift")
	}
	// Every one of these must still be ambiguous. A name that has become
	// single-owner is a move candidate and belongs in the report's main list,
	// not here — and a name that has dropped out of the report entirely is a
	// third state again, with a different cause and a different next step.
	// The message distinguishes them because "look at this" is only useful
	// advice if it says which of the two things happened.
	for _, sym := range []string{"Event", "Rule", "Usecase", "Caller"} {
		if ambiguous[sym] {
			continue
		}
		if !reported[sym] {
			t.Errorf("%s has dropped out of the shared report, which means it no longer has two or "+
				"more consuming domains; it is not a move candidate, it is edge's private "+
				"vocabulary again, and whoever reads this should say whether that was intended",
				sym)
			continue
		}
		t.Errorf("%s now has a single declaring domain, so it is a real move candidate and "+
			"belongs at the top of the shared list; it was ambiguous when this was written", sym)
	}
}

// TestTheReportFindsSharedSymbols keeps the report from silently going empty.
// A report that finds nothing reads exactly like a report that stopped
// working, and the conclusion anyone would draw from the empty version — "no
// shape is worth moving" — is the one this test exists to prevent being drawn
// by accident.
func TestTheReportFindsSharedSymbols(t *testing.T) {
	rows := sharedRows(t)
	if len(rows) == 0 {
		t.Fatal("the shared report found no symbol selected by more than one domain; either the " +
			"tree stopped having shared vocabulary or the report stopped reading it")
	}
	// The heaviest entry in the current tree is selected by four domains. If
	// the ceiling collapses, the report has lost its input rather than the
	// tree having simplified, and a reader would see a much shorter list and
	// conclude the work is nearly done.
	max := 0
	for _, r := range rows {
		if len(r.consumers) > max {
			max = len(r.consumers)
		}
	}
	if max < 3 {
		t.Errorf("the heaviest shared symbol now has %d consumers, down from 4 when this was "+
			"written; a collapsed ceiling is more likely a broken report than a simplified tree", max)
	}
}

type sharedSummary struct {
	sym       string
	consumers map[string]bool
	targets   map[string]bool
	shapes    []string
}

func sharedRows(t *testing.T) []sharedSummary {
	t.Helper()
	sources, _, err := parseControlPlane("../..")
	if err != nil {
		t.Fatalf("reading the shipped tree: %v", err)
	}
	var sb strings.Builder
	printShared(&sb, sources, defaultRules())
	var out []sharedSummary
	lines := strings.Split(sb.String(), "\n")
	for i := 0; i < len(lines); i++ {
		m := sharedRowRE.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		row := sharedSummary{
			sym:       m[2],
			consumers: setOf(m[3]),
			targets:   setOf(m[4]),
		}
		// The shape lines are indented under the row they belong to and carry
		// no leading number, so they are consumed by position: everything
		// indented deeper than the row, until the next row starts.
		for j := i + 1; j < len(lines); j++ {
			if !strings.HasPrefix(lines[j], "        ") || !strings.Contains(lines[j], ":") {
				break
			}
			row.shapes = append(row.shapes, strings.TrimSpace(lines[j]))
		}
		out = append(out, row)
	}
	return out
}

func setOf(field string) map[string]bool {
	out := map[string]bool{}
	for _, s := range strings.Fields(field) {
		out[s] = true
	}
	return out
}

var sharedRowRE = regexp.MustCompile(
	`^\s+(\d+)\s+(\S+)\s+consumers:\s+(.*?)\s+targets:\s+(.*?)(?:\s+<--.*)?$`)

// TestTheTrapColumnSaysWhyNotJustThat is the assertion behind the shapes the
// ambiguous rows print.
//
// A warning that only says "these two are different types" leaves the reader
// with a puzzle, and puzzles get skipped. The finding this report exists to
// deliver is the opposite: `Usecase` is not one shared shape under sixteen
// names, it is sixteen unrelated service structs — biz/edge's holds
// {repo devices links mirror plugins log phMu pluginHealth}, biz/audit's holds
// {repo log chain chainStore}, biz/report's holds {repo read gen idGen
// defaultLocale}. They agree on the word "repo" and on nothing else. Once that
// is printed, the row stops looking like a move candidate and starts looking
// like a coincidence, which is the correct reading and the one a ranking by
// consumer count would never have produced.
//
// The subject used to be `ListFilter`, and moved here when decision 235 cut
// the two consumers that kept it in the report. The finding it guarded did
// not become false when the report stopped showing it — three packages still
// declare three unrelated `ListFilter` vocabularies — so that half is now
// pinned directly, in the test below, rather than through a column that no
// longer prints it.
func TestTheTrapColumnSaysWhyNotJustThat(t *testing.T) {
	shapes := map[string][]string{}
	for _, r := range sharedRows(t) {
		if len(r.targets) > 1 {
			shapes[r.sym] = r.shapes
		}
	}
	uc, ok := shapes["Usecase"]
	if !ok {
		t.Fatal("Usecase is no longer reported as ambiguous; if that is real it has become a " +
			"move candidate and belongs at the top of the shared list")
	}
	if len(uc) < 2 {
		t.Fatalf("Usecase has %d declaring packages reported, want at least 2", len(uc))
	}
	// The owners must be visibly different. The comparison is on the field
	// list alone, not on the whole rendered line: the package path differs
	// even when the fields are identical, so comparing lines would pass for a
	// report that printed the same struct three times under three names. That
	// is exactly the mutation this assertion was written against.
	shapesSeen := map[string]bool{}
	for _, line := range uc {
		open := strings.Index(line, "{")
		shape := line
		if open >= 0 {
			shape = line[open:]
		}
		shapesSeen[shape] = true
	}
	if len(shapesSeen) < 2 {
		t.Errorf("all Usecase owners rendered the same field list %v; the report is not showing "+
			"the shapes it claims to show, and a reader would merge types that are not alike", uc)
	}
	// And the specific shape that makes the point: no owner of Usecase is
	// documented as holding an audit chain, which is what one shared service
	// struct would look like if it existed. biz/audit is the one that has
	// `chain`, and it is the only one.
	withChain := 0
	for _, line := range uc {
		if strings.Contains(line, "{") && strings.Contains(line, "chain") {
			withChain++
		}
	}
	if withChain > 1 {
		t.Errorf("%d owners of Usecase carry an audit chain; they are documented as unrelated, "+
			"so either the tree changed or the report is wrong", withChain)
	}
}

// TestTheThreeListFilterDeclarationsAreStillThreeVocabularies pins the finding
// decision 233 recorded, now that the report can no longer show it.
//
// The report only prints a symbol two or more domains *select*, and after
// decision 235 only aiops still selects `ListFilter`. So the column that used
// to carry this warning is silent — and the warning was not about the report.
// It is about the tree: three packages declare a type with the same name and
// unrelated fields, and a future move that treats them as one type will
// compile and change what one of them means.
//
// Asserted against the parsed declarations rather than the report, because
// the report's silence is a property of who consumes a symbol and this is a
// property of who declares it. Those are different facts and only one of them
// moved.
func TestTheThreeListFilterDeclarationsAreStillThreeVocabularies(t *testing.T) {
	sources, _, err := parseControlPlane("../..")
	if err != nil {
		t.Fatalf("parse the control plane: %v", err)
	}
	fields := collectStructFields(sources)
	owners := map[string][]string{}
	for pkg, syms := range fields {
		if f, ok := syms["ListFilter"]; ok {
			owners[pkg] = f
		}
	}
	if len(owners) < 3 {
		t.Fatalf("ListFilter is declared by %d package(s), want the 3 unrelated vocabularies it "+
			"has always had; if two of them were merged that is a decision to record, not a drift "+
			"to absorb", len(owners))
	}
	// The only field the three are documented to share is Limit. Asserting the
	// intersection is empty of everything else is stronger than asserting the
	// three differ, and it is the form that catches a merge: give one owner a
	// field the others have and this goes red.
	shared := map[string]bool{}
	for _, f := range owners {
		for _, name := range f {
			shared[name] = true
		}
	}
	common := map[string]bool{}
	for name := range shared {
		inAll := true
		for _, f := range owners {
			found := false
			for _, x := range f {
				if x == name {
					found = true
					break
				}
			}
			if !found {
				inAll = false
				break
			}
		}
		if inAll {
			common[name] = true
		}
	}
	for name := range common {
		if name != "Limit" {
			t.Errorf("every ListFilter owner now carries %q, so the three have stopped being "+
				"unrelated vocabularies and this file's premise is stale", name)
		}
	}
	if !common["Limit"] {
		t.Error("the three ListFilter owners no longer share Limit; the shape of the coincidence " +
			"changed and whoever reads this should say whether the two still look alike for a " +
			"different reason")
	}
}
