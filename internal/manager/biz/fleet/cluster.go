package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	alertbiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/alert"
	devicebiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/device"
	alertmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/alert"
	devicemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/device"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

// Cluster-wide incident aggregation — plan1.1 Phase F round F-2.
//
// The alert pipeline already deduplicates per host: one (scope, device, rule)
// triple maps to exactly one alert_incidents row, keyed by dedupe_key. That
// keeps a single host's flap from spamming the board, but it also means a
// fault that hits N hosts (a bad deploy, a shared storage backend, a network
// partition) shows up as N unrelated incidents, each with its own RCA.
//
// F-2 adds the missing layer: a deterministic, LLM-free read model that folds
// per-host incidents into ONE cluster-wide incident when they share
//
//	(1) the same anomaly class        — derived from the rule key,
//	(2) the same signal dimension     — host-identity labels stripped,
//	(3) a bounded first-firing window — sliding, default 10 minutes,
//
// and touch at least MinHosts distinct hosts. A second wave outside the
// window becomes a separate cluster incident, so the view never merges an
// unrelated recurrence into a stale group.
//
// Determinism matters: the same incident rows always produce the same groups
// in the same order, so the API is safe to cache and the tests can assert
// exact output. Ranking a group's "same root cause" beyond this stays with
// the LLM semantic-dedup layer in internal/manager/biz/alert, which this
// read model consumes rather than duplicates.
const (
	// DefaultClusterWindow is the sliding first-firing window used to decide
	// whether two per-host incidents belong to the same wave.
	DefaultClusterWindow = 10 * time.Minute
	// DefaultClusterMinHosts keeps single-host incidents out of the
	// cluster-wide view; a group needs at least this many distinct hosts.
	DefaultClusterMinHosts = 2
	// MaxClusterWindow bounds the caller-supplied window so a pathological
	// query cannot fold an entire incident history into one group.
	MaxClusterWindow = 24 * time.Hour
	// MaxClusterMinHosts bounds the caller-supplied host floor.
	MaxClusterMinHosts = 100
	// maxClusterScan bounds how many alert_incidents rows one request reads.
	// The store returns newest-first by last_fired_at, so the cap trims the
	// oldest history rather than the active wave.
	maxClusterScan = 2000
)

// ClusterFilter is the biz-layer query for the cluster-wide incident view.
// Zero values fall back to the defaults above; Limit/Offset page the grouped
// result, not the underlying incident rows.
type ClusterFilter struct {
	Status   string
	Severity string
	Since    *time.Time
	Window   time.Duration
	MinHosts int
	Limit    int
	Offset   int
}

// ClusterMember is one per-host incident that folded into a cluster incident.
type ClusterMember struct {
	IncidentID   uint64
	DeviceID     *uint64
	Title        string
	Severity     string
	Status       string
	FirstFiredAt time.Time
	LastFiredAt  time.Time
	Value        *float64
	Threshold    *float64
}

// ClusterHost is the per-host rollup of a cluster incident: the host identity
// plus how many of its incidents landed in this group.
type ClusterHost struct {
	DeviceID      uint64
	Name          string
	Hostname      string
	Online        bool
	IncidentCount int
	Severity      string
	LastFiredAt   time.Time
}

// ClusterIncident is one cross-host wave.
type ClusterIncident struct {
	// Key is the stable grouping identity (anomaly class + signal dimension)
	// suffixed with the wave start, so two waves of the same class in the
	// same response are distinguishable.
	Key           string
	Rule          string
	AnomalyClass  string
	Signal        string
	Severity      string
	Status        string
	HostCount     int
	IncidentCount int
	FirstFiredAt  time.Time
	LastFiredAt   time.Time
	Hosts         []ClusterHost
	Members       []ClusterMember
}

// IncidentRepo is the read-only slice of the alert sub-domain this usecase
// needs. Declared locally so the fleet read model depends on a contract, not
// on the alert usecase.
type IncidentRepo interface {
	ListIncidents(context.Context, alertbiz.IncidentFilter) ([]*alertmodel.Incident, error)
}

// ClusterUsecase folds per-host incidents into cluster-wide incidents.
type ClusterUsecase struct {
	incidents IncidentRepo
	devices   DeviceRepo
}

func NewClusterUsecase(incidents IncidentRepo, devices DeviceRepo) *ClusterUsecase {
	return &ClusterUsecase{incidents: incidents, devices: devices}
}

// List returns the paginated cluster-wide incidents plus the pre-pagination
// total, mirroring the host list contract.
func (u *ClusterUsecase) List(ctx context.Context, f ClusterFilter) ([]ClusterIncident, int, error) {
	if u.incidents == nil || u.devices == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	if f.Status != "" && !isKnownIncidentStatus(f.Status) {
		return nil, 0, fmt.Errorf("%w: invalid incident status %q", errs.ErrInvalid, f.Status)
	}
	if f.Severity != "" && severityRank(f.Severity) == 0 {
		return nil, 0, fmt.Errorf("%w: invalid incident severity %q", errs.ErrInvalid, f.Severity)
	}
	window := f.Window
	if window == 0 {
		window = DefaultClusterWindow
	}
	if window < 0 || window > MaxClusterWindow {
		return nil, 0, fmt.Errorf("%w: window must be between 0 and %s", errs.ErrInvalid, MaxClusterWindow)
	}
	minHosts := f.MinHosts
	if minHosts == 0 {
		minHosts = DefaultClusterMinHosts
	}
	if minHosts < 0 || minHosts > MaxClusterMinHosts {
		return nil, 0, fmt.Errorf("%w: min_hosts must be between 0 and %d", errs.ErrInvalid, MaxClusterMinHosts)
	}

	rows, err := u.incidents.ListIncidents(ctx, alertbiz.IncidentFilter{
		Status:   f.Status,
		Severity: f.Severity,
		Limit:    maxClusterScan,
	})
	if err != nil {
		return nil, 0, err
	}

	// Host facts enrich the response; a failure here must not hide incidents,
	// so the map is best-effort and missing hosts simply render with their ID.
	hosts := map[uint64]*devicemodel.Device{}
	devices, err := u.devices.List(ctx, devicebiz.ListFilter{})
	if err != nil {
		return nil, 0, err
	}
	for _, device := range devices {
		if device != nil {
			hosts[device.ID] = device
		}
	}

	clusters := groupClusterIncidents(rows, f.Since, window, hosts)
	if minHosts > 0 {
		kept := clusters[:0]
		for _, c := range clusters {
			if c.HostCount >= minHosts {
				kept = append(kept, c)
			}
		}
		clusters = kept
	}
	total := len(clusters)

	start := max(0, f.Offset)
	if start > total {
		start = total
	}
	end := total
	if f.Limit > 0 && start+f.Limit < end {
		end = start + f.Limit
	}
	return clusters[start:end], total, nil
}

// groupClusterIncidents buckets incidents by (anomaly class, signal dimension)
// and then splits each bucket into waves by the sliding first-firing window.
// The result is sorted most-severe-first so a truncated page still leads with
// the wave an operator should look at.
func groupClusterIncidents(rows []*alertmodel.Incident, since *time.Time, window time.Duration, hosts map[uint64]*devicemodel.Device) []ClusterIncident {
	type bucketKey struct {
		class  string
		signal string
	}
	buckets := map[bucketKey][]*alertmodel.Incident{}
	for _, row := range rows {
		if row == nil || row.FirstFiredAt.IsZero() {
			continue
		}
		if since != nil && row.LastFiredAt.Before(*since) {
			continue
		}
		key := bucketKey{class: classifyAnomaly(row.Rule), signal: signalDimension(row)}
		buckets[key] = append(buckets[key], row)
	}

	out := make([]ClusterIncident, 0, len(buckets))
	for key, members := range buckets {
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].FirstFiredAt.Equal(members[j].FirstFiredAt) {
				return members[i].ID < members[j].ID
			}
			return members[i].FirstFiredAt.Before(members[j].FirstFiredAt)
		})
		var wave []*alertmodel.Incident
		var waveEnd time.Time
		flush := func() {
			if len(wave) == 0 {
				return
			}
			out = append(out, buildClusterIncident(key.class, key.signal, wave, hosts))
			wave = nil
		}
		for _, row := range members {
			if len(wave) > 0 && row.FirstFiredAt.After(waveEnd.Add(window)) {
				flush()
				waveEnd = time.Time{}
			}
			wave = append(wave, row)
			if row.LastFiredAt.After(waveEnd) {
				waveEnd = row.LastFiredAt
			}
		}
		flush()
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := severityRank(out[i].Severity), severityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if !out[i].LastFiredAt.Equal(out[j].LastFiredAt) {
			return out[i].LastFiredAt.After(out[j].LastFiredAt)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// buildClusterIncident materializes one wave: member rows plus the per-host
// rollup. wave must be non-empty and sorted by FirstFiredAt.
func buildClusterIncident(class, signal string, wave []*alertmodel.Incident, hosts map[uint64]*devicemodel.Device) ClusterIncident {
	cluster := ClusterIncident{
		Rule:         wave[0].Rule,
		AnomalyClass: class,
		Signal:       signal,
		Severity:     wave[0].Severity,
		Status:       wave[0].Status,
		FirstFiredAt: wave[0].FirstFiredAt,
	}
	byHost := map[uint64]*ClusterHost{}
	hostOrder := make([]uint64, 0, len(wave))
	for _, row := range wave {
		if severityRank(row.Severity) > severityRank(cluster.Severity) {
			cluster.Severity = row.Severity
		}
		if statusRank(row.Status) > statusRank(cluster.Status) {
			cluster.Status = row.Status
		}
		if row.LastFiredAt.After(cluster.LastFiredAt) {
			cluster.LastFiredAt = row.LastFiredAt
		}
		cluster.Members = append(cluster.Members, ClusterMember{
			IncidentID:   row.ID,
			DeviceID:     row.DeviceID,
			Title:        row.Title,
			Severity:     row.Severity,
			Status:       row.Status,
			FirstFiredAt: row.FirstFiredAt,
			LastFiredAt:  row.LastFiredAt,
			Value:        row.Value,
			Threshold:    row.Threshold,
		})
		if row.DeviceID == nil {
			// An incident without a host cannot make the wave cluster-wide;
			// it still counts as a member but not as a host.
			continue
		}
		entry, ok := byHost[*row.DeviceID]
		if !ok {
			entry = &ClusterHost{DeviceID: *row.DeviceID, Severity: row.Severity, LastFiredAt: row.LastFiredAt}
			if device, found := hosts[*row.DeviceID]; found {
				entry.Name = device.Name
				entry.Hostname = device.Hostname
				entry.Online = device.Online
			}
			byHost[*row.DeviceID] = entry
			hostOrder = append(hostOrder, *row.DeviceID)
		}
		entry.IncidentCount++
		if severityRank(row.Severity) > severityRank(entry.Severity) {
			entry.Severity = row.Severity
		}
		if row.LastFiredAt.After(entry.LastFiredAt) {
			entry.LastFiredAt = row.LastFiredAt
		}
	}
	cluster.IncidentCount = len(wave)
	cluster.HostCount = len(hostOrder)
	cluster.Hosts = make([]ClusterHost, 0, len(hostOrder))
	for _, id := range hostOrder {
		cluster.Hosts = append(cluster.Hosts, *byHost[id])
	}
	cluster.Key = fmt.Sprintf("%s|%s|%d", cluster.AnomalyClass, signal, cluster.FirstFiredAt.UTC().Unix())
	return cluster
}

// signalDimension fingerprints the non-host identity labels of an incident so
// two hosts reporting the same subject (same mountpoint, same queue) group,
// while a different subject does not. Host identity labels are excluded
// because aggregating across hosts is the whole point of F-2; provenance
// labels are excluded for the same reason the per-host dedupe key excludes
// them (embedded vs cloud collector must not split a group).
func signalDimension(row *alertmodel.Incident) string {
	labels, err := row.Labels()
	if err != nil || len(labels) == 0 {
		return "_"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if _, skip := clusterHostIdentityLabels[k]; skip {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return "_"
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

// clusterHostIdentityLabels are labels whose value differs per host (or per
// collector) and therefore must not participate in the group identity.
var clusterHostIdentityLabels = map[string]struct{}{
	"__name__":         {},
	"opskeeper_source": {},
	"device_id":        {},
	"device":           {},
	"device_name":      {},
	"host":             {},
	"host_name":        {},
	"hostname":         {},
	"edge":             {},
	"edge_id":          {},
	"edge_name":        {},
	"instance":         {},
	"node":             {},
	"nodename":         {},
}

// anomalyClasses maps a normalized rule key onto the root-cause class shared
// by every host that trips it. Rules that hit the same class aggregate even
// when their keys differ (host/cpu-spike and host.cpu.throttle are both CPU
// saturation). Unclassified rules fall back to their own normalized key, so
// "same rule" still aggregates across hosts.
//
// Matching is a prefix test on the normalized key: the key must equal the
// prefix or continue with a '.' separator, so "host.cpu" matches
// "host/cpu-spike" but never "host.cpu2".
//
// The entries cover the two rule-key families that actually reach
// alert_incidents: the built-in seed rules
// (internal/manager/data/alert/store/seed_rules.go) and the harness /
// custom keys that keep their '<domain>/<case>' spelling.
var anomalyClasses = []struct {
	prefix string
	class  string
}{
	// Harness case ids and custom '<domain>/<case>' keys.
	{"host.cpu", "host.cpu_saturation"},
	{"host.mem", "host.memory_pressure"},
	{"host.swap", "host.memory_pressure"},
	{"host.disk", "host.disk_exhaustion"},
	{"host.load", "host.load_pressure"},
	{"host.fd", "host.fd_exhaustion"},
	{"host.net", "network.flapping"},
	{"host.edge", "observability.edge_unavailable"},
	{"host.heartbeat", "observability.edge_unavailable"},
	{"network", "network.flapping"},
	{"pg", "pg.degradation"},
	{"redis", "redis.memory_pressure"},
	{"k8s", "k8s.workload_failure"},
	{"mq", "mq.backlog"},
	{"pipeline", "observability.scrape_failure"},
	// Built-in seed rules. Their keys are snake_case, so the '<domain>/<case>'
	// prefixes above never see them.
	{"cpu_high", "host.cpu_saturation"},
	{"cpu_high_default", "host.cpu_saturation"},
	{"mem_high", "host.memory_pressure"},
	{"swap_high", "host.memory_pressure"},
	{"disk_high", "host.disk_exhaustion"},
	{"disk_full_warning", "host.disk_exhaustion"},
	{"load1_high", "host.load_pressure"},
	{"fd_exhaustion", "host.fd_exhaustion"},
	{"device_offline", "observability.edge_unavailable"},
	{"scrape_down", "observability.scrape_failure"},
	{"prom_ingest_fail", "observability.scrape_failure"},
}

// anomalyClassKeywords is the last classifier stage before the per-rule
// fallback. It exists for rule keys that carry no separator at all — notably
// Alertmanager rule names forwarded by the webhook path, where the incident
// Rule column holds the alert name verbatim (e.g. "HostHighCpuLoad"). Such a
// key normalizes to one token, so no prefix entry can ever match it, yet
// every host behind that Prometheus rule should still land in one class.
//
// Matching is a plain substring test on the normalized key, so tokens are
// chosen to be unambiguous: "load1"/"loadavg" rather than "load" (which also
// appears in "download"), "consumerlag" rather than "lag" (which also
// appears in "flag"). Order matters — the first hit wins — so the
// Kubernetes-specific tokens precede the generic host ones.
var anomalyClassKeywords = []struct {
	token string
	class string
}{
	{"oomkill", "k8s.workload_failure"},
	{"crashloop", "k8s.workload_failure"},
	{"imagepull", "k8s.workload_failure"},
	{"rolloutfail", "k8s.workload_failure"},
	{"deployfail", "k8s.workload_failure"},
	{"notready", "k8s.workload_failure"},
	{"cpu", "host.cpu_saturation"},
	{"memory", "host.memory_pressure"},
	{"memused", "host.memory_pressure"},
	{"memusage", "host.memory_pressure"},
	{"swap", "host.memory_pressure"},
	{"oom", "host.memory_pressure"},
	{"disk", "host.disk_exhaustion"},
	{"filesystem", "host.disk_exhaustion"},
	{"inode", "host.disk_exhaustion"},
	{"load1", "host.load_pressure"},
	{"loadavg", "host.load_pressure"},
	{"filefd", "host.fd_exhaustion"},
	{"fdexhaust", "host.fd_exhaustion"},
	{"offline", "observability.edge_unavailable"},
	{"heartbeat", "observability.edge_unavailable"},
	{"unreachable", "observability.edge_unavailable"},
	{"scrapedown", "observability.scrape_failure"},
	{"scrapefail", "observability.scrape_failure"},
	{"targetdown", "observability.scrape_failure"},
	{"backlog", "mq.backlog"},
	{"consumerlag", "mq.backlog"},
	{"partition", "network.flapping"},
}

func classifyAnomaly(rule string) string {
	normalized := normalizeRuleKey(rule)
	if normalized == "" {
		return "unknown"
	}
	for _, entry := range anomalyClasses {
		if normalized == entry.prefix || strings.HasPrefix(normalized, entry.prefix+".") {
			return entry.class
		}
	}
	for _, entry := range anomalyClassKeywords {
		if strings.Contains(normalized, entry.token) {
			return entry.class
		}
	}
	return "rule." + normalized
}

// normalizeRuleKey lowercases a rule key and collapses every separator
// ('/', '-', ':', ' ') to '.', so "host/cpu-spike" and "host.cpu_spike" are
// one class.
func normalizeRuleKey(rule string) string {
	replacer := strings.NewReplacer("/", ".", "-", ".", ":", ".", " ", ".")
	return strings.Trim(replacer.Replace(strings.ToLower(strings.TrimSpace(rule))), ".")
}

func isKnownIncidentStatus(status string) bool {
	switch status {
	case alertmodel.StatusOpen, alertmodel.StatusAcknowledged, alertmodel.StatusSilenced, alertmodel.StatusResolved:
		return true
	default:
		return false
	}
}

// severityRank orders severities; 0 also means "unknown", which is what the
// filter validation rejects.
func severityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

// statusRank orders statuses worst-first so a group reports the state that
// needs the most attention.
func statusRank(status string) int {
	switch status {
	case alertmodel.StatusOpen:
		return 4
	case alertmodel.StatusAcknowledged:
		return 3
	case alertmodel.StatusSilenced:
		return 2
	case alertmodel.StatusResolved:
		return 1
	default:
		return 0
	}
}
