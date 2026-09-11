package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/cmdpolicy"
)

// bashRequest is the wire shape posted by the Pi sidecar (and any
// other on-host caller) for shell execution.
//
// Mode "read" is the default and uses the cmdpolicy sandbox
// without an approval check — every read-only call is allowed if
// the underlying policy says yes.
//
// Mode "write" REQUIRES:
//   - X-Opskeeper-Pi-Approval-Token header (single-use, 60 s TTL)
//   - proposal_id in the body matching the token's binding
//
// Approval consumes the token (CacheApprovalChecker flow); retry
// after rejection needs a fresh proposal.
type bashRequest struct {
	Cmd        string `json:"cmd"`
	Mode       string `json:"mode,omitempty"`        // "read" (default) or "write"
	ProposalID string `json:"proposal_id,omitempty"` // required when mode=write
}

// bashResponse mirrors cmdpolicy.ShellResult with the wire-facing
// allowed/reason split. We do not re-export cmdpolicy's internal
// type so the HTTP contract is decoupled from the in-Go struct —
// a future wire-format change (e.g. cap stats, wall-time vs CPU
// time) can land without touching cmdpolicy.
type bashResponse struct {
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	// AuditSeq is the sequence number of the audit event this call
	// recorded. Empty when audit isn't wired. Callers can use it to
	// cross-reference /v1/edge/audit.
	AuditSeq uint64 `json:"audit_seq,omitempty"`
}

func (s *Server) handleBash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Bash == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "bash_sandbox_not_configured",
		})
		return
	}
	var req bashRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Cmd == "" {
		http.Error(w, "cmd required", http.StatusBadRequest)
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "read"
	}
	if mode != "read" && mode != "write" {
		http.Error(w, "mode must be \"read\" or \"write\"", http.StatusBadRequest)
		return
	}

	// Mode=write → consume an approval token BEFORE running. We do
	// this even if the policy itself would reject the command; the
	// proposal was made, the audit trail is the artifact, and a
	// burn-the-token behaviour signals to the cloud that the
	// proposal was acted on.
	if mode == "write" {
		kind := "bash_write"
		if _, err := s.approvalFromHeader(r, kind, req.ProposalID); err != nil {
			s.auditAppend(r, "bash.rejected", map[string]any{
				"cmd":         req.Cmd,
				"mode":        mode,
				"proposal_id": req.ProposalID,
				"reason":      err.Error(),
			}, req.ProposalID)
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	}

	// Run via the cmdpolicy sandbox. We honour the policy's verdict
	// regardless of mode — a write-mode call against a read-only
	// class will still be rejected by the policy, and the audit
	// event will reflect both the policy rejection AND the consumed
	// approval (caller's choice to do so is recorded for forensics).
	result, err := s.Bash.Exec(r.Context(), req.Cmd)
	if err != nil {
		// OS-level failure (sandbox uninitialised etc.) — distinct
		// from a policy rejection.
		s.log().Error("bash exec internal error",
			slog.String("cmd", req.Cmd),
			slog.Any("err", err))
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resp := bashResponse{
		Allowed:    result.Allowed,
		Reason:     result.Reason,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		ExitCode:   result.ExitCode,
		DurationMs: result.DurationMs,
		Truncated:  result.Truncated,
	}

	// Audit. We record EVERY call — read or write, allowed or
	// rejected — because the audit chain's value comes from
	// completeness, not from selectivity. The Kind field
	// distinguishes them ("tool.call.bash" vs "tool.deny.bash").
	auditKind := "tool.call.bash"
	if !result.Allowed {
		auditKind = "tool.deny.bash"
	}
	if s.Audit != nil {
		e, err := s.Audit.Append(auditKind, mustJSON(map[string]any{
			"cmd":         req.Cmd,
			"mode":        mode,
			"allowed":     result.Allowed,
			"reason":      result.Reason,
			"exit_code":   result.ExitCode,
			"proposal_id": req.ProposalID,
		}), req.ProposalID)
		if err == nil {
			resp.AuditSeq = e.Sequence
		} else {
			s.log().Warn("audit append failed for bash call",
				slog.Any("err", err))
		}
	}

	status := http.StatusOK
	if !result.Allowed {
		status = http.StatusForbidden
	}
	writeJSON(w, status, resp)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Map types with no cycles should never fail to marshal;
		// this is a programming bug if it does.
		return []byte(`{"_marshal_error":"` + err.Error() + `"}`)
	}
	return b
}

// Compile-time guard: the handler reads cmdpolicy via Sandbox.Exec,
// but if that method ever moves we'll catch it here.
var _ = (*cmdpolicy.Sandbox)(nil).Exec