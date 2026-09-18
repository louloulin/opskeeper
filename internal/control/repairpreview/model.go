package repairpreview

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Decision string

const (
	DecisionPass   Decision = "PASS"
	DecisionFail   Decision = "FAIL"
	DecisionReject Decision = "REJECTED_BY_PREVIEW"
)

type Run struct {
	ID                  string
	TenantID            string
	IncidentID          string
	BranchPrefix        string
	SeedFingerprint     string
	WorkloadFingerprint string
	WorkloadRevision    string
	ControlledLoad      bool
	IsolationBoundary   string
	Status              string
	StartedAt           time.Time
	FinishedAt          time.Time
	ErrorSummary        string
	ArtifactRef         string
	Candidates          []Candidate
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Candidate struct {
	ID                string
	RunID             string
	TenantID          string
	IncidentID        string
	CandidateID       string
	Name              string
	Kind              string
	Action            string
	ChangeSummary     string
	Branch            string
	ResultChecksum    string
	Consistent        bool
	AverageLatencyMS  float64
	MedianLatencyMS   float64
	P95LatencyMS      float64
	SampleCount       int
	TPS               float64
	ErrorCount        int
	WriteImpact       string
	StorageDeltaBytes int64
	BusinessProbePass bool
	Decision          Decision
	RejectionReason   string
}

func (run Run) Validate() error {
	if run.TenantID == "" {
		return errors.New("repair preview: tenant id is required")
	}
	if run.IncidentID == "" {
		return errors.New("repair preview: incident id is required")
	}
	if run.ID == "" {
		return errors.New("repair preview: id is required")
	}
	if run.BranchPrefix == "" {
		return errors.New("repair preview: branch prefix is required")
	}
	if run.SeedFingerprint == "" {
		return errors.New("repair preview: seed fingerprint is required")
	}
	if run.WorkloadFingerprint == "" || run.WorkloadRevision == "" {
		return errors.New("repair preview: workload fingerprint and revision are required")
	}
	if !run.ControlledLoad {
		return errors.New("repair preview: controlled load is required")
	}
	if run.IsolationBoundary == "" {
		return errors.New("repair preview: isolation boundary is required")
	}
	if run.Status == "" {
		return errors.New("repair preview: status is required")
	}
	if run.StartedAt.IsZero() {
		return errors.New("repair preview: started_at is required")
	}
	if !run.FinishedAt.IsZero() && run.FinishedAt.Before(run.StartedAt) {
		return errors.New("repair preview: finished_at precedes started_at")
	}
	for _, candidate := range run.Candidates {
		if candidate.TenantID != run.TenantID || candidate.IncidentID != run.IncidentID || candidate.RunID != run.ID {
			return errors.New("repair preview: candidate bindings must match run")
		}
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("candidate %s: %w", candidate.CandidateID, err)
		}
	}
	return nil
}

func (candidate Candidate) Validate() error {
	if candidate.ID == "" {
		return errors.New("id is required")
	}
	if candidate.RunID == "" || candidate.TenantID == "" || candidate.IncidentID == "" || candidate.CandidateID == "" {
		return errors.New("run, tenant, incident, and candidate ids are required")
	}
	if candidate.Name == "" || candidate.Kind == "" || candidate.Action == "" || candidate.ChangeSummary == "" || candidate.Branch == "" {
		return errors.New("name, kind, action, change summary, and branch are required")
	}
	if candidate.AverageLatencyMS < 0 || candidate.MedianLatencyMS < 0 || candidate.P95LatencyMS < 0 {
		return errors.New("latency metrics cannot be negative")
	}
	if candidate.SampleCount <= 0 {
		return errors.New("sample count must be positive")
	}
	if candidate.TPS < 0 || candidate.ErrorCount < 0 || candidate.StorageDeltaBytes < 0 {
		return errors.New("throughput, error, and storage metrics cannot be negative")
	}
	if candidate.ResultChecksum == "" {
		return errors.New("result checksum is required")
	}
	if candidate.WriteImpact == "" {
		return errors.New("write impact is required")
	}
	return nil
}

func Evaluate(candidate Candidate) (Decision, string) {
	if err := candidate.Validate(); err != nil {
		return DecisionFail, err.Error()
	}
	if !candidate.BusinessProbePass {
		return DecisionReject, "business probe failed"
	}
	if !candidate.Consistent {
		return DecisionReject, "checksum divergence from baseline"
	}
	if !allowedWriteImpact(candidate.WriteImpact) {
		return DecisionReject, "write impact exceeds preview boundary: " + candidate.WriteImpact
	}
	return DecisionPass, ""
}

func allowedWriteImpact(writeImpact string) bool {
	switch writeImpact {
	case "none", "preview_only", "bounded":
		return true
	default:
		return false
	}
}

var sensitiveErrorPattern = regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key)\s*=\s*[^\s,;]+`)
var connectionSecretPattern = regexp.MustCompile(`(?i)((?:postgres(?:ql)?|mysql|redis|https?)://)[^:/\s]+:[^@\s]+@`)

func SanitizeErrorSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if len(summary) > 2000 {
		summary = summary[:2000]
	}
	summary = connectionSecretPattern.ReplaceAllString(summary, "$1[redacted]@")
	return sensitiveErrorPattern.ReplaceAllString(summary, "$1=[redacted]")
}
