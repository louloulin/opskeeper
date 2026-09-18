package repairpreview

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCompactSummary_SelectsPassingAndRejectedCandidates(t *testing.T) {
	run := validRun()
	run.IsolationBoundary = isolationBoundary
	passing := validCandidate("candidate-a", "resize_pool")
	passing.Decision = DecisionPass
	rejected := validCandidate("candidate-b", "reset_pool")
	rejected.BusinessProbePass = false
	rejected.Decision = DecisionReject
	rejected.RejectionReason = "business probe failed"
	run.Candidates = []Candidate{passing, rejected}

	summary, err := BuildCompactSummary([]Run{run})

	require.NoError(t, err)
	require.Equal(t, run.IncidentID, summary.IncidentID)
	require.Equal(t, run.ID, summary.RunID)
	require.Equal(t, run.SeedFingerprint, summary.SeedFingerprint)
	require.Equal(t, run.WorkloadFingerprint, summary.WorkloadFingerprint)
	require.Equal(t, "baseline", summary.Baseline.CandidateID)
	require.Equal(t, "candidate-a", summary.Passing.CandidateID)
	require.Equal(t, "candidate-b", summary.Rejected.CandidateID)
	require.NotEqual(t, "", summary.Baseline.ResultChecksum)
	require.Equal(t, isolationBoundary, summary.IsolationBoundary)
}

func TestBuildCompactSummary_MissingPreviewIsEmpty(t *testing.T) {
	summary, err := BuildCompactSummary(nil)

	require.NoError(t, err)
	require.Equal(t, CompactSummary{}, summary)
}

func TestBoundArchiveRuns_LimitsThreeRunsAndTwelveCandidates(t *testing.T) {
	runs := make([]Run, 0, 4)
	for runIndex := 0; runIndex < 4; runIndex++ {
		run := validRun()
		run.ID = "017f2b01-500" + string(rune('0'+runIndex)) + "-4000-8000-000000000000"
		run.Candidates = nil
		for candidateIndex := 0; candidateIndex < 5; candidateIndex++ {
			candidate := validCandidate("candidate-"+string(rune('0'+candidateIndex)), "resize_pool")
			candidate.RunID = run.ID
			run.Candidates = append(run.Candidates, candidate)
		}
		runs = append(runs, run)
	}

	bounded := BoundArchiveRuns(runs)

	require.Len(t, bounded, 3)
	require.Equal(t, 12, TotalCandidates(bounded))
}
