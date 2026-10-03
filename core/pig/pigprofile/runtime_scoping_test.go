//go:build pigscoping

package pigprofile

// The gate that the unit tests in this package structurally cannot be.
//
// TestTheProfileRemovesTheShellFromTheMenu calls piglet.ScopeTools with a
// hand-built tool list, because ScopeTools is the only part of PiG's
// scoping this module can call. That proves the profile is correct *given*
// a runtime that classifies extension tools by extension name — and the
// shipped runtime does not. The real path runs an unexported conversion
// that derives each tool's source from the SourceInfo its own host
// attaches, and nothing in a unit test can reach it.
//
// So this file runs the real binary: a real `pig` built from core/pig the
// way a release builds it, the node's own generated profile, the shipped
// read-only package, and a fake model that records exactly which tools the
// runtime offered.
//
// It is behind a build tag because it is red on the pinned PiG, and a red
// test in `make test` trains everyone to ignore red. Run it explicitly:
//
//	make pig-tool-scoping-check
//
// The failure it reports today is an upstream defect, not a mistake in this
// repository's profile; see docs/opskeeper2-architecture.md §4.67 for the
// one-line fix and why no profile can work around it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/edge/agentprofile"
)

// readonlyPackageRel is the package a node admits first: the read-only
// diagnosis toolset, which is the set of tools the plan's §3.3 table calls
// "✅ 可插件化" and the only toolset a node may run without a human.
const readonlyPackageRel = "../../../plugins/pig-ops/opskeeper-sre-readonly"

func TestTheNodeProfileActuallyOffersTheToolsItsPackagesDeclare(t *testing.T) {
	pkg, err := filepath.Abs(readonlyPackageRel)
	if err != nil {
		t.Fatalf("resolve package: %v", err)
	}
	declared := declaredToolNames(t, pkg)
	if len(declared) == 0 {
		t.Fatal("the package declares no tools, so this gate has nothing to assert")
	}

	offered := offeredToolNames(t, pkg)

	var missing []string
	for _, name := range declared {
		if !contains(offered, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("the node agent was offered %d of the %d tools its admitted package declares.\n"+
			"missing: %s\n\n"+
			"The model cannot call a tool it was never shown, so a node in this state "+
			"holds a correct profile, a signed package, a gate, an allow-list and an audit "+
			"ledger, and an agent that can do nothing with any of them.\n\n"+
			"This is upstream: coding/piglet derives a tool's source by reading "+
			"SourceInfo[\"name\"], while its own host writes SourceInfo[\"source\"]. Every "+
			"tool therefore resolves to \"\", is classified \"builtin\", and the profile's "+
			"`tools: []` — which exists to remove the shell — removes every plugin tool as "+
			"well. The fix is to read \"source\"; it is in PiG, not here. See §4.67.",
			len(offered), len(declared), strings.Join(missing, ", "))
	}
}

// offeredToolNames runs a real agent against a fake model and returns the
// tool names the model was actually offered.
func offeredToolNames(t *testing.T, pkg string) []string {
	t.Helper()

	var mu sync.Mutex
	var offered []string
	seen := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if !seen {
			seen = true
			for _, tool := range body.Tools {
				offered = append(offered, tool.Function.Name)
			}
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		send := func(payload string) {
			fmt.Fprint(w, payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
		send(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"probe",` +
			`"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n")
		send(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"probe",` +
			`"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n")
		send(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"probe",` +
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n")
		send("data: [DONE]\n\n")
	}))
	defer server.Close()

	dir := t.TempDir()
	// The agent's own scope, exactly as the node writes it: a model
	// endpoint, the admitted package set, and the generated profile. The
	// key is a literal because the model is a fake in this process.
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("models.json", fmt.Sprintf(`{"providers":{"probe":{"name":"probe","baseUrl":%q,`+
		`"apiKey":"sk-probe","api":"openai-completions","models":[{"id":"probe","name":"probe"}]}}}`,
		server.URL+"/v1"), 0o600)
	write("settings.json", fmt.Sprintf(`{"packages":["file://%s"]}`, pkg), 0o600)

	profile := filepath.Join(dir, agentprofile.FileName)
	if err := os.WriteFile(profile, []byte(agentprofile.Render()), 0o640); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, pigBinary(t),
		"--piglet", profile, "--provider", "probe", "--model", "probe", "-p", "say ok")
	cmd.Env = append(os.Environ(), "PIG_CODING_AGENT_DIR="+dir)
	out, runErr := cmd.CombinedOutput()

	mu.Lock()
	defer mu.Unlock()
	if !seen {
		t.Fatalf("the agent never reached the model, so nothing was offered or withheld\n"+
			"exit: %v\noutput:\n%s", runErr, out)
	}
	sort.Strings(offered)
	return offered
}

// pigBinary builds the agent the way a release builds it, or uses one the
// caller already built.
func pigBinary(t *testing.T) string {
	t.Helper()
	if pre := os.Getenv("OPSKEEPER_PIG_BIN"); pre != "" {
		return pre
	}
	out := filepath.Join(t.TempDir(), "pig")
	cmd := exec.Command("go", "build", "-o", out, "github.com/MichaelKinsy/PiG/cmd/pig")
	cmd.Dir = ".."
	// The workspace is off on purpose: a release builds the agent from the
	// pinned tag, and a gate that silently built against a developer's
	// local PiG checkout would prove something else.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build pig: %v\n%s", err, combined)
	}
	return out
}

// declaredToolNames reads the tool names a package's governance manifest
// declares, which is the node's own allow-list.
func declaredToolNames(t *testing.T, pkg string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pkg, "pig-ops.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		// The manifests write their tool list in both shapes — `- name: x`
		// and `- { name: x, class: read }` — and a reader that understood
		// only one of them would report an empty list and quietly turn this
		// gate into a no-op.
		if comment := strings.IndexByte(line, '#'); comment >= 0 {
			line = line[:comment]
		}
		// A list item, not any line that happens to name something: the
		// manifest's own metadata block carries a `name:` too, and
		// counting it would make the gate assert on a tool that does not
		// exist.
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		marker := strings.Index(trimmed, "name:")
		if marker < 0 {
			continue
		}
		rest := trimmed[marker+len("name:"):]
		if cut := strings.IndexAny(rest, ",}"); cut >= 0 {
			rest = rest[:cut]
		}
		if name := strings.TrimSpace(rest); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
