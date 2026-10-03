package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/aiops/tools/toolcore"
	"time"
)

// ToolNameQueryPromQL is the stable wire name the LLM sees for the PromQL tool.
const ToolNameQueryPromQL = "query_promql"

// QueryPromQLDescription is the single-sentence description shown to the LLM.
// Phrased to push the model toward this tool whenever the host-load /
// process-list tools are too narrow.
const QueryPromQLDescription = "Run a PromQL range query against the cluster's Prometheus. " +
	"Use this when you need any host or container metric beyond the few host-level fields the basic tools return. " +
	"For fleet or multi-device questions, write one vectorized PromQL expression with by(device_id, ...) / regex selectors / topk instead of one query per device or metric. " +
	"Returns the raw Prom HTTP API response."

// QueryPromQLSchema is the JSON Schema of the tool's argument object.
var QueryPromQLSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "expr": {
      "type": "string",
      "description": "PromQL expression. Prefer one vectorized expression for multiple devices/labels. Example: \"avg by (device_id) (rate(node_cpu_seconds_total{mode!=\\\"idle\\\"}[5m]))\". For filesystem percent, combine numerator/denominator in one expression instead of separate used/size queries."
    },
    "lookback_seconds": {
      "type": "integer",
      "minimum": 60,
      "maximum": 604800,
      "description": "How far back to query in seconds (default 300 = 5 minutes; max 604800 = 7d). Use one 7d range query for weekly trends instead of repeating short lookbacks."
    }
  },
  "required": ["expr"]
}`)

// QueryPromQLArgs is the typed form of QueryPromQLSchema.
type QueryPromQLArgs struct {
	Expr            string `json:"expr"`
	LookbackSeconds int    `json:"lookback_seconds,omitempty"`
}

// queryPromqlCallTimeout caps how long a single dispatch may wait. Same
// rationale as the other tool timeouts.
const queryPromqlCallTimeout = 30 * time.Second

// executeQueryPromQL runs the PromQL range query and hands the raw Prom
// response back to the LLM via ResultJSON. EdgeID is intentionally left
// nil — query_promql is not bound to a specific edge.
func (r *Registry) executeQueryPromQL(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.promQuery == nil {
		// Should not happen — when promQuery is nil at NewRegistry the
		// tool is never registered. Defensive guard.
		return ExecuteResult{}, fmt.Errorf("query_promql: prom query client not configured")
	}
	var in QueryPromQLArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("query_promql: bad args: %w", err)
	}
	if in.Expr == "" {
		return ExecuteResult{}, fmt.Errorf("query_promql: expr required")
	}
	if in.LookbackSeconds <= 0 {
		in.LookbackSeconds = 300 // 5 min
	}
	if in.LookbackSeconds > toolcore.MaxQueryPromQLLookbackSeconds {
		in.LookbackSeconds = toolcore.MaxQueryPromQLLookbackSeconds
	}

	end := time.Now()
	start := end.Add(-time.Duration(in.LookbackSeconds) * time.Second)
	step := toolcore.StepFor(in.LookbackSeconds)

	callCtx, cancel := context.WithTimeout(ctx, queryPromqlCallTimeout)
	defer cancel()

	res, err := r.promQuery.QueryRange(callCtx, in.Expr, start, end, step)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_promql: dispatch: %w", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_promql: marshal response: %w", err)
	}
	return ExecuteResult{ResultJSON: out}, nil
}
