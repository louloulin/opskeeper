# Final Demo P0 Regression Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the stale-manifest pool exporter failure and AgentTeams role/context message storm blocking the final-demo E2E.

**Architecture:** Pool fixture remains the authoritative metrics source and exports every manifest with stable labels. A versioned demo proxy performs authenticated pass-through without manifest configuration. TeamHarness resolves Manager identity before prompt injection or continuation, then applies outbound sanitization, rate, target-role, and incident STOP boundaries.

**Tech Stack:** Go standard library `net/http` tests; Python 3 standard-library runtime plugin and `unittest`; existing QwenPaw AgentScope middleware hooks; existing TeamHarness package scripts.

**Spec:** `openspec/changes/fix-final-demo-p0-regression/specs/pool-metrics-discovery/spec.md`; `openspec/changes/fix-final-demo-p0-regression/specs/agentteams-dispatch-isolation/spec.md`; `docs/superpowers/specs/2026-09-17-fix-final-demo-p0-regression-design.md`

---
change: fix-final-demo-p0-regression
design-doc: docs/superpowers/specs/2026-09-17-fix-final-demo-p0-regression-design.md
base-ref: 2a94e6df8acf18ea8a132e932e3c3bacc2d049c0
---

## Global Constraints

- Do not build anything on the Alibaba Cloud host; local macOS builds only.
- Do not modify shared PostgreSQL or Redis schemas or data.
- OpsKeeper Manager remains authoritative for incident records, permissions, approval, recovery execution, and redaction.
- Never print or commit runtime tokens, passwords, DSNs, or Matrix access tokens.
- Preserve incident-owned pool fixtures; do not reuse the contaminated incident `opskeeper-final-2a94e6df-20260917-144442`.
- Public deployment requires a separate explicit user approval after local tests pass.

### Task 1: Dynamic Pool Fixture Metrics

**Files:**
- Modify: `cmd/pool-fixture/main.go`
- Test: `cmd/pool-fixture/main_test.go`

**Interfaces:**
- Produces: aggregate `GET /metrics` lines in the form `opskeeper_pool_fixture_active_connections{target="...",pool_manifest_id="..."} value`.
- Produces: aggregate `GET /metrics` lines in the form `opskeeper_pool_fixture_capacity{target="...",pool_manifest_id="..."} value`.
- Preserves: authenticated per-fixture `GET /v1/pool-fixtures/{id}/metrics`.

- [ ] **Step 1: Write the failing multi-fixture metrics test**

Append a focused test after `TestHandlerProtectsAndExposePoolLifecycle`. It starts two running fixtures with distinct incident IDs, reads aggregate `/metrics` with the existing bearer token, and asserts both manifest labels and values:

```go
func TestAggregatePrometheusMetricsLabelEveryManifest(t *testing.T) {
	controller, _, _ := newTestController(t)
	first, err := controller.Start(context.Background(), StartRequest{
		CaseID: "pg-pool-exhaustion", IncidentID: "incident-live-001",
		InitialCapacity: 2, TargetCapacity: 4, TTLSeconds: 60,
	})
	if err != nil { t.Fatal(err) }
	second, err := controller.Start(context.Background(), StartRequest{
		CaseID: "pg-pool-exhaustion", IncidentID: "incident-live-002",
		InitialCapacity: 3, TargetCapacity: 6, TTLSeconds: 60,
	})
	if err != nil { t.Fatal(err) }

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	addPoolAuth(request)
	recorder := httptest.NewRecorder()
	NewHandler(controller).ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK { t.Fatalf("status = %d", recorder.Code) }
	for _, expected := range []string{
		`opskeeper_pool_fixture_active_connections{target="pg:pool-fixture",pool_manifest_id="` + first.ManifestID + `"} 2`,
		`opskeeper_pool_fixture_capacity{target="pg:pool-fixture",pool_manifest_id="` + first.ManifestID + `"} 2`,
		`opskeeper_pool_fixture_active_connections{target="pg:pool-fixture",pool_manifest_id="` + second.ManifestID + `"} 3`,
		`opskeeper_pool_fixture_capacity{target="pg:pool-fixture",pool_manifest_id="` + second.ManifestID + `"} 3`,
	} {
		if !strings.Contains(body, expected) { t.Fatalf("missing %s in:\n%s", expected, body) }
	}
}
```

- [ ] **Step 2: Run the focused Go test and verify the new assertions fail**

Run: `go test ./cmd/pool-fixture -run TestAggregatePrometheusMetricsLabelEveryManifest -count=1`

Expected: FAIL because aggregate lines currently omit `pool_manifest_id`.

- [ ] **Step 3: Implement stable manifest labels**

In `prometheusMetrics`, render labels once and use them for both series:

```go
func (h *Handler) prometheusMetrics(writer http.ResponseWriter) {
	statuses := h.controller.Statuses()
	writer.Header().Set("Content-Type", `text/plain; version=0.0.4; charset=utf-8`)
	for _, status := range statuses {
		capacity := status.TargetCapacity
		if status.Status == poolStateRunning { capacity = status.InitialCapacity }
		labels := fmt.Sprintf("target=%q,pool_manifest_id=%q", status.Resource, status.ManifestID)
		fmt.Fprintf(writer, "opskeeper_pool_fixture_active_connections{%s} %d\n", labels, status.ActiveConnections)
		fmt.Fprintf(writer, "opskeeper_pool_fixture_capacity{%s} %d\n", labels, capacity)
	}
}
```

- [ ] **Step 4: Verify all pool fixture tests**

Run: `go test ./cmd/pool-fixture -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Task 1**

```bash
git add cmd/pool-fixture/main.go cmd/pool-fixture/main_test.go
git commit -m "fix(pool-fixture): label aggregate metrics by manifest"
```

### Task 2: Manifest-Agnostic Public Metrics Proxy

**Files:**
- Create: `deploy/demo/monitoring/pgpool-fixture/pool_metrics_proxy.py`
- Create: `deploy/demo/monitoring/pgpool-fixture/test_pool_metrics_proxy.py`
- Modify: `deploy/demo/monitoring/pgpool-fixture/README.md`

**Interfaces:**
- Consumes environment `POOL_FIXTURE_URL`, default `http://pool-fixture:8092`.
- Consumes token file `POOL_FIXTURE_TOKEN_FILE`, default `/var/run/secrets/pool-token`.
- Produces local `GET /healthz` and authenticated pass-through `GET /metrics`.
- Explicitly does not consume `POOL_MANIFEST_ID`.

- [ ] **Step 1: Write failing proxy tests**

Create standard-library tests using `http.server.HTTPServer` and a stub upstream. Test success, empty fixture output, and upstream 503:

```python
class PoolMetricsProxyTest(unittest.TestCase):
    def test_metrics_is_authenticated_pass_through(self):
        upstream = StubUpstream([b"opskeeper_pool_fixture_active_connections{} 2\n"])
        with upstream.server() as base_url, temporary_token("test-token-1234567890") as token_file:
            with patch.dict(os.environ, {"POOL_FIXTURE_URL": base_url, "POOL_FIXTURE_TOKEN_FILE": token_file}):
                status, headers, body = request_local_server(PoolMetricsProxy)
        self.assertEqual(status, 200)
        self.assertEqual(headers.get_content_type(), "text/plain")
        self.assertEqual(body, upstream.last_body)
        self.assertEqual(upstream.last_authorization, "Bearer test-token-1234567890")

    def test_upstream_failure_returns_503_without_body(self):
        upstream = StubUpstream([], status=500)
        with upstream.server() as base_url, temporary_token("test-token-1234567890") as token_file:
            with patch.dict(os.environ, {"POOL_FIXTURE_URL": base_url, "POOL_FIXTURE_TOKEN_FILE": token_file}):
                status, _, _ = request_local_server(PoolMetricsProxy)
        self.assertEqual(status, 503)
```

- [ ] **Step 2: Run proxy tests and verify import failure**

Run: `python3 -m unittest discover -s deploy/demo/monitoring/pgpool-fixture -p 'test_pool_metrics_proxy.py' -v`

Expected: FAIL because `pool_metrics_proxy.py` does not exist.

- [ ] **Step 3: Implement the proxy**

Use only `http.server`, `os`, and `urllib.request`. Read the token per request, require a nonempty token, preserve the Prometheus content type returned upstream, and return 503 on any upstream or token error. Do not cache metrics or manifest IDs.

- [ ] **Step 4: Update deployment documentation**

Document that the old command passing `-e POOL_MANIFEST_ID=...` is invalid. The replacement mounts this script and token file only; Prometheus continues scraping `opskeeper-pool-metrics:8094`.

- [ ] **Step 5: Verify the proxy tests**

Run: `python3 -m unittest discover -s deploy/demo/monitoring/pgpool-fixture -p 'test_pool_metrics_proxy.py' -v`

Expected: PASS.

- [ ] **Step 6: Commit Task 2**

```bash
git add deploy/demo/monitoring/pgpool-fixture/pool_metrics_proxy.py deploy/demo/monitoring/pgpool-fixture/test_pool_metrics_proxy.py deploy/demo/monitoring/pgpool-fixture/README.md
git commit -m "fix(demo): proxy pool metrics without manifest pinning"
```

### Task 3: Manager and Worker Context Isolation

**Files:**
- Modify: `plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.py`
- Test: `plugins/opskeeper-teamharness/adapters/qwenpaw/test_manager_gate.py`

**Interfaces:**
- Produces `_is_manager_agent(agent: Any) -> bool` with explicit `worker` and `standalone` precedence.
- Produces `_register_prompt_sections(api: Any)` with Manager, Worker, and shared-team conditions.
- Produces `_is_message_for_agent(message: str, agent: Any) -> bool`.
- Preserves `ManagerDispatchGate`, but its runtime hook returns immediately for non-Manager contexts.

- [ ] **Step 1: Write failing role tests**

Add tests that model the public environment:

```python
def test_standalone_worker_is_not_manager_even_with_manager_runtime(self):
    agent = SimpleNamespace(name="opskeeper-repairer")
    environment = {
        "AGENTTEAMS_WORKER_NAME": "opskeeper-repairer",
        "AGENTTEAMS_WORKER_ROLE": "standalone",
        "AGENTTEAMS_MANAGER_RUNTIME": "qwenpaw",
    }
    with patch.dict("os.environ", environment, clear=False):
        self.assertFalse(self.module._is_manager_agent(agent))

def test_worker_hook_does_not_consume_manager_marker(self):
    worker_session = "matrix:!worker-room:hs"
    self.module._MANAGER_DISPATCH_GATE.record(worker_session, "OPSKEEPER TASK task-001")
    context = self._hook_context("OPSKEEPER_RESULT task-001 {}", sender="@worker:hs")
    context.agent = SimpleNamespace(name="opskeeper-repairer")
    with patch.dict("os.environ", {"AGENTTEAMS_WORKER_ROLE": "standalone"}, clear=False):
        result = asyncio.run(self._registered_hook().run(context))
    self.assertEqual(result.action.value, "continue")
    self.assertEqual(self.module._MANAGER_DISPATCH_GATE.pending_markers(worker_session), ("task-001",))
    self.module._MANAGER_DISPATCH_GATE.clear(worker_session)
```

Add prompt and target-role tests:

```python
def test_prompt_providers_are_role_gated(self):
    manager = SimpleNamespace(name="manager")
    worker = SimpleNamespace(name="opskeeper-repairer")
    with patch.dict("os.environ", {"AGENTTEAMS_WORKER_ROLE": "standalone"}, clear=False):
        self.assertFalse(self.module._is_manager_agent(worker))
        self.assertTrue(self.module._is_message_for_agent("@opskeeper-repairer:hs status", worker))
        self.assertFalse(self.module._is_message_for_agent("@opskeeper-verifier:hs status", worker))
    with patch.dict("os.environ", {"AGENTTEAMS_MANAGER_RUNTIME": "qwenpaw"}, clear=False):
        self.assertTrue(self.module._is_manager_agent(manager))
```

- [ ] **Step 2: Run focused Python tests and verify failures**

Run: `python3 -m unittest plugins.opskeeper-teamharness.adapters.qwenpaw.test_manager_gate -v`

Expected: FAIL because standalone currently loses to `AGENTTEAMS_MANAGER_RUNTIME`, workers can consume markers, and cross-role targeting is unimplemented.

- [ ] **Step 3: Implement conservative identity and prompt gating**

Update `_is_manager_agent` in this order: explicit worker/standalone returns false; explicit Manager role or Manager name returns true; Manager runtime is only a fallback when no explicit worker identity exists. Replace unconditional prompt registration with a helper accepting conditions:

```python
def _register_prompt_sections(api: Any) -> None:
    sections = (
        ("opskeeper_team_context", team_prompt, lambda agent: _is_manager_agent(agent), 40),
        ("opskeeper_worker_context", worker_prompt, lambda agent: not _is_manager_agent(agent), 30),
        ("opskeeper_manager_context", manager_prompt, lambda agent: _is_manager_agent(agent), 30),
    )
    for name, provider, condition, priority in sections:
        try:
            api.register_prompt_section(
                name,
                after="workspace",
                provider=provider,
                condition=condition,
                priority=priority,
            )
        except TypeError:
            api.register_prompt_section(name, after="workspace", provider=_ gated_prompt(provider, condition), priority=priority)
        except Exception:
            pass
```

Remove the space in `_ gated_prompt` when implementing; it is shown split only to make the plan's fallback name explicit.

- [ ] **Step 4: Implement cross-role input gating and Manager-only continuation**

At the start of `ManagerContinuationGateHook.run`, resolve `ctx.agent`; if it is not Manager, return `HookResult()` without reading or consuming gate state. Before checking pending state, if `_is_message_for_agent(message, ctx.agent)` returns false and the message contains an explicit `@opskeeper-` mention, return `HookAction.SKIP_AGENT`.

- [ ] **Step 5: Run Manager gate tests**

Run: `python3 -m unittest plugins.opskeeper-teamharness.adapters.qwenpaw.test_manager_gate -v`

Expected: PASS, including all existing relay tests.

- [ ] **Step 6: Commit Task 3**

```bash
git add plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.py plugins/opskeeper-teamharness/adapters/qwenpaw/test_manager_gate.py
git commit -m "fix(teamharness): isolate manager and worker contexts"
```

### Task 4: Outbound Sanitization, Rate Limit, and STOP

**Files:**
- Modify: `plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.py`
- Test: `plugins/opskeeper-teamharness/adapters/qwenpaw/test_readonly_enforcement.py`
- Test: `plugins/opskeeper-teamharness/adapters/qwenpaw/test_manager_gate.py`

**Interfaces:**
- Produces `_strip_thinking(text: str) -> str`.
- Produces `_sanitize_reply_event(event: Any) -> Any`.
- Produces `OutboundSafetyMiddleware` with `on_reply` and `on_acting`.
- Produces `_outbound_safety_factory(context: Any, agent_config: Any)`.
- Produces `_register_incident_stop_hook(api: Any)`.
- Consumes `OPSKEEPER_OUTBOUND_LIMIT`, default `12`, values `1..1000`.
- Consumes `OPSKEEPER_OUTBOUND_WINDOW_SECONDS`, default `10`, values `1..3600`.

- [ ] **Step 1: Write failing sanitization and limit tests**

In `test_readonly_enforcement.py`, create a text event with `<think>private</think>\npublic result` and assert `_strip_thinking` returns `public result`. Instantiate `OutboundSafetyMiddleware` with a patched monotonic clock or limit `1`; its first `on_reply` yields one event and the second raises `RuntimeError` matching `outbound rate limit`.

Use the existing stub `MiddlewareBase`; extend it only if needed to make `is_implemented("on_reply")` true.

- [ ] **Step 2: Write failing STOP tests**

In `test_manager_gate.py`, register the new hook and execute:

```python
def test_admin_stop_skips_later_non_admin_incident_input(self):
    hook = self._registered_stop_hook()
    admin_context = self._hook_context(
        "ADMIN STOP opskeeper-final-fresh-001", sender="@admin:hs"
    )
    self.assertEqual(asyncio.run(hook.run(admin_context)).action.value, "skip_agent")
    worker_context = self._hook_context(
        "@opskeeper-alerter:hs retry opskeeper-final-fresh-001", sender="@worker:hs"
    )
    self.assertEqual(asyncio.run(hook.run(worker_context)).action.value, "skip_agent")
```

Expected before implementation: registered stop-hook helper or behavior is missing.

- [ ] **Step 3: Implement reply sanitization**

Add a tolerant literal-span remover that handles multiline `<think>...</think>` and a fallback leading partial span. `_sanitize_reply_event` mutates or reconstructs text-bearing objects: final message `content` lists, `TextBlock.text`, and text delta `.text`. It never logs the removed value.

- [ ] **Step 4: Implement the outbound boundary**

Use `collections.deque`, `time.monotonic`, and a lock. Count each `on_reply` invocation and each successful message-tool attempt. Before exceeding the configured count, raise `RuntimeError("OpsKeeper outbound rate limit exceeded; stopping this turn")`. Do not yield a denial message after the boundary.

- [ ] **Step 5: Implement admin incident STOP**

At PRE_EXECUTE priority 0, parse `ADMIN STOP <incident-id>` only when `_is_admin_sender` is true, store the ID in a process-local set, and skip the model for that admin turn. For later input, if sender is not admin and any stopped ID appears in the request text, return `HookAction.SKIP_AGENT`. Admin input remains allowed so the administrator can issue a new explicit instruction.

- [ ] **Step 6: Run focused Python suites**

```bash
python3 -m unittest plugins.opskeeper-teamharness.adapters.qwenpaw.test_readonly_enforcement -v
python3 -m unittest plugins.opskeeper-teamharness.adapters.qwenpaw.test_manager_gate -v
```

Expected: PASS.

- [ ] **Step 7: Commit Task 4**

```bash
git add plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.py plugins/opskeeper-teamharness/adapters/qwenpaw/test_readonly_enforcement.py plugins/opskeeper-teamharness/adapters/qwenpaw/test_manager_gate.py
git commit -m "fix(teamharness): bound outbound agent messages"
```

### Task 5: Regression, Packaging Readiness, and OpenSpec Task Closure

**Files:**
- Modify: `openspec/changes/fix-final-demo-p0-regression/tasks.md`
- Modify: TeamHarness version metadata only if the repository requires a new package version for deployment.
- Modify generated release artifacts only through the existing local build command after code approval.

**Interfaces:**
- Produces local test evidence for all code tasks.
- Produces a candidate TeamHarness package version and artifacts when needed.
- Does not deploy to Alibaba Cloud without explicit approval.

- [ ] **Step 1: Run focused implementations**

```bash
go test ./cmd/pool-fixture -count=1
python3 -m unittest discover -s deploy/demo/monitoring/pgpool-fixture -p 'test_pool_metrics_proxy.py' -v
python3 -m unittest plugins.opskeeper-teamharness.adapters.qwenpaw.test_manager_gate -v
python3 -m unittest plugins.opskeeper-teamharness.adapters.qwenpaw.test_readonly_enforcement -v
```

Expected: all PASS.

- [ ] **Step 2: Run broader repository checks**

Run `go test ./...` and the existing TeamHarness Python test discovery command used by the package script. Resolve only failures caused by this change.

- [ ] **Step 3: Validate package structure**

Run the existing QwenPaw validator against a newly built package. Confirm the package contains the modified `plugin.py` and does not contain any runtime token or public-host credential.

- [ ] **Step 4: Build local release artifacts**

Use the repository's existing local macOS build commands for the Manager/site image artifacts, Dashboard ZIP, plugin-manager TAR, and QwenPaw ZIP. Do not invoke docker build on `8.160.172.235`.

- [ ] **Step 5: Update OpenSpec task state**

Mark Tasks 1.1 through 3.3 complete only after their code and focused tests pass. Leave 3.4 unchecked until the user explicitly approves public deployment and the fresh-manifest E2E passes.

- [ ] **Step 6: Commit verification and packaging state**

```bash
git add openspec/changes/fix-final-demo-p0-regression/tasks.md
git commit -m "test: verify final demo p0 regression fixes"
```

If version metadata or release artifacts are changed, include those exact generated paths in the same commit.
