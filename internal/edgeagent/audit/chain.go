// Package audit is the edge-side append-only ledger for Pi actions.
//
// Every mutating tool call Pi makes through edge is recorded here
// before the response is returned to the LLM. Each event carries:
//
//   - Sequence     — monotonic counter assigned at Append time.
//   - Timestamp    — wall-clock at append (UTC, second precision
//                    inside the hash; full nanos kept outside for
//                    forensics).
//   - Kind         — short class string ("tool.call.bash",
//                    "approval.consume", "session.fork", ...).
//   - EdgeID       — which edge produced it (multi-tenant replay
//                    protection).
//   - ProposalID   — empty for read-only events, the proposal id
//                    for events that flowed through the
//                    approval-token gate.
//   - Payload      — json.RawMessage; the tool-specific body
//                    (command + argv, file paths, restart unit).
//   - PrevHash     — hex HMAC-SHA256 of the previous event, or all
//                    zeros for the genesis event.
//   - Hash         — hex HMAC-SHA256 over the canonicalized event
//                    with Hash excluded (chicken-and-egg).
//
// The chain is tamper-evident: any edit to a historical event breaks
// the hash of every subsequent event. Verify walks the chain and
// rejects the first bad link. The cloud side will replay a snapshot
// of this chain on /v1/edge/audit and reject any divergence — see
// plan1.0.md §G-6.
//
// We deliberately do NOT include a network outbound for Append;
// Append is local + sync. The tunnel.v1.pi_audit uploader is a
// separate concern (P-6 second half) — it consumes Snapshot() and
// streams over geminio.
package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// MinKeyBytes is the floor for the HMAC key. 32 bytes matches the
// SHA-256 block size and forces operators to wire in a real key
// rather than reuse a short hostname.
const MinKeyBytes = 32

// Event is the wire shape persisted in the ledger. JSON tags are the
// canonical encoding for both the disk format and the HMAC input;
// changing a tag is a breaking change to the chain — bump a version
// field if you must.
type Event struct {
	Sequence   uint64         `json:"seq"`
	Timestamp  time.Time      `json:"ts"`
	Kind       string         `json:"kind"`
	EdgeID     string         `json:"edge_id"`
	ProposalID string         `json:"proposal_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	PrevHash   string         `json:"prev_hash"`
	Hash       string         `json:"hash"`
}

// canonicalEvent is the projection of Event that gets HMAC'd. It
// omits Hash (chicken-and-egg) and lowercases ProposalID into the
// canonical layout. Kept as a named type so the wire shape and the
// signed shape can diverge safely — never inline this struct into a
// public API.
type canonicalEvent struct {
	Sequence   uint64          `json:"seq"`
	Timestamp  string          `json:"ts"`
	Kind       string          `json:"kind"`
	EdgeID     string          `json:"edge_id"`
	ProposalID string          `json:"proposal_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	PrevHash   string          `json:"prev_hash"`
}

// Chain is the in-memory append-only ledger. It is safe for
// concurrent use; Append takes a mutex so two callers cannot mint
// the same Sequence. The persistent form is the []Event returned by
// Snapshot — the caller is responsible for fsync'ing it (we never
// touch the disk directly so we can run in a container without a
// writable /var).
type Chain struct {
	mu       sync.Mutex
	key      []byte
	edgeID   string
	seq      uint64
	prevHash []byte
	events   []Event
}

// NewChain returns a fresh chain rooted at genesis. If restored is
// non-empty it MUST verify cleanly against the same key — otherwise
// the restored state has been tampered with on disk and we refuse to
// continue (otherwise we'd mint events whose hash doesn't join the
// trusted history).
func NewChain(key []byte, edgeID string, restored []Event) (*Chain, error) {
	if len(key) < MinKeyBytes {
		return nil, fmt.Errorf("audit: key must be >= %d bytes (got %d)", MinKeyBytes, len(key))
	}
	if edgeID == "" {
		return nil, errors.New("audit: edgeID required")
	}
	c := &Chain{
		key:    append([]byte(nil), key...),
		edgeID: edgeID,
	}
	if len(restored) == 0 {
		// Genesis: prevHash is the all-zero 32-byte block.
		c.prevHash = make([]byte, sha256.Size)
		return c, nil
	}
	if err := Verify(restored, key); err != nil {
		return nil, fmt.Errorf("audit: restore failed verification: %w", err)
	}
	last := restored[len(restored)-1]
	c.seq = last.Sequence
	prev, err := hex.DecodeString(last.Hash)
	if err != nil {
		return nil, fmt.Errorf("audit: restore decode prev hash: %w", err)
	}
	c.prevHash = prev
	c.events = append(c.events, restored...)
	return c, nil
}

// Append signs a new event into the chain. kind must be non-empty;
// payload may be nil (we encode JSON null). proposalID is the empty
// string for non-approval-gated events. The returned Event is the
// signed form ready to ship.
func (c *Chain) Append(kind string, payload []byte, proposalID string) (Event, error) {
	if kind == "" {
		return Event{}, errors.New("audit: empty kind")
	}
	rawPayload := payload
	if len(rawPayload) == 0 {
		rawPayload = []byte("null")
	}
	// json.Validity check — a malformed payload would break the
	// canonical encoding later.
	if !json.Valid(rawPayload) {
		return Event{}, errors.New("audit: payload is not valid JSON")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.seq++
	e := Event{
		Sequence:   c.seq,
		Timestamp:  time.Now().UTC(),
		Kind:       kind,
		EdgeID:     c.edgeID,
		ProposalID: proposalID,
		Payload:    rawPayload,
		PrevHash:   hex.EncodeToString(c.prevHash),
	}
	digest, err := signEvent(c.key, e)
	if err != nil {
		return Event{}, err
	}
	e.Hash = hex.EncodeToString(digest)
	c.prevHash = digest
	c.events = append(c.events, e)
	return e, nil
}

// Len returns the current event count. Cheap; takes the mutex.
func (c *Chain) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// Snapshot returns a deep copy of all events. Safe to ship to disk
// or over the wire without worrying about concurrent Append.
func (c *Chain) Snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

// Last returns the most recent event, or zero-value + false if the
// chain is empty. Used by the cloud-side replay guard to detect
// split-brain / replay attempts.
func (c *Chain) Last() (Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return Event{}, false
	}
	return c.events[len(c.events)-1], true
}

// Verify walks every event in events and confirms (a) Sequence is
// dense (1, 2, 3, ...), (b) PrevHash matches the previous event's
// Hash (or the all-zero genesis), and (c) Hash recomputes under key.
// Returns nil on success; the first failure's index and reason on
// mismatch.
func Verify(events []Event, key []byte) error {
	if len(key) < MinKeyBytes {
		return fmt.Errorf("audit: key must be >= %d bytes", MinKeyBytes)
	}
	if len(events) == 0 {
		return nil
	}
	// Genesis: prevHash is the all-zero 32-byte block, matching the
	// initial state in NewChain when restored is empty.
	prev := make([]byte, sha256.Size)
	for i, e := range events {
		if e.Sequence != uint64(i+1) {
			return fmt.Errorf("audit: event %d sequence=%d (want %d)",
				i, e.Sequence, i+1)
		}
		if got, want := e.PrevHash, hex.EncodeToString(prev); got != want {
			return fmt.Errorf("audit: event %d prev_hash=%s (want %s)",
				i, got, want)
		}
		wantDigest, err := signEvent(key, e)
		if err != nil {
			return fmt.Errorf("audit: event %d canonicalize: %w", i, err)
		}
		if got := hex.EncodeToString(wantDigest); got != e.Hash {
			return fmt.Errorf("audit: event %d hash=%s (want %s)",
				i, e.Hash, got)
		}
		prev = wantDigest
	}
	return nil
}

// signEvent produces the canonical bytes and HMACs them with key.
// The canonical form excludes the Hash field itself and serialises
// Timestamp at second precision so that re-encoding the same logical
// event yields the same digest.
func signEvent(key []byte, e Event) ([]byte, error) {
	canon := canonicalEvent{
		Sequence:   e.Sequence,
		Timestamp:  e.Timestamp.UTC().Format(time.RFC3339),
		Kind:       e.Kind,
		EdgeID:     e.EdgeID,
		ProposalID: e.ProposalID,
		Payload:    e.Payload,
		PrevHash:   e.PrevHash,
	}
	// json.Marshal on a struct with json.RawMessage field passes the
	// raw bytes through verbatim — we still want the rest of the
	// fields deterministically ordered, which is what struct tags
	// give us.
	canonBytes, err := json.Marshal(canon)
	if err != nil {
		return nil, fmt.Errorf("audit: canonicalize: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(canonBytes)
	return mac.Sum(nil), nil
}