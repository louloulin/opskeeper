package federation

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/floor/pluginmanifest"
)

// ErrNotStaged means the tree this bundle refers to is not on this machine
// yet.
//
// It is the one failure that is *not* remembered, and the distinction is the
// whole of this type's error handling. A transport problem is not a decision:
// the same bytes will arrive again and this time they will be applied. A policy
// refusal and a malformed bundle are decisions, they are recorded against the
// version, and a repeat of the same version replays the same answer instead of
// running the check a second time.
var ErrNotStaged = errors.New("federation: policy tree not staged yet")

// ErrVersionRegressed means the bundle is older than what this cluster is
// already enforcing.
//
// The message says "regressed" rather than "already seen" because it covers
// two cases that look identical from here: a replay of a bundle that has since
// been superseded, and a root that restarted its numbering. The child cannot
// tell them apart, and in both cases going backwards is the wrong answer, so
// both are refused with the same word. A root that legitimately needs to
// renumber does not send a lower number; an operator resets the floor on the
// child (Receiver.AdoptFloor), because that is a decision about this machine
// and no message from a peer should be able to make it.
var ErrVersionRegressed = errors.New("federation: bundle version is not newer than the enforced policy")

// Outcome is what happened to one version, and is what a retry of that version
// is answered with.
type Outcome struct {
	// Version is the bundle this outcome is about.
	Version uint64
	// Accepted is true when this receiver switched to that version. It is
	// false both for a malformed bundle and for a policy refusal, because
	// the caller's next move is the same in each case: do not retry.
	Accepted bool
	// Live is the version in force when this call returned, and it is
	// refreshed on every call including a redelivery. It is never a
	// historical value: a root that read a stale Live would conclude the
	// cluster had gone back to a version it left days ago.
	//
	// On a refusal it is the *previous* version, which is the answer an
	// operator needs: the cluster is still enforcing something, and it is
	// not this.
	Live uint64
	// Superseded is true when a version is replayed after the cluster has
	// moved past it. The replay is not an error — it is a decision that
	// was already made, delivered twice — but it is also not a no-op the
	// caller should ignore, because the answer to "is this cluster on
	// version 6?" is no, and without this field the replayed outcome would
	// read as a cheerful yes.
	Superseded bool `json:"superseded,omitempty"`
	// Reason explains a refusal in one line, for the log and the ack.
	Reason string `json:"reason,omitempty"`
	// At is when the decision was made.
	At time.Time `json:"at"`
}

// Switcher makes a staged policy tree the live one.
//
// The contract is one sentence: on error, the previously live tree is still
// live. That is why Receiver verifies and reviews before it calls Switch and
// why Switch is not given a rollback method — a swap that cannot promise
// atomicity cannot be made safe by a compensating action after the fact.
type Switcher interface {
	// Switch promotes staged to live and returns the path now live.
	Switch(staged string) (live string, err error)
	// Live reports the path currently in force, empty when none is.
	Live() string
}

// Receiver is the child cluster's side of federation.
//
// It is a state machine with memory, and the memory is the feature. A child
// that has enforced policy v7 and then lost its root is still enforcing v7,
// refuses v6 as a replay, and can answer "what are you running?" while
// disconnected. That is the plan's "子集群可独立运行" made concrete: the
// answer to a question the root cannot answer is one the child already has.
type Receiver struct {
	clusterID ClusterID
	trust     *pluginmanifest.TrustStore
	policy    pluginmanifest.Policy
	switcher  Switcher
	now       func() time.Time

	mu      sync.Mutex
	live    uint64
	liveLog Bundle
	// seen holds the decision for every version this receiver has reached
	// one about, accepted or not. It is what makes a retry idempotent and a
	// replay inert.
	seen map[uint64]decision
}

// decision is a remembered outcome together with the error that came with it.
//
// The error is stored rather than reconstructed from Reason because a refusal
// replayed as a *success* is the worst possible bug in this file: the caller
// checks err, sees nil, and concludes the cluster adopted a policy it refused.
// Remembering the error is what stops a redelivery from inverting a decision.
type decision struct {
	out Outcome
	err error
}

// NewReceiver builds a Receiver for one cluster.
//
// trust and policy are the child's own, not the root's: a root that could
// choose the key or the policy it is judged against would make verification
// decorative. now is a field so the decision timestamps in the audit are
// testable.
func NewReceiver(clusterID ClusterID, trust *pluginmanifest.TrustStore, policy pluginmanifest.Policy, sw Switcher) (*Receiver, error) {
	if !clusterID.Valid() {
		return nil, fmt.Errorf("federation: %q is not a cluster identity", clusterID)
	}
	if sw == nil {
		return nil, errors.New("federation: a receiver needs something that can switch a tree")
	}
	return &Receiver{
		clusterID: clusterID,
		trust:     trust,
		policy:    policy,
		switcher:  sw,
		now:       time.Now,
		seen:      map[uint64]decision{},
	}, nil
}

// Adopter is the optional interface a Switcher implements to support a
// deliberate floor change.
//
// It exists for the operational case the version rule otherwise blocks: a
// root that was rebuilt and restarted its numbering, or a child that was
// re-provisioned from a backup and holds a floor its root has never heard of.
// The fix is a person, on this machine, saying so — because every alternative
// is a remote party choosing what this cluster enforces, which is the thing
// the whole package is built to prevent.
type Adopter interface {
	AdoptFloor(version uint64, reason string) error
}

// AdoptFloor resets this receiver's floor to version, discarding the record of
// everything at or below it.
//
// It refuses to move the floor backwards, because the one legitimate caller —
// an operator resynchronising after a root rebuild — is going forwards, and a
// method that can walk policy backwards is a method someone will eventually
// call from a handler.
func (r *Receiver) AdoptFloor(version uint64, reason string) error {
	if version < MinBundleVersion {
		return fmt.Errorf("federation: floor %d is below the first publishable version %d", version, MinBundleVersion)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if version < r.live {
		return fmt.Errorf("federation: refusing to move the enforced floor from %d back to %d (%s)",
			r.live, version, reason)
	}
	for v := range r.seen {
		if v <= version {
			delete(r.seen, v)
		}
	}
	r.live = version
	if reason != "" {
		r.liveLog.Reason = reason
	}
	return nil
}

// Live reports the version this cluster is enforcing, and the bundle that put
// it there.
func (r *Receiver) Live() (uint64, Bundle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live, r.liveLog
}

// Seen reports the recorded outcome for a version, if this receiver has
// reached a decision about it.
func (r *Receiver) Seen(version uint64) (Outcome, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.seen[version]
	if !ok {
		return Outcome{}, false
	}
	out := d.out
	out.Live = r.live
	out.Superseded = r.live != version
	return out, true
}

// Apply is the child answering a push.
//
// The order is the design: remember-check, shape, staging, signature,
// admission, switch, remember. Nothing that can fail after the switch is
// allowed to fail before it, and nothing that would move the cluster off its
// last good policy is allowed to run at all once the switch has begun.
func (r *Receiver) Apply(b Bundle, stagedRoot string) (Outcome, error) {
	// 1. A version already decided is answered from the record. This is
	//    what makes a redelivery idempotent and a replay inert, and it is
	//    checked before anything else so a retry cannot re-run a decision
	//    through a path that has since been changed.
	r.mu.Lock()
	if prior, ok := r.seen[b.Version]; ok {
		// Answer from the record, with Live refreshed to what is in force
		// now and Superseded saying whether this version still is.
		out := prior.out
		out.Live = r.live
		out.Superseded = r.live != b.Version
		r.mu.Unlock()
		return out, prior.err
	}
	if b.Version < MinBundleVersion || b.Version <= r.live {
		live := r.live
		r.mu.Unlock()
		// Below the floor and never seen: a replay of a superseded
		// policy, or a root that restarted its numbering. Both are
		// refused, and neither is recorded — a root that is merely
		// confused should be able to keep pushing and be told so each
		// time, rather than being permanently locked out by a record.
		return Outcome{Version: b.Version, Live: live, At: r.now()}, ErrVersionRegressed
	}
	r.mu.Unlock()

	// 2. The claim's own fields. A bundle addressed to another cluster is
	//    refused before its tree is looked at.
	if b.ClusterID != r.clusterID {
		return Outcome{Version: b.Version, Live: r.liveVersion(), At: r.now()},
			fmt.Errorf("%w: bundle is addressed to cluster %q, this is %q", ErrMalformedBundle, b.ClusterID, r.clusterID)
	}
	env, err := b.Validate()
	if err != nil {
		return r.record(b.Version, false, err.Error(), err), err
	}

	// 3. The tree. Absent is a transport problem, not a decision, so it is
	//    the one failure that leaves no record and the root may retry.
	if stagedRoot == "" {
		return Outcome{Version: b.Version, Live: r.liveVersion(), At: r.now()}, ErrNotStaged
	}
	if _, statErr := filepath.Abs(stagedRoot); statErr != nil {
		return Outcome{Version: b.Version, Live: r.liveVersion(), At: r.now()},
			fmt.Errorf("%w: %v", ErrNotStaged, statErr)
	}
	// The envelope in the bundle and the sidecar in the tree are two
	// copies of one signature. pluginmanifest already refuses a pair that
	// disagree; the reason it matters here is that the bundle's copy is
	// what an operator reads when they ask "what is this cluster
	// running", so a child that applied a tree whose signature was not
	// the one it was handed would be able to answer that question wrongly.
	sidecar, err := pluginmanifest.VerifyDir(stagedRoot, r.trust)
	if err != nil {
		return r.record(b.Version, false, "signature: "+err.Error(), err), err
	}
	if sidecar.KeyID != env.KeyID || sidecar.TreeDigest != env.TreeDigest {
		mismatch := fmt.Errorf("%w: bundle carries an envelope for %s/%s but the staged tree is signed for %s/%s",
			ErrMalformedBundle, env.KeyID, shortDigest(env.TreeDigest), sidecar.KeyID, shortDigest(sidecar.TreeDigest))
		return r.record(b.Version, false, "envelope does not match the tree's sidecar", mismatch), mismatch
	}

	// 4. Admission. Signed is not the same as permitted: a package the
	//    release key vouched for can still exceed what this cluster's own
	//    policy allows it to hold.
	if d := pluginmanifest.Review(stagedRoot, r.trust, r.policy); !d.Allowed {
		refused := fmt.Errorf("federation: cluster %q refuses %s: %s", r.clusterID, b, d)
		return r.record(b.Version, false, "admission: "+d.String(), refused), refused
	}

	// 5. The switch, and only now.
	if _, err := r.switcher.Switch(stagedRoot); err != nil {
		// The contract says the previous tree is still live, so this is
		// recorded as a refusal at the old version rather than a partial
		// success. A recorded refusal is final for this version: the
		// root publishes a new version carrying the fix.
		return r.record(b.Version, false, "switch: "+err.Error(), err), err
	}

	r.mu.Lock()
	r.liveLog = b
	r.mu.Unlock()
	return r.record(b.Version, true, "applied", nil), nil
}

// shortDigest trims a hex tree digest to something a log line can carry.
func shortDigest(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}

// record remembers a decision and returns it.
//
// The caller passes the error it is about to return so that the same decision
// can be replayed with the same answer, including the same nil-ness. Everything
// is written under one lock so that Live can never be a value from before the
// decision that changed it.
func (r *Receiver) record(version uint64, accepted bool, reason string, cause error) Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	if accepted {
		r.live = version
	}
	o := Outcome{
		Version:  version,
		Accepted: accepted,
		Live:     r.live,
		Reason:   reason,
		At:       r.now(),
	}
	r.seen[version] = decision{out: o, err: cause}
	return o
}

// liveVersion reports the enforced version, for the paths that need only the
// number and must not also copy the bundle out from under the lock.
func (r *Receiver) liveVersion() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live
}
