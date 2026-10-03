package main

import "testing"

func TestDomainOfUsesTheSameLayerRuleDomaincheckDoes(t *testing.T) {
	for path, want := range map[string]string{
		"core/manager/biz/aiops/loop":        "aiops",
		"core/manager/server/federation/http": "federation",
		"core/manager/service/federationlink": "federationlink",
		"core/manager/model/audit":            "audit",
		"core/manager/data/hitl/store":        "hitl",
		"core/manager/pkg/audit":              "pkg",
		"core/manager/knowledge/index":        "knowledge",
		// Outside the control plane entirely.
		"core/edge/agentprofile/profile.go": "",
		"core/floor/skill/builtin/host.go":   "",
		"cmd/opskeeper/main.go":              "",
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
