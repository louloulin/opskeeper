package version

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

type stubHealth struct {
	summary HealthSummary
	err     error
}

func (s stubHealth) Health(_ context.Context) (HealthSummary, error) {
	return s.summary, s.err
}

func writeFixture(t *testing.T, dir, id, ver string) {
	t.Helper()
	skillDir := filepath.Join(dir, id)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "skill_meta.yaml"),
		[]byte("name: "+id+"\nversion: "+ver+"\n"), 0o644); err != nil {
		t.Fatalf("write skill_meta: %v", err)
	}
}

func writePluginManifest(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{
  "apiVersion":"dashboard.agentteams/v1",
  "kind":"DashboardPlugin",
  "id":"opskeeper-teamharness",
  "name":"Opskeeper TeamHarness",
  "version":"1.0.50"
}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "dashboard", "plugin.json")
	writePluginManifest(t, pluginPath)
	skillsDir := filepath.Join(dir, "skills", "agent")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	writeFixture(t, skillsDir, "opskeeper-alerter", "1.0.0")
	writeFixture(t, skillsDir, "opskeeper-investigator", "1.1.0")
	writeFixture(t, skillsDir, "opskeeper-critic", "1.0.0")
	writeFixture(t, skillsDir, "opskeeper-reviewer", "1.0.0")
	writeFixture(t, skillsDir, "opskeeper-repairer", "1.0.0")
	writeFixture(t, skillsDir, "opskeeper-verifier", "1.0.0")
	writeFixture(t, skillsDir, "opskeeper-postmortem", "1.0.0")
	h, err := NewHandler("v2026.09.14-rc4", Source{
		PluginPath:    pluginPath,
		SkillsDir:     skillsDir,
		HealthService: stubHealth{summary: HealthSummary{Overall: "ok", DB: "ok", Prom: "ok", Logs: "ok", Traces: "ok", LLM: "ok"}},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestNewHandler_RequiresPaths(t *testing.T) {
	t.Parallel()
	if _, err := NewHandler("v1", Source{PluginPath: "", SkillsDir: "x"}); err == nil {
		t.Errorf("expected error for empty PluginPath")
	}
	if _, err := NewHandler("v1", Source{PluginPath: "x", SkillsDir: ""}); err == nil {
		t.Errorf("expected error for empty SkillsDir")
	}
}

func TestDeployment_FullPayload(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)
	r := chi.NewRouter()
	h.Register(r)

	req := httptest.NewRequest(http.MethodGet, "/v1/version/deployment", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp DeploymentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if resp.Manager != "v2026.09.14-rc4" {
		t.Errorf("manager = %q, want v2026.09.14-rc4", resp.Manager)
	}
	if resp.Plugin.Version != "1.0.50" {
		t.Errorf("plugin version = %q, want 1.0.50", resp.Plugin.Version)
	}
	if len(resp.Skills) != 7 {
		t.Errorf("len(skills) = %d, want 7", len(resp.Skills))
	}
	// Skill versions include the investigator upgrade 1.1.0.
	found := false
	for _, s := range resp.Skills {
		if s.ID == "opskeeper-investigator" && s.Version == "1.1.0" {
			found = true
		}
	}
	if !found {
		t.Errorf("opskeeper-investigator v1.1.0 not surfaced")
	}
	if resp.Health.Overall != "ok" {
		t.Errorf("health.overall = %q, want ok", resp.Health.Overall)
	}
	if len(resp.Dependencies) < 5 {
		t.Errorf("dependencies = %d, want >= 5", len(resp.Dependencies))
	}
	if resp.Recovery.IncidentID != "INC-PG-POOL-001" {
		t.Errorf("recovery.incident_id = %q, want INC-PG-POOL-001", resp.Recovery.IncidentID)
	}
	if len(resp.Recovery.Phases) != 7 {
		t.Errorf("recovery.phases = %d, want 7", len(resp.Recovery.Phases))
	}
	if !resp.Recovery.RecoverySignal {
		t.Errorf("recovery.recovery_signal = false, want true")
	}
	if !resp.Recovery.Closed {
		t.Errorf("recovery.closed = false, want true")
	}
}

func TestDeployment_PluginManifestMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, err := NewHandler("v1", Source{
		PluginPath: filepath.Join(dir, "missing.json"),
		SkillsDir:  filepath.Join(dir, "skills"),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	r := chi.NewRouter()
	h.Register(r)
	req := httptest.NewRequest(http.MethodGet, "/v1/version/deployment", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp DeploymentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Plugin.Version != "(manifest unreadable)" {
		t.Errorf("plugin.version = %q, want (manifest unreadable)", resp.Plugin.Version)
	}
	if resp.Plugin.Description == "" {
		t.Errorf("plugin.description empty, want error note")
	}
}

func TestReadSkillVersions_SkipsFilesAndDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFixture(t, dir, "opskeeper-alerter", "2.0.0")
	// Add a stray file (not a directory) — should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	skills := readSkillVersions(dir)
	if len(skills) != 1 {
		t.Fatalf("len(skills) = %d, want 1", len(skills))
	}
	if skills[0].ID != "opskeeper-alerter" || skills[0].Version != "2.0.0" {
		t.Errorf("skill = %+v, want opskeeper-alerter 2.0.0", skills[0])
	}
}

func TestDeriveDependencies_DoesNotLeakURLs(t *testing.T) {
	
	t.Setenv("OPSKEEPER_DB_DSN", "postgres://secret@host:5432/db")
	t.Setenv("OPSKEEPER_PROM_URL", "http://internal-prom:9090")
	deps := deriveDependencies()
	for _, d := range deps {
		if d.Name == "postgres" && !d.Configured {
			t.Errorf("postgres should be configured")
		}
		if d.Name == "prometheus" && !d.Configured {
			t.Errorf("prometheus should be configured")
		}
	}
	// We do NOT inspect the DSN / URL — we only mark configured.
	// Ensure no field of the response can leak the secret.
	if len(deps) == 0 {
		t.Fatalf("no dependencies returned")
	}
	for _, d := range deps {
		if d.Note == "" {
			continue
		}
		// Note is a static string; never the env value.
		if d.Note == "postgres://secret@host:5432/db" || d.Note == "http://internal-prom:9090" {
			t.Errorf("dependency %q leaked env value into Note", d.Name)
		}
	}
}
