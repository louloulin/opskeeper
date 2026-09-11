package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/audit"
)

// handleAudit serves GET /v1/edge/audit?since=<seq>&kind=<kind>.
// The local chain is the source of truth (cloud replays this
// snapshot via tunnel.v1.pi_audit); queries here are for the
// opskeeper-edge loop, the web UI, and on-host debug curls.
//
// Filtering:
//   - since=N (default 0): return events with Sequence > N.
//   - kind=K (default ""): return events whose Kind == K.
//   - limit=M (default 1000, max 10000): cap the response size.
//
// All filters are best-effort and the chain is always returned in
// Sequence order. Pagination is via opaque since cursor; clients
// shouldn't assume the cursor is reusable across restarts (the
// chain restarts with seq=1 unless restored from disk).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Audit == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "audit_unconfigured",
		})
		return
	}
	q := r.URL.Query()
	since, _ := strconv.ParseUint(q.Get("since"), 10, 64)
	kindFilter := q.Get("kind")
	limit := 1000
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 10000 {
		limit = 10000
	}

	all := s.Audit.Snapshot()
	out := make([]audit.Event, 0, limit)
	for _, e := range all {
		if e.Sequence <= since {
			continue
		}
		if kindFilter != "" && e.Kind != kindFilter {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	resp := auditListResponse{
		EdgeID:   s.EdgeID,
		Count:    len(out),
		Events:   out,
		NextPage: nextSince(out),
	}
	writeJSON(w, http.StatusOK, resp)
}

type auditListResponse struct {
	EdgeID   string        `json:"edge_id"`
	Count    int           `json:"count"`
	Events   []audit.Event `json:"events"`
	NextPage uint64        `json:"next_page,omitempty"`
}

func nextSince(events []audit.Event) uint64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Sequence
}

// auditAppend is the internal helper every mutating handler calls
// before returning. Pulled out so the bash / restart / propose
// handlers share the same JSON-encode-then-Append discipline; a
// payload that doesn't JSON-encode is a programming bug and we
// surface it via 500 rather than silently swallowing it.
func (s *Server) auditAppend(r *http.Request, kind string, body any, proposalID string) error {
	if s.Audit == nil {
		return nil // audit is best-effort here; handler decides if it cares
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = s.Audit.Append(kind, payload, proposalID)
	return err
}