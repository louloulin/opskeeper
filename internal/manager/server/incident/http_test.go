package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	incidentcontrol "github.com/vincent-wuhan/opskeeper/internal/control/incident"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tenantctx"
)

func TestMetricsReturnsJudgeReport(t *testing.T) {
	repository := &stubMetricsRepository{tenantID: "opskeeper-demo"}
	router := routerWithHandler(NewHandler(repository))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/metrics?tenant_id=opskeeper-demo", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 1, Role: "admin"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Code int                    `json:"code"`
		Data incidentcontrol.Report `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != 0 || response.Data.IncidentCount != 1 {
		t.Fatalf("response = %+v", response)
	}
	if repository.lastTenantID != "opskeeper-demo" {
		t.Fatalf("tenant = %q", repository.lastTenantID)
	}
}

func TestMetricsUserCannotOverrideTenant(t *testing.T) {
	repository := &stubMetricsRepository{tenantID: "2"}
	router := routerWithHandler(NewHandler(repository))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/metrics?tenant_id=opskeeper-demo", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 2, Role: "user"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if repository.lastTenantID != "2" {
		t.Fatalf("tenant = %q", repository.lastTenantID)
	}
}

func TestMetricsDefaultTenantCanBeConfigured(t *testing.T) {
	t.Setenv("OPSKEEPER_DEFAULT_INCIDENT_TENANT_ID", "open-source-test")
	repository := &stubMetricsRepository{tenantID: "open-source-test"}
	router := routerWithHandler(NewHandler(repository))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/metrics", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{
		AgentTeams: &tenantctx.AgentTeamsIdentity{TenantID: "default", Role: "worker"},
	}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if repository.lastTenantID != "open-source-test" {
		t.Fatalf("tenant = %q, want open-source-test", repository.lastTenantID)
	}
}

func TestMetricsRepositoryErrorReturns500(t *testing.T) {
	router := routerWithHandler(NewHandler(&stubMetricsRepository{err: errors.New("database unavailable")}))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/metrics", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 2, Role: "user"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRunbooksAndRecallLogsAreExposed(t *testing.T) {
	repository := &stubMetricsRepository{tenantID: "opskeeper-demo"}
	router := routerWithHandler(NewHandler(repository))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/runbooks?tenant_id=opskeeper-demo&database_type=PostgreSQL", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 1, Role: "admin"}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("runbook status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/incidents/INC-API-001/recall-logs?tenant_id=opskeeper-demo", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 1, Role: "admin"}))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("recall status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestArchiveReturnsCompleteEvidenceChain(t *testing.T) {
	events := completeArchiveEvents("opskeeper-demo", "INC-ARCHIVE-FULL")
	repository := &stubMetricsRepository{
		incidentEvents: map[string][]incidentcontrol.Event{
			"opskeeper-demo/INC-ARCHIVE-FULL": events,
		},
		tenantEvents: append(events, similarArchiveEvent("opskeeper-demo", "INC-ARCHIVE-SIMILAR")),
		runbooks: []incidentcontrol.Postmortem{{
			ID: "runbook-1", TenantID: "opskeeper-demo", IncidentID: "INC-ARCHIVE-FULL",
			Diagnosis:   incidentcontrol.PostmortemDiagnosis{RootCause: "connection pool saturation"},
			ConfirmedBy: "reviewer", ConfirmedAt: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
		}},
	}
	router := routerWithHandler(NewHandler(repository))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/INC-ARCHIVE-FULL/archive?tenant_id=opskeeper-demo", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 1, Role: "admin"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Code int            `json:"code"`
		Data archiveSummary `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	archive := response.Data
	if !archive.EvidenceComplete || archive.EventCount != 7 || len(archive.MissingEventTypes) != 0 {
		t.Fatalf("archive completeness = %+v", archive)
	}
	if !archive.RecoveryObserved || !archive.Closed {
		t.Fatalf("archive recovery status = %+v", archive)
	}
	if archive.LocalizationSeconds != 60 || archive.RecoverySeconds != 90 {
		t.Fatalf("archive timing = %+v", archive)
	}
	if len(archive.SimilarIncidents) != 1 || archive.SimilarIncidents[0].IncidentID != "INC-ARCHIVE-SIMILAR" {
		t.Fatalf("similar incidents = %+v", archive.SimilarIncidents)
	}
	if len(archive.PostmortemRefs) != 1 || archive.PostmortemRefs[0].RootCause != "connection pool saturation" {
		t.Fatalf("postmortem refs = %+v", archive.PostmortemRefs)
	}
	if repository.lastIncidentTenantID != "opskeeper-demo" || repository.lastIncidentID != "INC-ARCHIVE-FULL" {
		t.Fatalf("incident lookup = %s/%s", repository.lastIncidentTenantID, repository.lastIncidentID)
	}
}

func TestArchiveReportsMissingEvidence(t *testing.T) {
	events := completeArchiveEvents("opskeeper-demo", "INC-ARCHIVE-MISSING")[:2]
	repository := &stubMetricsRepository{
		incidentEvents: map[string][]incidentcontrol.Event{"opskeeper-demo/INC-ARCHIVE-MISSING": events},
	}
	router := routerWithHandler(NewHandler(repository))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/INC-ARCHIVE-MISSING/archive?tenant_id=opskeeper-demo", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 1, Role: "admin"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data archiveSummary `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.EvidenceComplete || len(response.Data.MissingEventTypes) != 5 {
		t.Fatalf("archive = %+v", response.Data)
	}
}

func TestArchiveIsTenantIsolated(t *testing.T) {
	events := completeArchiveEvents("opskeeper-demo", "INC-ARCHIVE-FULL")
	repository := &stubMetricsRepository{
		incidentEvents: map[string][]incidentcontrol.Event{"opskeeper-demo/INC-ARCHIVE-FULL": events},
	}
	router := routerWithHandler(NewHandler(repository))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/INC-ARCHIVE-FULL/archive?tenant_id=opskeeper-demo", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 2, Role: "user"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if repository.lastIncidentTenantID != "2" {
		t.Fatalf("tenant = %q", repository.lastIncidentTenantID)
	}
}

func TestArchiveEmptyIncidentReturns404(t *testing.T) {
	router := routerWithHandler(NewHandler(&stubMetricsRepository{}))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/INC-MISSING/archive", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 2, Role: "user"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestArchiveRepositoryErrorReturns500(t *testing.T) {
	router := routerWithHandler(NewHandler(&stubMetricsRepository{err: errors.New("database unavailable")}))
	request := httptest.NewRequest(http.MethodGet, "/v1/incidents/INC-API-001/archive", nil)
	request = request.WithContext(tenantctx.With(request.Context(), tenantctx.Tenant{UserID: 2, Role: "user"}))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); strings.Contains(body, "database unavailable") {
		t.Fatalf("error leaked repository detail: %s", body)
	}
}

func routerWithHandler(handler *Handler) http.Handler {
	router := chi.NewRouter()
	handler.Register(router)
	return router
}

type stubMetricsRepository struct {
	tenantID             string
	lastTenantID         string
	lastIncidentTenantID string
	lastIncidentID       string
	err                  error
	incidentEvents       map[string][]incidentcontrol.Event
	tenantEvents         []incidentcontrol.Event
	runbooks             []incidentcontrol.Postmortem
}

func (repository *stubMetricsRepository) ListTenant(_ context.Context, tenantID string) ([]incidentcontrol.Event, error) {
	repository.lastTenantID = tenantID
	if repository.err != nil {
		return nil, repository.err
	}
	if repository.tenantEvents != nil {
		return repository.tenantEvents, nil
	}
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return []incidentcontrol.Event{
		{
			ID: "017f2b01-4001-4000-8000-000000000001", TenantID: tenantID, IncidentID: "INC-API-001",
			OccurredAt: base, Phase: "detection", EventType: incidentcontrol.EventAlertReceived,
			ActorType: "system", Actor: "prometheus", Status: "firing",
			EvidenceRef: "evidence/alert.json", TraceID: "trace-api",
		},
		{
			ID: "017f2b01-4002-4000-8000-000000000002", TenantID: tenantID, IncidentID: "INC-API-001",
			OccurredAt: base.Add(time.Minute), Phase: "diagnosis", EventType: incidentcontrol.EventRootCause,
			ActorType: "agent", Actor: "diagnostics", Status: "confirmed",
			EvidenceRef: "evidence/diagnosis.json", TraceID: "trace-api",
		},
	}, nil
}

func (repository *stubMetricsRepository) ListIncident(_ context.Context, tenantID, incidentID string) ([]incidentcontrol.Event, error) {
	repository.lastIncidentTenantID = tenantID
	repository.lastIncidentID = incidentID
	if repository.err != nil {
		return nil, repository.err
	}
	return repository.incidentEvents[tenantID+"/"+incidentID], nil
}

func (repository *stubMetricsRepository) ListRunbooks(_ context.Context, tenantID, databaseType, faultFingerprint string) ([]incidentcontrol.Postmortem, error) {
	postmortem := incidentcontrol.Postmortem{
		TenantID: tenantID, IncidentID: "INC-API-001", DatabaseType: databaseType,
		FaultFingerprint: faultFingerprint,
	}
	return []incidentcontrol.Postmortem{postmortem}, nil
}

func (repository *stubMetricsRepository) ListIncidentRunbooks(_ context.Context, tenantID, incidentID string) ([]incidentcontrol.Postmortem, error) {
	if repository.err != nil {
		return nil, repository.err
	}
	return repository.runbooks, nil
}

func completeArchiveEvents(tenantID, incidentID string) []incidentcontrol.Event {
	base := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	eventTypes := []struct {
		eventType string
		phase     string
		actorType string
		actor     string
		status    string
		offset    time.Duration
	}{
		{incidentcontrol.EventAlertReceived, "detection", "system", "prometheus", "firing", 0},
		{incidentcontrol.EventRootCause, "diagnosis", "agent", "investigator", "confirmed", time.Minute},
		{incidentcontrol.EventEvidenceRefreshed, "diagnosis", "agent", "investigator", "complete", 2 * time.Minute},
		{incidentcontrol.EventApproved, "approval", "human", "reviewer", "approved", 3 * time.Minute},
		{incidentcontrol.EventAction, "execution", "agent", "repairer", "executed", 4 * time.Minute},
		{incidentcontrol.EventRecovery, "verification", "system", "verifier", "observed", 5*time.Minute + 30*time.Second},
		{incidentcontrol.EventClosed, "closure", "agent", "reporter", "closed", 6 * time.Minute},
	}
	events := make([]incidentcontrol.Event, 0, len(eventTypes))
	for index, item := range eventTypes {
		event := incidentcontrol.Event{
			ID: fmt.Sprintf("event-%02d", index+1), TenantID: tenantID, IncidentID: incidentID,
			OccurredAt: base.Add(item.offset), Phase: item.phase, EventType: item.eventType,
			ActorType: item.actorType, Actor: item.actor, Status: item.status,
			EvidenceRef: "evidence/" + item.eventType + ".json", TraceID: "trace-archive",
		}
		if item.eventType == incidentcontrol.EventRecovery {
			event.RecoverySignal = true
		}
		events = append(events, event)
	}
	return events
}

func similarArchiveEvent(tenantID, incidentID string) incidentcontrol.Event {
	return incidentcontrol.Event{
		ID: "event-similar", TenantID: tenantID, IncidentID: incidentID,
		OccurredAt: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC), Phase: "detection",
		EventType: incidentcontrol.EventAlertReceived, ActorType: "system", Actor: "prometheus", Status: "firing",
		EvidenceRef: "evidence/similar.json", TraceID: "trace-similar",
	}
}

func (repository *stubMetricsRepository) ListRecallLogs(_ context.Context, tenantID, incidentID string) ([]incidentcontrol.RecallLog, error) {
	return []incidentcontrol.RecallLog{{
		TenantID: tenantID, IncidentID: incidentID, CandidateRef: "runbook:INC-API-001",
		QueryText: "pool saturation", RRFScore: 0.03225806451612903, Selected: true,
	}}, nil
}
