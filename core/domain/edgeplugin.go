package domain

import "time"

// This file exists for the same reason edge.go does, and it is the second
// measurement of the same kind: the tunnel-facing handlers in
// service/frontierbound called seven methods on the edge domain, and every one
// of the three types that crossed the boundary already belonged to a different
// layer than the seam did.
//
//   - PluginHealth / PluginTargetHealth were edge's own health rows, built by
//     the handler and stored by the edge domain. The handler could not name
//     them without importing biz/edge, and the edge domain could not receive
//     them without that same import coming back the other way.
//   - WireSnapshot was a second copy of the plugin-config wire shape, living
//     next to core/floor/tunnel's own. The handler read three fields off it and
//     immediately re-projected them into the tunnel type, so the copy existed
//     only to be converted away.
//   - ChangeEventRow is the worst of the three, and the reason this file is not
//     just two structs: it is a GORM model. The tunnel handler was building
//     persistence rows column by column, including the JSON-encoding of the
//     labels map and the "seq 0 means NULL, not zero" rule — a rule that exists
//     because a unique index sits on (edge_id, seq) and SQL treats NULL as
//     distinct from NULL. That rule is a property of the edge domain's schema,
//     and the handler was the one implementing it. ChangeEventInput is the
//     shape the handler can honestly build; the row, the encoding and the
//     nullability stay where the index that needs them lives.

// PluginHealth is the last health snapshot one edge reported for one plugin.
type PluginHealth struct {
	Name         string               `json:"name"`
	State        string               `json:"state"` // stopped|starting|running|crashed
	LastError    string               `json:"last_error,omitempty"`
	RestartCount int                  `json:"restart_count,omitempty"`
	PID          int                  `json:"pid,omitempty"`
	StartedAt    time.Time            `json:"started_at,omitempty"`
	UpdatedAt    time.Time            `json:"updated_at,omitempty"`  // edge-side update time
	ReportedAt   time.Time            `json:"reported_at,omitempty"` // manager receive time
	Targets      []PluginTargetHealth `json:"targets,omitempty"`
}

// PluginTargetHealth is a per-source health row for metric sub-plugins.
type PluginTargetHealth struct {
	ID            string    `json:"id"`
	Name          string    `json:"name,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	State         string    `json:"state"`
	LastError     string    `json:"last_error,omitempty"`
	Samples       int       `json:"samples,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// PluginConfigSnapshot is one edge's plugin configuration, as the edge sees it.
// It is the same shape core/floor/tunnel puts on the wire, and it is here
// because the edge domain produced it before the wire type was written down.
type PluginConfigSnapshot struct {
	EdgeID  uint64                  `json:"edge_id"`
	Configs map[string]PluginConfig `json:"configs"`
}

// PluginConfig is one plugin's config as the edge sees it.
type PluginConfig struct {
	Enabled  bool                   `json:"enabled"`
	Endpoint string                 `json:"endpoint,omitempty"`
	Spec     map[string]interface{} `json:"spec,omitempty"`
}

// ChangeEventInput is one journald / dockerd / packagemgr event as the tunnel
// handler can state it: labels still a map, seq still absent rather than zero.
//
// The two differences from the stored row are the whole point. Labels is a map
// because JSON-encoding a map is the edge domain's business, not the
// transport's; Seq is a pointer because "this node never logged it" has to stay
// distinguishable from "it logged it as 0", and only the owner of the
// (edge_id, seq) unique index knows that the two must not collide.
type ChangeEventInput struct {
	EdgeID    uint64
	Seq       *uint64
	Source    string
	Kind      string
	Subject   string
	Action    string
	Timestamp time.Time
	Severity  string
	Labels    map[string]string
}
