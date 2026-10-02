package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	bizaudit "github.com/vincent-wuhan/opskeeper/core/manager/biz/audit"
	auditmodel "github.com/vincent-wuhan/opskeeper/core/manager/model/audit"
	auditport "github.com/vincent-wuhan/opskeeper/core/manager/pkg/audit"
)

// recordingRepo is the writer's seam. The point of the test below is the
// path from a handler's SetAuditEvent to the row, so the row has to land
// somewhere observable, and an in-memory repo is the only honest way to
// observe it without a database.
type recordingRepo struct{ rows []*auditmodel.Log }

func (r *recordingRepo) Insert(_ context.Context, row *auditmodel.Log) error {
	r.rows = append(r.rows, row)
	return nil
}

func (r *recordingRepo) List(context.Context, bizaudit.ListFilters) ([]auditmodel.Log, int64, error) {
	return nil, 0, nil
}

func (r *recordingRepo) DeleteOlderThan(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func newAuditStack(t *testing.T, repo bizaudit.Repo, handler http.Handler) http.Handler {
	t.Helper()
	uc := bizaudit.New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// rewrap stands in for auth / tenant / otel, each of which re-wraps
	// the request on its way down. They are the reason the audit slot is a
	// pointer in the context, so a stack without them would not exercise
	// the property being claimed.
	rewrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), struct{ mid string }{"tenant"}, "t-1")
			ctx = context.WithValue(ctx, struct{ mid string }{"otel"}, "s-1")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	return chimw.RequestID(AuditMiddleware(uc)(rewrap(handler)))
}

// TestTheRowAHandlerAsksForIsTheRowTheLedgerGets is the end-to-end claim
// behind moving the shape into a port: a handler in a bounded context that
// knows nothing about the ledger can still get a row into it, and the row
// carries what the handler set plus what only the host can know.
//
// The handler below sets an action, a resource and an actor, and nothing
// else. The status, the client IP and the request id are the middleware's
// to fill, which is why a handler cannot be trusted to write them: it has
// no way to see the response.
func TestTheRowAHandlerAsksForIsTheRowTheLedgerGets(t *testing.T) {
	repo := &recordingRepo{}
	uid := uint64(7)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auditport.SetAuditEvent(r, auditport.Event{
			UserID:       &uid,
			Action:       auditport.ActionUserCreate,
			ResourceType: auditport.ResourceUser,
			ResourceID:   "u-7",
			Payload:      map[string]string{"field": "role", "to": "admin"},
		})
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/iam/users", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	// The id an upstream proxy already stamped: the row has to carry the
	// same one the request's log lines do, or joining them is guesswork.
	req.Header.Set(chimw.RequestIDHeader, "req-1")
	newAuditStack(t, repo, handler).ServeHTTP(httptest.NewRecorder(), req)

	if len(repo.rows) != 1 {
		t.Fatalf("expected exactly one row, got %d", len(repo.rows))
	}
	row := repo.rows[0]
	if row.Action != auditport.ActionUserCreate {
		t.Errorf("action = %q, want the one the handler set", row.Action)
	}
	if row.ResourceType != auditport.ResourceUser || row.ResourceID != "u-7" {
		t.Errorf("resource = %q/%q, want user/u-7", row.ResourceType, row.ResourceID)
	}
	if row.UserID == nil || *row.UserID != uid {
		t.Errorf("actor = %v, want the one the handler set", row.UserID)
	}
	if row.Status != auditmodel.StatusSuccess {
		t.Errorf("status = %q, want the middleware to bucket 201 as success", row.Status)
	}
	// First XFF hop only: the rest of the chain is our own proxies, and
	// recording them as the client would make the trail useless.
	if row.IP != "203.0.113.7" {
		t.Errorf("ip = %q, want the first XFF hop", row.IP)
	}
	if row.RequestID != "req-1" {
		t.Errorf("request id = %q, want the id stamped on the request's log lines", row.RequestID)
	}
	if row.OccurredAt.IsZero() {
		t.Error("occurred_at was not stamped by the writer")
	}
	if row.PayloadJSON == "" {
		t.Error("payload was dropped; the row no longer says what changed")
	}
}

// TestAnUnannotatedRequestIsNotAudited holds the curation rule this
// middleware was written around: audit_logs is a trail of user-meaningful
// actions, not an access log. A GET that nobody annotated must not become a
// row, or the signal drowns and the retention job spends its budget on
// noise.
func TestAnUnannotatedRequestIsNotAudited(t *testing.T) {
	repo := &recordingRepo{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	newAuditStack(t, repo, handler).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/v1/iam/users", nil))

	if len(repo.rows) != 0 {
		t.Fatalf("an unannotated request produced %d rows: %+v", len(repo.rows), repo.rows[0])
	}
}

// TestAFailingRequestIsAuditedAsAFailure is the other half of the status
// bucket. A handler that annotates and then fails must not leave a
// success-shaped row behind, because the status is decided by the response
// the handler never sees.
func TestAFailingRequestIsAuditedAsAFailure(t *testing.T) {
	repo := &recordingRepo{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auditport.SetAuditEvent(r, auditport.Event{
			Action:       auditport.ActionUserDelete,
			ResourceType: auditport.ResourceUser,
			ResourceID:   "u-9",
		})
		w.WriteHeader(http.StatusInternalServerError)
	})
	newAuditStack(t, repo, handler).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodDelete, "/api/v1/iam/users/u-9", nil))

	if len(repo.rows) != 1 {
		t.Fatalf("expected exactly one row, got %d", len(repo.rows))
	}
	if got := repo.rows[0].Status; got != auditmodel.StatusFailure {
		t.Errorf("status = %q, want failure for a 500", got)
	}
}
