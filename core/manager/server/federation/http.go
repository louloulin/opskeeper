// Package federation exposes the root control plane's cluster-federation routes.
//
// Routes (all admin):
//
//	POST /v1/federation/clusters                  enrol a child, get its token once
//	GET  /v1/federation/clusters                  what is enrolled
//	GET  /v1/federation/clusters/{id}             one cluster's ledger state
//	POST /v1/federation/clusters/{id}/policy      sign a tree and issue a version
//	POST /v1/federation/clusters/{id}/policy/ack  record what a child answered
//	GET  /v1/federation/clusters/{id}/state       ask the child what it enforces
//
// Every one is admin, and that is not a formality. Enrolment mints a
// credential that lets a remote process act for a cluster, and a publish puts
// new code — including policy that widens what a cluster may do — onto hosts
// this root does not administer directly.
//
// The two reads answer different questions on purpose. GET .../{id} is the
// root's own ledger: what it published and what it last heard. GET .../{id}/
// state is the child's answer: what it is actually enforcing right now. They
// disagree exactly when a rollout is in flight, and an operator debugging a
// cluster needs both sides of that disagreement rather than the one the
// server happened to have.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vincent-wuhan/opskeeper/core/manager/pkg/errs"
	"github.com/vincent-wuhan/opskeeper/core/manager/pkg/tenantctx"

	floorfed "github.com/vincent-wuhan/opskeeper/core/floor/federation"
	"github.com/vincent-wuhan/opskeeper/core/floor/tunnel"
	fedbiz "github.com/vincent-wuhan/opskeeper/core/manager/biz/federation"
)

// roleAdmin mirrors iam/model.RoleAdmin without crossing the BC boundary,
// the same trade core/manager/server/edge makes.
const roleAdmin = "admin"

// Service is the narrow surface this handler needs.
type Service interface {
	Enroll(id floorfed.ClusterID, name string) (string, error)
	Members() []fedbiz.Member
	Member(id floorfed.ClusterID) (fedbiz.Member, bool)
	Publish(ctx context.Context, id floorfed.ClusterID, req fedbiz.PublishRequest) (fedbiz.PublishResult, error)
	Acknowledge(id floorfed.ClusterID, out floorfed.Outcome) error
}

// Pusher reaches one child cluster over the tunnel.
//
// It is optional and separate from Service on purpose. Membership and the
// publish ledger are things this root owns and can answer with or without a
// tunnel; asking a child what it is enforcing is a round trip, and a root
// whose tunnel is down must still be able to say who it has enrolled and what
// it has sent them.
type Pusher interface {
	// PushPolicy delivers one decision and returns the child's verdict.
	PushPolicy(ctx context.Context, id floorfed.ClusterID, req tunnel.ClusterPolicyRequest) (tunnel.ClusterPolicyResponse, error)
	// AskState asks the child what it is enforcing. It is the only call
	// whose answer is authoritative about the child rather than about this
	// root.
	AskState(ctx context.Context, id floorfed.ClusterID) (tunnel.ClusterStateResponse, error)
}

// ErrChildUnreachable means the tunnel to that child did not answer.
var ErrChildUnreachable = errors.New("federation: the child cluster did not answer")

// Handler serves the federation routes.
type Handler struct {
	svc Service
	// push is optional; a nil push still mounts the routes, which answer
	// 503 rather than 404. "This root cannot reach its children" and
	// "this route does not exist" send an operator to completely different
	// pages.
	push Pusher
}

// NewHandler builds the handler. A nil service is tolerated so wiring can
// mount the routes before the service exists; every endpoint answers 503
// until it is set.
func NewHandler(svc Service) *Handler { return &Handler{svc: svc} }

// SetService back-fills the service post-construction.
func (h *Handler) SetService(svc Service) { h.svc = svc }

// SetPusher back-fills the tunnel-side pusher.
func (h *Handler) SetPusher(p Pusher) { h.push = p }

// Register mounts the federation routes.
func (h *Handler) Register(r chi.Router) {
	r.Route("/v1/federation", func(r chi.Router) {
		r.With(h.requireAdmin).Post("/clusters", h.enroll)
		r.With(h.requireAdmin).Get("/clusters", h.list)
		r.With(h.requireAdmin).Get("/clusters/{id}", h.get)
		r.With(h.requireAdmin).Post("/clusters/{id}/policy", h.publish)
		r.With(h.requireAdmin).Post("/clusters/{id}/policy/ack", h.ack)
		r.With(h.requireAdmin).Get("/clusters/{id}/state", h.state)
	})
}

// enrollResp is the one and only time a provisioning token is on the wire.
//
// It is a separate type from the member view rather than a field on it,
// because the member view is readable many times and this is not. A console
// that rendered the member list could not tell whether a token field was
// empty because there is none to give or because the operator already took it.
type enrollResp struct {
	Cluster floorfed.Cluster `json:"cluster"`
	// ProvisioningToken is shown once. Losing it means re-enrolling the
	// cluster, which rotates the credential and stops the old child from
	// being able to say anything. That is the intended cost: a "resend
	// me the token" affordance is a "show me the credential" affordance
	// with extra steps.
	ProvisioningToken string `json:"provisioning_token"`
}

func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	id, err := floorfed.NewClusterID(body.ID)
	if err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	token, err := h.svc.Enroll(id, strings.TrimSpace(body.Name))
	if err != nil {
		writeErr(w, err)
		return
	}
	// The identity is echoed back from the registry rather than from the
	// request, so what the operator is shown is what was stored.
	member, _ := h.svc.Member(id)
	writeJSON(w, http.StatusCreated, enrollResp{Cluster: member.Cluster, ProvisioningToken: token})
}

// clusterView is the console's view of one cluster.
//
// TokenHash is deliberately absent and so is any field derived from it: a
// read endpoint is the one place a credential hash has no business being.
type clusterView struct {
	Cluster       floorfed.Cluster `json:"cluster"`
	HighestIssued uint64           `json:"highest_issued"`
	Acknowledged  uint64           `json:"acknowledged"`
	// Behind is true when the cluster is not enforcing what this root last
	// published — including when it answered and declined, which is a
	// different situation from having not answered at all and needs to
	// read differently on a console.
	Behind     bool             `json:"behind"`
	LastAck    floorfed.Outcome `json:"last_ack,omitempty"`
	EnrolledAt string           `json:"enrolled_at,omitempty"`
}

func viewOf(m fedbiz.Member) clusterView {
	v := clusterView{
		Cluster:       m.Cluster,
		HighestIssued: m.HighestIssued,
		Acknowledged:  m.Acknowledged,
		Behind:        m.Behind(),
		LastAck:       m.LastAck,
	}
	if !m.Cluster.JoinedAt.IsZero() {
		v.EnrolledAt = m.Cluster.JoinedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return v
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	members := h.svc.Members()
	// A list is never null on the wire. "No clusters enrolled" and "the
	// response failed to parse" must not look the same to a console that
	// is deciding whether to render an empty table or an error.
	views := make([]clusterView, 0, len(members))
	for _, m := range members {
		views = append(views, viewOf(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": views})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	m, ok := h.svc.Member(clusterIDFrom(r))
	if !ok {
		writeErr(w, errNoSuchCluster)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(m))
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	var body fedbiz.PublishRequest
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if strings.TrimSpace(body.StagedRoot) == "" {
		writeErr(w, errors.Join(errs.ErrInvalid, errors.New("staged_root is required")))
		return
	}
	id, err := floorfed.NewClusterID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	res, err := h.svc.Publish(r.Context(), id, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bundle":         res.Bundle,
		"highest_issued": res.Member.HighestIssued,
	})
}

// ackResp records what a child answered.
//
// It is a separate endpoint from publish because the two are separate events
// on a link that is at-least-once and can drop: the decision goes out, the
// answer comes back later or not at all, and a rollout that cannot record its
// own outcome is a rollout whose console shows green forever.
func (h *Handler) ack(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	var body struct {
		Outcome floorfed.Outcome `json:"outcome"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if body.Outcome.Version == 0 {
		writeErr(w, errors.Join(errs.ErrInvalid, errors.New("outcome.version is required")))
		return
	}
	id, err := floorfed.NewClusterID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if err := h.svc.Acknowledge(id, body.Outcome); err != nil {
		writeErr(w, err)
		return
	}
	m, _ := h.svc.Member(id)
	writeJSON(w, http.StatusOK, viewOf(m))
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeErr(w, errNotWired)
		return
	}
	if h.push == nil {
		writeErr(w, errNotWired)
		return
	}
	id, err := floorfed.NewClusterID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	st, err := h.push.AskState(r.Context(), id)
	if err != nil {
		// A child that did not answer is reported as unreachable, not as
		// an internal error and not as "enforcing nothing". It may well be
		// enforcing something perfectly well, and the difference between
		// those two is the whole reason this endpoint asks the child.
		writeErr(w, fmt.Errorf("%w: %v", ErrChildUnreachable, err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

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

// clusterIDFrom reads the id in the path. An unparseable one yields the zero
// value, and every caller then fails the registry lookup and answers 404 —
// which is the right answer for a path that is not a cluster identity, and
// does not leak whether a differently-spelled id would have existed.
func clusterIDFrom(r *http.Request) floorfed.ClusterID {
	id, _ := floorfed.NewClusterID(chi.URLParam(r, "id"))
	return id
}

var (
	errNotWired      = notWiredError("cluster federation is not wired on this manager")
	errNoSuchCluster = notFoundError("no such child cluster")
)

type notWiredError string

func (e notWiredError) Error() string { return string(e) }

type notFoundError string

func (e notFoundError) Error() string { return string(e) }

func decodeBody(r *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	return nil
}

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
	case errors.Is(err, errNoSuchCluster), errors.Is(err, fedbiz.ErrNotEnrolled):
		return "no_such_cluster", http.StatusNotFound
	case errors.Is(err, fedbiz.ErrNothingToPublish):
		return "nothing_to_publish", http.StatusBadRequest
	case errors.Is(err, fedbiz.ErrUnknownVersion):
		// A conflict, not a 500: the child and this root disagree about
		// the numbering, and that is a fact about the world rather than a
		// fault in the server. It is also the one an operator most needs
		// to be told plainly, because the alternative reading is "try
		// again".
		return "unknown_version", http.StatusConflict
	case errors.Is(err, ErrChildUnreachable):
		return "child_unreachable", http.StatusBadGateway
	case errors.Is(err, errNotWired):
		return "not_wired", http.StatusServiceUnavailable
	default:
		return "internal", http.StatusInternalServerError
	}
}
