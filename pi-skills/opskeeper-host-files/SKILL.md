---
name: opskeeper-host-files
description: Read or edit host files via opskeeper-edge tools. Read-only is direct; writes go through propose + approval token. Never write outside an allow-listed path.
---

# opskeeper-host-files

Read or modify a host file using `opskeeper-edge` HTTP endpoints. Reads
are direct (no approval). Writes require a proposal + one-shot approval
token.

## When to use

- A runbook asks you to "verify the contents of /etc/..." or "patch X in
  /etc/...".
- You need a config snapshot to attach to a postmortem.
- An RCA suspects a misconfiguration (typo, stale value, missing key).

Do NOT use for:

- Files outside the configured allow-list (`OPSKEEPER_PI_FILE_ALLOWLIST`,
  default: `/etc/opskeeper/**`, `/var/log/opskeeper/**`,
  `/opt/opskeeper/**`). Files outside the allow-list return 403 from
  `cmdpolicy`; do not retry.
- Bulk edits across many hosts (use `opskeeper-edge` `batch` API, not
  this skill).
- Anything under `/proc`, `/sys`, or `/var/lib/docker` (read-only via
  `host_diagnostics` only).

## Workflow — read

```
GET http://127.0.0.1:9101/v1/edge/tools/host_files?path=<abs-path>
```

Optional `?max_bytes=65536` to cap. If the file is larger, the response
includes a `truncated: true` flag and a `next_offset` you can pass as
`?offset=<n>` to read the next chunk.

## Workflow — write

1. **Snapshot first** — call the read endpoint, save the contents to the
   session log (not committed; just held in context). The audit record
   needs the pre-state to make the diff reviewable.
2. **Compute the new content** — small enough that a reviewer can read
   it inline; otherwise split into a sequence of single-purpose writes.
3. **Propose** — POST `/v1/edge/propose`:
   ```json
   {
     "kind": "host_file_write",
     "path": "<abs-path>",
     "current_sha256": "<sha of pre-state>",
     "new_sha256": "<sha of new content>",
     "diff": "<unified diff or full new content for small files>",
     "rationale": "<why this change>"
   }
   ```
4. **Wait for approval token** — same tunnel round-trip as
   `opskeeper-restart-service`.
5. **Apply** — POST `http://127.0.0.1:9101/v1/edge/tools/host_files`:
   ```json
   {
     "path": "<abs-path>",
     "mode": "0600",
     "owner": "root:root",
     "content_b64": "<base64 of new content>"
   }
   ```
   with header `X-Opskeeper-Pi-Approval-Token: <token>`.
6. **Verify** — re-read the file and confirm the SHA matches the
   proposed `new_sha256`.
7. **Audit** — the edge emits an HMAC audit event with the full diff;
   the cloud mirrors it via `tunnel.v1.pi_audit`.

## Forbidden patterns

- Writing to a path that resolves through a symlink — `cmdpolicy` blocks
  this with `symlink_in_path` error.
- Writing `.env` files anywhere — explicitly denied by
  `pi-yaml-hooks` (rule `deny.env-write`).
- Editing `node_modules/**` — explicitly denied by `pi-yaml-hooks` (rule
  `deny.node_modules-write`); use your package manager instead.
- Writing to `/etc/shadow`, `/etc/passwd`, `/etc/sudoers.d/**`,
  `/boot/**`, `/var/lib/docker/**` — hard-deny at the cmdpolicy layer.
