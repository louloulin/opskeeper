package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	model "github.com/vincent-wuhan/opskeeper/core/manager/model/mcp"
	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// fakeMCP is a real MCP server, in the sense that matters here: it speaks the
// JSON-RPC the client speaks, over a socket, and the test drives Call through
// it. A stubbed CallTool would have proved only that the arguments were
// passed along, which is the part that was never in doubt — the part that can
// be wrong is whether a composed name reaches the server it was meant to.
type fakeMCP struct {
	// tools is what tools/list reports.
	tools []map[string]any
	// calls records every tools/call name, in order.
	calls []string
	// isError makes tools/call report a tool-level error.
	isError bool
}

func (f *fakeMCP) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     *int           `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// A notification has no id and expects no body.
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": ports.MCPToolSeparator, // any string; unused by the client
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": f.tools}
		case "tools/call":
			name, _ := msg.Params["name"].(string)
			f.calls = append(f.calls, name)
			if f.isError {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "tool said no"}}, "isError": true}
				break
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ran " + name}}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": *msg.ID, "result": result,
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// addServer registers a row and returns its id.
//
// The id rather than the row is deliberate: fakeRepo keeps its own copy, so
// a test that mutated the struct it passed in would be asserting against a
// row nothing else can see. The two helpers below are the supported way to
// change one afterwards, and they go through the repository methods the
// production code actually uses.
func addServer(t *testing.T, repo *fakeRepo, name string, tools []map[string]any, enabled bool) uint64 {
	t.Helper()
	s := &model.Server{
		Name:           name,
		Transport:      TransportHTTP,
		Endpoint:       "http://example.invalid/mcp",
		Enabled:        enabled,
		ToolsCacheJSON: mustJSON(t, tools),
	}
	if err := repo.Create(context.Background(), s); err != nil {
		t.Fatalf("create server %q: %v", name, err)
	}
	return s.ID
}

// setCache replaces a server's probe snapshot, as a probe would.
func setCache(t *testing.T, repo *fakeRepo, id uint64, toolsJSON string) {
	t.Helper()
	if err := repo.SetToolsCache(context.Background(), id, toolsJSON); err != nil {
		t.Fatalf("set tools cache: %v", err)
	}
}

// setEndpoint points a server at a real listener.
//
// It reads the row first because fakeRepo.Update overwrites every field it
// knows about, so a patch carrying only an endpoint silently disables the
// server. That is a property of the test double rather than of the product —
// the production update path patches — but a helper that quietly turned a
// server off cost this file two confusing failures, so it reads first.
func setEndpoint(t *testing.T, repo *fakeRepo, id uint64, endpoint string) {
	t.Helper()
	ctx := context.Background()
	row, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("read server %d: %v", id, err)
	}
	err = repo.Update(ctx, id, &model.Server{
		Endpoint:           endpoint,
		Enabled:            row.Enabled,
		Transport:          row.Transport,
		HeaderTemplateJSON: row.HeaderTemplateJSON,
	})
	if err != nil {
		t.Fatalf("set endpoint: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func toolEntry(name, desc string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": desc,
		"inputSchema": map[string]any{"type": "object"},
	}
}

// TestToolsComposesFromTheProbeSnapshot is the gap this file exists to close:
// a registered, probed server whose tools nobody read.
func TestToolsComposesFromTheProbeSnapshot(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "grafana", []map[string]any{
		toolEntry("query_dashboard", "Run a dashboard query."),
		toolEntry("list_datasources", "List datasources."),
	}, true)
	// A server that was never probed contributes nothing and is not an
	// error: registering a server and not testing it yet is a state an
	// operator creates on purpose.
	addServer(t, repo, "half-configured", nil, true)
	// A disabled server is off the menu even though its cache is populated.
	addServer(t, repo, "retired", []map[string]any{toolEntry("old_tool", "Gone.")}, false)

	uc := NewUsecase(repo, nil, nil)
	tools, err := uc.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	want := []string{"grafana__list_datasources", "grafana__query_dashboard"}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools (%v), want %d", len(tools), names(tools), len(want))
	}
	// Sorted, so two callers in one process see the same order.
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("tool %d is %q, want %q (order must be deterministic)", i, tools[i].Name, name)
		}
	}
	if tools[0].Server != "grafana" || tools[0].Tool != "list_datasources" {
		t.Errorf("server/tool not carried through: %+v", tools[0])
	}
	if tools[0].Description != "List datasources." {
		t.Errorf("description = %q, want the server's own", tools[0].Description)
	}
	if len(tools[0].Schema) == 0 {
		t.Error("schema was dropped; the model would be told to call a tool with no parameters")
	}
}

// TestToolsRefusesAnUnaddressableToolName is the anti-silent-loss property.
//
// A third-party server is free to publish a tool name the model cannot be
// given. Dropping it would leave an operator with a green probe, a healthy
// server and an agent that quietly lacks a capability — the one failure that
// cannot be diagnosed from outside the process.
func TestToolsRefusesAnUnaddressableToolName(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "grafana", []map[string]any{
		toolEntry("list datasources", "Has a space in it."),
	}, true)

	uc := NewUsecase(repo, nil, nil)
	_, err := uc.Tools(context.Background())
	if err == nil {
		t.Fatal("Tools accepted a tool name the model cannot address")
	}
	// The message has to name the server and the tool, or the operator is
	// left with a failure and no way to find its cause.
	for _, want := range []string{"grafana", "list datasources"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestToolsRefusesTwoServersOfferingTheSameName covers the one way two
// servers can compose to the same tool name: "a" offering "b__c" and "a__b"
// offering "c" both produce a__b__c. The server-name rule is what stops it,
// and this is the test that says so — an earlier version of it expected a
// collision report, which no code path produces, because that is the point.
func TestToolsRefusesTwoServersOfferingTheSameName(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "a", []map[string]any{toolEntry("b__c", "One.")}, true)
	addServer(t, repo, "a__b", []map[string]any{toolEntry("c", "Two.")}, true)

	uc := NewUsecase(repo, nil, nil)
	_, err := uc.Tools(context.Background())
	if err == nil {
		t.Fatal("Tools offered two servers whose tools would answer for each other")
	}
	if !strings.Contains(err.Error(), "a__b") {
		t.Errorf("error %q does not name the server an operator has to rename", err)
	}
}

// TestTheSeparatorInsideAToolNameIsHarmless is the other half: a tool name
// that itself contains the separator must survive, because that is what real
// MCP servers publish and the rule above only constrains the server half.
func TestTheSeparatorInsideAToolNameIsHarmless(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "a", []map[string]any{toolEntry("b__c", "Nested.")}, true)

	uc := NewUsecase(repo, nil, nil)
	tools, err := uc.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "a__b__c" || tools[0].Tool != "b__c" {
		t.Fatalf("got %+v, want one tool named a__b__c whose server-side name is b__c", tools)
	}
}

// TestToolsRefusesAnAmbiguousServerName stops the collision at the source.
func TestToolsRefusesAnAmbiguousServerName(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "a__b", []map[string]any{toolEntry("c", "Two.")}, true)

	uc := NewUsecase(repo, nil, nil)
	if _, err := uc.Tools(context.Background()); err == nil {
		t.Fatal("Tools accepted a server whose name contains the separator")
	}
}

// TestToolsReportsAnUnreadableSnapshot keeps the two "nothing here" cases
// apart. A server that was never probed and a server whose cached snapshot no
// longer parses both look like zero tools, and only one of them is normal.
func TestToolsReportsAnUnreadableSnapshot(t *testing.T) {
	repo := newFakeRepo()
	id := addServer(t, repo, "grafana", nil, true)
	setCache(t, repo, id, "{not json")

	uc := NewUsecase(repo, nil, nil)
	_, err := uc.Tools(context.Background())
	if err == nil {
		t.Fatal("Tools treated an unreadable snapshot as an empty one; a schema change would silently remove capabilities")
	}
	if !strings.Contains(err.Error(), "grafana") {
		t.Errorf("error %q does not name the server to re-probe", err)
	}
}

// TestToolsRefusesAnUnusableSchema covers the third-party contract check that
// the catalogue owns so that core/pig never has to decide what a vendor is
// allowed to publish.
func TestToolsRefusesAnUnusableSchema(t *testing.T) {
	// Only valid JSON reaches this check. A schema that is truncated or
	// otherwise malformed makes the whole snapshot unparseable, and that
	// is caught one level up with a message about re-probing — which is
	// the right answer, because the row is corrupt rather than the tool.
	cases := []struct {
		name   string
		schema string
		want   string
	}{
		{"array", `["not", "an", "object"]`, "not a JSON object"},
		{"bare string", `"nope"`, "not a JSON object"},
		{"number", `42`, "not a JSON object"},
		{"boolean", `true`, "not a JSON object"},
		{"null", `null`, "declares no parameters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			id := addServer(t, repo, "grafana", nil, true)
			// The snapshot is written as text rather than marshalled: a
			// truncated schema is one of the cases, and json.Marshal
			// refuses to produce it.
			setCache(t, repo, id, `[{"name":"q","description":"d","inputSchema":`+tc.schema+`}]`)

			uc := NewUsecase(repo, nil, nil)
			_, err := uc.Tools(context.Background())
			if err == nil {
				t.Fatalf("Tools accepted input schema %s", tc.schema)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "q") {
				t.Errorf("error %q does not name the offending tool", err)
			}
		})
	}
}

// TestAToolWithNoDeclaredSchemaIsAccepted keeps the rule from rejecting
// servers that behave correctly. MCP permits a no-argument tool, and refusing
// one would make this deployment reject conformant servers.
func TestAToolWithNoDeclaredSchemaIsAccepted(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "grafana", []map[string]any{
		{"name": "list_datasources", "description": "No parameters at all."},
	}, true)

	uc := NewUsecase(repo, nil, nil)
	tools, err := uc.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools rejected a tool with no declared schema: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "grafana__list_datasources" {
		t.Fatalf("got %v, want the no-argument tool", names(tools))
	}
}

// TestTheToolFilterIsApplied proves authorisation is the catalogue's job and
// not the agent's.
func TestTheToolFilterIsApplied(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "grafana", []map[string]any{toolEntry("query_dashboard", "q")}, true)
	addServer(t, repo, "k8s", []map[string]any{toolEntry("list_pods", "l")}, true)

	uc := NewUsecase(repo, nil, nil)
	uc.SetToolFilter(func(_ context.Context, tools []ports.MCPTool) []ports.MCPTool {
		out := make([]ports.MCPTool, 0, len(tools))
		for _, t := range tools {
			if t.Server == "k8s" {
				continue
			}
			out = append(out, t)
		}
		return out
	})

	tools, err := uc.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "grafana__query_dashboard" {
		t.Fatalf("got %v, want only the grafana tool; the filter is not the last word on what a caller sees", names(tools))
	}
}

// TestCallReachesTheServerTheNameBelongsTo is the property that makes the
// prefix scheme safe. A name that splits one way for the model and another
// way for the lookup would let one server answer for another, so the server
// is resolved by matching a registered prefix rather than by cutting at the
// first separator.
func TestCallReachesTheServerTheNameBelongsTo(t *testing.T) {
	fake := &fakeMCP{tools: []map[string]any{toolEntry("b__c", "One.")}}
	repo := newFakeRepo()
	id := addServer(t, repo, "a", []map[string]any{toolEntry("b__c", "One.")}, true)
	setEndpoint(t, repo, id, fake.start(t))

	uc := NewUsecase(repo, nil, nil)
	tools, err := uc.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "a__b__c" {
		t.Fatalf("composed %v, want a__b__c", names(tools))
	}

	out, err := uc.Call(context.Background(), tools[0].Name, map[string]any{"q": "up"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out != "ran b__c" {
		t.Errorf("Call returned %q, want the server's own text", out)
	}
	// The server must have been asked for its own name, not for the part
	// after the first separator. Asking for "b__c" here happens to be
	// right, so the assertion that matters is the negative one below.
	if len(fake.calls) != 1 || fake.calls[0] != "b__c" {
		t.Errorf("server received %v, want exactly one call for b__c", fake.calls)
	}
}

// TestCallRefusesAnAmbiguousDeployment is the determinism test, and it exists
// because the first version of Call did not have it.
//
// Server "a" offering "b__c" and server "a__b" offering "c" both compose to
// a__b__c, and "a__" is a prefix of it exactly as "a__b__" is. Walking the
// servers and taking the first prefix that matches therefore picks between
// two of them by map iteration order: the same call reaching a different
// server from one request to the next, reproducing on roughly half its runs.
//
// Call refuses rather than guessing. Skipping the ambiguous server would
// answer confidently from the wrong one, and a caller that cannot tell "this
// went somewhere else" from "this worked" is worse off than one that is told
// the deployment has to be renamed.
func TestCallRefusesAnAmbiguousDeployment(t *testing.T) {
	shortSrv := &fakeMCP{}
	longSrv := &fakeMCP{}
	repo := newFakeRepo()
	shortID := addServer(t, repo, "a", []map[string]any{toolEntry("b__c", "Short.")}, true)
	setEndpoint(t, repo, shortID, shortSrv.start(t))
	longID := addServer(t, repo, "a__b", []map[string]any{toolEntry("c", "Long.")}, true)
	setEndpoint(t, repo, longID, longSrv.start(t))

	uc := NewUsecase(repo, nil, nil)
	for i := 0; i < 20; i++ {
		_, err := uc.Call(context.Background(), "a__b__c", nil)
		if err == nil {
			t.Fatalf("call %d was answered; a deployment whose server names overlap has no single right answer", i)
		}
		if !strings.Contains(err.Error(), "a__b") {
			t.Fatalf("error %q does not name the server an operator has to rename", err)
		}
	}
	if len(shortSrv.calls) != 0 || len(longSrv.calls) != 0 {
		t.Errorf("a refused deployment still reached a server: %v %v", shortSrv.calls, longSrv.calls)
	}
}

// TestCallReportsAMissingToolAsAbsentNotBroken keeps a refusal retryable-
// looking. A caller that cannot tell "not on offer" from "server is down"
// retries a refusal for ever.
func TestCallReportsAMissingToolAsAbsentNotBroken(t *testing.T) {
	repo := newFakeRepo()
	addServer(t, repo, "retired", []map[string]any{toolEntry("old", "x")}, false)

	uc := NewUsecase(repo, nil, nil)
	for _, name := range []string{"retired__old", "never_registered__tool", "__leading", "trailing__"} {
		if _, err := uc.Call(context.Background(), name, nil); !errors.Is(err, ports.ErrNoSuchMCPTool) {
			t.Errorf("Call(%q) error = %v, want ErrNoSuchMCPTool", name, err)
		}
	}
}

// TestCallSurfacesAToolLevelErrorAsAFailure keeps a server that ran and
// refused distinguishable from a server that never answered.
func TestCallSurfacesAToolLevelErrorAsAFailure(t *testing.T) {
	fake := &fakeMCP{isError: true}
	repo := newFakeRepo()
	id := addServer(t, repo, "grafana", []map[string]any{toolEntry("q", "x")}, true)
	setEndpoint(t, repo, id, fake.start(t))

	uc := NewUsecase(repo, nil, nil)
	_, err := uc.Call(context.Background(), "grafana__q", nil)
	if err == nil {
		t.Fatal("Call reported success for a tool that returned isError")
	}
	if errors.Is(err, ports.ErrNoSuchMCPTool) {
		t.Error("a tool-level failure was reported as an absent tool; the caller would never retry it")
	}
}

func names(tools []ports.MCPTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
