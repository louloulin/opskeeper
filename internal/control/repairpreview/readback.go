package repairpreview

type CompactSummary struct {
	IncidentID          string    `json:"incident_id"`
	RunID               string    `json:"run_id"`
	SeedFingerprint     string    `json:"seed_fingerprint"`
	WorkloadFingerprint string    `json:"workload_fingerprint"`
	ControlledLoad      bool      `json:"controlled_load"`
	IsolationBoundary   string    `json:"isolation_boundary"`
	Baseline            Candidate `json:"baseline"`
	Passing             Candidate `json:"passing"`
	Rejected            Candidate `json:"rejected"`
}

func BuildCompactSummary(runs []Run) (CompactSummary, error) {
	for _, run := range runs {
		var passing Candidate
		var rejected Candidate
		for _, candidate := range run.Candidates {
			if candidate.Decision == DecisionPass && passing.CandidateID == "" {
				passing = candidate
				continue
			}
			if candidate.Decision != DecisionPass && rejected.CandidateID == "" {
				rejected = candidate
			}
		}
		if passing.CandidateID == "" {
			continue
		}
		baseline := passing
		baseline.ID = ""
		baseline.CandidateID = "baseline"
		baseline.Name = "Baseline replay"
		baseline.Action = "baseline"
		baseline.ChangeSummary = "Controlled fixed-workload baseline"
		baseline.Branch = run.BranchPrefix + "/baseline"
		baseline.Decision = DecisionPass
		baseline.RejectionReason = ""
		return CompactSummary{
			IncidentID: run.IncidentID, RunID: run.ID, SeedFingerprint: run.SeedFingerprint,
			WorkloadFingerprint: run.WorkloadFingerprint, ControlledLoad: run.ControlledLoad,
			IsolationBoundary: run.IsolationBoundary, Baseline: baseline, Passing: passing,
			Rejected: rejected,
		}, nil
	}
	return CompactSummary{}, nil
}

func BoundArchiveRuns(runs []Run) []Run {
	if len(runs) > 3 {
		runs = runs[:3]
	}
	candidateBudget := 12
	for runIndex := range runs {
		if len(runs[runIndex].Candidates) > candidateBudget {
			runs[runIndex].Candidates = runs[runIndex].Candidates[:candidateBudget]
		}
		candidateBudget -= len(runs[runIndex].Candidates)
		if candidateBudget <= 0 {
			runs[runIndex].Candidates = runs[runIndex].Candidates[:len(runs[runIndex].Candidates)+candidateBudget]
			runs = runs[:runIndex+1]
			break
		}
	}
	return runs
}

func TotalCandidates(runs []Run) int {
	total := 0
	for _, run := range runs {
		total += len(run.Candidates)
	}
	return total
}
