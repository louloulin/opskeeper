package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	alertbiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/alert"
	alertmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/alert"
	devicemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/device"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

type fakeIncidents struct {
	rows []*alertmodel.Incident
	last alertbiz.IncidentFilter
	err  error
}

func (f *fakeIncidents) ListIncidents(_ context.Context, filter alertbiz.IncidentFilter) ([]*alertmodel.Incident, error) {
	f.last = filter
	return f.rows, f.err
}

func incident(id uint64, device uint64, rule, severity, status string, first, last time.Time, labels string) *alertmodel.Incident {
	deviceID := device
	return &alertmodel.Incident{
		ID:           id,
		DeviceID:     &deviceID,
		Rule:         rule,
		Title:        rule + " on host",
		Severity:     severity,
		Status:       status,
		LabelsJSON:   labels,
		FirstFiredAt: first,
		LastFiredAt:  last,
	}
}

func clusterFixture(t *testing.T) (*fakeIncidents, *fakeDevices) {
	t.Helper()
	base := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	incidents := &fakeIncidents{rows: []*alertmodel.Incident{
		// Two hosts, same rule, same non-host label dimension, 3 minutes apart
		// -> one cluster wave covering 2 hosts.
		incident(1, 100, "host/cpu-spike", "warning", alertmodel.StatusOpen, base, base, `{"mount":"/data","host":"db-a","device_id":"100"}`),
		incident(2, 200, "host.cpu.throttle", "critical", alertmodel.StatusAcknowledged, base.Add(3*time.Minute), base.Add(4*time.Minute), `{"mount":"/data","host":"db-b","device_id":"200"}`),
		// Same class, different dimension (different mount) -> its own cluster.
		incident(3, 300, "host/cpu-spike", "warning", alertmodel.StatusOpen, base, base, `{"mount":"/var","host":"db-c","device_id":"300"}`),
		// Same class and dimension but 40 minutes later -> second wave.
		incident(4, 100, "host/cpu-spike", "warning", alertmodel.StatusOpen, base.Add(40*time.Minute), base.Add(41*time.Minute), `{"mount":"/data","host":"db-a","device_id":"100"}`),
		// A lone host in an unrelated class -> filtered by MinHosts=2.
		incident(5, 400, "redis/memory", "info", alertmodel.StatusOpen, base, base, `{"host":"cache-a"}`),
	}}
	devices := &fakeDevices{rows: []*devicemodel.Device{
		{ID: 100, Name: "db-a", Hostname: "db-a", Online: true},
		{ID: 200, Name: "db-b", Hostname: "db-b", Online: true},
		{ID: 300, Name: "db-c", Hostname: "db-c", Online: false},
		{ID: 400, Name: "cache-a", Hostname: "cache-a", Online: true},
	}}
	return incidents, devices
}

func TestClusterListGroupsCrossHostWave(t *testing.T) {
	incidents, devices := clusterFixture(t)
	uc := NewClusterUsecase(incidents, devices)

	got, total, err := uc.List(context.Background(), ClusterFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// Waves: cpu-spike/mount=/data @10:00 (2 hosts), cpu-spike/mount=/data
	// @10:40 (1 host), cpu-spike/mount=/var (1 host). The redis singleton and
	// the second /data wave fall below MinHosts=2.
	if total != 1 || len(got) != 1 {
		t.Fatalf("total=%d items=%+v", total, got)
	}
	wave := got[0]
	if wave.AnomalyClass != "host.cpu_saturation" || wave.HostCount != 2 || wave.IncidentCount != 2 {
		t.Fatalf("wave=%+v", wave)
	}
	// The rule label comes from the first member; both rules normalize into
	// the same anomaly class.
	if wave.Rule != "host/cpu-spike" {
		t.Fatalf("rule=%q", wave.Rule)
	}
	// Worst severity wins; the status is ranked worst-first too, so an
	// unacknowledged member outranks an acknowledged one.
	if wave.Severity != "critical" || wave.Status != alertmodel.StatusOpen {
		t.Fatalf("severity=%q status=%q", wave.Severity, wave.Status)
	}
	if wave.FirstFiredAt != wave.Members[0].FirstFiredAt || wave.LastFiredAt != wave.Members[1].LastFiredAt {
		t.Fatalf("window=%s..%s", wave.FirstFiredAt, wave.LastFiredAt)
	}
	if len(wave.Hosts) != 2 || wave.Hosts[0].DeviceID != 100 || wave.Hosts[1].DeviceID != 200 {
		t.Fatalf("hosts=%+v", wave.Hosts)
	}
	if wave.Hosts[0].Name != "db-a" || wave.Hosts[1].Name != "db-b" {
		t.Fatalf("host facts not joined: %+v", wave.Hosts)
	}
	if incidents.last.Limit != maxClusterScan || incidents.last.Status != "" {
		t.Fatalf("repo filter=%+v", incidents.last)
	}
}

func TestClusterListKeepsWavesSeparateOutsideWindow(t *testing.T) {
	incidents, devices := clusterFixture(t)
	uc := NewClusterUsecase(incidents, devices)

	got, total, err := uc.List(context.Background(), ClusterFilter{MinHosts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(got) != 4 {
		t.Fatalf("total=%d items=%d", total, len(got))
	}
	// The 10:00 and 10:40 /data waves must not merge.
	var dataWaves []ClusterIncident
	for _, c := range got {
		if c.AnomalyClass == "host.cpu_saturation" && c.Signal == "mount=/data" {
			dataWaves = append(dataWaves, c)
		}
	}
	if len(dataWaves) != 2 {
		t.Fatalf("data waves=%d, want 2: %+v", len(dataWaves), dataWaves)
	}
	if !dataWaves[0].FirstFiredAt.Before(dataWaves[1].FirstFiredAt) {
		t.Fatalf("waves out of order: %+v", dataWaves)
	}
	// Deterministic keys: class + dimension + wave start epoch.
	if dataWaves[0].Key == dataWaves[1].Key {
		t.Fatalf("wave keys collide: %q", dataWaves[0].Key)
	}
}

func TestClusterListWideningWindowMergesWaves(t *testing.T) {
	incidents, devices := clusterFixture(t)
	uc := NewClusterUsecase(incidents, devices)

	got, _, err := uc.List(context.Background(), ClusterFilter{Window: 45 * time.Minute, MinHosts: 1})
	if err != nil {
		t.Fatal(err)
	}
	var merged *ClusterIncident
	for i := range got {
		if got[i].AnomalyClass == "host.cpu_saturation" && got[i].Signal == "mount=/data" {
			merged = &got[i]
		}
	}
	if merged == nil || merged.IncidentCount != 3 {
		t.Fatalf("merged=%+v", merged)
	}
}

func TestClusterListSortsMostSevereFirst(t *testing.T) {
	incidents, devices := clusterFixture(t)
	uc := NewClusterUsecase(incidents, devices)

	got, _, err := uc.List(context.Background(), ClusterFilter{MinHosts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Severity != "critical" {
		t.Fatalf("first severity=%q, want critical", got[0].Severity)
	}
}

func TestClusterListPaginatesAfterGrouping(t *testing.T) {
	incidents, devices := clusterFixture(t)
	uc := NewClusterUsecase(incidents, devices)

	page, total, err := uc.List(context.Background(), ClusterFilter{MinHosts: 1, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(page) != 2 {
		t.Fatalf("total=%d page=%d", total, len(page))
	}
	all, _, err := uc.List(context.Background(), ClusterFilter{MinHosts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page[0].Key != all[1].Key || page[1].Key != all[2].Key {
		t.Fatalf("page=%+v all=%+v", page, all)
	}
}

func TestClusterListDropsIncidentsWithoutHostFromHostCount(t *testing.T) {
	base := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	orphan := incident(9, 0, "host/load", "warning", alertmodel.StatusOpen, base, base, `{"host":"x"}`)
	orphan.DeviceID = nil
	incidents := &fakeIncidents{rows: []*alertmodel.Incident{
		orphan,
		incident(10, 100, "host/load", "warning", alertmodel.StatusOpen, base, base, `{"host":"a"}`),
		incident(11, 200, "host/load", "warning", alertmodel.StatusOpen, base, base, `{"host":"b"}`),
	}}
	devices := &fakeDevices{rows: []*devicemodel.Device{{ID: 100, Name: "a"}, {ID: 200, Name: "b"}}}

	got, _, err := NewClusterUsecase(incidents, devices).List(context.Background(), ClusterFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IncidentCount != 3 || got[0].HostCount != 2 {
		t.Fatalf("got=%+v", got)
	}
}

func TestClusterListAppliesSinceOnLastFiredAt(t *testing.T) {
	incidents, devices := clusterFixture(t)
	uc := NewClusterUsecase(incidents, devices)

	cut := time.Date(2026, 9, 11, 10, 30, 0, 0, time.UTC)
	got, total, err := uc.List(context.Background(), ClusterFilter{Since: &cut, MinHosts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("total=%d got=%+v", total, got)
	}
	if !got[0].FirstFiredAt.Equal(time.Date(2026, 9, 11, 10, 40, 0, 0, time.UTC)) {
		t.Fatalf("first_fired_at=%s", got[0].FirstFiredAt)
	}
}

func TestClusterListRejectsInvalidFilters(t *testing.T) {
	incidents, devices := clusterFixture(t)
	uc := NewClusterUsecase(incidents, devices)

	cases := []ClusterFilter{
		{Status: "flapping"},
		{Severity: "urgent"},
		{Window: -time.Minute},
		{Window: MaxClusterWindow + time.Minute},
		{MinHosts: -1},
		{MinHosts: MaxClusterMinHosts + 1},
	}
	for _, filter := range cases {
		if _, _, err := uc.List(context.Background(), filter); !errors.Is(err, errs.ErrInvalid) {
			t.Errorf("filter=%+v error=%v, want ErrInvalid", filter, err)
		}
	}
	if incidents.last.Status != "" {
		t.Fatalf("invalid filter reached the repo: %+v", incidents.last)
	}
}

func TestClusterListNotWiredYet(t *testing.T) {
	if _, _, err := NewClusterUsecase(nil, nil).List(context.Background(), ClusterFilter{}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("error=%v, want ErrNotWiredYet", err)
	}
}

func TestClusterListPropagatesRepoError(t *testing.T) {
	sentinel := errors.New("boom")
	uc := NewClusterUsecase(&fakeIncidents{err: sentinel}, &fakeDevices{})
	if _, _, err := uc.List(context.Background(), ClusterFilter{}); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v, want sentinel", err)
	}
}

func TestClassifyAnomalyNormalizesRuleKeys(t *testing.T) {
	cases := map[string]string{
		// Harness case ids / custom '<domain>/<case>' keys.
		"host/cpu-spike":        "host.cpu_saturation",
		"host.cpu.throttle":     "host.cpu_saturation",
		"host/disk-full":        "host.disk_exhaustion",
		"pg/long-running-tx":    "pg.degradation",
		"redis/memory-burst":    "redis.memory_pressure",
		"k8s/pod-oom":           "k8s.workload_failure",
		"mq/kafka-consumer-lag": "mq.backlog",
		"network/partition":     "network.flapping",
		// Built-in seed rules (seed_rules.go).
		"cpu_high":          "host.cpu_saturation",
		"cpu_high_default":  "host.cpu_saturation",
		"mem_high":          "host.memory_pressure",
		"swap_high":         "host.memory_pressure",
		"disk_full_warning": "host.disk_exhaustion",
		"load1_high":        "host.load_pressure",
		"fd_exhaustion":     "host.fd_exhaustion",
		"device_offline":    "observability.edge_unavailable",
		"scrape_down":       "observability.scrape_failure",
		"prom_ingest_fail":  "observability.scrape_failure",
		// Alertmanager rule names arrive with no separator at all.
		"HostHighCpuLoad":    "host.cpu_saturation",
		"NodeMemoryPressure": "host.memory_pressure",
		"KubePodOOMKilled":   "k8s.workload_failure",
		// Unknown keys keep their own class so same-rule still aggregates.
		"custom/thing": "rule.custom.thing",
		"":             "unknown",
	}
	for rule, want := range cases {
		if got := classifyAnomaly(rule); got != want {
			t.Errorf("classifyAnomaly(%q)=%q, want %q", rule, got, want)
		}
	}
}

func TestClassifyAnomalyDoesNotMatchOnPrefixSubstrings(t *testing.T) {
	// A rule key that merely starts with a domain word must not borrow that
	// domain's class — the prefix table only matches on a '.' boundary.
	for _, rule := range []string{"pgbackup", "redislike", "myqueue"} {
		if got := classifyAnomaly(rule); got != "rule."+normalizeRuleKey(rule) {
			t.Errorf("classifyAnomaly(%q)=%q, want per-rule fallback", rule, got)
		}
	}
	// The keyword stage is deliberately looser: an unseparated key that
	// mentions CPU is still CPU saturation, which is what lets Alertmanager
	// rule names aggregate across hosts.
	if got := classifyAnomaly("host.cpu2"); got != "host.cpu_saturation" {
		t.Errorf("classifyAnomaly(\"host.cpu2\")=%q, want host.cpu_saturation", got)
	}
}

func TestSignalDimensionStripsHostIdentityLabels(t *testing.T) {
	base := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	a := incident(1, 100, "host/load", "warning", alertmodel.StatusOpen, base, base,
		`{"mount":"/data","host":"db-a","device_id":"100","instance":"10.0.0.1:9100","opskeeper_source":"edge"}`)
	b := incident(2, 200, "host/load", "warning", alertmodel.StatusOpen, base, base,
		`{"instance":"10.0.0.2:9100","host":"db-b","device_id":"200","opskeeper_source":"cloud","mount":"/data"}`)
	if signalDimension(a) != signalDimension(b) {
		t.Fatalf("dimensions differ: %q vs %q", signalDimension(a), signalDimension(b))
	}
	c := incident(3, 300, "host/load", "warning", alertmodel.StatusOpen, base, base, `{"mount":"/var","host":"db-c"}`)
	if signalDimension(a) == signalDimension(c) {
		t.Fatalf("different mountpoints must not share a dimension: %q", signalDimension(a))
	}
	if got := signalDimension(&alertmodel.Incident{LabelsJSON: "not-json"}); got != "_" {
		t.Fatalf("malformed labels=%q, want _", got)
	}
}
