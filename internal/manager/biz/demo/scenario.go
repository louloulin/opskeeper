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
	"strconv"
	"strings"
	"time"

	alertmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/alert"
	demomodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/demo"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

const (
	ScenarioID       = "pg-pool-exhaustion"
	ScenarioTarget   = "pg:pool-fixture"
	maxFixtureBytes  = 1 << 20
	requestTimeout   = 5 * time.Second
	businessTimeout  = 3 * time.Second
	fixtureCapacity  = 2
	fixtureTargetCap = 4
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
	IncidentID        uint64 `json:"incident_id"`
	ScenarioID        string `json:"scenario_id"`
	Status            string `json:"status"`
	PoolManifestID    string `json:"pool_manifest_id"`
	TargetFingerprint string `json:"target_fingerprint"`
	AlertFingerprint  string `json:"alert_fingerprint"`
	UpdatedAt         string `json:"updated_at"`
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
}

type PoolFixtureRepository interface {
	Start(ctx context.Context, input FixtureStartInput) (FixtureStartResult, error)
	Status(ctx context.Context, manifestID string) (FixtureStatus, error)
	BusinessSnapshot(ctx context.Context, section string) (json.RawMessage, error)
}

type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Usecase struct {
	scenarios ScenarioRepository
	incidents IncidentRepository
	fixtures  PoolFixtureRepository
	clock     Clock
}

func NewUsecase(scenarios ScenarioRepository, incidents IncidentRepository, fixtures PoolFixtureRepository) *Usecase {
	return &Usecase{scenarios: scenarios, incidents: incidents, fixtures: fixtures, clock: realClock{}}
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
		InitialCapacity: fixtureCapacity, TargetCapacity: fixtureTargetCap, TTLSeconds: input.DurationSeconds,
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
				if err := u.scenarios.UpdateStatus(ctx, run.ID, aggregateStatus, nil); err != nil {
					return nil, err
				}
				run.Status = aggregateStatus
			}
		}
	}
	return statusFromRun(run), nil
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
		"initial_capacity": fixtureCapacity, "target_capacity": fixtureTargetCap,
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
