// Package pirpc speaks the real Pi coding-agent RPC protocol.
//
// # Why this package exists
//
// plan1.0.md §P-2/§P-3 assumed Pi could be supervised as an HTTP
// sidecar (`pi --mode http --bind 127.0.0.1 --port 19000` plus a
// `GET /health` probe). That mode does not exist. Verified against
// the published artefact `@earendil-works/pi-coding-agent@0.85.1`
// (`pi --help`, 2026-09-12):
//
//	--mode <mode>   Output mode: text (default), json, or rpc
//
// The only headless, bidirectional surface is `--mode rpc`: newline
// delimited JSON over the child's stdin/stdout. There is no socket,
// no port and no health endpoint. This package implements that
// protocol so the edge agent talks to Pi as it actually ships.
//
// # Protocol invariants this package upholds
//
// Taken from Pi's own docs/rpc.md ("Framing") and enforced by tests:
//
//  1. LF (\n) is the ONLY record delimiter. A trailing \r is
//     stripped. Unicode separators U+2028 / U+2029 are NOT record
//     delimiters — they are legal inside JSON strings, so a generic
//     line reader (Node's readline, Go's bufio.Scanner with a
//     Unicode-aware split) corrupts the stream. readLine reads
//     bytes up to '\n' and nothing else.
//
//  2. Records are unbounded in practice. A single `get_commands`
//     response on a host with a handful of skills installed exceeds
//     64 KiB, so the default limit here is 8 MiB and the reader
//     fails loudly (ErrLineTooLong) rather than silently splitting
//     a record.
//
//  3. Commands carry a client-generated `id`; the matching response
//     echoes it. Responses can interleave with events and with each
//     other, so correlation is by id, never by arrival order.
//
//  4. Dialog-class extension UI requests (select / confirm / input /
//     editor) BLOCK Pi until the client answers on stdin. An
//     unattended ops sidecar must therefore answer every one of
//     them. This package answers fail-closed: dialogs are
//     cancelled and confirmations are denied (see UIPolicy). An
//     extension that asks "allow this dangerous command?" must
//     never be auto-approved by the transport layer — approval is
//     the cloud reviewer's decision (plan1.0.md §6.5).
//
// # What this package deliberately does not do
//
// It owns no process lifecycle policy: restart backoff, crash-loop
// gating and upgrades stay in pisupervisor. Spawn is a thin helper
// for tests and for the supervisor's attach step. It also holds no
// opinion about which tools Pi may run — that is cmdpolicy's job.
package pirpc
