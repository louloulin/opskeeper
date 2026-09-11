package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/audit"
)

// restartRequest is the wire shape for restart_service. The
// package's existing tunnel-based handler does the unit allow-list
// check (defence-in-depth: cloud says ok, edge still re-checks);
// this handler does the same plus the approval-token gate.
type restartRequest struct {
	Service    string `json:"service"`     // short unit name, no ".service" suffix
	ProposalID string `json:"proposal_id"` // required
}

type restartResponse struct {
	Allowed  bool   `json:"allowed"`
	Reason   string `json:"reason,omitempty"`
	Mocked   bool   `json:"mocked"`
	AuditSeq uint64 `json:"audit_seq,omitempty"`
	Service  string `json:"service"`
}

// handleRestart validates the unit against the allow-list, then
// consumes an approval token (always required — restart is
// inherently a write), then returns success or denial.
//
// The actual systemctl shell-out is delegated to the underlying
// restart_service package; in this v1.7 skeleton we just call the
// package's exported handler-shape helper. When Mocked=true the
// response is returned without shelling out (CI-friendly).
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Restart == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "restart_service_not_configured",
		})
		return
	}
	var req restartRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Service == "" {
		http.Error(w, "service required", http.StatusBadRequest)
		return
	}
	if req.ProposalID == "" {
		http.Error(w, "proposal_id required", http.StatusBadRequest)
		return
	}

	// Defence-in-depth: the cloud-side BaseTool already checked the
	// unit against its own allow-list. We re-check against edge's
	// list so a compromised / replayed wire body can't bypass it.
	cleanService := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Service)), ".service")
	allowed := false
	for _, u := range s.Restart.AllowedUnits {
		if strings.EqualFold(u, cleanService) {
			allowed = true
			break
		}
	}
	if !allowed {
		s.auditAppend(r, "restart.deny", map[string]any{
			"service":     cleanService,
			"reason":      "unit not in edge allow-list",
			"proposal_id": req.ProposalID,
		}, req.ProposalID)
		writeJSON(w, http.StatusForbidden, restartResponse{
			Allowed: false,
			Reason:  "service not in edge allow-list",
			Service: cleanService,
		})
		return
	}

	// Approval token — restart is always a write.
	if _, err := s.approvalFromHeader(r, "restart_service", req.ProposalID); err != nil {
		s.auditAppend(r, "restart.deny", map[string]any{
			"service":     cleanService,
			"reason":      err.Error(),
			"proposal_id": req.ProposalID,
		}, req.ProposalID)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Hand off to the package. Real systemctl shell-out is not yet
	// implemented (restart_service.SandboxConfig.Mocked gates this);
	// when Mocked=true we short-circuit with a clean success, which
	// is the correct posture for the dev-box CI loop.
	if s.Restart.Mocked {
		auditSeq := s.appendAudit(audit.Event{}, "restart.mock", map[string]any{
			"service":     cleanService,
			"mocked":      true,
			"proposal_id": req.ProposalID,
		}, req.ProposalID)
		writeJSON(w, http.StatusOK, restartResponse{
			Allowed:  true,
			Mocked:   true,
			AuditSeq: auditSeq,
			Service:  cleanService,
		})
		return
	}

	// Real shell-out path: not yet implemented. The audit entry
	// marks the attempt so we know the gate fired; the response
	// tells the caller to retry later once real systemctl lands.
	auditSeq := s.appendAudit(audit.Event{}, "restart.attempt", map[string]any{
		"service":     cleanService,
		"mocked":      false,
		"proposal_id": req.ProposalID,
	}, req.ProposalID)
	s.log().Warn("restart_service real shell-out not implemented yet",
		slog.String("service", cleanService),
		slog.String("proposal_id", req.ProposalID))
	writeJSON(w, http.StatusNotImplemented, restartResponse{
		Allowed:  false,
		Reason:   "real systemctl shell-out not yet implemented; configure Mocked=true on edge or wait for follow-up",
		Mocked:   false,
		AuditSeq: auditSeq,
		Service:  cleanService,
	})
}

// appendAudit is a small wrapper that turns a typed body into the
// JSON bytes audit.Chain.Append expects, then returns the resulting
// sequence number. Returns 0 if audit is unconfigured (callers
// accept that silently).
func (s *Server) appendAudit(_ audit.Event, kind string, body any, proposalID string) uint64 {
	if s.Audit == nil {
		return 0
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return 0
	}
	e, err := s.Audit.Append(kind, payload, proposalID)
	if err != nil {
		s.log().Warn("audit append failed",
			slog.String("kind", kind),
			slog.Any("err", err))
		return 0
	}
	return e.Sequence
}