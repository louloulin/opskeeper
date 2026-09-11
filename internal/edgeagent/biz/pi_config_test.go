package biz

import (
	"reflect"
	"testing"
	"time"
)

// envKV is a tiny helper that turns (key, value) pairs into the
// KEY=VALUE slice shape LoadPiConfigFrom expects.
func envKV(pairs ...string) []string {
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p)
	}
	return out
}

func TestLoadPiConfig_DefaultsWhenEmpty(t *testing.T) {
	cfg, err := LoadPiConfigFrom(nil)
	if err != nil {
		t.Fatalf("empty env: %v", err)
	}
	if cfg.Enabled {
		t.Errorf("Enabled=true; want false")
	}
	if cfg.HTTPBind != "127.0.0.1" {
		t.Errorf("HTTPBind=%q; want 127.0.0.1", cfg.HTTPBind)
	}
	if cfg.HTTPPort != 19000 {
		t.Errorf("HTTPPort=%d; want 19000", cfg.HTTPPort)
	}
	if cfg.TagLock != "v0.85.1" {
		t.Errorf("TagLock=%q; want v0.85.1", cfg.TagLock)
	}
	if cfg.ApprovalTokenTTL != 60*time.Second {
		t.Errorf("ApprovalTokenTTL=%v; want 60s", cfg.ApprovalTokenTTL)
	}
	if cfg.Bin == "" {
		t.Errorf("Bin should default to vendor/pi path")
	}
}

func TestLoadPiConfig_OverridesApply(t *testing.T) {
	cfg, err := LoadPiConfigFrom(envKV(
		"OPSKEEPER_PI_ENABLED=true",
		"OPSKEEPER_PI_HTTP_BIND=127.0.0.1",
		"OPSKEEPER_PI_HTTP_PORT=19001",
		"OPSKEEPER_PI_BIN=/usr/local/bin/pi",
		"OPSKEEPER_PI_TAG_LOCK=v0.86.0",
		"OPSKEEPER_PI_EXTRA_PACKAGES=pi-yaml-hooks, pi-toolbox ",
		"OPSKEEPER_PI_FILE_ALLOWLIST=/etc/opskeeper/**,/var/log/opskeeper/**",
		"OPSKEEPER_PI_APPROVAL_TOKEN_TTL=2m",
		"OPSKEEPER_PI_LLM_PROVIDER=anthropic",
		"OPSKEEPER_PI_LLM_MODEL=claude-haiku-4-5",
		"OPSKEEPER_PI_LLM_BASE_URL=",
		"OPSKEEPER_PI_LLM_API_KEY=sk-test",
	))
	if err != nil {
		t.Fatalf("overrides: %v", err)
	}
	if !cfg.Enabled {
		t.Errorf("Enabled=false; want true")
	}
	if cfg.HTTPPort != 19001 {
		t.Errorf("HTTPPort=%d; want 19001", cfg.HTTPPort)
	}
	if cfg.Bin != "/usr/local/bin/pi" {
		t.Errorf("Bin=%q", cfg.Bin)
	}
	if cfg.TagLock != "v0.86.0" {
		t.Errorf("TagLock=%q", cfg.TagLock)
	}
	wantPkgs := []string{"pi-yaml-hooks", "pi-toolbox"}
	if !reflect.DeepEqual(cfg.ExtraPackages, wantPkgs) {
		t.Errorf("ExtraPackages=%v; want %v", cfg.ExtraPackages, wantPkgs)
	}
	wantAllow := []string{"/etc/opskeeper/**", "/var/log/opskeeper/**"}
	if !reflect.DeepEqual(cfg.FileAllowlist, wantAllow) {
		t.Errorf("FileAllowlist=%v; want %v", cfg.FileAllowlist, wantAllow)
	}
	if cfg.ApprovalTokenTTL != 2*time.Minute {
		t.Errorf("ApprovalTokenTTL=%v; want 2m", cfg.ApprovalTokenTTL)
	}
	if cfg.LLM.Provider != "anthropic" || cfg.LLM.Model != "claude-haiku-4-5" || cfg.LLM.APIKey != "sk-test" {
		t.Errorf("LLM=%+v", cfg.LLM)
	}
}

func TestLoadPiConfig_ParseBoolVariants(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"off", false},
		{"", false},
	}
	for _, c := range cases {
		cfg, err := LoadPiConfigFrom(envKV("OPSKEEPER_PI_ENABLED=" + c.in))
		if err != nil {
			t.Errorf("parseBool(%q): %v", c.in, err)
			continue
		}
		if cfg.Enabled != c.want {
			t.Errorf("parseBool(%q): got %v want %v", c.in, cfg.Enabled, c.want)
		}
	}
}

func TestLoadPiConfig_InvalidValuesError(t *testing.T) {
	cases := [][]string{
		envKV("OPSKEEPER_PI_ENABLED=maybe"),
		envKV("OPSKEEPER_PI_HTTP_PORT=not-a-number"),
		envKV("OPSKEEPER_PI_HTTP_PORT=0"),
		envKV("OPSKEEPER_PI_HTTP_PORT=99999"),
		envKV("OPSKEEPER_PI_APPROVAL_TOKEN_TTL=not-a-duration"),
		envKV("OPSKEEPER_PI_APPROVAL_TOKEN_TTL=-1m"),
		envKV("OPSKEEPER_PI_APPROVAL_TOKEN_TTL=0s"),
	}
	for _, env := range cases {
		if _, err := LoadPiConfigFrom(env); err == nil {
			t.Errorf("expected error for env %v", env)
		}
	}
}

func TestLoadPiConfig_AutoUpgradeRequiresTagLock(t *testing.T) {
	_, err := LoadPiConfigFrom(envKV(
		"OPSKEEPER_PI_AUTO_UPGRADE=true",
		"OPSKEEPER_PI_SYNC_PI_SCRIPT=/usr/local/bin/sync-pi.sh",
		// TagLock intentionally absent → defaults to v0.85.1, so
		// this case actually passes (default kicks in). Test the
		// failure mode separately by overriding TagLock to empty.
	))
	if err != nil {
		t.Fatalf("with default TagLock: %v", err)
	}
	_, err = LoadPiConfigFrom(envKV(
		"OPSKEEPER_PI_AUTO_UPGRADE=true",
		"OPSKEEPER_PI_TAG_LOCK=",
		"OPSKEEPER_PI_SYNC_PI_SCRIPT=/usr/local/bin/sync-pi.sh",
	))
	if err == nil {
		t.Errorf("expected error when AutoUpgrade=true and TagLock empty")
	}
}

func TestLoadPiConfig_AutoUpgradeRequiresSyncPiScript(t *testing.T) {
	_, err := LoadPiConfigFrom(envKV(
		"OPSKEEPER_PI_AUTO_UPGRADE=true",
		"OPSKEEPER_PI_SYNC_PI_SCRIPT=",
	))
	if err == nil {
		t.Errorf("expected error when AutoUpgrade=true and SyncPiScript empty")
	}
}

func TestLoadPiConfig_AggregatesErrors(t *testing.T) {
	// Two independent errors should both surface in the returned
	// error message — single-field failure mode would hide the
	// second problem and waste an operator round-trip.
	_, err := LoadPiConfigFrom(envKV(
		"OPSKEEPER_PI_ENABLED=maybe",
		"OPSKEEPER_PI_HTTP_PORT=banana",
	))
	if err == nil {
		t.Fatalf("expected aggregated error")
	}
	msg := err.Error()
	if !contains(msg, "OPSKEEPER_PI_ENABLED") || !contains(msg, "OPSKEEPER_PI_HTTP_PORT") {
		t.Errorf("error %q should mention both keys", msg)
	}
}

func TestBuildSupervisorConfig_DefaultsFlowThrough(t *testing.T) {
	cfg, err := LoadPiConfigFrom(envKV(
		"OPSKEEPER_PI_ENABLED=true",
		"OPSKEEPER_PI_HTTP_BIND=127.0.0.1",
		"OPSKEEPER_PI_HTTP_PORT=19000",
		"OPSKEEPER_PI_BIN=/usr/local/bin/pi",
		"OPSKEEPER_PI_TAG_LOCK=v0.85.1",
		"OPSKEEPER_PI_EXTRA_PACKAGES=pi-yaml-hooks",
	))
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.BuildSupervisorConfig()
	if sc.Bin != "/usr/local/bin/pi" {
		t.Errorf("Bin=%q", sc.Bin)
	}
	wantArgs := []string{"--mode", "http", "--bind", "127.0.0.1", "--port", "19000"}
	if !reflect.DeepEqual(sc.Args, wantArgs) {
		t.Errorf("Args=%v; want %v", sc.Args, wantArgs)
	}
	if sc.HealthURL != "http://127.0.0.1:19000/health" {
		t.Errorf("HealthURL=%q", sc.HealthURL)
	}
	if !reflect.DeepEqual(sc.ExtraPackages, []string{"pi-yaml-hooks"}) {
		t.Errorf("ExtraPackages=%v", sc.ExtraPackages)
	}
	if sc.TagLock != "v0.85.1" {
		t.Errorf("TagLock=%q", sc.TagLock)
	}
	// BuildSupervisorConfig does not set HealthInterval /
	// RestartBackoffMin / etc — those have their own defaults in
	// pisupervisor.New(). We assert here that the builder leaves
	// them at zero so the supervisor's defaults fire.
	if sc.HealthInterval != 0 || sc.RestartBackoffMin != 0 {
		t.Errorf("builder should leave tunables at zero; got %+v", sc)
	}
}

func TestBuildSupervisorConfig_ExtraPackagesDefensiveCopy(t *testing.T) {
	cfg, _ := LoadPiConfigFrom(envKV("OPSKEEPER_PI_EXTRA_PACKAGES=a,b"))
	sc := cfg.BuildSupervisorConfig()
	sc.ExtraPackages[0] = "MUTATED"
	if cfg.ExtraPackages[0] != "a" {
		t.Errorf("BuildSupervisorConfig should not share backing array; cfg.ExtraPackages=%v", cfg.ExtraPackages)
	}
}

func TestEnvToMap_IgnoresMalformed(t *testing.T) {
	m := envToMap([]string{"=novalue", "noequals", "FOO=bar", "BAZ=qux=extra"})
	if m["FOO"] != "bar" {
		t.Errorf("FOO=%q", m["FOO"])
	}
	if m["BAZ"] != "qux=extra" {
		t.Errorf("BAZ=%q", m["BAZ"])
	}
	if _, ok := m[""]; ok {
		t.Errorf("empty key should not be stored")
	}
	if _, ok := m["noequals"]; ok {
		t.Errorf("malformed line should be dropped")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
