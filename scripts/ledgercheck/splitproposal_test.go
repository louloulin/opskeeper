package ledgercheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryEdgeIntoTheAuditChainIsInTheSplitProposal keeps decision 196's
// conclusion from rotting.
//
// That decision read all 43 declared cross-domain edges and concluded that
// exactly four of them are physical constraints: the four that write the one
// ordered HMAC chain, which is why they cannot be split across processes
// without a distributed lock. The other 39 share rows or vocabulary, and rows
// can be reached over an interface while an ordered chain cannot.
//
// "The four that point at audit" is mechanically checkable even though "which
// edges are physical constraints" is not. So this gate checks the part that
// is: every edge whose target is the audit domain has to be named in the split
// proposal's constraint list. A new `something -> audit` edge — a sixth writer
// of the chain, which is exactly the change that would make the proposal's
// five-domain lower bound wrong — turns this red instead of quietly making a
// document stale.
var proposalHardEdgeRE = regexp.MustCompile(`(?m)^#\s+(\d+)\.\s+([a-z0-9]+)\s*->\s*audit\b`)

var edgeRE = regexp.MustCompile(`\{"([a-z0-9]+)",\s*"([a-z0-9]+)"\}`)

func TestEveryEdgeIntoTheAuditChainIsInTheSplitProposal(t *testing.T) {
	// The edges the checker itself declares.
	raw, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "domaincheck", "main.go"))
	if err != nil {
		t.Fatalf("read the domain table: %v", err)
	}
	intoAudit := map[string]bool{}
	for _, m := range edgeRE.FindAllStringSubmatch(string(raw), -1) {
		if m[2] == "audit" {
			intoAudit[m[1]] = true
		}
	}
	if len(intoAudit) == 0 {
		t.Fatal("no declared edge points at the audit domain, so this gate is looking at nothing")
	}

	// The ones the proposal names as chain writers.
	proposalRaw, err := os.ReadFile(filepath.Join(repoRoot, "docs", "manager-split.proposed"))
	if err != nil {
		t.Fatalf("read the split proposal: %v", err)
	}
	named := map[string]bool{}
	for _, m := range proposalHardEdgeRE.FindAllStringSubmatch(string(proposalRaw), -1) {
		named[m[2]] = true
	}
	if len(named) == 0 {
		t.Fatal("the split proposal names no edges into the audit chain, so this gate is looking at nothing")
	}

	var missing, extra []string
	for from := range intoAudit {
		if !named[from] {
			missing = append(missing, from)
		}
	}
	for from := range named {
		if !intoAudit[from] {
			extra = append(extra, from)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "these domains have a declared edge into the audit chain but the split "+
			"proposal does not name them among the writers: "+strings.Join(missing, ", ")+
			". A sixth writer changes the five-domain lower bound decision 196 derived")
	}
	if len(extra) > 0 {
		problems = append(problems, "the split proposal names these as audit-chain writers but no such edge "+
			"is declared: "+strings.Join(extra, ", ")+". The proposal's lower bound is resting on an edge that moved")
	}
	if len(problems) > 0 {
		t.Errorf("the split proposal and the declared domain edges disagree about who writes the audit chain:\n  %s",
			strings.Join(problems, "\n  "))
	}
}
