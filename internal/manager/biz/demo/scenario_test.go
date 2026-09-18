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
	nextID uint64
	rows   map[string]*demomodel.ScenarioRun
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

type fakeFixtures struct {
	starts   int
	business int
	fails    bool
	order    *[]string
}

func (f *fakeFixtures) Start(context.Context, FixtureStartInput) (FixtureStartResult, error) {
	f.starts++
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
