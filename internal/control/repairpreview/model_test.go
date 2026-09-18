package repairpreview

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunValidate_BindsTenantAndIncident(t *testing.T) {
	run := validRun()
	require.NoError(t, run.Validate())

	run.TenantID = ""
	require.ErrorContains(t, run.Validate(), "tenant id is required")

	run = validRun()
	run.IncidentID = ""
	require.ErrorContains(t, run.Validate(), "incident id is required")
}

func TestEvaluateCandidate_PassRequiresCompleteMetrics(t *testing.T) {
	candidate := validCandidate("candidate-a", "resize_pool")
	decision, reason := Evaluate(candidate)
	require.Equal(t, DecisionPass, decision)
	require.Empty(t, reason)

	candidate.ResultChecksum = ""
	decision, reason = Evaluate(candidate)
	require.Equal(t, DecisionFail, decision)
	require.Contains(t, reason, "result checksum")

	candidate = validCandidate("candidate-a", "resize_pool")
	candidate.SampleCount = 0
	decision, reason = Evaluate(candidate)
	require.Equal(t, DecisionFail, decision)
	require.Contains(t, reason, "sample count")
}

func TestEvaluateCandidate_RejectsChecksumDivergence(t *testing.T) {
	candidate := validCandidate("candidate-b", "reset_pool")
	candidate.Consistent = false

	decision, reason := Evaluate(candidate)

	require.Equal(t, DecisionReject, decision)
	require.Contains(t, reason, "checksum divergence")
}

func TestEvaluateCandidate_RejectsWriteLossOrFailedBusinessProbe(t *testing.T) {
	candidate := validCandidate("candidate-b", "reset_pool")
	candidate.BusinessProbePass = false
	decision, reason := Evaluate(candidate)
	require.Equal(t, DecisionReject, decision)
	require.Contains(t, reason, "business probe failed")

	candidate = validCandidate("candidate-b", "reset_pool")
	candidate.WriteImpact = "production_write_loss"
	decision, reason = Evaluate(candidate)
	require.Equal(t, DecisionReject, decision)
	require.Contains(t, reason, "write impact")
}

func TestEvaluateCandidate_RejectsInvalidMetrics(t *testing.T) {
	candidate := validCandidate("candidate-a", "resize_pool")
	candidate.AverageLatencyMS = -1

	decision, reason := Evaluate(candidate)

	require.Equal(t, DecisionFail, decision)
	require.Contains(t, reason, "latency metrics")
}

func TestEvaluateCandidate_DoesNotRecordHumanApproval(t *testing.T) {
	candidate := validCandidate("candidate-a", "resize_pool")

	decision, reason := Evaluate(candidate)

	require.Equal(t, DecisionPass, decision)
	require.Empty(t, reason)
	require.NotContains(t, strings.ToLower(string(decision)), "approved")
}

func TestSanitizeErrorSummary(t *testing.T) {
	require.Equal(t, "pq: connection refused password=[redacted]", SanitizeErrorSummary("pq: connection refused password=secret"))
	require.Equal(
		t,
		"postgresql://[redacted]@preview-pg:5432/opskeeper",
		SanitizeErrorSummary("postgresql://user:secret@preview-pg:5432/opskeeper"),
	)
}

func validRun() Run {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	return Run{
		ID: "017f2b01-3001-4000-8000-000000000001", TenantID: "opskeeper-demo", IncidentID: "INC-REPAIR-001",
		BranchPrefix: "preview/inc-repair-001", SeedFingerprint: "sha256:seed-v1",
		WorkloadFingerprint: "sha256:workload-v1", WorkloadRevision: "workload-v1", ControlledLoad: true,
		IsolationBoundary: "preview-pg", Status: "finished", StartedAt: now.Add(-time.Minute), FinishedAt: now,
		Candidates: []Candidate{validCandidate("candidate-a", "resize_pool")},
	}
}

func validCandidate(candidateID, action string) Candidate {
	candidateRowID := "017f2b01-3101-4000-8000-000000000001"
	if candidateID == "candidate-b" {
		candidateRowID = "017f2b01-3102-4000-8000-000000000002"
	}
	return Candidate{
		ID: candidateRowID, RunID: "017f2b01-3001-4000-8000-000000000001",
		TenantID: "opskeeper-demo", IncidentID: "INC-REPAIR-001", CandidateID: candidateID,
		Name: "candidate " + candidateID, Kind: "postgresql", Action: action, ChangeSummary: "candidate change",
		Branch: "preview/inc-repair-001/" + candidateID, ResultChecksum: "sha256:seed-v1",
		Consistent: true, AverageLatencyMS: 12, MedianLatencyMS: 11, P95LatencyMS: 20,
		SampleCount: 100, TPS: 500, ErrorCount: 0, WriteImpact: "preview_only",
		StorageDeltaBytes: 1024, BusinessProbePass: true,
	}
}
