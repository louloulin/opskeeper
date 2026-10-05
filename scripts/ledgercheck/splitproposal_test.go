package ledgercheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestTheCurrentAuditEdgesAreNamedInTheSplitProposal keeps the split
// proposal's audit-chain list from rotting against the declared edges.
//
// The list used to be decision 196's four physical constraints — the four
// writers of the one ordered HMAC chain, which is why they could not be split
// across processes without a distributed lock. Decision 272 gave the throat a
// port (core/base/pkg/audit), all four of those holders now hold interfaces
// rather than the concrete façade, and the four edges are gone. What is left
// pointing at the audit domain is one READER: the change-events tool joins a
// change to the operator who authorised it, and rows carry no chaining
// property, so a projection bridges them and a chain would not.
//
// So the gate's subject changed and its purpose did not. It still asks the one
// mechanically checkable half of "which edges into the chain are physical":
// every declared edge whose target is the audit domain has to be named in the
// proposal's list. A new `something -> audit` edge — a second writer, which is
// exactly the change that would make the proposal's lower bound wrong again —
// turns this red instead of quietly making a document stale.
//
// The `now` prefix on the lines it reads is what keeps the two lists apart.
// The proposal keeps decision 196's original four lines as a record, and a
// regex that matched both would demand the document name three edges that no
// longer exist — which is the same class of error as a stale price quote, in
// the opposite direction.
var proposalHardEdgeRE = regexp.MustCompile(`(?m)^#\s+now\s+(\d+)\.\s+([a-z0-9]+)\s*->\s*audit\b`)

var edgeRE = regexp.MustCompile(`\{"([a-z0-9]+)",\s*"([a-z0-9]+)"\}`)

func TestTheCurrentAuditEdgesAreNamedInTheSplitProposal(t *testing.T) {
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
		t.Fatal("the split proposal's current list names no edges into the audit chain, so this " +
			"gate is looking at nothing. The `now` prefix is what the regex reads; if the list was " +
			"reformatted, restore it rather than deleting the list")
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
			"proposal's current list does not name them: "+strings.Join(missing, ", ")+
			". A second writer of the chain changes the lower bound the proposal derives")
	}
	if len(extra) > 0 {
		problems = append(problems, "the split proposal's current list names these but no such edge is "+
			"declared: "+strings.Join(extra, ", ")+". The proposal's lower bound is resting on an edge "+
			"that moved; the four decision-196 writer lines above it are history and are not read here")
	}
	if len(problems) > 0 {
		t.Errorf("the split proposal and the declared domain edges disagree about who writes the audit chain:\n  %s",
			strings.Join(problems, "\n  "))
	}
}
