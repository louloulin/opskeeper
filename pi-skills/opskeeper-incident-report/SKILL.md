---
name: opskeeper-incident-report
description: Write a postmortem to the opskeeper incident store via edge tools. Read-only except for the postmortem commit itself, which is signed and audited.
---

# opskeeper-incident-report

Author an incident postmortem in the opskeeper incident store. Pulls
context from the active session, the diagnostic bundle (if
`opskeeper-diagnostics` ran first), and the audit trail.

## When to use

- An incident is moving to "resolved" state and the runbook says
  "postmortem within 24 h".
- The reviewer worker explicitly delegates postmortem authoring to you.
- A user pastes a `~postmortem` directive in the incident chat.

## Inputs

Required (gather before composing):

- `incident_id` — from the active session context (look for
  `INCIDENT_ID=` env or the most recent alert payload).
- `host_id` — the `edge_id` that owns the host.
- `timeline` — the tunnel `pi_audit` events for this incident
  (already in the session log if Pi has been running).
- `diag_bundle` — the JSON bundle from `opskeeper-diagnostics`, if it
  ran. If absent, fetch via the same skill now and attach as appendix.

## Workflow

1. **Compose the postmortem** — follow the seven-section template
   (summary / impact / timeline / root cause / contributing factors /
   what went well / what we'll change). Each section ≤ 200 words.
   Don't editorialize; cite evidence.
2. **Sign** — append a footer with `pi_session_id`, `pi_version`, and
   the SHA-256 of the postmortem body. The edge will sign the request
   with the local HMAC key.
3. **Submit** — POST `http://127.0.0.1:9101/v1/edge/postmortem`:
   ```json
   {
     "incident_id": "<id>",
     "host_id": "<edge_id>",
     "body_md": "<full markdown>",
     "body_sha256": "<hex>",
     "signing": {
       "pi_session_id": "<id>",
       "pi_version": "0.85.1"
     }
   }
   ```
4. **Acknowledge** — the edge returns `{ "postmortem_id": "...",
   "audit_event_id": "..." }`. Echo both back to the incident chat so
   the human reviewer can find the artifact.

## Output to the chat

```
postmortem_id: <id>
audit_event_id: <id>
sha256: <hex>
sections: 7
attachments: <diag_bundle_size> bytes
```

## Style rules

- No emojis. Pi's AGENTS.md forbids them globally.
- No first-person narratives ("we did X") — third-person impersonal
  ("edge agent invoked restart-service") reads cleaner in audit logs.
- Cite by ID, not by quoted block. References like
  `[diag.journal.entry_421]` survive log truncation better than
  embedded quotes.
- Never modify evidence to fit a narrative. If the data is ambiguous,
  say "ambiguous; see appendix A" rather than picking a side.

## Failure modes

- **No incident_id in context** — do NOT invent one. Ask the human
  reviewer to confirm which incident the postmortem belongs to.
- **Edge returns 409 (already exists)** — fetch the existing one,
  present the diff, ask whether to overwrite. Default: do not
  overwrite.
- **Postmortem body > 512 KiB** — split the timeline into an appendix
  attachment; the body should stay focused on the narrative.

## Hooks interaction

`pi-yaml-hooks` does not block postmortem writes (they're first-class
opskeeper state). Do not write the postmortem to disk via `bash`;
always go through `/v1/edge/postmortem` so the audit chain captures
it.
