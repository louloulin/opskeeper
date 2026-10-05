package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestTheSameNameSeveralOwnersListIsReal is the assertion behind the report's
// most useful column.
//
// Five symbols in this tree are selected by more than one domain and declared
// by more than one domain: `Event`, `Rule`, `Usecase`, `ListFilter`, `Caller`.
// Each of those is two or more unrelated types that happen to share a name —
// alert.Event is not audit.Event, device.ListFilter is not edge.ListFilter.
//
// That makes them the most dangerous entries in the shared-symbol list, and
// the reason a shared-symbol ranking cannot be acted on by sorting it. A move
// that carries "ListFilter" to a shared home without noticing it has two
// owners does not fail to compile; it compiles, and it silently changes what
// one of the two means. Nothing downstream can see it.
//
// So the list is pinned. The assertion is that these are still ambiguous, so
// that the day someone gives one of them a single owner this test goes red and
// the name is promoted to a candidate — and, just as importantly, so nobody
// reads the current ranking and concludes the top of the list is safe to move.
func TestTheSameNameSeveralOwnersListIsReal(t *testing.T) {
	ambiguous := map[string]bool{}
	for _, r := range sharedRows(t) {
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
			"column is empty; the five names below have all become single-owner and are now " +
			"move candidates. Fail on purpose so this is a decision, not a drift")
	}
	// Every one of these must still be ambiguous. A name that has become
	// single-owner is a move candidate and belongs in the report's main list,
	// not here — and the failure message says so rather than just "changed".
	for _, sym := range []string{"Event", "Rule", "Usecase", "ListFilter", "Caller"} {
		if !ambiguous[sym] {
			t.Errorf("%s now has a single declaring domain, so it is a real move candidate and "+
				"belongs at the top of the shared list; it was ambiguous when this was written", sym)
		}
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
// deliver is the opposite: `ListFilter` is not a shared shape that happens to
// have two names, it is one name over three unrelated query vocabularies —
// edge filters by status and creator, device filters by role bits and
// hostname, gitartifact filters by tenant and branch. They share `Limit` and
// nothing else. Once that is printed, the row stops looking like a move
// candidate and starts looking like a coincidence, which is the correct
// reading and the one a ranking by consumer count would never have produced.
func TestTheTrapColumnSaysWhyNotJustThat(t *testing.T) {
	shapes := map[string][]string{}
	for _, r := range sharedRows(t) {
		if len(r.targets) > 1 {
			shapes[r.sym] = r.shapes
		}
	}
	lf, ok := shapes["ListFilter"]
	if !ok {
		t.Fatal("ListFilter is no longer reported as ambiguous; if that is real it has become a " +
			"move candidate and belongs at the top of the shared list")
	}
	if len(lf) < 2 {
		t.Fatalf("ListFilter has %d declaring packages reported, want at least 2", len(lf))
	}
	// The owners must be visibly different. The comparison is on the field
	// list alone, not on the whole rendered line: the package path differs
	// even when the fields are identical, so comparing lines would pass for a
	// report that printed the same struct three times under three names. That
	// is exactly the mutation this assertion was written against.
	shapesSeen := map[string]bool{}
	for _, line := range lf {
		open := strings.Index(line, "{")
		shape := line
		if open >= 0 {
			shape = line[open:]
		}
		shapesSeen[shape] = true
	}
	if len(shapesSeen) < 2 {
		t.Errorf("all ListFilter owners rendered the same field list %v; the report is not showing "+
			"the shapes it claims to show, and a reader would merge types that are not alike", lf)
	}
	// And the specific shape that makes the point: none of the three owners
	// filters by both a status and a hostname, which is what "one shared
	// filter type" would look like if it existed.
	joined := strings.Join(lf, " ")
	if strings.Contains(joined, "Hostname") && strings.Contains(joined, "Status") {
		for _, line := range lf {
			if strings.Contains(line, "Hostname") && strings.Contains(line, "Status") {
				t.Errorf("one owner of ListFilter carries both Hostname and Status (%s); the three "+
					"are documented as unrelated, so either the tree changed or the report is wrong", line)
			}
		}
	}
}
