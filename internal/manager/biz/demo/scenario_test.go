package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	repairpreview "github.com/vincent-wuhan/opskeeper/internal/control/repairpreview"
	alertmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/alert"
	demomodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/demo"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

type fakeIncidents struct {
	nextID uint64
	rows   map[string]*alertmodel.Incident
	byID   map[uint64]*alertmodel.Incident
	order  *[]string
	events []*alertmodel.Event
}

func newFakeIncidents() *fakeIncidents {
	var order []string
	return &fakeIncidents{
		nextID: 100, rows: map[string]*alertmodel.Incident{}, byID: map[uint64]*alertmodel.Incident{}, order: &order,
	}
}

func (f *fakeIncidents) GetIncidentByDedupeKey(_ context.Context, key string) (*alertmodel.Incident, error) {
	if incident, ok := f.rows[key]; ok {
		return incident, nil
	}
	return nil, errs.ErrNotFound
}

func (f *fakeIncidents) GetIncidentByID(_ context.Context, id uint64) (*alertmodel.Incident, error) {
	if incident, ok := f.byID[id]; ok {
		return incident, nil
	}
	return nil, errs.ErrNotFound
}

func (f *fakeIncidents) CreateIncident(_ context.Context, incident *alertmodel.Incident) error {
	*f.order = append(*f.order, "incident")
	incident.ID = f.nextID
	f.nextID++
	f.rows[incident.DedupeKey] = incident
	f.byID[incident.ID] = incident
	return nil
}

func (f *fakeIncidents) CreateEvent(_ context.Context, event *alertmodel.Event) error {
	*f.order = append(*f.order, "event")
	f.events = append(f.events, event)
	return nil
}

type fakeScenarios struct {
	nextID         uint64
	rows           map[string]*demomodel.ScenarioRun
	events         []*alertmodel.Event
	failEventWrite bool
}

func newFakeScenarios() *fakeScenarios {
	return &fakeScenarios{nextID: 10, rows: map[string]*demomodel.ScenarioRun{}}
}

func (f *fakeScenarios) CreateOrUpdate(_ context.Context, run *demomodel.ScenarioRun) error {
	key := fmt.Sprintf("%d/%s/%s", run.TenantID, run.ScenarioID, run.IdempotencyKey)
	if existing, ok := f.rows[key]; ok {
		if existing.TargetFingerprint != run.TargetFingerprint {
			return errs.ErrConflict
		}
		run.ID = existing.ID
		run.UpdatedAt = time.Now().UTC()
	} else {
		run.ID = f.nextID
		f.nextID++
		run.UpdatedAt = time.Now().UTC()
	}
	copied := *run
	f.rows[key] = &copied
	return nil
}

func (f *fakeScenarios) GetByIdempotencyKey(_ context.Context, tenantID uint64, scenarioID, key string) (*demomodel.ScenarioRun, error) {
	row, ok := f.rows[fmt.Sprintf("%d/%s/%s", tenantID, scenarioID, key)]
	if !ok {
		return nil, errs.ErrNotFound
	}
	return row, nil
}

func (f *fakeScenarios) UpdateStatus(_ context.Context, id uint64, status string, mutation func(*demomodel.ScenarioRun) error) error {
	for _, row := range f.rows {
		if row.ID != id {
			continue
		}
		row.Status = status
		if mutation != nil {
			if err := mutation(row); err != nil {
				return err
			}
		}
		row.UpdatedAt = time.Now().UTC()
		return nil
	}
	return errs.ErrNotFound
}

func (f *fakeScenarios) UpdateStatusWithEvent(
	_ context.Context, id uint64, status string, event *alertmodel.Event, allowedCurrent ...string,
) error {
	for _, row := range f.rows {
		if row.ID != id {
			continue
		}
		allowed := len(allowedCurrent) == 0
		for _, current := range allowedCurrent {
			if current == row.Status {
				allowed = true
			}
		}
		if !allowed {
			return errs.ErrConflict
		}
		if f.failEventWrite {
			return errors.New("event write failed")
		}
		previous := row.Status
		row.Status = status
		if event != nil {
			event.StatusAfter = alertmodel.IncidentStatusOpen
			f.events = append(f.events, event)
		}
		_ = previous
		row.UpdatedAt = time.Now().UTC()
		return nil
	}
	return errs.ErrNotFound
}

type fakeFixtures struct {
	starts    int
	business  int
	fails     bool
	order     *[]string
	lastStart FixtureStartInput
}

type fakePreviewRepository struct {
	runs          []repairpreview.Run
	eligibleCalls int
}

type fakeWorkflowPublisher struct {
	stages []string
	fails  bool
}

func (f *fakeWorkflowPublisher) PublishWorkflow(_ context.Context, _ *demomodel.ScenarioRun, stage string, _ *PreviewDecisionSummary) error {
	f.stages = append(f.stages, stage)
	if f.fails {
		return errors.New("matrix unavailable")
	}
	return nil
}

func (f *fakePreviewRepository) ListByIncident(
	_ context.Context, _, _ string, _ int,
) ([]repairpreview.Run, error) {
	return f.runs, nil
}

func (f *fakePreviewRepository) FindEligible(
	_ context.Context, _, _, runID, candidateID, action string,
) (repairpreview.Candidate, error) {
	f.eligibleCalls++
	for _, run := range f.runs {
		for _, candidate := range run.Candidates {
			if candidate.RunID == runID && candidate.CandidateID == candidateID &&
				candidate.Action == action && candidate.Decision == repairpreview.DecisionPass {
				return candidate, nil
			}
		}
	}
	return repairpreview.Candidate{}, repairpreview.ErrCandidateNotFound
}

func (f *fakeFixtures) Start(_ context.Context, input FixtureStartInput) (FixtureStartResult, error) {
	f.starts++
	f.lastStart = input
	if f.order != nil {
		*f.order = append(*f.order, "fixture")
	}
	if f.fails {
		return FixtureStartResult{}, &FixtureError{HTTPStatus: http.StatusServiceUnavailable}
	}
	return FixtureStartResult{ManifestID: fmt.Sprintf("manifest-%d", f.starts)}, nil
}

func (f *fakeFixtures) Status(context.Context, string) (FixtureStatus, error) {
	return FixtureStatus{State: "running"}, nil
}

func (f *fakeFixtures) BusinessSnapshot(_ context.Context, _ string) (json.RawMessage, error) {
	f.business++
	if f.fails {
		return nil, &FixtureError{HTTPStatus: http.StatusServiceUnavailable, Code: "pool_exhausted"}
	}
	return json.RawMessage(`{"section":"orders"}`), nil
}

func TestBaselineBusinessSnapshotDoesNotRequireScenario(t *testing.T) {
	fixtures := &fakeFixtures{}
	usecase := NewUsecase(newFakeScenarios(), newFakeIncidents(), fixtures)
	data, err := usecase.BusinessSnapshotBaseline(context.Background(), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"section":"orders"}` || fixtures.business != 1 || fixtures.starts != 0 {
		t.Fatalf("data = %s business = %d starts = %d", data, fixtures.business, fixtures.starts)
	}
	if _, err := usecase.BusinessSnapshotBaseline(context.Background(), "unknown"); err == nil {
		t.Fatal("expected invalid section")
	}
}

func validInput() StartScenarioInput {
	return StartScenarioInput{
		IdempotencyKey: "final-demo-key", ScenarioID: ScenarioID, Target: ScenarioTarget,
		TargetFingerprint: "0123456789abcdef", AlertFingerprint: "fedcba9876543210", DurationSeconds: 90,
	}
}

func TestStartCreatesIncidentBeforeFixture(t *testing.T) {
	incidents := newFakeIncidents()
	fixtures := &fakeFixtures{order: incidents.order}
	usecase := NewUsecase(newFakeScenarios(), incidents, fixtures)
	status, err := usecase.Start(context.Background(), 1, validInput())
	if err != nil {
		t.Fatal(err)
	}
	if order := *incidents.order; len(order) != 3 || order[0] != "incident" || order[1] != "fixture" || order[2] != "event" {
		t.Fatalf("order = %v", order)
	}
	if fixtures.starts != 1 || status.PoolManifestID != "manifest-1" || status.Status != demomodel.ScenarioStatusAwaitingAlert {
		t.Fatalf("status = %+v fixture starts = %d", status, fixtures.starts)
	}
	if len(incidents.events) != 1 || strings.Contains(incidents.events[0].SnapshotJSON, "token") {
		t.Fatalf("event = %+v", incidents.events[0])
	}
}

func TestStartUsesFinalDemoPoolCapacity(t *testing.T) {
	fixtures := &fakeFixtures{}
	usecase := NewUsecase(newFakeScenarios(), newFakeIncidents(), fixtures)
	if _, err := usecase.Start(context.Background(), 1, validInput()); err != nil {
		t.Fatal(err)
	}
	if fixtures.lastStart.InitialCapacity != 4 || fixtures.lastStart.TargetCapacity != 8 {
		t.Fatalf(
			"capacity = %d/%d, want initial 4 and recovered 8",
			fixtures.lastStart.InitialCapacity,
			fixtures.lastStart.TargetCapacity,
		)
	}
}

func TestRepeatedStartReturnsSameIncident(t *testing.T) {
	fixtures := &fakeFixtures{}
	usecase := NewUsecase(newFakeScenarios(), newFakeIncidents(), fixtures)
	first, err := usecase.Start(context.Background(), 1, validInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := usecase.Start(context.Background(), 1, validInput())
	if err != nil {
		t.Fatal(err)
	}
	if first.IncidentID != second.IncidentID || fixtures.starts != 1 {
		t.Fatalf("first = %+v second = %+v starts = %d", first, second, fixtures.starts)
	}
}

func TestStartValidatesAllowlistedTargetAndDuration(t *testing.T) {
	usecase := NewUsecase(newFakeScenarios(), newFakeIncidents(), &fakeFixtures{})
	for name, mutate := range map[string]func(*StartScenarioInput){
		"scenario":  func(in *StartScenarioInput) { in.ScenarioID = "cpu" },
		"target":    func(in *StartScenarioInput) { in.Target = "redis" },
		"duration":  func(in *StartScenarioInput) { in.DurationSeconds = 59 },
		"idempower": func(in *StartScenarioInput) { in.IdempotencyKey = "short" },
		"fingerpr":  func(in *StartScenarioInput) { in.TargetFingerprint = "not-hex" },
	} {
		input := validInput()
		mutate(&input)
		if _, err := usecase.Start(context.Background(), 1, input); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("%s err = %v", name, err)
		}
	}
}

func TestFixtureStartFailureLeavesRestartableScenario(t *testing.T) {
	incidents := newFakeIncidents()
	fixtures := &fakeFixtures{fails: true}
	scenarios := newFakeScenarios()
	usecase := NewUsecase(scenarios, incidents, fixtures)
	input := validInput()
	if _, err := usecase.Start(context.Background(), 1, input); err == nil {
		t.Fatal("first start should fail")
	}
	run, err := scenarios.GetByIdempotencyKey(context.Background(), 1, input.ScenarioID, input.IdempotencyKey)
	if err != nil || run.Status != demomodel.ScenarioStatusStartFailed {
		t.Fatalf("failed run = %+v err = %v", run, err)
	}
	fixtures.fails = false
	status, err := usecase.Start(context.Background(), 1, input)
	if err != nil {
		t.Fatal(err)
	}
	if status.IncidentID != 100 || fixtures.starts != 2 || len(incidents.rows) != 1 {
		t.Fatalf("restart status = %+v starts = %d incidents = %d", status, fixtures.starts, len(incidents.rows))
	}
}

func TestBusinessSnapshotReturnsPoolExhausted(t *testing.T) {
	scenarios := newFakeScenarios()
	input := validInput()
	run := &demomodel.ScenarioRun{
		TenantID: 1, ScenarioID: ScenarioID, IdempotencyKey: input.IdempotencyKey, IncidentID: 100,
		PoolManifestID: "manifest", TargetFingerprint: input.TargetFingerprint, Status: demomodel.ScenarioStatusAwaitingAlert,
		ExpiresAt: time.Now().Add(time.Minute), UpdatedAt: time.Now().UTC(),
	}
	if err := scenarios.CreateOrUpdate(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	_, err := NewUsecase(scenarios, newFakeIncidents(), &fakeFixtures{fails: true}).BusinessSnapshot(
		context.Background(), 1, ScenarioID, input.IdempotencyKey, "orders",
	)
	var fixtureErr *FixtureError
	if !errors.As(err, &fixtureErr) || fixtureErr.Code != "pool_exhausted" {
		t.Fatalf("err = %v", err)
	}
}

func previewCandidate(id, action string, decision repairpreview.Decision) repairpreview.Candidate {
	return repairpreview.Candidate{
		ID: "row-" + id, RunID: "preview-run", TenantID: "1", IncidentID: "100",
		CandidateID: id, Name: id, Kind: "candidate", Action: action,
		ChangeSummary: action, Branch: "preview/" + id, ResultChecksum: "sha256:" + id,
		Consistent: decision == repairpreview.DecisionPass, AverageLatencyMS: 18,
		MedianLatencyMS: 17, P95LatencyMS: 29, SampleCount: 20, TPS: 120,
		WriteImpact: "bounded", BusinessProbePass: decision == repairpreview.DecisionPass,
		Decision: decision,
	}
}

func previewRun(candidates ...repairpreview.Candidate) repairpreview.Run {
	return repairpreview.Run{
		ID: "preview-run", TenantID: "1", IncidentID: "100", BranchPrefix: "preview",
		SeedFingerprint: "sha256:seed-v1", WorkloadFingerprint: "sha256:workload-v1",
		WorkloadRevision: "workload-v1", ControlledLoad: true,
		IsolationBoundary: "preview-pg", Status: "finished", Candidates: candidates,
	}
}

func scenarioPartsWithPreview(
	t *testing.T, previews *fakePreviewRepository, expectedProfile string,
) (*Usecase, StartScenarioInput, *fakeIncidents, *fakeScenarios) {
	t.Helper()
	input := validInput()
	scenarios := newFakeScenarios()
	incidents := newFakeIncidents()
	incident := &alertmodel.Incident{
		ID: 100, Title: "PostgreSQL connection pool exhaustion rehearsal",
		DedupeKey: "demo-scenario:" + input.IdempotencyKey, Status: alertmodel.IncidentStatusOpen,
	}
	incidents.rows[incident.DedupeKey] = incident
	incidents.byID[incident.ID] = incident
	run := &demomodel.ScenarioRun{
		TenantID: 1, ScenarioID: ScenarioID, IdempotencyKey: input.IdempotencyKey,
		IncidentID: 100, PoolManifestID: "manifest", TargetFingerprint: input.TargetFingerprint,
		Status: demomodel.ScenarioStatusDiagnosisSent, ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := scenarios.CreateOrUpdate(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return NewUsecaseWithPreviews(
		scenarios, incidents, &fakeFixtures{}, previews, expectedProfile,
	), input, incidents, scenarios
}

func scenarioWithPreview(
	t *testing.T, previews *fakePreviewRepository, expectedProfile string,
) (*Usecase, StartScenarioInput, *fakeIncidents) {
	usecase, input, incidents, _ := scenarioPartsWithPreview(t, previews, expectedProfile)
	return usecase, input, incidents
}

func TestPreviewPASSCreatesOnlyHITLEligibility(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	passing := previewCandidate("candidate-a", "resize_pool", repairpreview.DecisionPass)
	rejected := previewCandidate("candidate-b", "reset_pool", repairpreview.DecisionReject)
	previews := &fakePreviewRepository{runs: []repairpreview.Run{
		previewRun(baseline, passing, rejected),
	}}
	usecase, input, _, scenarios := scenarioPartsWithPreview(t, previews, "sha256:workload-v1")

	status, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != demomodel.ScenarioStatusAwaitingApproval {
		t.Fatalf("status = %s, want awaiting_approval", status.Status)
	}
	decision := status.PreviewDecision
	if decision == nil || !decision.EligibleForHITL ||
		decision.ReplayProfileID != "sha256:workload-v1" ||
		decision.CandidateA != "candidate-a" || decision.CandidateB != "candidate-b" {
		t.Fatalf("decision = %+v", decision)
	}
	if previews.eligibleCalls != 1 {
		t.Fatalf("eligible calls = %d", previews.eligibleCalls)
	}
	if len(scenarios.events) != 2 ||
		scenarios.events[0].EventType != demomodel.ScenarioStatusPreviewReady ||
		scenarios.events[1].EventType != demomodel.ScenarioStatusAwaitingApproval {
		t.Fatalf("preview events = %+v", scenarios.events)
	}
	if _, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if previews.eligibleCalls != 1 {
		t.Fatalf("repeat calls = %d", previews.eligibleCalls)
	}
}

func TestPreviewFAILCannotReachApproval(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	rejected := previewCandidate("candidate-b", "reset_pool", repairpreview.DecisionReject)
	usecase, input, _ := scenarioWithPreview(
		t, &fakePreviewRepository{runs: []repairpreview.Run{previewRun(baseline, rejected)}}, "",
	)

	status, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != demomodel.ScenarioStatusPreviewReady {
		t.Fatalf("status = %s, want preview_ready", status.Status)
	}
	if decision := status.PreviewDecision; decision == nil || decision.EligibleForHITL ||
		decision.CandidateA != "" || decision.CandidateB != "candidate-b" {
		t.Fatalf("decision = %+v", status.PreviewDecision)
	}
}

func TestMismatchedReplayProfileIsNotComparable(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	passing := previewCandidate("candidate-a", "resize_pool", repairpreview.DecisionPass)
	run := previewRun(baseline, passing)
	run.WorkloadFingerprint = "sha256:workload-v2"
	previews := &fakePreviewRepository{runs: []repairpreview.Run{run}}
	usecase, input, _ := scenarioWithPreview(t, previews, "sha256:workload-v1")

	status, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != demomodel.ScenarioStatusPreviewReady {
		t.Fatalf("status = %s, want preview_ready", status.Status)
	}
	decision := status.PreviewDecision
	if decision == nil || decision.EligibleForHITL ||
		decision.ReplayProfileID != "sha256:workload-v2" ||
		!strings.Contains(decision.BoundaryText, "NOT COMPARABLE") {
		t.Fatalf("decision = %+v", decision)
	}
	if previews.eligibleCalls != 0 {
		t.Fatalf("mismatched profile queried eligibility calls = %d", previews.eligibleCalls)
	}
}

func TestIncompleteBaselineMetricsCannotReachApproval(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	baseline.SampleCount = 0
	passing := previewCandidate("candidate-a", "resize_pool", repairpreview.DecisionPass)
	previews := &fakePreviewRepository{runs: []repairpreview.Run{
		previewRun(baseline, passing),
	}}
	usecase, input, _ := scenarioWithPreview(t, previews, "sha256:workload-v1")

	status, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != demomodel.ScenarioStatusPreviewReady ||
		status.PreviewDecision == nil || status.PreviewDecision.EligibleForHITL {
		t.Fatalf("status = %+v decision = %+v", status, status.PreviewDecision)
	}
	if previews.eligibleCalls != 0 {
		t.Fatalf("incomplete baseline queried eligibility calls = %d", previews.eligibleCalls)
	}
}

func TestEmptyExpectedReplayProfileIsNotComparable(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	passing := previewCandidate("candidate-a", "resize_pool", repairpreview.DecisionPass)
	previews := &fakePreviewRepository{runs: []repairpreview.Run{previewRun(baseline, passing)}}
	usecase, input, _ := scenarioWithPreview(t, previews, "")

	status, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != demomodel.ScenarioStatusPreviewReady ||
		status.PreviewDecision == nil || status.PreviewDecision.EligibleForHITL ||
		!strings.Contains(status.PreviewDecision.BoundaryText, "NOT COMPARABLE") {
		t.Fatalf("status = %+v decision = %+v", status, status.PreviewDecision)
	}
	if previews.eligibleCalls != 0 {
		t.Fatalf("empty profile queried eligibility calls = %d", previews.eligibleCalls)
	}
}

func TestAlertCorrelatedCannotSkipDiagnosis(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	passing := previewCandidate("candidate-a", "resize_pool", repairpreview.DecisionPass)
	previews := &fakePreviewRepository{runs: []repairpreview.Run{previewRun(baseline, passing)}}
	usecase, input, incidents := scenarioWithPreview(t, previews, "sha256:workload-v1")
	run, err := usecase.scenarios.GetByIdempotencyKey(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = demomodel.ScenarioStatusAlertCorrelated

	status, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != demomodel.ScenarioStatusAlertCorrelated || previews.eligibleCalls != 0 {
		t.Fatalf("status = %+v calls = %d", status, previews.eligibleCalls)
	}
	if len(incidents.events) != 0 {
		t.Fatalf("unexpected events = %+v", incidents.events)
	}
}

func TestPreviewEventFailureRollsBackStatusAndDoesNotPublish(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	passing := previewCandidate("candidate-a", "resize_pool", repairpreview.DecisionPass)
	previews := &fakePreviewRepository{runs: []repairpreview.Run{previewRun(baseline, passing)}}
	usecase, input, incidents, scenarios := scenarioPartsWithPreview(t, previews, "sha256:workload-v1")
	scenarios.failEventWrite = true
	publisher := &fakeWorkflowPublisher{}
	usecase.workflowPublisher = publisher

	status, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if err == nil {
		t.Fatalf("expected atomic transition failure, status = %+v", status)
	}
	run, getErr := scenarios.GetByIdempotencyKey(context.Background(), 1, ScenarioID, input.IdempotencyKey)
	if getErr != nil || run.Status != demomodel.ScenarioStatusDiagnosisSent {
		t.Fatalf("run = %+v err = %v", run, getErr)
	}
	if len(scenarios.events) != 0 || len(incidents.events) != 0 || len(publisher.stages) != 0 {
		t.Fatalf("events = %+v incident events = %+v stages = %v", scenarios.events, incidents.events, publisher.stages)
	}
}

func TestWorkflowAdvancePublishesManagerAuthorityStages(t *testing.T) {
	baseline := previewCandidate("baseline", "baseline", repairpreview.DecisionPass)
	baseline.Kind = "baseline"
	passing := previewCandidate("candidate-a", "resize_pool", repairpreview.DecisionPass)
	rejected := previewCandidate("candidate-b", "reset_pool", repairpreview.DecisionReject)
	previews := &fakePreviewRepository{runs: []repairpreview.Run{
		previewRun(baseline, passing, rejected),
	}}
	usecase, input, _, scenarios := scenarioPartsWithPreview(t, previews, "sha256:workload-v1")
	publisher := &fakeWorkflowPublisher{}
	usecase.workflowPublisher = publisher

	if _, err := usecase.Get(context.Background(), 1, ScenarioID, input.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{
		demomodel.ScenarioStatusRepairDispatched,
		demomodel.ScenarioStatusVerifying,
		demomodel.ScenarioStatusRecovered,
	} {
		if _, err := usecase.AdvanceWorkflow(context.Background(), 1, ScenarioID, input.IdempotencyKey, stage); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		demomodel.ScenarioStatusPreviewReady,
		demomodel.ScenarioStatusAwaitingApproval,
		demomodel.ScenarioStatusRepairDispatched,
		demomodel.ScenarioStatusVerifying,
		demomodel.ScenarioStatusRecovered,
	}
	if strings.Join(publisher.stages, ",") != strings.Join(want, ",") {
		t.Fatalf("stages = %v want = %v", publisher.stages, want)
	}
	if len(scenarios.events) != len(want) {
		t.Fatalf("event count = %d want = %d", len(scenarios.events), len(want))
	}
}
