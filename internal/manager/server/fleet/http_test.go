package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	fleetbiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/fleet"
	devicemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/device"
	edgemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/edge"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tenantctx"
)

// fakeHostService keeps HTTP tests independent of persistence and verifies the
// handler only exposes the read-only fleet contract.
type fakeHostService struct {
	items []fleetbiz.Host
	total int
	err   error
	last  fleetbiz.Filter
}

func (s *fakeHostService) List(_ context.Context, f fleetbiz.Filter) ([]fleetbiz.Host, int, error) {
	s.last = f
	return s.items, s.total, s.err
}

func fleetRouter(s HostService, tenant *tenantctx.Tenant) http.Handler {
	r := chi.NewRouter()
	if tenant != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(tenantctx.With(req.Context(), *tenant)))
			})
		})
	}
	NewHandler(s).Register(r)
	return r
}

func TestHosts_RequiresAuthentication(t *testing.T) {
	r := fleetRouter(&fakeHostService{}, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/hosts", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

func TestHosts_ReturnsJoinedHostAndEdgeWithoutSecrets(t *testing.T) {
	svc := &fakeHostService{
		items: []fleetbiz.Host{{
			Device: &devicemodel.Device{ID: 7, Name: "db-a", Hostname: "db-a", Roles: devicemodel.RoleBitDatabase, Online: true},
			Edges:  []*edgemodel.Edge{{ID: 11, Name: "edge-a", Status: edgemodel.StatusOnline, AgentVersion: "0.85.1", AccessKeyID: "must-not-leak", SecretKeyHash: "must-not-leak"}},
		}},
		total: 1,
	}
	r := fleetRouter(svc, &tenantctx.Tenant{UserID: 3, Role: "user"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/hosts?status=online&role=database&since=2026-09-01T00:00:00Z&limit=10&offset=2", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if svc.last.Status != "online" || svc.last.Role != "database" || svc.last.Limit != 10 || svc.last.Offset != 2 || svc.last.Since == nil {
		t.Fatalf("filter = %+v", svc.last)
	}
	body := w.Body.String()
	for _, secret := range []string{"must-not-leak", "secret_key_hash", "access_key_id"} {
		if contains(body, secret) {
			t.Fatalf("response leaks %q: %s", secret, body)
		}
	}
	var got struct {
		Items []struct {
			Device map[string]any `json:"device"`
			Edges  []any          `json:"edges"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || len(got.Items) != 1 || len(got.Items[0].Edges) != 1 {
		t.Fatalf("body = %+v", got)
	}
}

func TestHosts_RejectsInvalidSince(t *testing.T) {
	r := fleetRouter(&fakeHostService{}, &tenantctx.Tenant{UserID: 1, Role: "user"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/fleet/hosts?since=not-a-time", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
