package toolcore

import (
	"context"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/manager/pkg/promquery"
)

// PromQuerier is how a tool asks Prometheus something.
//
// It is here rather than in query_promql.go because twenty-one files ask
// Prometheus questions and only one of them is the query_promql tool:
// rank_edges, find_outlier_edges, correlate_incident, the metric catalog,
// the database analyzer, chat_to_query and their tests all hold one. When
// the interface lived next to the tool that happens to be called
// query_promql, every one of those files was reaching sideways into
// another cluster's file to name a type, and none of them could move
// without dragging the whole package along.
//
// The concrete *promquery.Client satisfies it unchanged; the interface
// exists so a test can inject a fake without a live Prometheus.
type PromQuerier interface {
	// QueryRange asks for a range of points at the given resolution.
	QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) (*promquery.InstantResult, error)
	// Query is the instant form. correlate_incident uses it to grab a
	// single point-in-time vector for cpu_pct / mem_pct / up.
	Query(ctx context.Context, expr string, ts time.Time) (*promquery.InstantResult, error)
}
