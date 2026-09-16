// Package loop — timeline_aggregate_test.go
//
// Day 11 tests for the trace-aggregation helpers. The
// SPA consumed the timeline endpoint with no aggregation before
// Day 11, so the test cases here focus on the new contract:
//
//   1. Empty event slice yields 7 forward phases (all "pending") and
//      a zero-value rubric.
//   2. A fully-walked PG-connection-pool incident produces 7 phases
//      in stable order with the rubric metrics matching the
//      loop-harness-rubric spec.
//   3. The chain footer reports coverage, current/final phase,
//      recovery signal, and closure flags.
//   4. A terminal-state event (failed) appears as a trailing row so
//      the renderer can show "loop closed by failure" instead of
//      hiding the failure inside the last forward phase.
package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	loopbiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/loop"
	loopmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/loop"
)

func TestBuildTimelinePhases_Empty(t *testing.T) {
	t.Parallel()
	phases := BuildTimelinePhases(nil)
	if len(phases) != 7 {
		t.Fatalf("len(phases) = %d, want 7", len(phases))
	}
	for i, p := range phases {
		if p.Phase != forwardPhaseOrder[i] {
			t.Errorf("phase[%d] = %q, want %q", i, p.Phase, forwardPhaseOrder[i])
		}
		if p.Status != "pending" {
			t.Errorf("phase[%d] status = %q, want pending", i, p.Status)
		}
	}
}

func TestBuildTimelinePhases_FullWalk(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	events := fullPGPoolWalk(now)

	phases := BuildTimelinePhases(events)
	if len(phases) != 7 {
		t.Fatalf("len(phases) = %d, want 7", len(phases))
	}

	// Forward phase order is preserved.
	for i, p := range phases {
		if p.Phase != forwardPhaseOrder[i] {
			t.Errorf("phase[%d] = %q, want %q", i, p.Phase, forwardPhaseOrder[i])
		}
	}

	// Every phase has its declared role and skill version.
	wantRoles := []string{
		"opskeeper-alerter",
		"opskeeper-investigator",
		"opskeeper-investigator",
		"opskeeper-critic",
		"opskeeper-reviewer",
		"opskeeper-repairer",
		"opskeeper-postmortem",
	}
	for i, p := range phases {
		if p.WorkerRole != wantRoles[i] {
			t.Errorf("phase[%d] role = %q, want %q", i, p.WorkerRole, wantRoles[i])
		}
		if p.WorkerSkillVer == "" {
			t.Errorf("phase[%d] skill version empty", i)
		}
	}

	// Detected / investigated / recovered / postmortem must end in
	// success and surface a contract summary.
	statusByName := map[string]string{}
	summaryByName := map[string]string{}
	for _, p := range phases {
		statusByName[p.Phase] = p.Status
		summaryByName[p.Phase] = p.ContractSummary
	}
	mustSuccess := []string{
		string(loopbiz.PhaseDetected),
		string(loopbiz.PhaseInvestigated),
		string(loopbiz.PhaseApproved),
		string(loopbiz.PhaseRecovered),
		string(loopbiz.PhasePostmortem),
	}
	for _, name := range mustSuccess {
		if statusByName[name] != "success" {
			t.Errorf("phase %s status = %q, want success", name, statusByName[name])
		}
		if summaryByName[name] == "" {
			t.Errorf("phase %s summary empty", name)
		}
	}

	// The approved phase must surface a bound-target audit row
	// (this is what Element shows for "approval bound to target").
	approved := phases[4]
	if len(approved.Audit) == 0 {
		t.Fatalf("approved phase has no audit rows")
	}
	foundApproval := false
	for _, row := range approved.Audit {
		if row.Kind == "approval" {
			foundApproval = true
			if row.BoundTarget == "" {
				t.Errorf("approval audit row has empty bound_target")
			}
		}
	}
	if !foundApproval {
		t.Errorf("approved phase has no approval audit row")
	}

	// Recovered phase must surface an execution audit row.
	recovered := phases[5]
	foundExecution := false
	for _, row := range recovered.Audit {
		if row.Kind == "execution" {
			foundExecution = true
		}
	}
	if !foundExecution {
		t.Errorf("recovered phase has no execution audit row")
	}

	// Postmortem phase must surface a close audit row.
	postmortem := phases[6]
	foundClose := false
	for _, row := range postmortem.Audit {
		if row.Kind == "close" {
			foundClose = true
		}
	}
	if !foundClose {
		t.Errorf("postmortem phase has no close audit row")
	}
}

func TestBuildRubric_FullWalk(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	events := fullPGPoolWalk(now)

	rubric := BuildRubric(events)
	if rubric.EventCount != len(events) {
		t.Errorf("rubric.EventCount = %d, want %d", rubric.EventCount, len(events))
	}
	if rubric.RCAAccuracy < 0.99 {
		t.Errorf("rubric.RCAAccuracy = %f, want ~1.0", rubric.RCAAccuracy)
	}
	if rubric.TimeToRemediate == "—" {
		t.Errorf("rubric.TimeToRemediate = —, want a duration")
	}
	if rubric.ApprovalRate < 0.99 {
		t.Errorf("rubric.ApprovalRate = %f, want ~1.0", rubric.ApprovalRate)
	}
	if rubric.RecoveryPassRate < 0.99 {
		t.Errorf("rubric.RecoveryPassRate = %f, want ~1.0", rubric.RecoveryPassRate)
	}
	if !rubric.HasRecoverySignal {
		t.Errorf("rubric.HasRecoverySignal = false, want true")
	}
	if !rubric.HasClosure {
		t.Errorf("rubric.HasClosure = false, want true")
	}
}

func TestBuildChainMeta_FullWalk(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	events := fullPGPoolWalk(now)

	chain := BuildChainMeta(events, "INC-PG-POOL-001")
	if chain.PhasesExpected != 7 {
		t.Errorf("chain.PhasesExpected = %d, want 7", chain.PhasesExpected)
	}
	if chain.PhasesObserved < 5 {
		t.Errorf("chain.PhasesObserved = %d, want >= 5", chain.PhasesObserved)
	}
	if chain.CoveragePct < 0.7 {
		t.Errorf("chain.CoveragePct = %f, want >= 0.7", chain.CoveragePct)
	}
	if chain.FinalPhase != string(loopbiz.PhasePostmortem) {
		t.Errorf("chain.FinalPhase = %q, want %q", chain.FinalPhase, loopbiz.PhasePostmortem)
	}
	if !chain.RecoverySignal {
		t.Errorf("chain.RecoverySignal = false, want true")
	}
	if !chain.Closed {
		t.Errorf("chain.Closed = false, want true")
	}
	if chain.IncidentIDAlias != "INC-PG-POOL-001" {
		t.Errorf("chain.IncidentIDAlias = %q, want INC-PG-POOL-001", chain.IncidentIDAlias)
	}
}

func TestBuildTimelinePhases_TerminalFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	events := []loopmodel.Event{
		{
			ID:           1,
			IncidentID:   "INC-X",
			Phase:        "detected",
			EventType:    loopmodel.EventTypePhaseEntered,
			CreatedAt:    now,
			IdempotencyKey: "detected:entered:1",
		},
		{
			ID:           2,
			IncidentID:   "INC-X",
			Phase:        "detected",
			EventType:    loopmodel.EventPhaseContractWritten,
			CreatedAt:    now.Add(10 * time.Second),
			Payload:      `{"summary":"alert dedup confirmed"}`,
			IdempotencyKey: "detected:contract:1",
		},
		{
			ID:           3,
			IncidentID:   "INC-X",
			Phase:        "failed",
			EventType:    loopmodel.EventPhaseFailed,
			CreatedAt:    now.Add(2 * time.Minute),
			Payload:      `{"error":"upstream MCP gateway down"}`,
			IdempotencyKey: "failed:phase_failed:1",
		},
	}

	phases := BuildTimelinePhases(events)
	last := phases[len(phases)-1]
	if last.Phase != "failed" {
		t.Errorf("last phase = %q, want failed", last.Phase)
	}
	if last.Status != "failed" {
		t.Errorf("last phase status = %q, want failed", last.Status)
	}
	if last.ContractSummary == "" {
		t.Errorf("last phase summary empty, want terminal-state message")
	}
}

// fullPGPoolWalk builds a synthetic but realistic event log for the
// PG connection-pool-exhaustion main scenario. Each event encodes
// the audit_kind + bound_target / execution identity that the
// Day 11 helpers need to surface in the timeline view.
func fullPGPoolWalk(start time.Time) []loopmodel.Event {
	return []loopmodel.Event{
		// detected (alerter)
		{
			ID: 1, IncidentID: "INC-PG-POOL-001", Phase: "detected",
			EventType: loopmodel.EventTypePhaseEntered, CreatedAt: start,
			IdempotencyKey: "detected:entered:1",
			TraceID:        "trace-inc-pg-pool-001-alerter",
		},
		{
			ID: 2, IncidentID: "INC-PG-POOL-001", Phase: "detected",
			EventType: loopmodel.EventPhaseContractWritten, CreatedAt: start.Add(8 * time.Second),
			Payload:        `{"summary":"alert dedup: 71 waiters, pg connections 116/120","audit_kind":"dispatch","actor":"opskeeper-alerter","actor_role":"opskeeper-alerter","bound_target":"incident:INC-PG-POOL-001","evidence_ref":"evidence/incidents/INC-PG-POOL-001/alert.json"}`,
			IdempotencyKey: "detected:contract:1",
			TraceID:        "trace-inc-pg-pool-001-alerter",
		},
		// correlated (investigator)
		{
			ID: 3, IncidentID: "INC-PG-POOL-001", Phase: "correlated",
			EventType: loopmodel.EventTypePhaseEntered, CreatedAt: start.Add(15 * time.Second),
			IdempotencyKey: "correlated:entered:1",
			TraceID:        "trace-inc-pg-pool-001-investigator",
		},
		{
			ID: 4, IncidentID: "INC-PG-POOL-001", Phase: "correlated",
			EventType: loopmodel.EventPhaseContractWritten, CreatedAt: start.Add(45 * time.Second),
			Payload:        `{"tool":"postgres.analyze_status","args":{"incident_id":"INC-PG-POOL-001"},"result":{"active":116,"waiters":71,"max_connections":120},"audit_kind":"dispatch","actor":"opskeeper-investigator","actor_role":"opskeeper-investigator","evidence_ref":"evidence/incidents/INC-PG-POOL-001/pg-stat-activity.json","knowledge_ref":"knowledge/pg-connection-pool-tuning"}`,
			IdempotencyKey: "correlated:contract:1",
			TraceID:        "trace-inc-pg-pool-001-investigator",
		},
		// investigated
		{
			ID: 5, IncidentID: "INC-PG-POOL-001", Phase: "investigated",
			EventType: loopmodel.EventTypePhaseEntered, CreatedAt: start.Add(time.Minute),
			IdempotencyKey: "investigated:entered:1",
			TraceID:        "trace-inc-pg-pool-001-investigator",
		},
		{
			ID: 6, IncidentID: "INC-PG-POOL-001", Phase: "investigated",
			EventType: loopmodel.EventPhaseContractWritten, CreatedAt: start.Add(2 * time.Minute),
			Payload:        `{"schema_version":"v1","root_cause_object":{"kind":"pool_capacity_exhausted","summary":"Application pool 90/90 saturated; pg 116/120; 2 idle-in-transaction workers holding budget"},"confidence":0.92,"remediation_options":[{"action":"resize_pool","target":"pg:pool-fixture","params":{"from":90,"to":120}}],"audit_kind":"dispatch","actor":"opskeeper-investigator","actor_role":"opskeeper-investigator","evidence_ref":"evidence/incidents/INC-PG-POOL-001/diagnosis.json"}`,
			IdempotencyKey: "investigated:contract:1",
			TraceID:        "trace-inc-pg-pool-001-investigator",
		},
		// critiqued
		{
			ID: 7, IncidentID: "INC-PG-POOL-001", Phase: "critiqued",
			EventType: loopmodel.EventTypePhaseEntered, CreatedAt: start.Add(2*time.Minute + 10*time.Second),
			IdempotencyKey: "critiqued:entered:1",
		},
		{
			ID: 8, IncidentID: "INC-PG-POOL-001", Phase: "critiqued",
			EventType: loopmodel.EventPhaseContractWritten, CreatedAt: start.Add(3 * time.Minute),
			Payload:        `{"replay":"success","depth":"success","consistency":"success","score":0.95,"audit_kind":"verification","actor":"opskeeper-critic","actor_role":"opskeeper-critic","note":"evidence chain covers capacity, waiters, probe failure, db counter-evidence"}`,
			IdempotencyKey: "critiqued:contract:1",
		},
		// approved (HITL-bound)
		{
			ID: 9, IncidentID: "INC-PG-POOL-001", Phase: "approved",
			EventType: loopmodel.EventTypePhaseEntered, CreatedAt: start.Add(3*time.Minute + 5*time.Second),
			IdempotencyKey: "approved:entered:1",
		},
		{
			ID: 10, IncidentID: "INC-PG-POOL-001", Phase: "approved",
			EventType: loopmodel.EventPhaseContractWritten, CreatedAt: start.Add(4 * time.Minute),
			Payload:        `{"decision":"approve","approver":"dba-oncall","signed_by":"opskeeper-observer","audit_kind":"approval","actor":"opskeeper-reviewer","actor_role":"opskeeper-reviewer","bound_target":"pg:pool-fixture","bound_params":"from=90 to=120","bound_scope":"pool_capacity_exhausted/INC-PG-POOL-001","fallback":"none","fallback_cause":"","evidence_ref":"evidence/incidents/INC-PG-POOL-001/approval.json"}`,
			IdempotencyKey: "approved:contract:1",
		},
		// recovered
		{
			ID: 11, IncidentID: "INC-PG-POOL-001", Phase: "recovered",
			EventType: loopmodel.EventTypePhaseEntered, CreatedAt: start.Add(4*time.Minute + 5*time.Second),
			IdempotencyKey: "recovered:entered:1",
		},
		{
			ID: 12, IncidentID: "INC-PG-POOL-001", Phase: "recovered",
			EventType: loopmodel.EventPhaseContractWritten, CreatedAt: start.Add(5 * time.Minute),
			Payload:        `{"schema_version":"v1","passed":true,"deltas":{"app_pool_waiters":-0.96,"app_pool_wait_latency_p95":-0.94,"pg_connections_active":-0.05},"recovery_signal":true,"sample_size":10,"tolerance":0.15,"audit_kind":"execution","actor":"opskeeper-repairer","actor_role":"opskeeper-repairer","action":"resize_pool","bound_target":"pg:pool-fixture","bound_params":"from=90 to=120","evidence_ref":"evidence/incidents/INC-PG-POOL-001/recovery-check.json"}`,
			IdempotencyKey: "recovered:contract:1",
		},
		// postmortem
		{
			ID: 13, IncidentID: "INC-PG-POOL-001", Phase: "postmortem",
			EventType: loopmodel.EventTypePhaseEntered, CreatedAt: start.Add(5*time.Minute + 5*time.Second),
			IdempotencyKey: "postmortem:entered:1",
		},
		{
			ID: 14, IncidentID: "INC-PG-POOL-001", Phase: "postmortem",
			EventType: loopmodel.EventPhaseContractWritten, CreatedAt: start.Add(6 * time.Minute),
			Payload:        `{"summary":"application pool 90/90 saturated; pg 116/120; resize + recycle_idle restored service in ~5m","recommendation":"set pool auto-resize 1.25x","audit_kind":"close","actor":"opskeeper-postmortem","actor_role":"opskeeper-postmortem","evidence_ref":"evidence/incidents/INC-PG-POOL-001/postmortem.md"}`,
			IdempotencyKey: "postmortem:contract:1",
		},
	}
}

// TestTimelineResponse_PhasesFieldIsPopulated covers the wire-format
// requirement: the timeline handler must return a non-empty phases
// slice + non-nil rubric for any non-empty event set. This is the
// regression test for the aggregation gap (the old handler returned
// only the raw events).
func TestTimelineResponse_PhasesFieldIsPopulated(t *testing.T) {
	t.Parallel()
	h, _, events, _ := newTestHandler(t)
	events.events = fullPGPoolWalk(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))

	r := newRequest(http.MethodGet, "/v1/loops/INC-PG-POOL-001/timeline", nil)
	r = r.WithContext(adminCtx(r))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("incident_id", "INC-PG-POOL-001")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.timeline(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var resp TimelineResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if len(resp.Phases) != 7 {
		t.Errorf("len(resp.Phases) = %d, want 7", len(resp.Phases))
	}
	if resp.Rubric == nil {
		t.Errorf("resp.Rubric = nil, want populated")
	}
	if resp.Chain.PhasesExpected != 7 {
		t.Errorf("resp.Chain.PhasesExpected = %d, want 7", resp.Chain.PhasesExpected)
	}
}
