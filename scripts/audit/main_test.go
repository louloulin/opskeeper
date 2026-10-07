package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The audit's whole value is that its classifications are true. So each test
// below breaks exactly one claim and asserts the audit notices.

func TestCountGapsReadsTheLiteralNotTheComment(t *testing.T) {
	const empty = `package p

// DiagnosisGaps once held four entries; all four are retired now.
var DiagnosisGaps = map[string]GapReason{
	// A retired entry, described at length, with numbers in the prose.
}
`
	if got := countGaps(empty); got != 0 {
		t.Errorf("countGaps = %d for an empty map whose comment mentions four entries", got)
	}
	one := empty[:strings.Index(empty, "\n}\n")] + "\n\t\"a.b\": {\n\t\tReason: \"r\",\n\t\tSearched: []string{\"core/edge\"},\n\t},\n}\n"
	if got := countGaps(one); got != 1 {
		t.Errorf("countGaps = %d for the elided-type form every real entry uses, want 1", got)
	}
	typed := empty[:strings.Index(empty, "\n}\n")] + "\n\t\"a.b\": GapReason{\n\t\tReason: \"r\",\n\t},\n}\n"
	if got := countGaps(typed); got != 1 {
		t.Errorf("countGaps = %d for the explicit-type form, want 1", got)
	}
	// A commented-out entry is not an entry, and one whose prose carries a
	// quote must not be mistaken for one either.
	commented := empty[:strings.Index(empty, "\n}\n")] + "\n\t// \"a.b\": { once retired\n}\n"
	if got := countGaps(commented); got != 0 {
		t.Errorf("countGaps = %d for a commented-out entry, want 0", got)
	}
	if got := countGaps("package p\n"); got != -1 {
		t.Errorf("countGaps = %d for a file with no map at all, want -1 so the item reports it", got)
	}
}

// An external item that leaked into the denominator would turn "one thing we
// cannot test" into "three of eight closed" — a smaller claim made by
// accident.
func TestOnlyRepoItemsAreScored(t *testing.T) {
	for _, it := range all() {
		if it.class != classRepo && it.done != nil {
			t.Errorf("%s is class %s but carries a predicate; only repo items are scored", it.id, it.class)
		}
		if it.class == classExternal && (it.evidence == "" || it.holder == "") {
			t.Errorf("%s says it cannot be tested here without saying what would close it or who holds that", it.id)
		}
	}
	closed, total, failures := score("../..")
	if failures != nil && len(failures) > 0 {
		t.Fatalf("this repository must be evaluable: %v", failures)
	}
	if total != len(repoItems) {
		t.Errorf("denominator %d, want %d repo items", total, len(repoItems))
	}
	// Pinned, and it moves when a claim is closed — which is the whole
	// point of the number. It was 2 when the audit was written (A1 and A2),
	// and 4 after 决策 457 closed the two rebalance-history items.
	const wantClosed = 4
	if closed != wantClosed {
		t.Errorf("closed = %d, want %d. A move in either direction is news: "+
			"up means a claim was closed, down means one of them stopped being true.", closed, wantClosed)
	}
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// fixture writes the smallest tree that closes every repo item, so a test
// only has to describe the one thing it takes away.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "core/floor/pluginmanifest/coverage_test.go", "package pluginmanifest\n\nconst want = 20\n")
	writeFile(t, root, "core/floor/pluginmanifest/coverage.go",
		"package pluginmanifest\n\nvar DiagnosisGaps = map[string]GapReason{\n}\n")
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		writeFile(t, root, filepath.Join("plugins", "pig-ops", name, "pig-ops.yaml"),
			"spec:\n  install:\n    min_edge_version: 0.8.0\n    min_pig_version: 0.4.0\n")
	}
	writeFile(t, root, "core/manager/middleware/adapter/mq/rebalance.go",
		"package mq\n\nfunc NewRebalanceHistoryStore() {}\n\nfunc (c *kafkaClient) StartRebalanceSampler() {}\n")
	writeFile(t, root, "core/manager/biz/marketplace/usecase.go",
		"package marketplace\n\nfunc (uc *Usecase) CatalogFromSources() {}\n")
	return root
}

func TestTheFixtureClosesEverythingAndEveryClaimCanBeBroken(t *testing.T) {
	root := fixture(t)
	closed, total, failures := score(root)
	if failures != nil && len(failures) > 0 {
		t.Fatalf("fixture is not evaluable: %v", failures)
	}
	if closed != total {
		t.Fatalf("fixture closes %d/%d", closed, total)
	}

	cases := []struct {
		id      string
		rel     string
		body    string
		comment string
	}{
		{"A1", "core/floor/pluginmanifest/coverage_test.go", "package pluginmanifest\n\nconst want = 19\n", "棘轮回退"},
		{"A1", "core/floor/pluginmanifest/coverage.go", "package pluginmanifest\n\nvar DiagnosisGaps = map[string]GapReason{\n\t\"x\": {Reason: \"r\"},\n}\n", "GAP 表非空"},
		{"A2", filepath.Join("plugins", "pig-ops", "c", "pig-ops.yaml"), "spec:\n  install:\n    min_edge_version: 0.8.0\n", "一个包丢了 pig 下限"},
		{"B1", "core/manager/middleware/adapter/mq/rebalance.go", "package mq\n\nfunc (c *kafkaClient) StartRebalanceSampler() {}\n", "持久化实现被删"},
		{"B2", "core/manager/middleware/adapter/mq/rebalance.go", "package mq\n\nfunc NewRebalanceHistoryStore() {}\n", "定时器实现被删"},
		{"B3", "core/manager/biz/marketplace/usecase.go", "package marketplace\n", "跨源索引实现被删"},
	}
	for _, tc := range cases {
		t.Run(tc.id+"/"+tc.comment, func(t *testing.T) {
			local := fixture(t)
			writeFile(t, local, tc.rel, tc.body)
			var (
				was bool
				why string
			)
			for _, it := range repoItems {
				if it.id != tc.id {
					continue
				}
				was, why, _ = it.done(local)
			}
			if was {
				t.Fatalf("%s still reports closed after %s", tc.id, tc.comment)
			}
			if why == "" {
				t.Errorf("%s went red without saying what is missing", tc.id)
			}
		})
	}
}
