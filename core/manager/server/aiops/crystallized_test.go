// crystallized_test.go — tests for the crystallisation review surface.
//
// The point of these tests is the wiring the mechanism was missing: a
// promoted pattern and the draft it would emit have to be reachable by an
// operator. They drive a real crystallize.Ledger, not a stub, because the
// bug class this surface can have is exactly "the listing and the document
// disagree about what would be installed" — and a stub cannot disagree with
// itself.
package aiops

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/aiops/crystallize"
	"github.com/vincent-wuhan/opskeeper/core/manager/pkg/tenantctx"
)

var crystallizedBase = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func crystallizedTrial(minutes int) crystallize.Trial {
	return crystallize.Trial{
		At: crystallizedBase.Add(time.Duration(minutes) * time.Minute),
		Pattern: crystallize.Pattern{
			Fault: crystallize.Fault{Kind: "host.disk_full", Family: "host"},
			Action: crystallize.Action{
				Tool:        "host.restart_service",
				Class:       domain.ClassWrite,
				Argv:        []string{"systemctl", "restart", "orders-api"},
				Target:      "host:i-0abc123",
				Trigger:     domain.AutonomyTrigger{Kind: domain.TriggerMetricAbove, Metric: "node_disk_used_ratio", Threshold: 0.92},
				BlastRadius: domain.RadiusPod,
				TTL:         15 * time.Minute,
			},
		},
		Outcome:  crystallize.OutcomeVerified,
		Evidence: "incident-" + string(rune('a'+minutes)),
	}
}

func promotedLedger(t *testing.T) *crystallize.Ledger {
	t.Helper()
	l := crystallize.NewLedger(crystallize.Policy{})
	for i := 1; i <= crystallize.DefaultMinCleanStreak; i++ {
		if _, err := l.Record(crystallizedTrial(i)); err != nil {
			t.Fatalf("record trial %d: %v", i, err)
		}
	}
	if got := len(l.Promoted()); got != 1 {
		t.Fatalf("promoted = %d, want 1", got)
	}
	return l
}

func adminTenant() tenantctx.Tenant {
	return tenantctx.Tenant{UserID: 42, Email: "admin@example.com", Role: "admin", IsSuperuser: true}
}

func TestCrystallized_NotWiredAnswers503(t *testing.T) {
	t.Parallel()
	r := buildRouter(NewHandler(&fakeService{}), adminTenant())
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/crystallized", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 body=%s", w.Code, w.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "not-wired" {
		t.Fatalf("code = %q, want not-wired", body.Code)
	}
}

func TestCrystallized_RequiresAdmin(t *testing.T) {
	t.Parallel()
	h := NewHandler(&fakeService{})
	h.SetPatterns(promotedLedger(t))
	r := buildRouter(h, tenantctx.Tenant{UserID: 7, Role: "user"})
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/crystallized", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestCrystallized_ListsPromotedPatternWithEvidence(t *testing.T) {
	t.Parallel()
	h := NewHandler(&fakeService{})
	h.SetPatterns(promotedLedger(t))
	r := buildRouter(h, adminTenant())
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/crystallized", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body CrystallizedListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Items) != 1 {
		t.Fatalf("total = %d, items = %d, want 1", body.Total, len(body.Items))
	}
	p := body.Items[0]
	if !strings.HasPrefix(p.Name, "opskeeper-crystallized-host-disk-full-") {
		t.Fatalf("name = %q, want the crystallized package prefix", p.Name)
	}
	if p.Tool != "host.restart_service" || p.Class != "write" {
		t.Fatalf("tool/class = %q/%q", p.Tool, p.Class)
	}
	if len(p.Argv) != 3 || p.Argv[0] != "systemctl" {
		t.Fatalf("argv = %v, want the exact vector that ran", p.Argv)
	}
	if p.Target != "host:i-0abc123" {
		t.Fatalf("target = %q", p.Target)
	}
	if p.Trigger.Metric != "node_disk_used_ratio" || p.Trigger.Threshold != 0.92 {
		t.Fatalf("trigger = %+v", p.Trigger)
	}
	if p.SafetyLevel != string(domain.SafetyL2) {
		t.Fatalf("safety = %q, want L2 for a write", p.SafetyLevel)
	}
	if p.Streak < crystallize.DefaultMinCleanStreak {
		t.Fatalf("streak = %d", p.Streak)
	}
	if len(p.Evidence) != crystallize.DefaultMinCleanStreak {
		t.Fatalf("evidence = %v, want the runs the promotion rests on", p.Evidence)
	}
	if body.Policy.MinCleanStreak != crystallize.DefaultMinCleanStreak {
		t.Fatalf("policy streak = %d", body.Policy.MinCleanStreak)
	}
}

func TestCrystallized_EmptyLedgerIsTotalZero(t *testing.T) {
	t.Parallel()
	h := NewHandler(&fakeService{})
	h.SetPatterns(crystallize.NewLedger(crystallize.Policy{}))
	r := buildRouter(h, adminTenant())
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/crystallized", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body CrystallizedListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.Items) != 0 {
		t.Fatalf("items = %v, want none", body.Items)
	}
}

func TestCrystallizedOne_RendersTheReviewableDocument(t *testing.T) {
	t.Parallel()
	ledger := promotedLedger(t)
	h := NewHandler(&fakeService{})
	h.SetPatterns(ledger)
	draft, err := ledger.DraftFor(ledger.Promoted()[0])
	if err != nil {
		t.Fatalf("DraftFor: %v", err)
	}
	r := buildRouter(h, adminTenant())
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/crystallized/"+draft.Name(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body CrystallizedDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pattern.Name != draft.Name() {
		t.Fatalf("name = %q, want %q", body.Pattern.Name, draft.Name())
	}
	if body.Pattern.SafetyLevel != string(domain.SafetyL2) {
		t.Fatalf("safety = %q, want L2", body.Pattern.SafetyLevel)
	}
	if !strings.Contains(body.YAML, "systemctl") {
		t.Fatalf("yaml does not show the argv:\n%s", body.YAML)
	}
	if !strings.Contains(body.YAML, "Draft, not a release") {
		t.Fatalf("yaml does not mark itself a draft:\n%s", body.YAML)
	}
}

func TestCrystallizedOne_UnknownNameIsNotFound(t *testing.T) {
	t.Parallel()
	h := NewHandler(&fakeService{})
	h.SetPatterns(promotedLedger(t))
	r := buildRouter(h, adminTenant())
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/crystallized/nope", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 body=%s", w.Code, w.Body.String())
	}
}

func TestCrystallizedPromote_WritesDraftForReview(t *testing.T) {
	t.Parallel()
	ledger := promotedLedger(t)
	h := NewHandler(&fakeService{})
	h.SetPatterns(ledger)
	root := t.TempDir()
	h.SetDraftRoot(root)
	draft, err := ledger.DraftFor(ledger.Promoted()[0])
	if err != nil {
		t.Fatalf("DraftFor: %v", err)
	}
	r := buildRouter(h, adminTenant())
	req := httptest.NewRequest(http.MethodPost, "/v1/loops/crystallized/"+draft.Name()+"/promote", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body CrystallizedPromoteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Name != draft.Name() {
		t.Fatalf("name = %q, want %q", body.Name, draft.Name())
	}
	doc := body.Dir + string(os.PathSeparator) + "pig-ops.yaml"
	if _, err := os.Stat(doc); err != nil {
		t.Fatalf("draft not written to %s: %v", doc, err)
	}
}

func TestCrystallizedPromote_RefusesToOverwriteAnEditedDraft(t *testing.T) {
	t.Parallel()
	ledger := promotedLedger(t)
	h := NewHandler(&fakeService{})
	h.SetPatterns(ledger)
	h.SetDraftRoot(t.TempDir())
	draft, err := ledger.DraftFor(ledger.Promoted()[0])
	if err != nil {
		t.Fatalf("DraftFor: %v", err)
	}
	r := buildRouter(h, adminTenant())
	path := "/v1/loops/crystallized/" + draft.Name() + "/promote"
	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := do(); w.Code != http.StatusOK {
		t.Fatalf("first promote = %d, body=%s", w.Code, w.Body.String())
	}
	if w := do(); w.Code != http.StatusConflict {
		t.Fatalf("second promote = %d, want 409: a re-promotion must not overwrite a review", w.Code)
	}
}

func TestCrystallizedPromote_NoRootAnswers503(t *testing.T) {
	t.Parallel()
	h := NewHandler(&fakeService{})
	h.SetPatterns(promotedLedger(t))
	r := buildRouter(h, adminTenant())
	req := httptest.NewRequest(http.MethodPost, "/v1/loops/crystallized/x/promote", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
