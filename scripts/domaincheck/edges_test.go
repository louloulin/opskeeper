package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The edge report is the only one here that reads method calls, and it exists
// because two earlier measurements got that question wrong in the same
// direction: both counted a domain that calls four methods as one that calls
// none, because they keyed on the field's name. So most of what follows is
// about the three ways a call can be missed, and one of them is a call that
// should not be counted at all.

// treeWith writes a two-domain control-plane tree to a temp directory and
// parses it the way the shipped tree is parsed. Parsing a fixture through
// parser.ParseFile and hand-building the `used` map instead would test a
// second implementation of the import walk, and a second implementation is
// exactly the thing that was wrong twice.
func treeWith(t *testing.T, files map[string]string) []source {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("make %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	sources, _, err := parseTree(dir, managerPrefix, testRules())
	if err != nil {
		t.Fatalf("parse the fixture tree: %v", err)
	}
	return sources
}

const producerPkg = managerPrefix + "biz/producer"

func edgeReport(t *testing.T, sources []source, r rules) string {
	t.Helper()
	var sb strings.Builder
	printEdges(&sb, sources, r)
	return sb.String()
}

// TestAMethodCalledThroughAFieldNamedAfterNothingFindsTheEdge is the regression
// for the measurement that reported a confident zero.
//
// `frontierbound` names its edge dependency `w.EdgeUC`. The regex that keyed
// on the field's name matched `edges` and `edge` and nothing else, so a domain
// calling four methods came out as a domain calling none — and a zero here
// reads exactly like "this edge is cheap to cut". The report resolves the
// method from the producer's own declarations instead, so the field's name is
// not in the question.
func TestAMethodCalledThroughAFieldNamedAfterNothingFindsTheEdge(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/producer/p.go": `package producer

import "context"

type Usecase struct{ n int }

func (u *Usecase) HandleHeartbeat(ctx context.Context) error { return nil }

func (u *Usecase) HandleOffline(ctx context.Context) error  { return nil }
`,
		"biz/consumer/c.go": `package consumer

import (
	"context"

	pb "` + producerPkg + `"
)

type worker struct{ EdgeUC *pb.Usecase }

func (w *worker) tick(ctx context.Context) error {
	if err := w.EdgeUC.HandleHeartbeat(ctx); err != nil {
		return err
	}
	return w.EdgeUC.HandleOffline(ctx)
}
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	for _, want := range []string{"HandleHeartbeat", "HandleOffline"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %s.\n%s\nA method reached through a field "+
				"whose name shares nothing with the domain is still a method on this edge, and "+
				"missing it is how a four-method edge was once reported as a zero-method one",
				want, out)
		}
	}
}

// TestACallInsideACommentIsNotACall is the other half of reading the tree
// instead of grepping it.
//
// server/webshell has carried `// edges, _ := h.edges.List(` for long enough
// that a text search counted webshell as a consumer of edge. It is a comment.
// A report whose numbers decide what gets cut next cannot be built on a
// matcher that reads prose.
func TestACallInsideACommentIsNotACall(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/producer/p.go": `package producer

import "context"

type Usecase struct{}

func (u *Usecase) List(ctx context.Context) error { return nil }
`,
		"biz/consumer/c.go": `package consumer

import (
	"context"

	pb "` + producerPkg + `"
)

type h struct{ edges *pb.Usecase }

// Kept for reference while the projection lands:
//
//	_, _ = h.edges.List(context.Background())
func (h *h) live(ctx context.Context) error { return nil }
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	if strings.Contains(out, "List ") {
		t.Errorf("the report counted a commented-out call.\n%s", out)
	}
	// The import itself is still live — the struct field names the type — so
	// the row legitimately reports one selected type. What it must not report
	// is the method, and the row has to say so in words rather than just
	// omitting a column a reader is looking for.
	if !strings.Contains(out, "no method") {
		t.Errorf("an edge whose only call is commented out must read as selecting a type and no "+
			"method, so a reader can tell it apart from an edge nothing was ever measured "+
			"against.\n%s", out)
	}
}

// TestASameNamedCallInAFileThatDoesNotImportTheProducerIsNotThisEdge keeps the
// method column from being a guess. The column is documented as an upper
// bound, and the bound only means something if it is bounded by something.
func TestASameNamedCallInAFileThatDoesNotImportTheProducerIsNotThisEdge(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/producer/p.go": `package producer

import "context"

type Usecase struct{}

func (u *Usecase) HandleHeartbeat(ctx context.Context) error { return nil }
`,
		"biz/consumer/c.go": `package consumer

import "context"

type unrelated struct{}

func (u *unrelated) HandleHeartbeat(ctx context.Context) error { return nil }

func run(ctx context.Context) error { return (&unrelated{}).HandleHeartbeat(ctx) }
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	if strings.Contains(out, "HandleHeartbeat") {
		t.Errorf("a same-named method on an unrelated type, in a file that does not import the "+
			"producer, was attributed to this edge.\n%s", out)
	}
}

// TestTheMethodColumnSaysItIsAnUpperBound keeps the report honest about the one
// thing about it that is not exact. A column of method names printed without
// that caveat would be read as a count, and a count is a thing people plan
// against.
func TestTheMethodColumnSaysItIsAnUpperBound(t *testing.T) {
	sources, _, err := parseControlPlane("../..")
	if err != nil {
		t.Fatalf("reading the shipped tree: %v", err)
	}
	out := edgeReport(t, sources, defaultRules())
	if !strings.Contains(out, "UPPER BOUND") {
		t.Errorf("the report does not say its method column is an upper bound.\n%s\n"+
			"Without that line every number below it reads as exact, and the ones that are not "+
			"are the ones a reader will plan a refactor around", out)
	}
	// Every hit names its file, so a wrong attribution is one glance away
	// rather than something the reader has to take on trust.
	var hits int
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "        method ") {
			hits++
			fields := strings.Fields(line)
			if len(fields) < 4 || !strings.Contains(fields[len(fields)-1], "/") {
				t.Errorf("method line does not name the file it was seen in: %q", line)
			}
		}
	}
	if hits == 0 {
		t.Fatal("no method lines in the report for the shipped tree; it stopped reading calls")
	}
}

var edgeRowRE = regexp.MustCompile(`^\s+(\d+)\s+(\S+)\s+->\s+(\S+)\s+(.*)$`)

// TestTheEdgeReportCoversEveryDeclaredEdge is the same tie to the graph the seam
// report has. A report that dropped an edge could report an empty cheap bucket
// without meaning one, and that is the conclusion nobody should draw from a
// tool this new.
func TestTheEdgeReportCoversEveryDeclaredEdge(t *testing.T) {
	sources, _, err := parseControlPlane("../..")
	if err != nil {
		t.Fatalf("reading the shipped tree: %v", err)
	}
	out := edgeReport(t, sources, defaultRules())
	seen := map[edge]bool{}
	for _, line := range strings.Split(out, "\n") {
		m := edgeRowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen[edge{from: m[2], to: m[3]}] = true
	}
	for e := range edges {
		if !seen[e] {
			t.Errorf("the edge report has no row for the declared edge %s -> %s", e.from, e.to)
		}
	}
	if len(seen) != len(edges) {
		t.Errorf("the edge report has %d rows for %d declared edges", len(seen), len(edges))
	}
}

// TestTheReportIsSortedCheapestFirst is the deliverable, so it is asserted
// rather than assumed: the ordering is what tells a reader which edge to cut
// today, and an unsorted table with the same content in it is a different
// report.
func TestTheReportIsSortedCheapestFirst(t *testing.T) {
	sources, _, err := parseControlPlane("../..")
	if err != nil {
		t.Fatalf("reading the shipped tree: %v", err)
	}
	out := edgeReport(t, sources, defaultRules())
	prev := -1
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		m := edgeRowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n := atoiOrFail(t, m[1])
		if n < prev {
			t.Errorf("the report goes from %d symbols back up to %d at %s -> %s; the ordering is "+
				"the deliverable and a table out of order is a different report",
				prev, n, m[2], m[3])
		}
		prev = n
		rows++
	}
	if rows == 0 {
		t.Fatal("the edge report produced no rows for the shipped tree")
	}
}

// TestTheTwoEdgesDecision235CutAreNotOnTheList is the reason the report exists
// in this shape, stated as a fact about the tree rather than as a comment
// about a commit. `alert` and `systemhealth` each called one method on the edge
// domain and passed it a bare limit; they now hold a one-method port declared
// in core/domain. If either edge ever comes back, it comes back with a
// compiler error or a red gate, not with a reader noticing.
func TestTheTwoEdgesDecision235CutAreNotOnTheList(t *testing.T) {
	for _, e := range []edge{{from: "alert", to: "edge"}, {from: "systemhealth", to: "edge"}} {
		if _, ok := edges[e]; ok {
			t.Errorf("%s -> %s is declared again; if it came back, say what now needs it, because "+
				"both of those domains hold a one-method port in core/domain and neither needs "+
				"the edge domain's twenty methods", e.from, e.to)
		}
	}
}
