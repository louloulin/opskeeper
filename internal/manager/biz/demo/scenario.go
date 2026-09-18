package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	repairpreview "github.com/vincent-wuhan/opskeeper/internal/control/repairpreview"
	alertmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/alert"
	demomodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/demo"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

const (
	ScenarioID                     = "pg-pool-exhaustion"
	ScenarioTarget                 = "pg:pool-fixture"
	maxFixtureBytes                = 1 << 20
	requestTimeout                 = 5 * time.Second
	businessTimeout                = 3 * time.Second
	finalDemoInitialPoolCapacity   = 4
	finalDemoRecoveredPoolCapacity = 8
)

var (
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{12,128}$`)
	hexPattern         = regexp.MustCompile(`^[0-9a-fA-F]{16,128}$`)
	sha256Pattern      = regexp.MustCompile(`^sha256:[0-9a-fA-F-]{9,121}$`)
)

type StartScenarioInput struct {
	IdempotencyKey    string `json:"idempotency_key"`
	ScenarioID        string `json:"scenario_id"`
	Target            string `json:"target"`
	TargetFingerprint string `json:"target_fingerprint"`
	AlertFingerprint  string `json:"alert_fingerprint"`
	DurationSeconds   int    `json:"duration_seconds"`
}

type ScenarioStatus struct {
	IncidentID        uint64                  `json:"incident_id"`
	ScenarioID        string                  `json:"scenario_id"`
	Status            string                  `json:"status"`
	PoolManifestID    string                  `json:"pool_manifest_id"`
	TargetFingerprint string                  `json:"target_fingerprint"`
	AlertFingerprint  string                  `json:"alert_fingerprint"`
	UpdatedAt         string                  `json:"updated_at"`
	PreviewDecision   *PreviewDecisionSummary `json:"preview_decision,omitempty"`
}

type PreviewDecisionSummary struct {
	ReplayProfileID string `json:"replay_profile_id"`
	BoundaryText    string `json:"boundary_text"`
	CandidateA      string `json:"candidate_a"`
	CandidateB      string `json:"candidate_b"`
	EligibleForHITL bool   `json:"eligible_for_hitl"`
}

type IncidentRepository interface {
	GetIncidentByDedupeKey(ctx context.Context, dedupeKey string) (*alertmodel.Incident, error)
	GetIncidentByID(ctx context.Context, id uint64) (*alertmodel.Incident, error)
	CreateIncident(ctx context.Context, incident *alertmodel.Incident) error
	CreateEvent(ctx context.Context, event *alertmodel.Event) error
}

type ScenarioRepository interface {
	CreateOrUpdate(ctx context.Context, run *demomodel.ScenarioRun) error
	GetByIdempotencyKey(ctx context.Context, tenantID uint64, scenarioID, key string) (*demomodel.ScenarioRun, error)
	UpdateStatus(ctx context.Context, id uint64, status string, mutation func(*demomodel.ScenarioRun) error) error
	UpdateStatusWithEvent(ctx context.Context, id uint64, status string, event *alertmodel.Event, allowedCurrent ...string) error
}

type PoolFixtureRepository interface {
	Start(ctx context.Context, input FixtureStartInput) (FixtureStartResult, error)
	Status(ctx context.Context, manifestID string) (FixtureStatus, error)
	BusinessSnapshot(ctx context.Context, section string) (json.RawMessage, error)
}

type PreviewRepository interface {
	ListByIncident(ctx context.Context, tenantID, incidentID string, limit int) ([]repairpreview.Run, error)
	FindEligible(ctx context.Context, tenantID, incidentID, runID, candidateID, action string) (repairpreview.Candidate, error)
}

type WorkflowPublisher interface {
	PublishWorkflow(ctx context.Context, run *demomodel.ScenarioRun, stage string, decision *PreviewDecisionSummary) error
}

type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Usecase struct {
	scenarios             ScenarioRepository
	incidents             IncidentRepository
	fixtures              PoolFixtureRepository
	previews              PreviewRepository
	expectedReplayProfile string
	workflowPublisher     WorkflowPublisher
	clock                 Clock
}

func NewUsecase(scenarios ScenarioRepository, incidents IncidentRepository, fixtures PoolFixtureRepository) *Usecase {
	return &Usecase{scenarios: scenarios, incidents: incidents, fixtures: fixtures, clock: realClock{}}
}

func NewUsecaseWithPreviews(
	scenarios ScenarioRepository,
	incidents IncidentRepository,
	fixtures PoolFixtureRepository,
	previews PreviewRepository,
	expectedReplayProfile string,
) *Usecase {
	return &Usecase{
		scenarios: scenarios, incidents: incidents, fixtures: fixtures, previews: previews,
		expectedReplayProfile: expectedReplayProfile, clock: realClock{},
	}
}

func NewUsecaseWithPreviewWorkflow(
	scenarios ScenarioRepository,
	incidents IncidentRepository,
	fixtures PoolFixtureRepository,
	previews PreviewRepository,
	expectedReplayProfile string,
	workflowPublisher WorkflowPublisher,
) *Usecase {
	usecase := NewUsecaseWithPreviews(scenarios, incidents, fixtures, previews, expectedReplayProfile)
	usecase.workflowPublisher = workflowPublisher
	return usecase
}

func (u *Usecase) Start(ctx context.Context, tenantID uint64, input StartScenarioInput) (*ScenarioStatus, error) {
	if err := ValidateStart(input); err != nil {
		return nil, err
	}
	existing, err := u.scenarios.GetByIdempotencyKey(ctx, tenantID, input.ScenarioID, input.IdempotencyKey)
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return nil, err
	}
	if existing != nil {
		if existing.TargetFingerprint != input.TargetFingerprint {
			return nil, errs.ErrConflict
		}
		if existing.Status != demomodel.ScenarioStatusStartFailed {
			return statusFromRun(existing), nil
		}
	}

	dedupeKey := "demo-scenario:" + input.IdempotencyKey
	incident, err := u.incidents.GetIncidentByDedupeKey(ctx, dedupeKey)
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return nil, err
	}
	now := u.clock.Now()
	if incident == nil {
		incident = &alertmodel.Incident{
			Title: "PostgreSQL connection pool exhaustion rehearsal", Rule: "pg_pool_exhaustion",
			RuleName: "PostgreSQL pool exhaustion", Severity: "critical", Status: alertmodel.IncidentStatusOpen,
			Summary:     "Controlled PostgreSQL connection-pool exhaustion for the final demo.",
			Description: "Manager-initiated controlled fault with bounded duration and blast radius.",
			DedupeKey:   dedupeKey, LabelsJSON: `{"scenario":"pg-pool-exhaustion","target":"pg:pool-fixture"}`,
			AnnotationsJSON: "{}", EventCount: 1, FirstFiredAt: now, LastFiredAt: now,
			SourceType: "demo",
		}
		if err := u.incidents.CreateIncident(ctx, incident); err != nil {
			return nil, fmt.Errorf("create incident: %w", err)
		}
	}

	expiresAt := now.Add(time.Duration(input.DurationSeconds) * time.Second)
	run := &demomodel.ScenarioRun{
		TenantID: tenantID, ScenarioID: input.ScenarioID, IdempotencyKey: input.IdempotencyKey,
		IncidentID: incident.ID, Target: input.Target, TargetFingerprint: input.TargetFingerprint,
		AlertFingerprint: input.AlertFingerprint, Status: demomodel.ScenarioStatusStarting, ExpiresAt: expiresAt,
	}
	if existing != nil {
		run.ID = existing.ID
	}
	if err := u.scenarios.CreateOrUpdate(ctx, run); err != nil {
		return nil, err
	}

	result, err := u.fixtures.Start(ctx, FixtureStartInput{
		CaseID: input.ScenarioID, IncidentID: strconv.FormatUint(incident.ID, 10),
		InitialCapacity: finalDemoInitialPoolCapacity,
		TargetCapacity:  finalDemoRecoveredPoolCapacity,
		TTLSeconds:      input.DurationSeconds,
	})
	if err != nil {
		_ = u.scenarios.UpdateStatus(ctx, run.ID, demomodel.ScenarioStatusStartFailed, nil)
		return nil, err
	}
	run.PoolManifestID = result.ManifestID
	run.Status = demomodel.ScenarioStatusAwaitingAlert
	if err := u.scenarios.CreateOrUpdate(ctx, run); err != nil {
		return nil, err
	}
	if err := u.appendStartEvent(ctx, incident.ID, input, result.ManifestID, now); err != nil {
		return nil, err
	}
	return statusFromRun(run), nil
}

func (u *Usecase) Get(ctx context.Context, tenantID uint64, scenarioID, key string) (*ScenarioStatus, error) {
	if !idempotencyPattern.MatchString(key) {
		return nil, errs.ErrInvalid
	}
	run, err := u.scenarios.GetByIdempotencyKey(ctx, tenantID, scenarioID, key)
	if err != nil {
		return nil, err
	}
	if _, err := u.incidents.GetIncidentByID(ctx, run.IncidentID); err != nil {
		return nil, err
	}
	if run.PoolManifestID != "" {
		fixture, fixtureErr := u.fixtures.Status(ctx, run.PoolManifestID)
		if fixtureErr == nil {
			aggregateStatus := run.Status
			switch fixture.State {
			case "recovered":
				aggregateStatus = demomodel.ScenarioStatusRecovered
			case "expired":
				aggregateStatus = demomodel.ScenarioStatusClosed
			}
			if aggregateStatus != run.Status {
				if aggregateStatus == demomodel.ScenarioStatusRecovered {
					decision := u.previewDecision(ctx, tenantID, run)
					if err := u.advanceLoadedWorkflow(ctx, run, aggregateStatus, decision); err != nil {
						return nil, err
					}
				} else if err := u.scenarios.UpdateStatus(ctx, run.ID, aggregateStatus, nil); err != nil {
					return nil, err
				}
			}
		}
	}
	status := statusFromRun(run)
	status.PreviewDecision = u.previewDecision(ctx, tenantID, run)
	if err := u.applyPreviewTransition(ctx, run, status.PreviewDecision); err != nil {
		return nil, err
	}
	if run.Status != status.Status {
		status.Status = run.Status
	}
	return status, nil
}

func (u *Usecase) BusinessSnapshot(ctx context.Context, tenantID uint64, scenarioID, key, section string) (json.RawMessage, error) {
	if !validBusinessSection(section) {
		return nil, errs.ErrInvalid
	}
	run, err := u.scenarios.GetByIdempotencyKey(ctx, tenantID, scenarioID, key)
	if err != nil {
		return nil, err
	}
	if run.PoolManifestID == "" {
		return nil, errs.ErrNotWiredYet
	}
	return u.fixtures.BusinessSnapshot(ctx, section)
}

func (u *Usecase) BusinessSnapshotBaseline(ctx context.Context, section string) (json.RawMessage, error) {
	if !validBusinessSection(section) {
		return nil, errs.ErrInvalid
	}
	return u.fixtures.BusinessSnapshot(ctx, section)
}

func (u *Usecase) appendStartEvent(ctx context.Context, incidentID uint64, input StartScenarioInput, manifestID string, now time.Time) error {
	snapshot, err := json.Marshal(map[string]any{
		"scenario_id": input.ScenarioID, "idempotency_key": input.IdempotencyKey, "target": input.Target,
		"target_fingerprint": input.TargetFingerprint, "alert_fingerprint": input.AlertFingerprint,
		"duration_seconds": input.DurationSeconds, "pool_manifest_id": manifestID,
		"initial_capacity": finalDemoInitialPoolCapacity,
		"target_capacity":  finalDemoRecoveredPoolCapacity,
	})
	if err != nil {
		return err
	}
	message := "Controlled pool-exhaustion rehearsal started with a bounded blast radius."
	return u.incidents.CreateEvent(ctx, &alertmodel.Event{
		IncidentID: incidentID, EventType: "scenario_start", StatusAfter: alertmodel.IncidentStatusOpen,
		Severity: "critical", Title: "Scenario started", Message: &message, ActorType: alertmodel.ActorTypeSystem,
		SnapshotJSON: string(snapshot), Reason: "Manager-controlled final demo injection", OccurredAt: now,
	})
}

func (u *Usecase) previewDecision(ctx context.Context, tenantID uint64, scenario *demomodel.ScenarioRun) *PreviewDecisionSummary {
	if u.previews == nil || scenario.IncidentID == 0 {
		return nil
	}
	runs, err := u.previews.ListByIncident(
		ctx, strconv.FormatUint(tenantID, 10), strconv.FormatUint(scenario.IncidentID, 10), 20,
	)
	if err != nil || len(runs) == 0 {
		return nil
	}
	sort.SliceStable(runs, func(left, right int) bool {
		return runs[left].UpdatedAt.After(runs[right].UpdatedAt)
	})
	selected := runs[0]
	if selected.TenantID != strconv.FormatUint(tenantID, 10) ||
		selected.IncidentID != strconv.FormatUint(scenario.IncidentID, 10) {
		return nil
	}

	var baseline, passing, rejected repairpreview.Candidate
	for _, candidate := range selected.Candidates {
		if candidate.IsBaseline() && baseline.CandidateID == "" {
			baseline = candidate
			continue
		}
		if candidate.Decision == repairpreview.DecisionPass && passing.CandidateID == "" {
			passing = candidate
			continue
		}
		if candidate.Decision != repairpreview.DecisionPass && rejected.CandidateID == "" {
			rejected = candidate
		}
	}
	if baseline.CandidateID == "" {
		return nil
	}

	profileMatches := u.expectedReplayProfile != "" &&
		selected.WorkloadFingerprint == u.expectedReplayProfile
	gateStageReady := scenario.Status == demomodel.ScenarioStatusDiagnosisSent ||
		scenario.Status == demomodel.ScenarioStatusPreviewReady
	summary := &PreviewDecisionSummary{
		ReplayProfileID: selected.WorkloadFingerprint,
		BoundaryText:    selected.IsolationBoundary,
		CandidateB:      rejected.CandidateID,
	}
	if !profileMatches {
		summary.BoundaryText = fmt.Sprintf(
			"NOT COMPARABLE: replay profile %s does not match expected %s. %s",
			selected.WorkloadFingerprint, u.expectedReplayProfile, selected.IsolationBoundary,
		)
	}
	if gateStageReady && profileMatches && selected.ControlledLoad && completePreviewMetrics(baseline) &&
		passing.CandidateID != "" &&
		passing.Decision == repairpreview.DecisionPass && passing.Validate() == nil {
		eligible, err := u.previews.FindEligible(
			ctx, strconv.FormatUint(tenantID, 10), strconv.FormatUint(scenario.IncidentID, 10),
			selected.ID, passing.CandidateID, passing.Action,
		)
		if err == nil && eligible.ID == passing.ID && eligible.Decision == repairpreview.DecisionPass {
			summary.CandidateA = passing.CandidateID
			summary.EligibleForHITL = true
		}
	}
	return summary
}

func completePreviewMetrics(candidate repairpreview.Candidate) bool {
	return candidate.ResultChecksum != "" && candidate.WriteImpact != "" &&
		candidate.SampleCount > 0 && candidate.AverageLatencyMS > 0 &&
		candidate.MedianLatencyMS > 0 && candidate.P95LatencyMS > 0 && candidate.TPS > 0
}

func (u *Usecase) applyPreviewTransition(
	ctx context.Context, run *demomodel.ScenarioRun, decision *PreviewDecisionSummary,
) error {
	if decision == nil || (run.Status != demomodel.ScenarioStatusDiagnosisSent &&
		run.Status != demomodel.ScenarioStatusPreviewReady) {
		return nil
	}
	target := demomodel.ScenarioStatusPreviewReady
	if decision.EligibleForHITL {
		target = demomodel.ScenarioStatusAwaitingApproval
	}
	if run.Status == target {
		return nil
	}
	if run.Status != demomodel.ScenarioStatusPreviewReady {
		if err := u.transitionPreview(ctx, run, decision, demomodel.ScenarioStatusPreviewReady); err != nil {
			return err
		}
	}
	if target == demomodel.ScenarioStatusPreviewReady {
		return nil
	}
	return u.transitionPreview(ctx, run, decision, target)
}

func (u *Usecase) transitionPreview(
	ctx context.Context, run *demomodel.ScenarioRun, decision *PreviewDecisionSummary, target string,
) error {
	allowedCurrent := demomodel.ScenarioStatusDiagnosisSent
	if target == demomodel.ScenarioStatusAwaitingApproval {
		allowedCurrent = demomodel.ScenarioStatusPreviewReady
	}
	event := u.previewEvent(run, decision, target)
	if err := u.scenarios.UpdateStatusWithEvent(ctx, run.ID, target, event, allowedCurrent); err != nil {
		return err
	}
	run.Status = target
	u.publishWorkflow(ctx, run, target, decision)
	return nil
}

func (u *Usecase) previewEvent(
	run *demomodel.ScenarioRun, decision *PreviewDecisionSummary, status string,
) *alertmodel.Event {
	snapshot, err := json.Marshal(map[string]any{
		"replay_profile_id": decision.ReplayProfileID, "candidate_a": decision.CandidateA,
		"candidate_b": decision.CandidateB, "eligible_for_hitl": decision.EligibleForHITL,
		"boundary": decision.BoundaryText,
	})
	if err != nil {
		return &alertmodel.Event{IncidentID: run.IncidentID, EventType: status}
	}
	message := "Repair preview evidence is ready."
	if status == demomodel.ScenarioStatusAwaitingApproval {
		message = "Repair preview passed; human approval is required before execution."
	}
	return &alertmodel.Event{
		IncidentID: run.IncidentID, EventType: status, StatusAfter: alertmodel.IncidentStatusOpen,
		Severity: "critical", Title: "Repair preview gate", Message: &message,
		ActorType: alertmodel.ActorTypeSystem, SnapshotJSON: string(snapshot),
		Reason: "Preview evidence creates HITL eligibility only", OccurredAt: u.clock.Now(),
	}
}

func (u *Usecase) AdvanceWorkflow(
	ctx context.Context, tenantID uint64, scenarioID, key, stage string,
) (*ScenarioStatus, error) {
	if !idempotencyPattern.MatchString(key) {
		return nil, errs.ErrInvalid
	}
	run, err := u.scenarios.GetByIdempotencyKey(ctx, tenantID, scenarioID, key)
	if err != nil {
		return nil, err
	}
	if _, err := u.incidents.GetIncidentByID(ctx, run.IncidentID); err != nil {
		return nil, err
	}
	switch stage {
	case demomodel.ScenarioStatusRepairDispatched,
		demomodel.ScenarioStatusVerifying,
		demomodel.ScenarioStatusRecovered:
	default:
		return nil, errs.ErrInvalid
	}
	if run.Status == stage {
		return statusFromRun(run), nil
	}
	decision := u.previewDecision(ctx, tenantID, run)
	if err := u.advanceLoadedWorkflow(ctx, run, stage, decision); err != nil {
		return nil, err
	}
	status := statusFromRun(run)
	status.PreviewDecision = decision
	return status, nil
}

func (u *Usecase) advanceLoadedWorkflow(
	ctx context.Context, run *demomodel.ScenarioRun, stage string, decision *PreviewDecisionSummary,
) error {
	allowedCurrent := ""
	switch stage {
	case demomodel.ScenarioStatusPreviewReady:
		allowedCurrent = demomodel.ScenarioStatusDiagnosisSent
	case demomodel.ScenarioStatusAwaitingApproval:
		allowedCurrent = demomodel.ScenarioStatusPreviewReady
	case demomodel.ScenarioStatusRepairDispatched:
		allowedCurrent = demomodel.ScenarioStatusAwaitingApproval
	case demomodel.ScenarioStatusVerifying:
		allowedCurrent = demomodel.ScenarioStatusRepairDispatched
	case demomodel.ScenarioStatusRecovered:
		allowedCurrent = demomodel.ScenarioStatusVerifying
	default:
		return errs.ErrInvalid
	}
	event := u.workflowEvent(run, stage)
	if err := u.scenarios.UpdateStatusWithEvent(ctx, run.ID, stage, event, allowedCurrent); err != nil {
		return err
	}
	run.Status = stage
	u.publishWorkflow(ctx, run, stage, decision)
	return nil
}

func (u *Usecase) workflowEvent(run *demomodel.ScenarioRun, stage string) *alertmodel.Event {
	message := "Manager recorded authoritative demo workflow stage " + stage + "."
	snapshot, _ := json.Marshal(map[string]any{
		"scenario_id": run.ScenarioID, "idempotency_key": run.IdempotencyKey,
		"incident_id": run.IncidentID, "target_fingerprint": run.TargetFingerprint,
		"pool_manifest_id": run.PoolManifestID, "stage": stage,
	})
	return &alertmodel.Event{
		IncidentID: run.IncidentID, EventType: stage, StatusAfter: alertmodel.IncidentStatusOpen,
		Severity: "critical", Title: "Final demo workflow", Message: &message,
		ActorType: alertmodel.ActorTypeSystem, SnapshotJSON: string(snapshot),
		Reason: "OpsKeeper Manager authoritative transition", OccurredAt: u.clock.Now(),
	}
}

func (u *Usecase) publishWorkflow(
	ctx context.Context, run *demomodel.ScenarioRun, stage string, decision *PreviewDecisionSummary,
) {
	if u.workflowPublisher == nil {
		return
	}
	_ = u.workflowPublisher.PublishWorkflow(ctx, run, stage, decision)
}

func ValidateStart(input StartScenarioInput) error {
	if input.ScenarioID != ScenarioID || input.Target != ScenarioTarget {
		return errs.ErrInvalid
	}
	if input.DurationSeconds < 60 || input.DurationSeconds > 600 {
		return errs.ErrInvalid
	}
	if !idempotencyPattern.MatchString(input.IdempotencyKey) ||
		!validFingerprint(input.TargetFingerprint) || !validFingerprint(input.AlertFingerprint) {
		return errs.ErrInvalid
	}
	return nil
}

func validFingerprint(value string) bool {
	return hexPattern.MatchString(value) || sha256Pattern.MatchString(value)
}

func validBusinessSection(section string) bool {
	return section == "orders" || section == "inventory" || section == "audit"
}

func statusFromRun(run *demomodel.ScenarioRun) *ScenarioStatus {
	return &ScenarioStatus{
		IncidentID: run.IncidentID, ScenarioID: run.ScenarioID, Status: run.Status,
		PoolManifestID: run.PoolManifestID, TargetFingerprint: run.TargetFingerprint,
		AlertFingerprint: run.AlertFingerprint, UpdatedAt: run.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type FixtureStartInput struct {
	CaseID          string `json:"case_id"`
	IncidentID      string `json:"incident_id"`
	InitialCapacity int    `json:"initial_capacity"`
	TargetCapacity  int    `json:"target_capacity"`
	TTLSeconds      int    `json:"ttl_seconds"`
}

type FixtureStartResult struct{ ManifestID string }
type FixtureStatus struct{ State string }

type FixtureError struct {
	HTTPStatus int
	Code       string
}

func (e *FixtureError) Error() string { return "pool fixture request failed" }

type PoolFixtureClient struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewPoolFixtureClient(endpoint, token string) *PoolFixtureClient {
	return &PoolFixtureClient{
		endpoint: strings.TrimRight(endpoint, "/"), token: token,
		client: &http.Client{Timeout: requestTimeout},
	}
}

func (c *PoolFixtureClient) Start(ctx context.Context, input FixtureStartInput) (FixtureStartResult, error) {
	var output struct {
		ManifestID string `json:"manifest_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/pool-fixtures", input, &output, requestTimeout); err != nil {
		return FixtureStartResult{}, err
	}
	if output.ManifestID == "" {
		return FixtureStartResult{}, &FixtureError{HTTPStatus: http.StatusBadGateway}
	}
	return FixtureStartResult{ManifestID: output.ManifestID}, nil
}

func (c *PoolFixtureClient) Status(ctx context.Context, manifestID string) (FixtureStatus, error) {
	var output struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/pool-fixtures/"+manifestID, nil, &output, requestTimeout); err != nil {
		return FixtureStatus{}, err
	}
	return FixtureStatus{State: output.Status}, nil
}

func (c *PoolFixtureClient) BusinessSnapshot(ctx context.Context, section string) (json.RawMessage, error) {
	var output json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/v1/business-snapshots/"+section, nil, &output, businessTimeout); err != nil {
		return nil, err
	}
	return output, nil
}

func (c *PoolFixtureClient) do(ctx context.Context, method, path string, input any, output any, timeout time.Duration) error {
	var body []byte
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = encoded
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return &FixtureError{HTTPStatus: http.StatusBadGateway}
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-Opskeeper-Version", "v1")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return &FixtureError{HTTPStatus: http.StatusServiceUnavailable}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxFixtureBytes))
	if err != nil {
		return &FixtureError{HTTPStatus: http.StatusBadGateway}
	}
	var envelope struct {
		Code      int             `json:"code"`
		Message   string          `json:"message"`
		Data      json.RawMessage `json:"data"`
		ErrorCode string          `json:"error_code"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return &FixtureError{HTTPStatus: http.StatusBadGateway}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &FixtureError{HTTPStatus: response.StatusCode, Code: envelope.ErrorCode}
	}
	if len(envelope.Data) == 0 {
		return &FixtureError{HTTPStatus: http.StatusBadGateway}
	}
	if err := json.Unmarshal(envelope.Data, output); err != nil {
		return &FixtureError{HTTPStatus: http.StatusBadGateway}
	}
	return nil
}
