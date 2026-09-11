package server

import (
	"net/http"
)

// handleHealth is the readiness probe. Always 200 unless Bash is
// missing (cmdpolicy not configured → bash would refuse every
// call, so reporting unhealthy is honest).
//
// The response is intentionally compact — kubelet / edge-loop
// consumers don't need a JSON document; "ok" is enough. Operators
// can curl /health for a quick sanity check.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Audit == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "audit_unconfigured",
			"reason": "audit chain not wired — refusing to serve until edge is fully initialised",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"edge_id":  s.EdgeID,
		"version":  s.Version,
		"audit_len": itoa(s.Audit.Len()),
	})
}

// itoa is a tiny helper to avoid pulling strconv just for one call.
// Hand-rolled because we only need base-10 positive ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}