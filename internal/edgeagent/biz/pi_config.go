package biz

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/pirpc"
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/pisupervisor"
)

// PiConfig is the typed view of the 13 OPSKEEPER_PI_* environment
// keys defined in .env.example. It is intentionally separate from
// pisupervisor.Config: the supervisor needs only a subset (Bin /
// Args / Health* / Restart* / AutoUpgrade / TagLock / ExtraPackages
// / SyncPiScript / Logger / NowFn); the LLM block + file allowlist
// + approval TTL live elsewhere in the edge runtime.
//
// PiConfig is the canonical "what the operator wrote in
// /etc/opskeeper/edge.env" shape. Downstream packages translate
// PiConfig into their own config structs (supervisor / cmdpolicy /
// ApprovalCache / audit) so adding a new key only touches PiConfig
// + the consumer that needs it.
//
// All fields are populated by LoadPiConfig; missing keys take
// defaults, malformed values fail loudly. Tests use LoadPiConfigFrom
// with an explicit map to exercise the parser without touching the
// process environment.
type PiConfig struct {
	// Enabled is the master switch. When false, edge boots without
	// Pi (supervisor is not started). Default false.
	Enabled bool

	// HTTPBind and HTTPPort are retained only so an existing
	// /etc/opskeeper/edge.env does not fail to parse.
	//
	// Deprecated: Pi has no HTTP server. Its headless surface is
	// `--mode rpc` over the child's stdin/stdout (verified against
	// @earendil-works/pi-coding-agent@0.85.1). Nothing reads these
	// two fields; the loopback invariant of plan1.0.md §6.5 is
	// satisfied more strongly by stdio, which is not reachable
	// off-host at all.
	HTTPBind string
	HTTPPort int

	// Node is the interpreter used to run Script. Default "node".
	Node string

	// Script is the path to Pi's CLI bundle. Default points at the
	// vendored submodule's built entry point. The package's own
	// package.json declares bin.pi = "dist/bundle/cli.js", so the
	// path inside the monorepo is
	// packages/coding-agent/dist/bundle/cli.js.
	Script string

	// Bin runs an installed `pi` shim directly instead of
	// Node + Script. Empty → use Node + Script.
	Bin string

	// Tools is Pi's own tool allowlist (--tools). Empty → Pi's full
	// built-in set. cmdpolicy still gates every write regardless;
	// this narrows what Pi will even attempt.
	Tools []string

	// ExcludeTools is Pi's tool denylist (--exclude-tools).
	ExcludeTools []string

	// SkillDirs are explicit skill roots (--skill). opskeeper keeps
	// its skills outside the submodule, so they must be passed.
	SkillDirs []string

	// SystemPromptFile is appended to Pi's system prompt
	// (--append-system-prompt). This is the rendered SYSTEM.md from
	// scripts/render-pi-system-md.py.
	SystemPromptFile string

	// SessionDir persists Pi session transcripts (--session-dir).
	// Empty → Pi's default location.
	SessionDir string

	// Offline passes --offline so Pi skips startup network calls.
	Offline bool

	// AutoUpgrade, when true, calls scripts/sync-pi.sh before the
	// first spawn. Requires TagLock + SyncPiScript to be set.
	AutoUpgrade bool

	// TagLock is the upstream tag the supervisor is pinned to. When
	// empty and AutoUpgrade is true, the supervisor refuses to start.
	// Default "v0.85.1" (the version pinned in vendor/pi).
	TagLock string

	// SyncPiScript is the path to scripts/sync-pi.sh. Required when
	// AutoUpgrade=true.
	SyncPiScript string

	// LLM is the provider + model + credentials Pi uses to call the
	// upstream model. Optional (Pi can boot in degraded mode when
	// missing), but the supervisor never gates on it.
	LLM PiLLMConfig

	// ExtraPackages is the list of Pi packages to install via
	// `pi install` before first spawn. Comma-separated in env.
	ExtraPackages []string

	// FileAllowlist is the operator-set prefix list Pi passes to
	// read tools. Comma-separated. Empty → use sandbox defaults.
	FileAllowlist []string

	// ApprovalTokenTTL is the lifetime of an approval token granted
	// by the cloud reviewer. Default 60s.
	ApprovalTokenTTL time.Duration
}

// PiLLMConfig is the LLM block of OPSKEEPER_PI_LLM_*.
type PiLLMConfig struct {
	Provider string // e.g. "openai" / "anthropic" / "ollama"
	Model    string // e.g. "gpt-4o-mini" / "claude-haiku-4-5"
	BaseURL  string // empty → provider default
	APIKey   string // sensitive; do NOT log
}

// LoadPiConfig reads the OPSKEEPER_PI_* keys from os.Environ and
// returns a fully-populated PiConfig. Validation failures are
// returned as a single error so callers can decide whether to abort
// edge boot or run without Pi.
func LoadPiConfig() (*PiConfig, error) {
	return LoadPiConfigFrom(os.Environ())
}

// LoadPiConfigFrom is the testable core of LoadPiConfig. It accepts
// the env slice in the same shape os.Environ returns (KEY=VALUE
// pairs) so unit tests can build a controlled []string without
// touching the process environment.
func LoadPiConfigFrom(env []string) (*PiConfig, error) {
	m := envToMap(env)
	cfg := &PiConfig{}

	var errs []string

	// Enabled (default false).
	if v, ok := m["OPSKEEPER_PI_ENABLED"]; ok {
		b, err := parseBool(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("OPSKEEPER_PI_ENABLED=%q: %v", v, err))
		} else {
			cfg.Enabled = b
		}
	}

	// HTTPBind / HTTPPort: parsed for backwards compatibility only,
	// then ignored. Pi never opens a socket.
	cfg.HTTPBind = envOr(m, "OPSKEEPER_PI_HTTP_BIND", "127.0.0.1")
	if v, ok := m["OPSKEEPER_PI_HTTP_PORT"]; ok && v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("OPSKEEPER_PI_HTTP_PORT=%q: not an int", v))
		} else if p < 1 || p > 65535 {
			errs = append(errs, fmt.Sprintf("OPSKEEPER_PI_HTTP_PORT=%d: out of range", p))
		} else {
			cfg.HTTPPort = p
		}
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 19000
	}

	// Node + Script (default: the vendored submodule's built entry).
	cfg.Node = envOr(m, "OPSKEEPER_PI_NODE", "node")
	cfg.Script = envOr(m, "OPSKEEPER_PI_SCRIPT",
		"vendor/pi/packages/coding-agent/dist/bundle/cli.js")

	// Bin: an installed `pi` shim. No default — Node + Script is the
	// vendored path. A value containing whitespace is rejected: the
	// previous default was the string "node vendor/.../cli.js",
	// which can never be exec'd and silently made Pi unstartable.
	if v, ok := m["OPSKEEPER_PI_BIN"]; ok && strings.TrimSpace(v) != "" {
		if strings.ContainsAny(v, " \t") {
			errs = append(errs, fmt.Sprintf(
				"OPSKEEPER_PI_BIN=%q: must be a single executable path; "+
					"use OPSKEEPER_PI_NODE + OPSKEEPER_PI_SCRIPT to run a .js entry point", v))
		} else {
			cfg.Bin = v
		}
	}

	// Pi-native least privilege (comma-separated).
	if v, ok := m["OPSKEEPER_PI_TOOLS"]; ok && v != "" {
		cfg.Tools = splitTrimmed(v, ",")
	}
	if v, ok := m["OPSKEEPER_PI_EXCLUDE_TOOLS"]; ok && v != "" {
		cfg.ExcludeTools = splitTrimmed(v, ",")
	}
	if v, ok := m["OPSKEEPER_PI_SKILL_DIRS"]; ok && v != "" {
		cfg.SkillDirs = splitTrimmed(v, ",")
	}
	cfg.SystemPromptFile = m["OPSKEEPER_PI_SYSTEM_PROMPT_FILE"]
	cfg.SessionDir = m["OPSKEEPER_PI_SESSION_DIR"]
	if v, ok := m["OPSKEEPER_PI_OFFLINE"]; ok {
		b, err := parseBool(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("OPSKEEPER_PI_OFFLINE=%q: %v", v, err))
		} else {
			cfg.Offline = b
		}
	}

	// AutoUpgrade (default false).
	if v, ok := m["OPSKEEPER_PI_AUTO_UPGRADE"]; ok {
		b, err := parseBool(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("OPSKEEPER_PI_AUTO_UPGRADE=%q: %v", v, err))
		} else {
			cfg.AutoUpgrade = b
		}
	}

	// TagLock (default v0.85.1; explicit empty string = "operator
	// meant to clear this", but then AutoUpgrade validation
	// downstream will fail — that's correct).
	if v, ok := m["OPSKEEPER_PI_TAG_LOCK"]; ok {
		cfg.TagLock = v
	} else {
		cfg.TagLock = "v0.85.1"
	}

	// SyncPiScript (no default — required only when AutoUpgrade).
	cfg.SyncPiScript = m["OPSKEEPER_PI_SYNC_PI_SCRIPT"]

	// LLM block.
	cfg.LLM.Provider = m["OPSKEEPER_PI_LLM_PROVIDER"]
	cfg.LLM.Model = m["OPSKEEPER_PI_LLM_MODEL"]
	cfg.LLM.BaseURL = m["OPSKEEPER_PI_LLM_BASE_URL"]
	cfg.LLM.APIKey = m["OPSKEEPER_PI_LLM_API_KEY"]

	// ExtraPackages (comma-separated).
	if v, ok := m["OPSKEEPER_PI_EXTRA_PACKAGES"]; ok && v != "" {
		cfg.ExtraPackages = splitTrimmed(v, ",")
	}

	// FileAllowlist (comma-separated, glob patterns per the env
	// comment).
	if v, ok := m["OPSKEEPER_PI_FILE_ALLOWLIST"]; ok && v != "" {
		cfg.FileAllowlist = splitTrimmed(v, ",")
	}

	// ApprovalTokenTTL (default 60s; accepts Go duration string).
	if v, ok := m["OPSKEEPER_PI_APPROVAL_TOKEN_TTL"]; ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("OPSKEEPER_PI_APPROVAL_TOKEN_TTL=%q: %v", v, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Sprintf("OPSKEEPER_PI_APPROVAL_TOKEN_TTL=%v: must be positive", d))
		} else {
			cfg.ApprovalTokenTTL = d
		}
	}
	if cfg.ApprovalTokenTTL == 0 {
		cfg.ApprovalTokenTTL = 60 * time.Second
	}

	// Cross-field validation.
	if cfg.AutoUpgrade {
		if cfg.TagLock == "" {
			errs = append(errs, "OPSKEEPER_PI_AUTO_UPGRADE=true requires OPSKEEPER_PI_TAG_LOCK")
		}
		if cfg.SyncPiScript == "" {
			errs = append(errs, "OPSKEEPER_PI_AUTO_UPGRADE=true requires OPSKEEPER_PI_SYNC_PI_SCRIPT")
		}
	}

	if len(errs) > 0 {
		return nil, errors.New("biz: invalid Pi config: " + strings.Join(errs, "; "))
	}
	return cfg, nil
}

// BuildLaunchOptions translates a PiConfig into the argv description
// pirpc understands.
//
// The hardening switches are on by default and not operator-tunable
// here on purpose: an edge sidecar must not inherit whatever the host
// user installed under ~/.pi. Discovered extensions, skills, prompt
// templates and AGENTS.md files would otherwise join the ops session
// and change its behaviour without ever appearing in the audit
// chain. opskeeper's own skills come in explicitly via SkillDirs.
func (c *PiConfig) BuildLaunchOptions() pirpc.LaunchOptions {
	// An explicit OPSKEEPER_PI_BIN wins over Node + Script: Script
	// always carries its vendored default, so passing both would be
	// ambiguous and pirpc rejects it.
	node, script := c.Node, c.Script
	if c.Bin != "" {
		node, script = "", ""
	}
	return pirpc.LaunchOptions{
		Node:              node,
		Script:            script,
		Bin:               c.Bin,
		Provider:          c.LLM.Provider,
		Model:             c.LLM.Model,
		SystemPromptFile:  c.SystemPromptFile,
		SkillDirs:         append([]string(nil), c.SkillDirs...),
		Tools:             append([]string(nil), c.Tools...),
		ExcludeTools:      append([]string(nil), c.ExcludeTools...),
		SessionDir:        c.SessionDir,
		NoExtensions:      true,
		NoSkills:          true,
		NoPromptTemplates: true,
		NoContextFiles:    true,
		Offline:           c.Offline,
	}
}

// BuildSupervisorConfig translates a PiConfig into a
// pisupervisor.Config. The supervisor is the only consumer today;
// when other packages grow their own env-driven configs, add a
// similar builder rather than reshaping PiConfig to fit everyone.
//
// Notes:
//   - Bin / Args come from BuildLaunchOptions, i.e. `pi --mode rpc`
//     with the flags the pinned release actually accepts. The
//     supervisor's liveness probe is wired by the caller via
//     pisupervisor.RPCAttach; HealthURL is left empty because Pi
//     serves no HTTP.
//   - ExtraPackages is plumbed through; the supervisor's New()
//     fail-closes if any package fails to install.
//   - Attach / Logger / NowFn / CmdFactory stay nil — production
//     callers inject them after BuildSupervisorConfig returns.
func (c *PiConfig) BuildSupervisorConfig() (pisupervisor.Config, error) {
	bin, args, err := c.BuildLaunchOptions().Command()
	if err != nil {
		return pisupervisor.Config{}, fmt.Errorf("biz: pi launch config: %w", err)
	}
	return pisupervisor.Config{
		Bin:           bin,
		Args:          args,
		AutoUpgrade:   c.AutoUpgrade,
		TagLock:       c.TagLock,
		SyncPiScript:  c.SyncPiScript,
		ExtraPackages: append([]string(nil), c.ExtraPackages...),
	}, nil
}

// envToMap turns a KEY=VALUE slice into a map. Empty KEY lines are
// dropped (some shells emit them when a variable is unset in
// docker-compose, and an empty key would silently shadow later
// lookups). Malformed lines (no '=') are dropped.
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			// no '=' OR '=' at position 0 (empty key) — drop.
			continue
		}
		m[kv[:i]] = kv[i+1:]
	}
	return m
}

// envOr returns the env value for `key` if the key was present and
// non-empty, otherwise `fallback`. To distinguish "absent" from
// "present-but-empty", use the two-value map lookup directly.
func envOr(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return fallback
}

func splitTrimmed(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseBool accepts the common truthy strings. Mirrors the
// convention used by Pi itself (per plan §6.4): true / 1 / yes /
// on ; false / 0 / no / off. Empty string is treated as false.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "0", "no", "off":
		return false, nil
	case "true", "1", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("not a bool: %q", s)
	}
}
