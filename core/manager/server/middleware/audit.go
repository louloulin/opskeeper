package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vincent-wuhan/opskeeper/core/manager/biz/audit"
	auditmodel "github.com/vincent-wuhan/opskeeper/core/manager/model/audit"
	auditport "github.com/vincent-wuhan/opskeeper/core/base/pkg/audit"
	"github.com/vincent-wuhan/opskeeper/core/base/pkg/tenantctx"
)

// The request-scoped slot — the key, the value, and the two accessors
// — now live in core/base/pkg/audit (decision 109). This middleware
// installs it; handlers in any bounded context write to it without
// importing this package. The re-exported functions below keep every
// existing `auditmw.SetAuditEvent` call site compiling unchanged.
var (
	// SetAuditEvent records the explicit Event the handler wants audited.
	// Safe to call even outside an AuditMiddleware chain — it just no-ops
	// if the slot isn't installed.
	SetAuditEvent = auditport.SetAuditEvent
	// GetAuditEvent returns the stashed Event, if any.
	GetAuditEvent = auditport.GetAuditEvent
)

// AuditMiddleware records HLD-010 audit_logs rows for **explicitly-
// annotated user actions only**. The middleware no longer derives a
// generic "http_<method>_<resource>" fallback — that produced ugly,
// non-actionable rows like `http_post_alerts` for any mutating
// request (operator feedback 2026-05-20).
//
// To audit a new operation, the handler must call SetAuditEvent with
// a canonical Action constant. Examples already wired:
//   - auth_login / auth_login_failed (iam/server/http.go)
//   - audit_view (manager/server/audit/http.go)
//   - alert/rule/channel/knowledge/user/settings CRUD (see each
//     handler — they pass model.Action* into SetAuditEvent)
//
// Anything not annotated is silently NOT audited. This is by design:
// audit_logs is a curated trail of user-meaningful actions, not an
// access log.
func AuditMiddleware(uc *audit.Usecase) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			// Install the port's mutable slot in the context so handlers
			// down the chain (past auth + otel + ...) can write to it via
			// SetAuditEvent. We hand the inner chain a new request pointer
			// carrying the slot; the slot is a pointer, so it survives
			// every subsequent r.WithContext call.
			ctx := auditport.WithSlot(r.Context())
			// tenantctx slot lives at the OUTER ctx so auth.Middleware
			// (deeper in the chain) can mutate it and we see the value
			// here post-handler. Without this, enrichFromRequest below
			// would see an unset tenant on every audit row except the
			// ones whose handler explicitly stuffed user_id/email in
			// SetAuditEvent (auth_login).
			ctx = tenantctx.WithSlot(ctx)
			next.ServeHTTP(ww, r.WithContext(ctx))

			ev, set := auditport.GetAuditEvent(ctx)
			if uc == nil || !set {
				return
			}
			// IMPORTANT: pass the wrapped ctx (the one carrying the
			// tenant slot pointer) into enrichFromRequest, not the
			// outer r.Context() — the outer ctx doesn't have the slot
			// key. The slot itself is a pointer so the mutation by
			// auth.Middleware deeper in the chain is visible here.
			enrichFromRequest(&ev, r, ctx, ww.Status())
			uc.Emit(ctx, ev)
		})
	}
}

func enrichFromRequest(ev *audit.Event, r *http.Request, ctx context.Context, status int) {
	if t, ok := tenantctx.From(ctx); ok {
		uid := t.UserID
		if uid != 0 && ev.UserID == nil {
			ev.UserID = &uid
		}
		if ev.UserEmail == "" {
			ev.UserEmail = t.Email
		}
		if ev.Role == "" {
			ev.Role = t.Role
		}
	}
	if ev.IP == "" {
		ev.IP = clientIP(r)
	}
	if ev.UserAgent == "" {
		ev.UserAgent = truncate(r.UserAgent(), 512)
	}
	if ev.RequestID == "" {
		ev.RequestID = chimw.GetReqID(r.Context())
	}
	if ev.Status == "" {
		ev.Status = statusBucket(status)
	}
}

func statusBucket(status int) string {
	switch {
	case status >= 500:
		return auditmodel.StatusFailure
	case status == http.StatusForbidden:
		return auditmodel.StatusDenied
	case status >= 400:
		return auditmodel.StatusFailure
	default:
		return auditmodel.StatusSuccess
	}
}

func clientIP(r *http.Request) string {
	// XFF first hop is the real client when behind nginx; fall back to
	// RemoteAddr. We accept the XFF header at face value because nginx
	// strips/replaces it before forwarding (ADR-008).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma >= 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
