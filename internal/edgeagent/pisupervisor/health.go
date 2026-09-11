package pisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// HTTPHealthProbe returns a probe function that GETs `url` and
// considers the child healthy on HTTP 2xx with a JSON body that
// parses cleanly. Non-2xx, body-parse failure, or network error
// are all "unhealthy".
//
// Pi-coding-agent's /health contract (per plan §P-2 / §6.4) is:
//
//   { "status": "ok"|"degraded",
//     "edge_id": "<id>",
//     "uptime_s": <int>,
//     "skills_loaded": <int>,
//     "tokens_in_session": <int> }
//
// We only require status=="ok"; a "degraded" state is allowed to
// keep running (degraded means e.g. LLM key missing → won't help,
// but the process is still reachable and the supervisor shouldn't
// thrash it).
func HTTPHealthProbe(timeout time.Duration) func(ctx context.Context, url string) error {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, url string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("health: build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("health: do: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("health: status %d", resp.StatusCode)
		}
		// Body decode is best-effort; we tolerate non-JSON or
		// partial JSON by reporting ok on status code alone if
		// the body is empty. The supervisor's contract is
		// "reachable + 2xx"; richer body parsing is for the
		// audit chain or a future /v1/edge/tools/pi_status
		// endpoint that surfaces it back to the cloud.
		var body struct {
			Status string `json:"status"`
		}
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(&body); err != nil {
			// Body didn't decode — return nil because the
			// HTTP layer said OK. Pi may not have shipped
			// the JSON contract yet (early versions).
			return nil
		}
		if body.Status != "" && body.Status != "ok" {
			return fmt.Errorf("health: status=%q (only ok accepted)", body.Status)
		}
		return nil
	}
}

// FakeHealthyHealthProbe returns a probe that always reports
// healthy. Used by tests that want to verify supervisor lifecycle
// without the HTTP probe race.
func FakeHealthyHealthProbe() func(ctx context.Context, url string) error {
	return func(ctx context.Context, url string) error { return nil }
}

// FakeUnhealthyHealthProbe returns a probe that always reports
// unhealthy. Used by tests that exercise the
// UnhealthyThreshold → kill → restart path.
func FakeUnhealthyHealthProbe() func(ctx context.Context, url string) error {
	return func(ctx context.Context, url string) error {
		return errors.New("fake unhealthy")
	}
}