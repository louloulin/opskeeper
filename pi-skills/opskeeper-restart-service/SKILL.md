---
name: opskeeper-restart-service
description: Restart a systemd unit on the host via opskeeper-edge tools. Read-only diagnostics first; propose + receive approval token before any mutating action.
---

# opskeeper-restart-service

Restart a systemd service on the host using only `opskeeper-edge` HTTP
endpoints. All mutating actions go through `cmdpolicy.DefaultPiCapable()`
and require an `X-Opskeeper-Pi-Approval-Token` header — never shell out
directly.

## When to use

- An alert says a service is unhealthy AND the runbook lists "restart" as
  the first mitigation step.
- Postmortem RCA identifies the service as the proximate cause.
- A user explicitly asks "restart X" inside an incident chat.

Do NOT use for:

- Process restarts that aren't systemd units (call `host_processes` and
  talk to a human).
- Configuration reloads (use `opskeeper-host-files` for config edits, then
  trigger a reload through this skill only if the runbook says so).

## Workflow

1. **Pre-flight (read-only)** — call `GET /v1/edge/tools/host_processes`
   and `GET /v1/edge/tools/host_status?unit=<name>` to confirm the unit
   is loaded and confirm the failure mode. Don't skip this — restarting a
   half-dead unit without understanding why it died just hides the root
   cause.
2. **Recent logs** — call `GET /v1/edge/tools/journal?unit=<name>&lines=200`
   to grab the last 200 log lines. Quote the smoking-gun entry in the
   proposal so the reviewer can validate the diagnosis.
3. **Proposal** — POST to `http://127.0.0.1:9101/v1/edge/propose` with:
   ```json
   {
     "kind": "host_restart_service",
     "unit": "<name>",
     "rationale": "<one sentence citing the alert or log line>",
     "evidence": ["<journal line 1>", "<journal line 2>"],
     "rollback": "systemctl stop <name> || true"
   }
   ```
4. **Wait for approval** — the cloud reviewer worker will respond on
   `tunnel.v1.pi_approval_grant` with a one-shot approval token. Edge
   forwards it as `X-Opskeeper-Pi-Approval-Token`.
5. **Execute** — POST `http://127.0.0.1:9101/v1/edge/tools/host_restart_service`
   with header `X-Opskeeper-Pi-Approval-Token: <token>` and body
   `{"unit": "<name>", "mode": "try-restart"}`. Default mode is
   `try-restart`; only use `restart` (full stop+start) if the unit's
   `Restart=` policy is unset.
6. **Verify** — re-call `GET /v1/edge/tools/host_status?unit=<name>` and
   confirm `ActiveState=active` and the timestamp matches.
7. **Audit** — every step above is captured in `cmdpolicy` + the tunnel
   `pi_audit` stream. Do not suppress audit events.

## Failure modes

- **No approval token within 30 s** — abort. Re-check the proposal; do
  NOT retry silently. The reviewer may have rejected the action.
- **Edge returns 403 "token expired"** — the token TTL is 60 s. Re-propose
  if the rationale still holds.
- **Service still unhealthy after restart** — escalate via
  `opskeeper-incident-report`. Do not loop restart attempts.

## Deny-list aware

The `pi-yaml-hooks` deny-list (`dist/hooks/hooks.yaml`) intercepts the
shell-equivalent commands `systemctl restart <name>` and
`systemctl stop <name>` when they are issued outside the edge HTTP path.
You MUST route through the edge endpoints above; do not bypass with
`bash` tool.
