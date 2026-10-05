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

// The closure is what a type drags behind it, and it is the half of the price
// the direct count could not see. Everything below is about that half, and
// about the two ways the first version of the walk was wrong: it followed
// unexported fields, which a dependent cannot read, and it read "declared
// elsewhere" off a string comparison, which called a type foreign when the
// producer declared it too.

// priceOf pulls the price column out of the row for one edge.
func priceOf(t *testing.T, out string, from, to string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		m := edgeRowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[2] == from && m[3] == to {
			return atoiOrFail(t, m[1])
		}
	}
	t.Fatalf("no row for %s -> %s in:\n%s", from, to, out)
	return 0
}

// TestAFieldTypeTheConsumerNeverNamesIsInThePrice is the regression for the
// number this round set out to correct.
//
// `marketplace` writes down two symbols when it uses `pluginimport`: an
// `Options` and a `Report`. It does not write down that `Report` has a `Kind`
// field of a type declared in a third domain and a `Warnings` field of a type
// declared in two. Moving `Report` to core/domain means moving those too, so
// the edge is six, not two — and a ranking built on two sends the next cut at
// the wrong edge.
func TestAFieldTypeTheConsumerNeverNamesIsInThePrice(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/producer/p.go": `package producer

type Kind string

const KindNone Kind = "none"

type Report struct {
	Name     string
	Kind     Kind
	Warnings []LoadWarning
}

type LoadWarning struct {
	Path   string
	Reason string
}
`,
		"biz/consumer/c.go": `package consumer

import pb "` + producerPkg + `"

type Handler struct{ Root string }

// The consumer names exactly one symbol and reads the other two out of it.
func (h *Handler) show(r *pb.Report) string { return string(r.Kind) }
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	if got := priceOf(t, out, "consumer", "producer"); got != 3 {
		t.Errorf("the edge is priced at %d, and it should be 3: one named type plus the two "+
			"types its fields name.\n%s\nA struct that names another type cannot be moved "+
			"without moving that type, and a price that says otherwise is a price that "+
			"picks the wrong edge to cut next", got, out)
	}
	for _, want := range []string{"closure Kind", "closure LoadWarning"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not list %q in the closure.\n%s", want, out)
		}
	}
	// The path is what makes a closure checkable against the code. A bare
	// list of names could be in the report and still be wrong.
	if !strings.Contains(out, "via Report.Kind") {
		t.Errorf("a closure hit is printed without the field path that reaches it.\n%s", out)
	}
}

// TestAClosureIntoAnotherDomainIsNamedIs the other half: not counting is not
// enough, the reader has to be told the cut leaves the edge it was priced
// under. `Pusher` is an interface here, so nothing would follow from it — the
// drag has to be visible on the row.
func TestAClosureIntoAnotherDomainIsNamed(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/other/o.go": `package other

type Bundle struct {
	Digest string
}
`,
		// The producer itself reaches into the other domain, which is the
		// shape the real federation edge has: `federation.Member` carries a
		// field typed `federation.Bundle`, and that type is declared in
		// aiops. The drag is in the producer's own shape, so it is in the
		// closure of anything that moves the shape.
		"biz/producer/p.go": `package producer

import ob "github.com/vincent-wuhan/opskeeper/core/manager/biz/other"

type Member struct {
	Cluster string
	Bundle  ob.Bundle
}
`,
		"biz/consumer/c.go": `package consumer

import (
	pb "` + producerPkg + `"
	ob "github.com/vincent-wuhan/opskeeper/core/manager/biz/other"
)

type Handler struct{ Members []pb.Member }

// The consumer holds a slice of the producer's type and also imports the
// other domain directly, so the Bundle below is not the only way in.
func (h *Handler) count(b ob.Bundle) int { return len(b.Digest) }
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	if !strings.Contains(out, "reaches other") {
		t.Errorf("an edge whose closure reaches a second domain is printed without saying so.\n%s\n"+
			"\"cheap\" and \"cheap and drags a third domain in\" are different amounts of work, "+
			"and the row is where a reader finds out which one this is", out)
	}
	// The row summary names the domain; this line names the type. Both are
	// printed because a reader deciding whether to cut an edge needs to know
	// which domain it lands in and what is landing there, and a row that
	// carried only the first would send them to the producer to find out.
	if !strings.Contains(out, "ob.Bundle") || !strings.Contains(out, "lives in other, not in producer") {
		t.Errorf("a cross-domain closure hit is printed without naming the type or the domain it "+
			"lives in.\n%s", out)
	}
}

// TestAnUnexportedFieldIsNotInThePrice is the mutation the first version of
// this walk needed.
//
// `audit.Usecase` is four unexported fields, and following them reported it as
// dragging nineteen domains — every one of them reached through `repo` and
// `log`. A dependent in another package cannot read those fields, so cutting
// the edge does not move them, and the price was inflated by exactly the
// fields that were never in question.
func TestAnUnexportedFieldIsNotInThePrice(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/producer/p.go": `package producer

type Repo interface{ List() error }

type Usecase struct {
	repo Repo
	Name string
}
`,
		"biz/consumer/c.go": `package consumer

import pb "` + producerPkg + `"

type Handler struct{ UC *pb.Usecase }

func (h *Handler) run() string { return h.UC.Name }
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	if got := priceOf(t, out, "consumer", "producer"); got != 1 {
		t.Errorf("the edge is priced at %d, and it should be 1.\n%s\n"+
			"Usecase has one exported field and one unexported one, and the unexported one is "+
			"not reachable from another package, so it is not part of what a cut moves",
			got, out)
	}
	if strings.Contains(out, "closure Repo") {
		t.Errorf("an unexported field's type was counted in the closure.\n%s", out)
	}
	// The shape column still lists it, and the header has to say that the two
	// columns differ on purpose — otherwise the shape looks like the price.
	if !strings.Contains(out, "Usecase") {
		t.Errorf("the selected type is no longer printed at all.\n%s", out)
	}
}

// TestATypeNoDomainDeclaresIsDropped keeps the walk from counting things that
// were never going to move. `time.Time` belongs to the standard library; a
// price that included it would be charging for a type the cut cannot touch.
func TestATypeNoDomainDeclaresIsDropped(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/producer/p.go": `package producer

import "time"

type Row struct {
	At   time.Time
	Name string
}
`,
		"biz/consumer/c.go": `package consumer

import pb "` + producerPkg + `"

type Handler struct{ Rows []pb.Row }

func (h *Handler) first() pb.Row { return h.Rows[0] }
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	if got := priceOf(t, out, "consumer", "producer"); got != 1 {
		t.Errorf("the edge is priced at %d, and it should be 1.\n%s\n"+
			"a time.Time field does not become a piece of work when a domain boundary moves",
			got, out)
	}
}

// TestAMarketplacePluginImportIsNotATwoSymbolEdge is the shipped-tree version of
// the same claim, and it is the one that would have stopped this round's cut
// from being the wrong cut. If the importer's report grows or shrinks, the
// test says the assertion moved, rather than the number quietly sliding back
// to something the ranking likes.
func TestAMarketplacePluginImportIsNotATwoSymbolEdge(t *testing.T) {
	sources, _, err := parseControlPlane("../..")
	if err != nil {
		t.Fatalf("reading the shipped tree: %v", err)
	}
	out := edgeReport(t, sources, defaultRules())

	row := ""
	for _, line := range strings.Split(out, "\n") {
		m := edgeRowRE.FindStringSubmatch(line)
		if m != nil && m[2] == "marketplace" && m[3] == "pluginimport" {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("the edge report has no row for marketplace -> pluginimport.\n%s", out)
	}
	if strings.Contains(row, "no method + 4 closure") == false && !strings.Contains(row, "closure") {
		t.Errorf("the marketplace -> pluginimport row prices the two types the route names and "+
			"nothing behind them, which is what made this edge look like the cheapest one on the "+
			"list.\n%s\nIts Report names a Kind declared in aiops and a Warnings slice of a type "+
			"declared in two domains; moving it means moving those", out)
	}
	// And the specific drag, named: this is the fact the cut has to plan for.
	if !strings.Contains(out, "via Report.Kind") {
		t.Errorf("the report does not say which field drags aiops vocabulary behind the "+
			"pluginimport report.\n%s", out)
	}
}

// TestATypeTheProducerAlsoDeclaresIsNotReportedAsSomebodyElses is decision 233's
// shape arriving in the closure walk.
//
// `pluginimport.Decision` is declared in pluginimport and in three other
// domains. The first version of this walk compared the declaring domain to the
// producer's with `!=`, so a name the producer declares itself came back as
// "lives in agentteams|control|nodefleet|pluginimport, not in pluginimport" —
// which is false in the direction that costs the most, because it tells a
// reader the type has to come from somewhere it already is.
func TestATypeTheProducerAlsoDeclaresIsNotReportedAsSomebodyElses(t *testing.T) {
	sources := treeWith(t, map[string]string{
		"biz/producer/p.go": `package producer

type Decision struct {
	Field    string
	Question string
}

type Report struct {
	Decisions []Decision
}
`,
		"biz/other/o.go": `package other

type Decision struct {
	Field string
	Other string
}
`,
		"biz/consumer/c.go": `package consumer

import pb "` + producerPkg + `"

type Handler struct{ Report *pb.Report }

func (h *Handler) unanswered() int { return len(h.Report.Decisions) }
`,
	})
	r := testRules()
	r.edges[edge{from: "consumer", to: "producer"}] = "a fixture edge"
	out := edgeReport(t, sources, r)

	if strings.Contains(out, "not in producer") {
		t.Errorf("a type the producer itself declares is reported as living in another domain.\n%s\n"+
			"string inequality is not membership: the producer is one of the four domains that "+
			"declare Decision, and the mover is already holding it", out)
	}
	// The ambiguity is still worth saying, because "which of the four shapes"
	// is a real question — but it is a different sentence from "not yours".
	if !strings.Contains(out, "so the shape is not settled") {
		t.Errorf("a name declared by two domains is reported with no word about the "+
			"ambiguity.\n%s", out)
	}
}

// TestTheEdgeDecision238CutIsNotOnTheList states the fact about the tree rather
// than about a commit, in the same shape as decision 235's test.
//
// `server/mcp` needed two event structs and an interface, and all three lived
// in `biz/aiops/tools/decorators` — the package that also holds governance,
// review gates, rate limiters and untrusted-output marking. So the edge
// existed to carry two structs with no behaviour in them. They now live in
// core/domain, which the plan already gives event contracts to.
func TestTheEdgeDecision238CutIsNotOnTheList(t *testing.T) {
	if _, ok := edges[edge{from: "mcp", to: "aiops"}]; ok {
		t.Error("mcp -> aiops is declared again; if it came back, say what now needs it, because " +
			"the tool-call audit seam is a port in core/domain and the only thing server/mcp " +
			"took from aiops was the two shapes that port is made of")
	}
}

// TestTheEdgeDecision240CutIsNotOnTheList states the fact about the tree, in
// the same shape as decisions 235's and 238's.
//
// `biz/aiops/tools/skill_bridge.go` had already written its own one-method
// `SkillRunner`; the only thing its signature named from the skill service were
// three structs. Those now live in core/domain as SkillCaller / SkillExecution
// / SkillOutcome, and the bridge holds `domain.SkillExecutor` by alias.
//
// The rename is the part worth remembering: `Caller` is declared seven times in
// this repository for five different things, so putting an eighth one in the
// namespace every domain shares would have made an already-ambiguous name
// ambiguous in one more place — and core/domain has a test that refuses a bare
// `Caller` for exactly that reason.
func TestTheEdgeDecision240CutIsNotOnTheList(t *testing.T) {
	if _, ok := edges[edge{from: "aiops", to: "skill"}]; ok {
		t.Error("aiops -> skill is declared again; if it came back, say what now needs it, " +
			"because the tool bridge holds a one-method port in core/domain and nothing in it " +
			"needs the skill service's audit rows, scope routing, tunnel round trip or catalogue")
	}
}
