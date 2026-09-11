package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/cmdpolicy"
)

// proposeRequest is the wire shape posted by the Pi sidecar when it
// wants reviewer-worker approval for a mutating action. The
// proposal is RECORDED into the audit chain here; the actual
// approval grant happens on the cloud side via
// tunnel.v1.pi_propose_action (P-5). When P-5 is wired, this
// handler will additionally POST to the cloud over the existing
// geminio connection; until then, the audit entry is the
// durable proof a proposal was made, and the cloud discovers
// it on the next sync.
type proposeRequest struct {
	Kind       string          `json:"kind"`        // e.g. "restart_service", "write_file"
	Rationale  string          `json:"rationale"`
	Evidence   json.RawMessage `json:"evidence,omitempty"`
	Rollback   string          `json:"rollback,omitempty"`
	ProposalID string          `json:"proposal_id"` // caller-minted UUID
}

type proposeResponse struct {
	Accepted   bool   `json:"accepted"`
	ProposalID string `json:"proposal_id"`
	Note       string `json:"note,omitempty"`
}

// handlePropose records a proposal into the local audit chain. It
// is intentionally not gated on the approval token — the proposal
// is the input to the approval flow, so it must arrive without one.
// Authentication of the caller is implicit in loopback binding.
func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Audit == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "audit_unconfigured",
		})
		return
	}
	var req proposeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProposalID == "" {
		http.Error(w, "proposal_id required", http.StatusBadRequest)
		return
	}
	if req.Kind == "" {
		http.Error(w, "kind required", http.StatusBadRequest)
		return
	}
	if req.Rationale == "" {
		http.Error(w, "rationale required (Pi MUST justify every proposal)", http.StatusBadRequest)
		return
	}
	if err := s.auditAppend(r, "propose", req, req.ProposalID); err != nil {
		s.log().Error("audit append failed for propose",
			slog.String("proposal_id", req.ProposalID),
			slog.Any("err", err))
		http.Error(w, "audit append failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, proposeResponse{
		Accepted:   true,
		ProposalID: req.ProposalID,
		Note:       "proposal recorded; awaiting reviewer grant via tunnel.v1.pi_propose_action (P-5)",
	})
}

// approvalFromHeader is shared by the bash + restart handlers.
// Returns the bearer token, the proposal_id from the body (caller
// already parsed it), and an error if the cache is unconfigured
// (fail-closed: every mutating call requires the token to be
// checked somewhere, and if no cache is wired we refuse).
func (s *Server) approvalFromHeader(r *http.Request, kind, proposalID string) (string, error) {
	if s.Approvals == nil {
		return "", errors.New("approval cache not configured; mutating actions refused (fail-closed)")
	}
	token := r.Header.Get("X-Opskeeper-Pi-Approval-Token")
	if token == "" {
		return "", errTokenMissing
	}
	if proposalID == "" {
		return "", errors.New("proposal_id required for mutating actions")
	}
	if _, err := s.Approvals.Consume(token, proposalID, kind, s.EdgeID); err != nil {
		return "", err
	}
	return token, nil
}

// errTokenMissing is exported so handler tests can assert against
// it without string matching.
var errTokenMissing = errors.New("X-Opskeeper-Pi-Approval-Token header required for mutating actions")

// Compile-time guard: keeps the import live even if the body of
// approvalFromHeader ever shrinks.
var _ = cmdpolicy.ApprovalCache{}