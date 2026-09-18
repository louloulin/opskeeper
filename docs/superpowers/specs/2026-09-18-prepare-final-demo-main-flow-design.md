---
comet_change: prepare-final-demo-main-flow
role: technical-design
canonical_spec: openspec
---

# Prepare Final Demo Main Flow — Technical Design

## Context

The final demonstration uses one PostgreSQL application connection-pool exhaustion incident. `home.yueming.xin` is both a lightweight business surface and the presenter console. The page shell and static information remain available while database-backed order, inventory, and audit cards degrade. The presenter can switch to monitoring to confirm pool pressure, then to the AgentTeams Element room to observe Manager-led diagnosis, preview evidence, and human approval.

The authoritative requirements live in `openspec/changes/prepare-final-demo-main-flow/`. Structured repair-preview storage, PASS/FAIL gating, and Archive readback are owned by the separate `repair-preview-readback` change; this change consumes that capability and does not create a second preview fact source.

## Architecture

### Manager-controlled scenario

Add a Manager demo-scenario endpoint for the PostgreSQL pool-exhaustion case. The endpoint accepts a scenario ID, idempotency key, target fingerprint, duration, and blast-radius declaration. Manager validates the allowlisted scenario, creates or returns the idempotent incident, persists the scenario-to-incident mapping, and starts the existing pool fixture. Home never connects to the fixture directly and never receives database or fixture credentials.

Prometheus continues to evaluate and deliver the alert independently. Manager correlates the incoming alert fingerprint with the active scenario and incident rather than creating a duplicate incident. Once correlated, Manager dispatches the read-only investigator automatically.

### Real business query path

For the shortest competition-safe delivery path, extend the pool-fixture process with three business query endpoints:

- order summary;
- inventory summary;
- audit-event summary.

They execute distinct SQL views or queries but share the same application PostgreSQL pool that the scenario saturates. Each endpoint has a bounded server timeout and returns no-store. Home's browser UI calls same-origin site APIs; the Next.js server proxies those calls to Manager, and Manager calls the fixture through its existing authenticated internal path. This keeps credentials server-side and preserves a single application connection path.

The three cards poll independently and render independent latency, data, timeout, and degraded states. They do not turn the entire page into a failure state. After remediation, the same APIs and pool path must return normal data; a fallback data path would invalidate the demonstration.

### Presenter surface

`home.yueming.xin/live-incident` shows:

- normal baseline and business query results;
- one-click scenario injection with confirmation and target/duration readback;
- scenario, incident, alert, diagnosis, preview, approval, repair, verification, and archive stages;
- compact baseline/Candidate A/Candidate B decision evidence before HITL;
- links to monitoring, Element, Dashboard, and preview reports;
- presenter prompts only where a human action is genuinely required.

Manager emits the existing `agentteams.workflow` projections so Element and Dashboard can display the same progression without host-code changes.

### Repair preview and approval gate

Preview execution occurs before the audience-facing rehearsal and is bound to the selected incident. At the approval gate, home and the room show a compact decision card:

- baseline saturation metrics;
- Candidate A PASS, therefore eligible for HITL;
- Candidate B FAIL, therefore rejected before HITL;
- replay fingerprint and controlled-load boundary.

Passing preview does not approve the change. The human approval remains bound to incident ID, candidate ID, execution ID, target fingerprint, scope, parameters, and expiry. Candidate B cannot enter the formal-change request.

### Reliability boundary

The public environment remains single-host Docker reliability hardening, not database HA. Control-plane state, scenario mappings, incident progress, approval state, and execution state persist in Manager storage. Restarting Manager during pending approval or verification must preserve progress. Repair retries validate execution ID and target fingerprint to prevent duplicate execution. The fixture retains TTL and recovery paths so a failed demonstration does not leave saturation active.

## Error Handling

- Scenario start is idempotent: repeated submission returns the existing active incident.
- Alert correlation failure leaves the scenario visible as `awaiting_alert`; it does not silently create a second incident.
- Business-card timeouts render card-local degraded states while the page shell remains usable.
- Preview metrics with inconsistent replay profiles are marked `NOT COMPARABLE` and cannot become approval evidence.
- Preview failure blocks HITL rather than falling back to an unmeasured formal proposal.
- Repair target or fingerprint mismatch rejects execution and records the safety failure.

## Testing Strategy

### Unit and integration

- Manager scenario validation, idempotency, incident binding, alert correlation, and restart retention.
- Pool-fixture business query success, saturation timeout, recovery, and TTL cleanup.
- Preview gate behavior for PASS, FAIL, and `NOT COMPARABLE`.
- Approval binding for incident, candidate, execution, target fingerprint, scope, and expiry.
- Frontend card isolation, no-store behavior, stage rendering, and external links.

### End-to-end

Run the public flow:

1. home baseline;
2. one-click injection;
3. order/inventory/audit degradation;
4. monitoring confirms 4/4 pool pressure;
5. alert correlates to the same incident;
6. Manager dispatches diagnosis automatically;
7. compact preview decision card appears;
8. human approves Candidate A only;
9. repair executes once;
10. monitoring and a sustained verification window confirm recovery;
11. home queries return normal data through the same path;
12. Archive exposes the complete evidence chain and candidate comparison.

## Delivery Risks

- The exited investigator worker is a P0 blocker and must be restored before E2E.
- Business-card caching would hide the incident; all card responses must be no-store.
- Alert fingerprint drift would duplicate incidents or leave the scenario awaiting alert.
- Coupling business queries to pool-fixture is an explicit short-term demo tradeoff; the production-oriented evolution is a separate `home-app` with an independently deployed business database.
- Live preview execution should remain a Q&A replay; the rehearsed main flow displays the incident-bound readback.
