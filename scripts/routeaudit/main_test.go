package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A gate that has never been shown to fail is a gate nobody can trust. These
// tests exist to make routeaudit red on purpose, in a scratch tree, four
// different ways.

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, "core", "manager", "server", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAMutatingRouteWithNoVerdictFails(t *testing.T) {
	root := tree(t, map[string]string{
		"widgets/http.go": `package widgets
func (h *Handler) Register(r chi.Router) {
	r.Delete("/v1/widgets/{id}", h.drop)
}
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
`,
	})
	res := Run(root)
	if len(res.Missing) != 1 {
		t.Fatalf("missing = %v, want exactly the one unrecorded route", res.Missing)
	}
	if !strings.Contains(res.Missing[0], "/v1/widgets/{id}") {
		t.Fatalf("missing = %q, want it to name the route", res.Missing[0])
	}
}

// A verdict that claims audited is only worth something if the gate checks
// it. Without this, someone can write {File, Route} with no reason and the
// table becomes a way to look busy.
func TestAVerdictClaimingAuditedIsCheckedAgainstTheHandler(t *testing.T) {
	root := tree(t, map[string]string{
		"widgets/http.go": `package widgets
func (h *Handler) Register(r chi.Router) {
	r.Delete("/v1/widgets/{id}", h.drop)
}
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
`,
	})
	// Same tree, but the table now claims the route is audited.
	saved := Verdicts
	Verdicts = append(append([]Verdict{}, saved...), Verdict{File: "widgets/http.go", Route: "/v1/widgets/{id}"})
	defer func() { Verdicts = saved }()

	res := Run(root)
	if len(res.Missing) != 1 {
		t.Fatalf("missing = %v, want the false audit claim", res.Missing)
	}
	if !strings.Contains(res.Missing[0], "never calls SetAuditEvent") {
		t.Fatalf("missing = %q, want the claim to be named as false", res.Missing[0])
	}
}

// The delegation case. Decisions 309 and 310 both put SetAuditEvent inside a
// local helper rather than in the handler body; a gate that cannot see
// through that will report the deliberate code as the gap.
func TestAHandlerDelegatingToAnAuditHelperCounts(t *testing.T) {
	root := tree(t, map[string]string{
		"widgets/http.go": `package widgets
func (h *Handler) Register(r chi.Router) {
	r.Delete("/v1/widgets/{id}", h.drop)
}
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) { note(w, r) }
func note(w http.ResponseWriter, r *http.Request) { auditport.SetAuditEvent(r, auditport.Event{}) }
`,
	})
	saved := Verdicts
	Verdicts = append(append([]Verdict{}, saved...), Verdict{File: "widgets/http.go", Route: "/v1/widgets/{id}"})
	defer func() { Verdicts = saved }()

	res := Run(root)
	if len(res.Missing) != 0 {
		t.Fatalf("delegation through a local helper was not recognised: %v", res.Missing)
	}
}

// The failure mode this gate was nearly born with: assigning a body to a
// callee's name, so the helper's text lands in the caller's slot and the
// real helper looks empty.
func TestCalleeAndCallerBodiesAreNotConfused(t *testing.T) {
	src := `package widgets
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) { note(w, r) }
func note(w http.ResponseWriter, r *http.Request) { auditport.SetAuditEvent(r, auditport.Event{}) }
`
	bodies := funcBodies(src)
	note, ok := bodies["note"]
	if !ok {
		t.Fatal("note was not indexed")
	}
	if !strings.Contains(note, "SetAuditEvent") {
		t.Fatalf("note holds the caller's text: %q", note)
	}
	drop := bodies["drop"]
	if strings.Contains(drop, "SetAuditEvent") {
		t.Fatalf("drop absorbed the callee's text: %q", drop)
	}
}

func TestStaleAndOrphanAreReported(t *testing.T) {
	root := tree(t, map[string]string{
		"widgets/http.go": `package widgets
func (h *Handler) Register(r chi.Router) {
	r.Delete("/v1/widgets/{id}", h.drop)
}
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) {
	auditport.SetAuditEvent(r, auditport.Event{})
}
`,
	})
	saved := Verdicts
	Verdicts = []Verdict{
		// Says backlog, but the handler now audits.
		{File: "widgets/http.go", Route: "/v1/widgets/{id}", Backlog: "left over from an earlier round"},
		// For a route that no longer exists.
		{File: "widgets/http.go", Route: "/v1/widgets/{name}", Backlog: "route was renamed"},
	}
	defer func() { Verdicts = saved }()

	res := Run(root)
	if len(res.Stale) != 1 || !strings.Contains(res.Stale[0], "/v1/widgets/{id}") {
		t.Fatalf("stale = %v", res.Stale)
	}
	if len(res.Orphan) != 1 || !strings.Contains(res.Orphan[0], "{name}") {
		t.Fatalf("orphan = %v", res.Orphan)
	}
}

// A deleted file must not quietly take its verdicts with it. An earlier
// version of this check only noticed routes that vanished from a file still
// in the tree, so removing a whole handler package silenced every verdict it
// had — and a reader would conclude the routes had been closed rather than
// forgotten.
func TestADeletedFileReportsGoneRatherThanNothing(t *testing.T) {
	root := tree(t, map[string]string{
		"widgets/http.go": `package widgets
func (h *Handler) Register(r chi.Router) {
	r.Post("/v1/widgets", h.list)
	r.Delete("/v1/sprockets/{id}", h.drop)
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) { auditport.SetAuditEvent(r, auditport.Event{}) }
`,
	})
	saved := Verdicts
	Verdicts = []Verdict{
		{File: "widgets/http.go", Route: "/v1/widgets"},
		{File: "widgets/http.go", Route: "/v1/sprockets/{id}"},
		{File: "sprockets/http.go", Route: "/v1/sprockets", Backlog: "package deleted in this change"},
	}
	defer func() { Verdicts = saved }()

	res := Run(root)
	if len(res.Orphan) != 0 {
		t.Fatalf("orphan = %v, want the still-registered route left alone", res.Orphan)
	}
	if len(res.Gone) != 1 || !strings.Contains(res.Gone[0], "sprockets/http.go") {
		t.Fatalf("gone = %v, want the deleted package reported", res.Gone)
	}
}

// The table in main.go is the thing being maintained. A route that has been
// deleted should not sit in it forever, and a route that exists should not
// be missing from it — so assert the real tree agrees with the real table.
func TestTheTableAgreesWithTheRepository(t *testing.T) {
	res := Run("../..")
	for _, m := range res.Missing {
		t.Errorf("MISSING: %s", m)
	}
	for _, o := range res.Orphan {
		t.Errorf("orphan: %s", o)
	}
	for _, g := range res.Gone {
		t.Errorf("gone: %s", g)
	}
	for _, s := range res.Stale {
		t.Errorf("stale: %s", s)
	}
	if t.Failed() {
		t.Fatalf("%d mutating routes are registered, %d verdicts are recorded, %d of them backlog",
			len(Verdicts), len(Verdicts), countBacklog())
	}
}
