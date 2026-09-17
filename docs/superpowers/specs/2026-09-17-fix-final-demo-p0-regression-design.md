---
comet_change: fix-final-demo-p0-regression
role: technical-design
canonical_spec: openspec
---

# Final Demo P0 Regression Fix Design

## Decision Summary

This hotfix repairs the two public final-demo P0 blockers without changing database topology, HITL authority, or the AgentTeams transport protocol.

### Pool metrics

`cmd/pool-fixture` remains the sole authority for fixture state. Its authenticated aggregate `/metrics` endpoint emits active and capacity series for every loaded fixture. Each series carries `target` and `pool_manifest_id`, allowing exact selection from an incident envelope and avoiding ambiguity when prior fixtures remain in memory.

The public demo adds a versioned Python proxy under `deploy/demo/monitoring/pgpool-fixture`. It reads only the authoritative endpoint URL and runtime token, proxies bytes to Prometheus, and does not accept a manifest ID. If the fixture service is unavailable, the proxy returns an upstream failure rather than fabricating values.

### TeamHarness role isolation

Manager identity is resolved conservatively. An explicit `worker` or `standalone` role always wins over a leaked Manager runtime setting. Manager continuation, result consumption, and relay behavior run only after this identity check. Manager-only, Worker-only, and shared team prompt sections use runtime conditions so instructions do not cross contexts.

Incoming text that explicitly mentions another named OpsKeeper role without mentioning the current role is skipped before model execution. This prevents a Manager from treating an admin-to-repairer instruction as a Manager task and prevents workers from amplifying one another.

### Outbound safety

A middleware intercepts final replies. It removes thinking blocks and literal `<think>...</think>` spans while preserving the public result line. A configurable sliding-window boundary limits both reply generation and message-tool attempts. Exceeding the limit raises a terminal error rather than emitting another Matrix message.

An administrator can issue `ADMIN STOP <incident-id>`. The plugin stores that incident before model execution and skips subsequent non-admin input referencing it. This is a process-local circuit breaker intended to stop an active runaway turn quickly; it is not a durable workflow state and does not alter backend incident or approval records.

## Data Flow

1. Pool fixture creation updates in-memory and persisted manifest state.
2. Prometheus scrapes the manifest-agnostic proxy.
3. The proxy authenticates to pool fixture `/metrics` and returns all current manifest-labeled series.
4. Investigators query with the current incident's `pool_manifest_id`.
5. AgentTeams input first passes STOP, role-target, and Manager-gate checks.
6. Approved role-specific execution proceeds with backend authority unchanged.
7. Outbound replies pass thinking sanitization and rate-boundary checks.

## Error Handling

- Upstream pool metrics failure returns HTTP 503 with no synthetic series.
- Unknown TeamHarness role defaults away from Manager behavior.
- Explicit messages for another role are skipped, not answered.
- STOP and rate-limit boundaries are fail-closed and emit no additional room message.
- Existing backend validation remains authoritative for incident records, approval, recovery, and redaction.

## Testing Strategy

- Go: create multiple fixtures and assert distinct manifest labels, active values, capacities, dynamic exposure without restart, and unchanged authentication.
- Python: assert standalone Worker identity, Manager gate exclusion, conditional prompts, cross-role skip, thinking sanitization, rate-boundary termination, and admin STOP behavior.
- Regression: run focused Go tests, TeamHarness Python tests, and package validation before any public deployment.

## Rollback

The change requires no database migration. Roll back the TeamHarness package and pool fixture image to the prior versions, then restart the affected Manager and Worker runtimes. Prometheus stale series expire naturally; current-incident queries must always filter by manifest ID.
