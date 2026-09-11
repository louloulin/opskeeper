package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	fleetbiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/fleet"
	alertmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/alert"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tenantctx"
)

// fakeClusterService keeps the F-2 HTTP contract testable without the alert
// store, and records the decoded filter the handler handed to the usecase.
type fakeClusterService struct {
	items []fleetbiz.ClusterIncident
	total int
	err   error
	last  fleetbiz.ClusterFilter
}

func (s *fakeClusterService) List(_ context.Context, f fleetbiz.ClusterFilter) ([]fleetbiz.ClusterIncident, int, error) {
	s.last = f
	return s.items, s.total, s.err
}

// clusterRouter mirrors fleetRouter but wires the optional cluster service.
// A nil svc reproduces the unwired-startup case.
func clusterRouter(hosts HostService, clusters ClusterService, tenant *tenantctx.Tenant) http.Handler {
	r := chi.NewRouter()
	if tenant != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(tenantctx.With(req.Context(), *tenant)))
			})
		})
	}
	h := NewHandler(hosts)
	if clusters != nil {
		h.SetClusterService(clusters)
	}
	h.Register(r)
	return r
}

func TestClusterIncidents_RequiresAuthentication(t *testing.T) {
	r := clusterRouter(&fakeHostService{}, &fakeClusterService{}, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/cluster-incidents", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

func TestClusterIncidents_UnwiredServiceAnswers501(t *testing.T) {
	r := clusterRouter(&fakeHostService{}, nil, &tenantctx.Tenant{UserID: 1, Role: "user"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/cluster-incidents", nil))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
}

func TestClusterIncidents_ParsesFilterAndSerializesGroup(t *testing.T) {
	base := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	deviceID := uint64(100)
	svc := &fakeClusterService{
		items: []fleetbiz.ClusterIncident{{
			Key:           "host.cpu_saturation|mount=/data|1757584800",
			Rule:          "host/cpu-spike",
			AnomalyClass:  "host.cpu_saturation",
			Signal:        "mount=/data",
			Severity:      "critical",
			Status:        alertmodel.StatusOpen,
			HostCount:     2,
			IncidentCount: 3,
			FirstFiredAt:  base,
			LastFiredAt:   base.Add(4 * time.Minute),
			Hosts: []fleetbiz.ClusterHost{
				{DeviceID: 100, Name: "db-a", Hostname: "db-a", Online: true, IncidentCount: 2, Severity: "critical", LastFiredAt: base.Add(4 * time.Minute)},
				{DeviceID: 200, Name: "db-b", Hostname: "db-b", Online: false, IncidentCount: 1, Severity: "warning", LastFiredAt: base},
			},
			Members: []fleetbiz.ClusterMember{
				{IncidentID: 1, DeviceID: &deviceID, Title: "cpu spike", Severity: "critical", Status: alertmodel.StatusOpen, FirstFiredAt: base, LastFiredAt: base},
			},
		}},
		total: 1,
	}
	r := clusterRouter(&fakeHostService{}, svc, &tenantctx.Tenant{UserID: 3, Role: "user"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/v1/fleet/cluster-incidents?status=open&severity=critical&min_hosts=2&window=15m&since=2026-09-11T09:00:00Z&limit=5&offset=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	f := svc.last
	if f.Status != "open" || f.Severity != "critical" || f.MinHosts != 2 || f.Limit != 5 || f.Offset != 1 {
		t.Fatalf("filter = %+v", f)
	}
	if f.Window != 15*time.Minute {
		t.Fatalf("window = %s", f.Window)
	}
	if f.Since == nil || !f.Since.Equal(time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("since = %v", f.Since)
	}

	var got struct {
		Items []struct {
			Key          string `json:"key"`
			AnomalyClass string `json:"anomaly_class"`
			HostCount    int    `json:"host_count"`
			Hosts        []struct {
				DeviceID uint64 `json:"device_id"`
				Name     string `json:"name"`
			} `json:"hosts"`
			Members []struct {
				IncidentID uint64 `json:"incident_id"`
			} `json:"members"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("body = %+v", got)
	}
	item := got.Items[0]
	if item.Key == "" || item.AnomalyClass != "host.cpu_saturation" || item.HostCount != 2 {
		t.Fatalf("item = %+v", item)
	}
	if len(item.Hosts) != 2 || item.Hosts[0].DeviceID != 100 || item.Hosts[0].Name != "db-a" {
		t.Fatalf("hosts = %+v", item.Hosts)
	}
	if len(item.Members) != 1 || item.Members[0].IncidentID != 1 {
		t.Fatalf("members = %+v", item.Members)
	}
}

func TestClusterIncidents_RejectsMalformedQuery(t *testing.T) {
	r := clusterRouter(&fakeHostService{}, &fakeClusterService{}, &tenantctx.Tenant{UserID: 1, Role: "user"})
	for _, query := range []string{"since=not-a-time", "window=soon"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/cluster-incidents?"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("query=%q status = %d, want 400", query, w.Code)
		}
	}
}

func TestClusterIncidents_MapsServiceErrors(t *testing.T) {
	cases := map[error]int{
		errs.ErrInvalid:      http.StatusBadRequest,
		errs.ErrUnauthorized: http.StatusUnauthorized,
		errs.ErrNotWiredYet:  http.StatusNotImplemented,
		errors.New("boom"):   http.StatusInternalServerError,
	}
	for err, want := range cases {
		r := clusterRouter(&fakeHostService{}, &fakeClusterService{err: err}, &tenantctx.Tenant{UserID: 1, Role: "user"})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/cluster-incidents", nil))
		if w.Code != want {
			t.Errorf("error=%v status = %d, want %d", err, w.Code, want)
		}
	}
}

func TestClusterIncidents_EmptyResultSerializesAsArrays(t *testing.T) {
	r := clusterRouter(&fakeHostService{}, &fakeClusterService{items: nil, total: 0}, &tenantctx.Tenant{UserID: 1, Role: "user"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/cluster-incidents", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if body := w.Body.String(); body != "{\"items\":[],\"total\":0}\n" {
		t.Fatalf("body = %s, want empty items array (not null)", body)
	}
}
