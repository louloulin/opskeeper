package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	fleetbiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/fleet"
	devicemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/device"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tenantctx"
)

// Handler exposes the read-only fleet host view and the cluster-wide
// incident view.
type Handler struct {
	svc      HostService
	clusters ClusterService
}

type HostService interface {
	List(context.Context, fleetbiz.Filter) ([]fleetbiz.Host, int, error)
}

// ClusterService is the F-2 cluster-wide incident read model.
type ClusterService interface {
	List(context.Context, fleetbiz.ClusterFilter) ([]fleetbiz.ClusterIncident, int, error)
}

func NewHandler(s HostService) *Handler { return &Handler{svc: s} }

// SetClusterService wires the F-2 cluster incident read model. Kept as a
// setter because the alert store is constructed after the host repos in
// cmd/opskeeper, and an unwired service must answer 501 rather than 500.
func (h *Handler) SetClusterService(s ClusterService) { h.clusters = s }

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/fleet/hosts", h.listHosts)
	r.Get("/v1/fleet/cluster-incidents", h.listClusterIncidents)
}

type hostItem struct {
	Device hostDevice `json:"device"`
	Edges  []hostEdge `json:"edges"`
}

type hostDevice struct {
	ID         uint64     `json:"id"`
	Name       string     `json:"name"`
	Hostname   string     `json:"hostname,omitempty"`
	OS         string     `json:"os,omitempty"`
	OSVersion  string     `json:"os_version,omitempty"`
	Arch       string     `json:"arch,omitempty"`
	Online     bool       `json:"online"`
	Roles      []string   `json:"roles"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type hostEdge struct {
	ID           uint64     `json:"id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	AgentVersion string     `json:"agent_version,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
}

func (h *Handler) listHosts(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	f := fleetbiz.Filter{Status: strings.TrimSpace(q.Get("status")), Role: strings.TrimSpace(q.Get("role"))}
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeErr(w, fmt.Errorf("%w: invalid since", errs.ErrInvalid))
			return
		}
		f.Since = &t
	}
	f.Limit = parseNonNegative(q.Get("limit"))
	f.Offset = parseNonNegative(q.Get("offset"))
	items, total, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]hostItem, 0, len(items))
	for _, item := range items {
		if item.Device == nil {
			continue
		}
		d := item.Device
		row := hostItem{Device: hostDevice{ID: d.ID, Name: d.Name, Hostname: d.Hostname, OS: d.OS, OSVersion: d.OSVersion, Arch: d.Arch, Online: d.Online, Roles: devicemodel.DecodeRoles(d.Roles), LastSeenAt: d.LastSeenAt}}
		for _, edge := range item.Edges {
			if edge == nil {
				continue
			}
			row.Edges = append(row.Edges, hostEdge{ID: edge.ID, Name: edge.Name, Status: edge.Status, AgentVersion: edge.AgentVersion, LastSeenAt: edge.LastSeenAt})
		}
		if row.Edges == nil {
			row.Edges = []hostEdge{}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

// clusterIncidentItem is the cluster-wide incident wire shape. It carries the
// grouped facts an operator needs to decide whether a wave is one root cause:
// class, dimension, worst severity/status, the host rollup and the member
// incident ids. Per-host secrets never appear here — the DTO is built from
// allow-listed fields only.
type clusterIncidentItem struct {
	Key           string              `json:"key"`
	Rule          string              `json:"rule"`
	AnomalyClass  string              `json:"anomaly_class"`
	Signal        string              `json:"signal"`
	Severity      string              `json:"severity"`
	Status        string              `json:"status"`
	HostCount     int                 `json:"host_count"`
	IncidentCount int                 `json:"incident_count"`
	FirstFiredAt  time.Time           `json:"first_fired_at"`
	LastFiredAt   time.Time           `json:"last_fired_at"`
	Hosts         []clusterHostItem   `json:"hosts"`
	Members       []clusterMemberItem `json:"members"`
}

type clusterHostItem struct {
	DeviceID      uint64    `json:"device_id"`
	Name          string    `json:"name,omitempty"`
	Hostname      string    `json:"hostname,omitempty"`
	Online        bool      `json:"online"`
	IncidentCount int       `json:"incident_count"`
	Severity      string    `json:"severity"`
	LastFiredAt   time.Time `json:"last_fired_at"`
}

type clusterMemberItem struct {
	IncidentID   uint64    `json:"incident_id"`
	DeviceID     *uint64   `json:"device_id,omitempty"`
	Title        string    `json:"title,omitempty"`
	Severity     string    `json:"severity"`
	Status       string    `json:"status"`
	FirstFiredAt time.Time `json:"first_fired_at"`
	LastFiredAt  time.Time `json:"last_fired_at"`
}

func (h *Handler) listClusterIncidents(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	if h.clusters == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	q := r.URL.Query()
	f := fleetbiz.ClusterFilter{
		Status:   strings.TrimSpace(q.Get("status")),
		Severity: strings.TrimSpace(q.Get("severity")),
		MinHosts: parseNonNegative(q.Get("min_hosts")),
		Limit:    parseNonNegative(q.Get("limit")),
		Offset:   parseNonNegative(q.Get("offset")),
	}
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeErr(w, fmt.Errorf("%w: invalid since", errs.ErrInvalid))
			return
		}
		f.Since = &t
	}
	if raw := strings.TrimSpace(q.Get("window")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			writeErr(w, fmt.Errorf("%w: invalid window", errs.ErrInvalid))
			return
		}
		f.Window = d
	}
	items, total, err := h.clusters.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]clusterIncidentItem, 0, len(items))
	for _, item := range items {
		row := clusterIncidentItem{
			Key:           item.Key,
			Rule:          item.Rule,
			AnomalyClass:  item.AnomalyClass,
			Signal:        item.Signal,
			Severity:      item.Severity,
			Status:        item.Status,
			HostCount:     item.HostCount,
			IncidentCount: item.IncidentCount,
			FirstFiredAt:  item.FirstFiredAt,
			LastFiredAt:   item.LastFiredAt,
			Hosts:         make([]clusterHostItem, 0, len(item.Hosts)),
			Members:       make([]clusterMemberItem, 0, len(item.Members)),
		}
		for _, host := range item.Hosts {
			row.Hosts = append(row.Hosts, clusterHostItem{
				DeviceID:      host.DeviceID,
				Name:          host.Name,
				Hostname:      host.Hostname,
				Online:        host.Online,
				IncidentCount: host.IncidentCount,
				Severity:      host.Severity,
				LastFiredAt:   host.LastFiredAt,
			})
		}
		for _, member := range item.Members {
			row.Members = append(row.Members, clusterMemberItem{
				IncidentID:   member.IncidentID,
				DeviceID:     member.DeviceID,
				Title:        member.Title,
				Severity:     member.Severity,
				Status:       member.Status,
				FirstFiredAt: member.FirstFiredAt,
				LastFiredAt:  member.LastFiredAt,
			})
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

func parseNonNegative(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errs.HTTPStatus(err))
	_ = json.NewEncoder(w).Encode(errorBody{Error: err.Error(), Code: errCode(err)})
}
func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not-wired-yet"
	default:
		return "internal"
	}
}
