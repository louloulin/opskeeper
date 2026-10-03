package main

import "testing"

func TestDomainOfUsesTheSameLayerRuleDomaincheckDoes(t *testing.T) {
	for path, want := range map[string]string{
		"core/manager/biz/aiops/loop":         "aiops",
		"core/manager/server/federation/http": "federation",
		"core/manager/service/federationlink": "federationlink",
		"core/manager/model/audit":            "audit",
		"core/manager/data/hitl/store":        "hitl",
		"core/manager/pkg/audit":              "pkg",
		"core/manager/knowledge/index":        "knowledge",
		// Outside the control plane entirely.
		"core/edge/agentprofile/profile.go": "",
		"core/floor/skill/builtin/host.go":  "",
		"cmd/opskeeper/main.go":             "",
	} {
		if got := domainOf(path); got != want {
			t.Errorf("domainOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// A build file is not a bounded context, and ranking it as one would put
// go.mod at the top of a report about which domains evolve on their own.
func TestAModuleFileIsNotADomain(t *testing.T) {
	for _, path := range []string{"core/manager/go.mod", "core/manager/go.sum"} {
		if got := domainOf(path); got != "" {
			t.Errorf("domainOf(%q) = %q, want no domain", path, got)
		}
	}
}

func TestSummariseCountsADomainChangedAloneOnlyWhenItWasAlone(t *testing.T) {
	r := summarise([]Change{
		{Commit: "a", Domains: []string{"aiops"}},
		{Commit: "b", Domains: []string{"aiops"}},
		{Commit: "c", Domains: []string{"aiops", "edge"}},
		{Commit: "d", Domains: []string{"edge"}},
		{Commit: "e", Domains: []string{"edge", "audit"}},
	})

	byName := map[string]Solo{}
	for _, s := range r.Solo {
		byName[s.Domain] = s
	}
	if got := byName["aiops"]; got.Solo != 2 || got.Total != 3 {
		t.Errorf("aiops = %+v, want 2 solo of 3", got)
	}
	if got := byName["edge"]; got.Solo != 1 || got.Total != 3 {
		t.Errorf("edge = %+v, want 1 solo of 3", got)
	}
	if got := byName["audit"]; got.Solo != 0 || got.Total != 1 {
		t.Errorf("audit = %+v, want 0 solo of 1", got)
	}
	// The ranking exists to put the most independently changed first.
	if r.Solo[0].Domain != "aiops" {
		t.Errorf("top of the ranking = %q, want aiops", r.Solo[0].Domain)
	}
}

func TestSummariseCountsCoChangeOncePerCommitNotPerFile(t *testing.T) {
	// Five files in one domain, one commit: that is one change, and a
	// pair that appears in a single commit is worth one, not five.
	r := summarise([]Change{
		{Commit: "a", Domains: []string{"aiops", "edge"}},
		{Commit: "b", Domains: []string{"aiops", "edge"}},
	})
	if len(r.Pairs) != 1 || r.Pairs[0].Times != 2 {
		t.Fatalf("pairs = %+v, want a single pair at 2", r.Pairs)
	}
	if r.Pairs[0].A != "aiops" || r.Pairs[0].B != "edge" {
		t.Errorf("pair = %+v, want aiops/edge", r.Pairs[0])
	}
}

// The report tells the reader whether the ranking is worth anything. A
// repository where every commit touches everything produces a high median,
// and presenting that ranking without saying so would be the whole failure
// this tool could cause.
func TestTheMedianIsWhatTheWarningIsBasedOn(t *testing.T) {
	spread := summarise([]Change{
		{Commit: "a", Domains: []string{"x"}},
		{Commit: "b", Domains: []string{"x", "y", "z"}},
		{Commit: "c", Domains: []string{"x", "y", "z", "w"}},
	})
	if spread.MedianDomains != 3 {
		t.Errorf("median = %v, want 3", spread.MedianDomains)
	}
	if spread.Widest != 4 {
		t.Errorf("widest = %d, want 4", spread.Widest)
	}
}

// Even counts take the upper of the two middle values, not their average.
// The report prints "domains per commit" as a whole number, and 1.5
// domains is not a thing anyone can act on; the convention is pinned here
// so a later "fix" to a true median is a decision rather than a drift.
func TestTheMedianTakesTheUpperMiddleOnAnEvenCount(t *testing.T) {
	r := summarise([]Change{
		{Commit: "a", Domains: []string{"x"}},
		{Commit: "b", Domains: []string{"x", "y", "z"}},
	})
	if r.MedianDomains != 3 {
		t.Errorf("median = %v, want the upper middle value 3", r.MedianDomains)
	}
}

func TestACommitThatTouchedNothingInTheControlPlaneIsNotCounted(t *testing.T) {
	r := summarise([]Change{
		{Commit: "a", Domains: []string{"aiops"}},
		{Commit: "b", Domains: nil},
	})
	if r.Touching != 1 {
		t.Errorf("Touching = %d, want 1", r.Touching)
	}
}

// A ratio of 50% reads the same whether a domain was worked on all quarter
// or refactored in one sitting. The run count is what tells them apart, and
// a reader who only sees the ratio will over-read every single-run domain.
func TestSoloRunsSeparatesOnePushFromAQuarterOfWork(t *testing.T) {
	cases := []struct {
		name        string
		positions   []int
		wantRuns    int
		wantLongest int
	}{
		{name: "nothing at all", positions: nil, wantRuns: 0, wantLongest: 0},
		{name: "one commit", positions: []int{4}, wantRuns: 1, wantLongest: 1},
		{name: "one unbroken push", positions: []int{3, 4, 5, 6, 7}, wantRuns: 1, wantLongest: 5},
		{name: "one push and a later one", positions: []int{3, 4, 5, 9}, wantRuns: 2, wantLongest: 3},
		{name: "all separate", positions: []int{1, 4, 7, 10}, wantRuns: 4, wantLongest: 1},
		// The order git hands commits back in must not change the answer.
		{name: "unsorted input is sorted first", positions: []int{9, 5, 4, 3}, wantRuns: 2, wantLongest: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runs, longest := soloRuns(tc.positions)
			if runs != tc.wantRuns || longest != tc.wantLongest {
				t.Fatalf("soloRuns(%v) = %d runs, longest %d; want %d runs, longest %d",
					tc.positions, runs, longest, tc.wantRuns, tc.wantLongest)
			}
		})
	}
}

// A domain created inside the measured window scores 100% solo for free:
// there was nothing to co-change with while it did not exist. Without this
// correction, "written once" is indistinguishable from "ships on its own".
func TestSummariseMarksSoloWorkThatIsOnlyTheDomainBeingBuilt(t *testing.T) {
	r := summarise([]Change{
		// newguy is born here and its opening three commits touch only it.
		{Commit: "1", Domains: []string{"newguy"}},
		{Commit: "2", Domains: []string{"newguy"}},
		{Commit: "3", Domains: []string{"newguy"}},
		{Commit: "4", Domains: []string{"newguy", "edge"}},
		// veteran is born already wired to edge, then goes quiet, and only
		// comes back for two commits of its own.
		{Commit: "5", Domains: []string{"veteran", "edge"}},
		{Commit: "6", Domains: []string{"unrelated"}},
		{Commit: "7", Domains: []string{"veteran"}},
		{Commit: "8", Domains: []string{"veteran"}},
	})

	byName := map[string]Solo{}
	for _, s := range r.Solo {
		byName[s.Domain] = s
	}

	if got := byName["newguy"]; got.Built != got.Solo || got.Solo != 3 {
		t.Errorf("newguy = %+v, want Built == Solo == 3 (all of it is construction)", got)
	}
	// veteran's first change touched edge, and it stayed quiet for a
	// commit, so its opening run is one commit long. The two solo commits
	// land after that, on a domain that already existed: real standalone
	// work, not construction.
	if got := byName["veteran"]; got.Built != 0 || got.Solo != 2 {
		t.Errorf("veteran = %+v, want Built == 0 and Solo == 2", got)
	}
}

// A commit that was part of building a domain does not stop being
// construction because it happened to touch a second domain.
func TestFirstRunCountsTheWholeOpeningStretchNotJustTheSoloPart(t *testing.T) {
	in := firstRun([]int{2, 3, 4, 9})
	want := map[int]bool{2: true, 3: true, 4: true}
	if len(in) != len(want) {
		t.Fatalf("firstRun = %v, want %v", in, want)
	}
	for pos := range want {
		if !in[pos] {
			t.Fatalf("firstRun is missing %d: %v", pos, in)
		}
	}
	if in[9] {
		t.Fatalf("firstRun swallowed the later change at 9: %v", in)
	}
	if got := countIn([]int{2, 3, 9}, in); got != 2 {
		t.Fatalf("countIn = %d, want 2", got)
	}
	if got := firstRun(nil); len(got) != 0 {
		t.Fatalf("firstRun(nil) = %v, want empty", got)
	}
}

// A commit that touched no domain is outside the study, not a gap in a
// domain's history. Letting it break a run would report a single campaign
// as several, which is the error this whole section exists to prevent.
func TestACommitTouchingNoDomainDoesNotBreakACampaign(t *testing.T) {
	withGap := summarise([]Change{
		{Commit: "1", Domains: []string{"aiops"}},
		{Commit: "2", Domains: nil}, // docs-only
		{Commit: "3", Domains: []string{"aiops"}},
	})
	byName := map[string]Solo{}
	for _, s := range withGap.Solo {
		byName[s.Domain] = s
	}
	if got := byName["aiops"]; got.Runs != 1 || got.LongestRun != 2 {
		t.Errorf("aiops = %+v, want 1 run of 2 — a docs commit is not a day", got)
	}
	if withGap.Touching != 2 {
		t.Errorf("Touching = %d, want 2 (the no-domain commit is not in the study)", withGap.Touching)
	}
}
