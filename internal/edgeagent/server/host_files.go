package server

import (
	"encoding/json"
	"net/http"
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