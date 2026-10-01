// Package plugin exposes the fleet plugin-release routes.
//
// The lifecycle is deliberately more than one endpoint. A release is a job
// that outlives the request that started it, so the console needs to poll
// it, stop it, and take it back — and a single POST that did all three
// would leave an operator watching a canary go bad with nothing to call.
//
// Routes (all admin):
//
//	POST   /v1/plugins/releases              start a release
//	GET    /v1/plugins/releases              what is running
//	GET    /v1/plugins/releases/{name}       one release's progress
//	POST   /v1/plugins/releases/{name}/advance  send the next wave
//	POST   /v1/plugins/releases/{name}/halt     stop it, keep what is installed
//	POST   /v1/plugins/releases/{name}/rollback take it back off
//
// Every one of them is admin. A release is the action that puts new code
// — including L2 tools that can restart services — onto hosts.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	bizaudit "github.com/vincent-wuhan/opskeeper/internal/manager/biz/audit"
	auditmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/audit"
	auditmw "github.com/vincent-wuhan/opskeeper/internal/manager/server/middleware"
	release "github.com/vincent-wuhan/opskeeper/internal/manager/service/plugin"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tenantctx"
)

// roleAdmin mirrors iam/model.RoleAdmin without crossing the BC boundary.
// If the literal changes in iam/model it must change here too, which is the
// same trade internal/manager/server/edge already makes.
const roleAdmin = "admin"

// Service is the narrow surface this handler needs. *release.Manager
// satisfies it structurally, so the HTTP layer can be tested against a
// scripted manager.
type Service interface {
	Start(ctx context.Context, req release.StartRequest) (release.Status, error)
	List() []release.Status
	Status(name string) (release.Status, error)
	Advance(ctx context.Context, name string) (bool, release.Status, error)
	Halt(name, reason string) (release.Status, error)
	Rollback(ctx context.Context, name string) (release.Status, error)
}

// Handler serves /v1/plugins/*.
type Handler struct {
	svc Service
}

// NewHandler builds the handler. A nil service is tolerated so the wiring
// can construct the routes before the tunnel that backs them exists; every
// endpoint answers 503 until it is set, which reads as "not configured"
// rather than as a release that failed.
func NewHandler(svc Service) *Handler { return &Handler{svc: svc} }

// SetService back-fills the service post-construction. Safe to call before
// HTTP traffic arrives; the release manager needs the tunnel client, which
// is built later in main than this handler.
func (h *Handler) SetService(svc Service) { h.svc = svc }

// Register attaches the release routes. The caller is expected to have
// wrapped r in the auth middleware so tenantctx is populated.
func (h *Handler) Register(r chi.Router) {
	r.With(h.requireAdmin).Post("/v1/plugins/releases", h.start)
	r.With(h.requireAdmin).Get("/v1/plugins/releases", h.list)
	r.With(h.requireAdmin).Get("/v1/plugins/releases/{name}", h.status)
	r.With(h.requireAdmin).Post("/v1/plugins/releases/{name}/advance", h.advance)
	r.With(h.requireAdmin).Post("/v1/plugins/releases/{name}/halt", h.halt)
	r.With(h.requireAdmin).Post("/v1/plugins/releases/{name}/rollback", h.rollback)
}

// requireAdmin is the legacy enforcement, kept here rather than borrowed
// from the edge handler so this package does not import a sibling server
// package for one middleware.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := tenantctx.From(r.Context())
		if !ok {
			writeErr(w, errs.ErrUnauthorized)
			return
		}
		if t.Role != roleAdmin {
			writeErr(w, errs.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// startReq is the body of a release.
//
// Strategy is required rather than defaulted. Substituting "rolling" for a
// missing strategy would mean a pinned package silently gets a canary it
// was never meant to have — or, worse, a package that asked for a canary
// goes out in one wave because the field was empty.
type startReq struct {
	Plugin    string   `json:"plugin"`
	Version   string   `json:"version"`
	URL       string   `json:"url"`
	SHA256    string   `json:"sha256"`
	Signature string   `json:"signature"`
	KeyID     string   `json:"key_id,omitempty"`
	Strategy  string   `json:"strategy"`
	Nodes     []uint64 `json:"nodes,omitempty"`
}

// startResp is what the console gets back.
type startResp struct {
	release.Status
	// Waves is the number of waves the operator should expect, so a
	// progress bar can say "wave 1 of 4" from the first poll.
	Waves int `json:"waves"`
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	var req startReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	st, err := h.svc.Start(r.Context(), release.StartRequest{
		Name: req.Plugin, Version: req.Version, URL: req.URL,
		SHA256: req.SHA256, Signature: req.Signature, KeyID: req.KeyID,
		Strategy: req.Strategy, Nodes: req.Nodes,
	})
	if err != nil {
		// A refused release is audited too. The interesting question when
		// a package reaches the fleet without a decision on record is who
		// tried, and an audit trail that only records the attempts that
		// succeeded cannot answer it.
		auditRelease(r, auditmodel.ActionPluginReleaseStart, req.Plugin, auditmodel.StatusFailure, err, map[string]any{
			"version":  req.Version,
			"strategy": req.Strategy,
			"nodes":    req.Nodes,
		})
		writeErr(w, err)
		return
	}
	auditRelease(r, auditmodel.ActionPluginReleaseStart, st.Plugin, auditmodel.StatusSuccess, nil, map[string]any{
		"version":  st.Version,
		"strategy": req.Strategy,
		"nodes":    req.Nodes,
		"waves":    st.Waves,
		"summary":  st.Summary,
	})
	writeJSON(w, http.StatusOK, startResp{Status: st, Waves: st.Waves})
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	items := h.svc.List()
	if items == nil {
		items = []release.Status{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	st, err := h.svc.Status(releaseName(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// advanceResp says whether a wave went out, and why not when it did not.
type advanceResp struct {
	Moved bool `json:"moved"`
	release.Status
}

func (h *Handler) advance(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	moved, st, err := h.svc.Advance(r.Context(), releaseName(r))
	if err != nil {
		auditRelease(r, auditmodel.ActionPluginReleaseAdvance, releaseName(r), auditmodel.StatusFailure, err, nil)
		writeErr(w, err)
		return
	}
	// A wave that did not move is recorded as a failure rather than a
	// success. Nothing happened, and a trail that logged "advanced" for a
	// release still sitting on its canary would make the row useless for
	// the one question it exists to answer: when did this reach the rest
	// of the fleet.
	//
	// The block is described in the payload instead of in ErrorMessage.
	// A wave that has not moved is not a malfunction — the nodes it went
	// to simply have not answered — so stamping it with an error code
	// would teach an operator reading the trail to page someone at 3am
	// for a release that is waiting, which is the normal case.
	status := auditmodel.StatusSuccess
	payload := map[string]any{
		"wave":    st.Wave,
		"waves":   st.Waves,
		"moved":   moved,
		"pending": st.Pending,
		"failed":  st.Failed,
		"summary": st.Summary,
	}
	if !moved {
		status = auditmodel.StatusFailure
		payload["blocked"] = "the current wave is not accounted for; the release did not move"
	}
	auditRelease(r, auditmodel.ActionPluginReleaseAdvance, st.Plugin, status, nil, payload)
	writeJSON(w, http.StatusOK, advanceResp{Moved: moved, Status: st})
}

// haltReq carries the operator's sentence.
type haltReq struct {
	Reason string `json:"reason,omitempty"`
}

func (h *Handler) halt(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	var req haltReq
	// A halt with no body is a legitimate call — the operator just wants it
	// stopped — so a decode failure on an empty body is not an error.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	reason := req.Reason
	if reason == "" {
		reason = "halted from the console"
	}
	st, err := h.svc.Halt(releaseName(r), reason)
	if err != nil {
		auditRelease(r, auditmodel.ActionPluginReleaseHalt, releaseName(r), auditmodel.StatusFailure, err, nil)
		writeErr(w, err)
		return
	}
	// The operator's own sentence goes in the payload rather than a
	// generic "halted". It is the only field in this row that explains
	// *why*, and the person reading it is usually not the person who
	// typed it.
	auditRelease(r, auditmodel.ActionPluginReleaseHalt, st.Plugin, auditmodel.StatusSuccess, nil, map[string]any{
		"version": st.Version,
		"reason":  reason,
		"wave":    st.Wave,
		"waves":   st.Waves,
	})
	writeJSON(w, http.StatusOK, st)
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	st, err := h.svc.Rollback(r.Context(), releaseName(r))
	if err != nil {
		// The status still goes out with the error: a partial rollback
		// names the nodes that still hold the package, and an operator
		// needs that list even though the call failed.
		if st.Plugin != "" {
			auditRelease(r, auditmodel.ActionPluginReleaseRollback, st.Plugin, auditmodel.StatusFailure, err, map[string]any{
				"version": st.Version,
				"pending": st.Pending,
				"failed":  st.Failed,
			})
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":  map[string]string{"message": err.Error(), "code": "rollback_incomplete"},
				"status": st,
			})
			return
		}
		writeErr(w, err)
		return
	}
	auditRelease(r, auditmodel.ActionPluginReleaseRollback, st.Plugin, auditmodel.StatusSuccess, nil, map[string]any{
		"version": st.Version,
		"summary": st.Summary,
	})
	writeJSON(w, http.StatusOK, st)
}

func releaseName(r *http.Request) string { return chi.URLParam(r, "name") }

// auditRelease records one release action on the request the middleware
// will pick up.
//
// The action is set from the handler rather than derived from the route
// because the audit trail this replaces was exactly the derived kind: a
// generic "http_post_plugins_releases" row says a request happened, not
// that an operator put a package on the fleet. Every route in this package
// mutates the fleet, so every one of them names its action.
//
// A failure is audited as well as a success. The question an audit trail
// exists to answer here is "who shipped this", and an attempt that was
// refused network-wise or rejected as a duplicate release is part of that
// answer — a trail that only records the successes cannot say whether a
// package arrived deliberately or by a script nobody remembers.
func auditRelease(r *http.Request, action, plugin, status string, cause error, payload map[string]any) {
	if plugin == "" {
		// No package name means the request never got far enough to name
		// one — a malformed body, or a release started under a name the
		// manager never echoed back. Still audited: the attempt is the
		// event, and the empty resource id is itself the signal that the
		// request died before it named what it was about.
		plugin = releaseName(r)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	ev := bizaudit.Event{
		Action:       action,
		ResourceType: auditmodel.ResourcePlugin,
		ResourceID:   plugin,
		ResourceName: plugin,
		Status:       status,
		Payload:      payload,
	}
	if cause != nil {
		// The code comes from the same map the HTTP layer uses, so the
		// audit row and the response body cannot disagree about what went
		// wrong. Deriving it twice is how a trail grows a code the console
		// has never seen.
		code, _ := mapErr(cause)
		ev.ErrorCode = code
		ev.ErrorMessage = cause.Error()
	}
	auditmw.SetAuditEvent(r, ev)
}

// errNotWired is what an endpoint says before the tunnel exists.
const errNotWired = notWiredError("plugin releases are not wired on this manager")

type notWiredError string

func (e notWiredError) Error() string { return string(e) }

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	code, status := mapErr(err)
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"message": err.Error(), "code": code},
	})
}

func mapErr(err error) (string, int) {
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized", http.StatusUnauthorized
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden", http.StatusForbidden
	case errors.Is(err, errs.ErrInvalid):
		return "invalid", http.StatusBadRequest
	case errors.Is(err, release.ErrReleaseRunning):
		return "release_running", http.StatusConflict
	case errors.Is(err, release.ErrNoRelease):
		return "no_release", http.StatusNotFound
	case errors.Is(err, errNotWired):
		return "not_wired", http.StatusServiceUnavailable
	default:
		return "internal", http.StatusInternalServerError
	}
}
