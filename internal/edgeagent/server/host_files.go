package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/host_files"
)

// hostFilesCheckRequest is the wire shape for the cheap
// "is-this-path-allowed?" probe. Pi calls this BEFORE issuing a
// read so the LLM sees a clean yes/no instead of a 4 KiB error
// blob from a deeper tool.
type hostFilesCheckRequest struct {
	Path string `json:"path"`
}

type hostFilesCheckResponse struct {
	Path    string `json:"path"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// hostFilesReadRequest is the wire shape for the three read-side
// tools (find_large_files / du_summary / stat_file). All three take
// a single path per HTTP call — the tunnel protocol supports
// 1..16-path batches for the manager BaseTool, but Pi issuing one
// tool call at a time maps cleanly to a one-path HTTP envelope.
// To migrate to multi-path later: accept `paths []string` and emit
// `results []T`; the underlying RunFindOne / RunDuOne / RunStatOne
// already work per-path.
type hostFilesReadRequest struct {
	Path       string   `json:"path"`
	TopN       int      `json:"top_n,omitempty"`
	Depth      int      `json:"depth,omitempty"`
	MinBytes   int64    `json:"min_bytes,omitempty"`
	Exclude    []string `json:"exclude_paths,omitempty"`
}

// hostFilesReadResponse is the envelope common to all three read
// tools. The result is one of FindLargeFilesResultEntry /
// DuSummaryResultEntry / StatFileResultEntry depending on the
// endpoint. Error holds the per-path failure string (sandbox reject
// / find exited / file missing) so the LLM sees it inline.
type hostFilesReadResponse struct {
	Path       string          `json:"path"`
	Allowed    bool            `json:"allowed"`
	Reason     string          `json:"reason,omitempty"`
	AuditSeq   uint64          `json:"audit_seq,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	DurationMs int64           `json:"duration_ms"`
}

func (s *Server) handleHostFilesCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HostFiles == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "host_files_not_configured",
		})
		return
	}
	var req hostFilesCheckRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if err := s.HostFiles.ValidatePath(req.Path); err != nil {
		writeJSON(w, http.StatusOK, hostFilesCheckResponse{
			Path:    req.Path,
			Allowed: false,
			Reason:  err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, hostFilesCheckResponse{
		Path:    req.Path,
		Allowed: true,
	})
}

// requireHostFiles is the shared gate for the three read tools:
// refuses non-POST, refuses when the sandbox isn't wired, and decodes
// the JSON body with strict unknown-field rejection. Returns the
// decoded request or writes the appropriate error response and
// returns ok=false so the caller can early-return.
func (s *Server) requireHostFiles(w http.ResponseWriter, r *http.Request, body any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if s.HostFiles == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "host_files_not_configured",
		})
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// recordHostFilesRead is the shared audit hook. Every read tool
// emits exactly one event whether the runner succeeded or failed
// — auditors expect the trace to be unbroken even for sandbox
// rejects (the deny itself is the security signal). The kind
// prefix is "tool.call.host_files.*" so /v1/edge/audit?kind=...
// can pivot across all three read endpoints at once.
func (s *Server) recordHostFilesRead(w http.ResponseWriter, kind string, req hostFilesReadRequest, result any, runErr string, started time.Time) {
	resp := hostFilesReadResponse{
		Path:       req.Path,
		Allowed:    runErr == "",
		DurationMs: time.Since(started).Milliseconds(),
	}
	if runErr != "" {
		resp.Reason = runErr
	} else {
		if raw, err := json.Marshal(result); err == nil {
			resp.Result = raw
		}
	}
	if s.Audit != nil {
		payload, _ := json.Marshal(map[string]any{
			"path":        req.Path,
			"allowed":     resp.Allowed,
			"reason":      resp.Reason,
			"duration_ms": resp.DurationMs,
		})
		if ev, err := s.Audit.Append(kind, payload, ""); err == nil {
			resp.AuditSeq = ev.Sequence
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleHostFilesFindLargeFiles wraps host_files.RunFindOneSimple
// for the HTTP edge layer. Single-path per request. Audit kind:
// "tool.call.host_files.find_large_files".
func (s *Server) handleHostFilesFindLargeFiles(w http.ResponseWriter, r *http.Request) {
	var req hostFilesReadRequest
	if !s.requireHostFiles(w, r, &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	started := time.Now()
	findBin, err := s.HostFiles.ResolveBinary("find")
	if err != nil {
		s.recordHostFilesRead(w, "tool.call.host_files.find_large_files", req, nil,
			"binary not in allow-list: "+err.Error(), started)
		return
	}
	if req.TopN <= 0 {
		req.TopN = 20
	}
	if req.MinBytes <= 0 {
		req.MinBytes = 1 << 20 // 1 MiB default
	}
	entry := host_files.RunFindOneSimple(r.Context(), s.HostFiles, findBin, req.Path, req.TopN, req.MinBytes, req.Exclude)
	s.recordHostFilesRead(w, "tool.call.host_files.find_large_files", req, entry, entry.Error, started)
}

// handleHostFilesDuSummary wraps host_files.RunDuOne. Single-path.
// Audit kind: "tool.call.host_files.du_summary".
func (s *Server) handleHostFilesDuSummary(w http.ResponseWriter, r *http.Request) {
	var req hostFilesReadRequest
	if !s.requireHostFiles(w, r, &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	started := time.Now()
	duBin, err := s.HostFiles.ResolveBinary("du")
	if err != nil {
		s.recordHostFilesRead(w, "tool.call.host_files.du_summary", req, nil,
			"binary not in allow-list: "+err.Error(), started)
		return
	}
	if req.Depth <= 0 {
		req.Depth = 1
	}
	entry := host_files.RunDuOne(r.Context(), s.HostFiles, duBin, req.Path, req.Depth)
	s.recordHostFilesRead(w, "tool.call.host_files.du_summary", req, entry, entry.Error, started)
}

// handleHostFilesStatFile wraps host_files.RunStatOne. Single-path.
// Audit kind: "tool.call.host_files.stat_file". Pure Go — no
// subprocess, so no ResolveBinary call.
func (s *Server) handleHostFilesStatFile(w http.ResponseWriter, r *http.Request) {
	var req hostFilesReadRequest
	if !s.requireHostFiles(w, r, &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	started := time.Now()
	entry := host_files.RunStatOne(r.Context(), s.HostFiles, req.Path)
	s.recordHostFilesRead(w, "tool.call.host_files.stat_file", req, entry, entry.Error, started)
}
