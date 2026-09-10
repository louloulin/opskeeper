---
name: opskeeper-diagnostics
description: Aggregate read-only host signals (dmesg, journal, top, iostat, netstat) via opskeeper-edge tools and produce a structured diagnostic bundle for review.
---

# opskeeper-diagnostics

Pull a coherent slice of host telemetry to support incident triage. All
calls in this skill are READ-ONLY — no approval token needed. Never
extend this skill to call mutating endpoints.

## When to use

- An alert just fired and you need a 60-second picture of the host.
- The reviewer worker asks for "more context" on a proposal.
- You are starting a `opskeeper-incident-report` and want a clean
  evidence base.

## Workflow

Issue the following calls in parallel (Promise.all-style) — they hit
different read endpoints and have no shared state:

1. **CPU / memory / load**
   `GET /v1/edge/tools/host_top?interval=1&samples=5`
2. **Disk I/O**
   `GET /v1/edge/tools/host_iostat?interval=2&samples=3`
3. **Filesystem usage**
   `GET /v1/edge/tools/host_disk_usage?warn_pct=85`
4. **Network sockets / listeners**
   `GET /v1/edge/tools/host_netstat?listen_only=true`
5. **Recent kernel ring buffer**
   `GET /v1/edge/tools/host_dmesg?since=15m&severity_at_least=warn`
6. **Recent journal (all units, last 15 min)**
   `GET /v1/edge/tools/journal?since=15m&max_lines=500`
7. **Process inventory**
   `GET /v1/edge/tools/host_processes?sort=memory&top=20`

## Output format

Combine into a single diagnostic bundle keyed by host_id + collected_at:

```json
{
  "host_id": "<edge_id>",
  "collected_at": "<RFC3339>",
  "triggers": ["<alert id>", "<runbook step>"],
  "signals": {
    "cpu":    { ... host_top response ... },
    "io":     { ... host_iostat response ... },
    "fs":     { ... host_disk_usage response ... },
    "net":    { ... host_netstat response ... },
    "dmesg":  { ... host_dmesg response ... },
    "journal":{ ... journal response ... },
    "procs":  { ... host_processes response ... }
  }
}
```

## Bundling rules

- **Always include timestamps** in every sub-section; future readers
  need to correlate signals across time.
- **Cap output**: `max_lines=500` for journal, `top=20` for processes.
  If the answer is "I need more", the right move is `since=5m` for a
  tighter window, not a larger cap.
- **Deduplicate journal**: collapse repeat `oom-kill` or `segfault`
  entries into `{count, last_seen, sample}` instead of dumping all 200
  copies.
- **Reference evidence by ID**: when you propose a fix in a follow-up
  skill, cite `diag.cpu.user_pct`, `diag.journal.<entry_id>`, etc.
  Don't re-quote raw output into a proposal — point to the bundle.

## Failure modes

- Any single endpoint returning 5xx → continue with the others, mark the
  failed one as `error: <status>` in the bundle; do not retry more than
  twice per call.
- Bundle > 256 KiB → drop `journal` lines to 100, drop `dmesg` severity
  floor to `err`. Do not split the bundle across multiple proposals.

## Hooks interaction

The `pi-yaml-hooks` deny-list does NOT block any of the read endpoints
above. If you find yourself wanting to call `bash` to read `/proc/*` or
`/var/log/*.gz`, you are doing it wrong — use the edge tools.
