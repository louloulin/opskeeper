// diagnose.go holds the read path: one function per Diagnose category, and
// the INFO parsing they share.
//
// The shared theme is that Redis answers in prose. INFO returns a
// line-oriented blob, CLIENT LIST returns a column-oriented blob, and
// neither is a data structure. Parsing them here, once, rather than leaving
// the text for a model to read is what makes "how much memory is used"
// answerable without the model having to know which of four similarly-named
// fields on three sections is the one that matters.
package redis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vincent-wuhan/opskeeper/core/manager/middleware/adapter"
)

// parseInfoSections turns an INFO reply into section -> field -> value.
//
// Fields whose names contain a colon (# Keyspace, db0:keys) are grouped one
// level deeper rather than being flattened, because collapsing "db0" and
// "db1" into one namespace loses the only thing those lines carry.
func parseInfoSections(raw string) map[string]map[string]string {
	out := map[string]map[string]string{}
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			// A new section header.
			section = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			if section != "" {
				if _, ok := out[section]; !ok {
					out[section] = map[string]string{}
				}
			}
			continue
		}
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		if section == "" {
			section = "default"
			if _, ok := out[section]; !ok {
				out[section] = map[string]string{}
			}
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		if key == "" {
			continue
		}
		out[section][key] = val
	}
	return out
}

// infoValue reads one field out of a parsed INFO reply.
func infoValue(sections map[string]map[string]string, section, field string) (string, bool) {
	if s, ok := sections[section]; ok {
		if v, ok := s[field]; ok {
			return v, true
		}
	}
	return "", false
}

func infoInt(sections map[string]map[string]string, section, field string) (int64, bool) {
	v, ok := infoValue(sections, section, field)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func fetchInfo(ctx context.Context, a *Adapter) (map[string]map[string]string, error) {
	client, err := a.handle()
	if err != nil {
		return nil, err
	}
	raw, err := client.Info(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: INFO: %w", err)
	}
	return parseInfoSections(raw), nil
}

// ── categories ─────────────────────────────────────────────────────────

func diagnoseServerInfo(ctx context.Context, a *Adapter, _ adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	sections, err := fetchInfo(ctx, a)
	if err != nil {
		return nil, "", err
	}
	row := map[string]any{}
	for _, f := range []struct{ section, field, as string }{
		{"Server", "redis_version", "redis_version"},
		{"Server", "redis_mode", "mode"},
		{"Server", "uptime_in_seconds", "uptime_seconds"},
		{"Server", "tcp_port", "tcp_port"},
		{"Clients", "connected_clients", "connected_clients"},
		{"Clients", "blocked_clients", "blocked_clients"},
		{"Stats", "total_commands_processed", "total_commands"},
		{"Stats", "instantaneous_ops_per_sec", "ops_per_sec"},
		{"Stats", "rejected_connections", "rejected_connections"},
		{"Keyspace", "db0", "db0_keyspace"},
	} {
		if v, ok := infoValue(sections, f.section, f.field); ok {
			row[f.as] = v
		}
	}
	if len(row) == 0 {
		return nil, "", errors.New("redis: INFO returned no recognisable sections")
	}
	return []map[string]any{row}, "", nil
}

func diagnoseKeyspace(ctx context.Context, a *Adapter, _ adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	sections, err := fetchInfo(ctx, a)
	if err != nil {
		return nil, "", err
	}
	ks, ok := sections["Keyspace"]
	if !ok {
		return []map[string]any{}, "no database has any key", nil
	}
	names := make([]string, 0, len(ks))
	for name := range ks {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		row := map[string]any{"database": name}
		// The value is "keys=123,expires=4,avg_ttl=0".
		for _, part := range strings.Split(ks[name], ",") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			if n, err := strconv.ParseInt(kv[1], 10, 64); err == nil {
				row[kv[0]] = n
			} else {
				row[kv[0]] = kv[1]
			}
		}
		rows = append(rows, row)
	}
	return rows, "", nil
}

func diagnoseBigKeys(ctx context.Context, a *Adapter, q adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	limit, err := intArg(q.Params, "limit", 20)
	if err != nil {
		return nil, "", err
	}
	scanLimit, err := intArg(q.Params, "scan_limit", defaultScanLimit)
	if err != nil {
		return nil, "", err
	}
	keys, sampled, err := a.scanKeys(ctx, "*", scanLimit)
	if err != nil {
		return nil, "", err
	}
	client, err := a.handle()
	if err != nil {
		return nil, "", err
	}
	type entry struct {
		key   string
		bytes int64
		typ   string
	}
	entries := make([]entry, 0, len(keys))
	for _, k := range keys {
		size, err := client.MemoryUsage(ctx, k).Result()
		if err != nil {
			// A key can expire between SCAN and MEMORY USAGE. Skipping it
			// is correct; failing the whole diagnostic because one key
			// vanished would make the report unavailable exactly when the
			// instance is under churn.
			continue
		}
		typ, _ := client.Type(ctx, k).Result()
		entries = append(entries, entry{key: k, bytes: size, typ: typ})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].bytes > entries[j].bytes })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	rows := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, map[string]any{"key": e.key, "type": e.typ, "bytes": e.bytes})
	}
	note := fmt.Sprintf("sampled %d key(s) of %d scanned; Redis has no key-size index, so this is not a global ranking",
		len(entries), sampled)
	return rows, note, nil
}

func diagnoseSlowLog(ctx context.Context, a *Adapter, q adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	limit, err := intArg(q.Params, "limit", 50)
	if err != nil {
		return nil, "", err
	}
	client, err := a.handle()
	if err != nil {
		return nil, "", err
	}
	entries, err := client.SlowLogGet(ctx, int64(limit)).Result()
	if err != nil {
		return nil, "", fmt.Errorf("redis: SLOWLOG GET: %w", err)
	}
	rows := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, map[string]any{
			"id":              e.ID,
			"timestamp":       time.Unix(e.Time.Unix(), 0).UTC().Format(time.RFC3339),
			"duration_micros": e.Duration.Microseconds(),
			"command":         strings.Join(e.Args, " "),
			"client_addr":     e.ClientAddr,
			"client_name":     e.ClientName,
		})
	}
	note := ""
	if len(entries) == 0 {
		// An empty slow log is not "no slow commands happened"; it is
		// also what a server returns when the threshold is set high or
		// the log was just reset. Saying which is the difference between
		// a finding and a blank.
		if threshold, ok := fetchSlowlogThreshold(ctx, client); ok {
			note = fmt.Sprintf("no entries; slowlog-log-slower-than is %d microseconds", threshold)
		} else {
			note = "no entries"
		}
	}
	return rows, note, nil
}

func fetchSlowlogThreshold(ctx context.Context, client redis.UniversalClient) (int64, bool) {
	v, err := client.ConfigGet(ctx, "slowlog-log-slower-than").Result()
	if err != nil {
		return 0, false
	}
	raw, ok := v["slowlog-log-slower-than"]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// clientColumns are the CLIENT LIST fields this adapter reports, chosen as
// the ones a diagnosis actually reads: who, from where, doing what, for how
// long.
var clientColumns = []string{"id", "addr", "name", "age", "idle", "flags", "db", "cmd", "resp", "user"}

func parseClientList(raw string) []map[string]any {
	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		row := map[string]any{}
		for _, field := range strings.Fields(line) {
			kv := strings.SplitN(field, "=", 2)
			if len(kv) != 2 {
				continue
			}
			row[kv[0]] = kv[1]
		}
		if len(row) == 0 {
			continue
		}
		filtered := make(map[string]any, len(clientColumns))
		for _, c := range clientColumns {
			if v, ok := row[c]; ok {
				filtered[c] = v
			}
		}
		if len(filtered) == 0 {
			filtered = row
		}
		rows = append(rows, filtered)
	}
	return rows
}

func diagnoseClients(ctx context.Context, a *Adapter, _ adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	client, err := a.handle()
	if err != nil {
		return nil, "", err
	}
	raw, err := client.ClientList(ctx).Result()
	if err != nil {
		return nil, "", fmt.Errorf("redis: CLIENT LIST: %w", err)
	}
	return parseClientList(raw), "", nil
}

func diagnoseBlocked(ctx context.Context, a *Adapter, _ adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	client, err := a.handle()
	if err != nil {
		return nil, "", err
	}
	// CLIENT LIST TYPE blocked is a filter the server applies, so this
	// reads the blocked set rather than fetching every client and
	// discarding most of them.
	raw, err := client.Do(ctx, "CLIENT", "LIST", "TYPE", "blocked").Text()
	if err != nil {
		return nil, "", fmt.Errorf("redis: CLIENT LIST TYPE blocked: %w", err)
	}
	rows := parseClientList(raw)
	note := ""
	if len(rows) == 0 {
		note = "no client is blocked"
	}
	return rows, note, nil
}

func diagnoseMemory(ctx context.Context, a *Adapter, _ adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	sections, err := fetchInfo(ctx, a)
	if err != nil {
		return nil, "", err
	}
	row := map[string]any{}
	for _, f := range []struct{ field, as string }{
		{"used_memory", "used_memory_bytes"},
		{"used_memory_human", "used_memory_human"},
		{"used_memory_rss", "used_memory_rss_bytes"},
		{"used_memory_peak", "used_memory_peak_bytes"},
		{"used_memory_lua", "used_memory_lua_bytes"},
		{"used_memory_dataset", "used_memory_dataset_bytes"},
		{"mem_fragmentation_ratio", "fragmentation_ratio"},
		{"maxmemory", "maxmemory_bytes"},
		{"maxmemory_policy", "maxmemory_policy"},
		{"evicted_keys", "evicted_keys"},
	} {
		if v, ok := infoValue(sections, "Memory", f.field); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				row[f.as] = n
			} else {
				row[f.as] = v
			}
		}
	}
	if len(row) == 0 {
		return nil, "", errors.New("redis: INFO Memory section is missing")
	}
	// The ratio between used_memory and RSS is the number that decides
	// whether a purge is worth proposing, so it is computed here rather
	// than left for the reader to divide.
	if used, ok := row["used_memory_bytes"].(int64); ok && used > 0 {
		if rss, ok := row["used_memory_rss_bytes"].(int64); ok {
			row["rss_over_used"] = float64(rss) / float64(used)
		}
	}
	return []map[string]any{row}, "", nil
}

func diagnoseFragmentation(ctx context.Context, a *Adapter, _ adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	rows, _, err := diagnoseMemory(ctx, a, adapter.DiagnoseQuery{})
	if err != nil {
		return nil, "", err
	}
	note := ""
	if len(rows) == 1 {
		if ratio, ok := rows[0]["fragmentation_ratio"].(string); ok {
			if f, err := strconv.ParseFloat(ratio, 64); err == nil {
				switch {
				case f > 1.5:
					note = "fragmentation is high; the allocator is holding pages the dataset no longer uses"
				case f < 1:
					note = "RSS is below used_memory, which means the process is swapping"
				default:
					note = "fragmentation is within the normal range"
				}
			}
		}
	}
	return rows, note, nil
}

func diagnoseCluster(ctx context.Context, a *Adapter, _ adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	if !a.isCluster() {
		// Saying so is the answer. A standalone server has no slots, and
		// returning an empty list would read as "a cluster with no nodes".
		return nil, "this adapter is connected in standalone mode; a cluster has no slots to report here", nil
	}
	client, err := a.handle()
	if err != nil {
		return nil, "", err
	}
	raw, err := client.Do(ctx, "CLUSTER", "SLOTS").Result()
	if err != nil {
		return nil, "", fmt.Errorf("redis: CLUSTER SLOTS: %w", err)
	}
	rows, err := clusterSlotsToRows(raw)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("%d slot range(s)", len(rows))
	return rows, note, nil
}

// clusterSlotsToRows decodes CLUSTER SLOTS into one row per range.
//
// go-redis returns the reply as nested []interface{} because the shape
// varies by server version. The decoder is defensive about depth on
// purpose: a panic in a diagnostic is worse than a partial answer, and the
// version that changes the shape is the one nobody tested against.
func clusterSlotsToRows(raw any) ([]map[string]any, error) {
	slots, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("redis: CLUSTER SLOTS returned %T, want a list of ranges", raw)
	}
	rows := make([]map[string]any, 0, len(slots))
	for _, s := range slots {
		entry, ok := s.([]interface{})
		if !ok || len(entry) < 3 {
			continue
		}
		row := map[string]any{}
		if lo, ok := toInt64(entry[0]); ok {
			row["slot_start"] = lo
		}
		if hi, ok := toInt64(entry[1]); ok {
			row["slot_end"] = hi
		}
		masters := []string{}
		replicas := []string{}
		for i := 2; i < len(entry); i++ {
			node, ok := entry[i].([]interface{})
			if !ok || len(node) < 2 {
				continue
			}
			host, _ := node[0].(string)
			port, _ := toInt64(node[1])
			addr := fmt.Sprintf("%s:%d", host, port)
			if i == 2 {
				masters = append(masters, addr)
			} else {
				replicas = append(replicas, addr)
			}
		}
		row["master"] = strings.Join(masters, ",")
		row["replicas"] = strings.Join(replicas, ",")
		rows = append(rows, row)
	}
	return rows, nil
}

func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func diagnoseConfig(ctx context.Context, a *Adapter, q adapter.DiagnoseQuery) ([]map[string]any, string, error) {
	pattern, _ := q.Params["parameter"].(string)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		pattern = "*"
	}
	client, err := a.handle()
	if err != nil {
		return nil, "", err
	}
	vals, err := client.ConfigGet(ctx, pattern).Result()
	if err != nil {
		return nil, "", fmt.Errorf("redis: CONFIG GET: %w", err)
	}
	names := make([]string, 0, len(vals))
	for name := range vals {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, map[string]any{"parameter": name, "value": vals[name]})
	}
	return rows, "", nil
}
