package federation

import (
	"context"
	"errors"

	"github.com/vincent-wuhan/opskeeper/core/floor/federation"
)

// ErrNoReleaseKey means this root holds no key it can sign a policy with.
//
// It is a distinct error from ErrNothingToPublish, and the difference is the
// whole point of having it: ErrNothingToPublish says "I looked at the tree
// you named and could not put a signature on it", which is an operator who
// pointed at the wrong directory. This one says "this root cannot sign
// anything at all", which is a deployment that was never given a release
// key. Both are refusals, and neither is a bug in the request.
var ErrNoReleaseKey = errors.New("federation: this root holds no release key, so it can issue no policy")

// Service is the root's cluster federation, composed.
//
// It exists because the two halves have genuinely different availability. The
// registry needs nothing but memory: a root with no release key can still
// enrol a child, still remember what it last heard, and still answer "what is
// this cluster enforcing" as long as the child said hello. The publisher
// needs the release key, and a root without one can do exactly one thing less:
// issue a decision.
//
// Folding those into one type with a nil publisher means the degradation is a
// property of the design rather than something the assembly root has to
// remember. The alternative — refusing to construct a publisher at all — turns
// a missing key into a control plane that will not start, which is the
// difference between "one feature is off" and "the platform is down".
type Service struct {
	reg *Registry
	pub *Publisher
}

// NewService composes the registry and, when there is a release key, the
// publisher.
//
// pub may be nil, and that is the keyless root. reg may not: a service with
// no registry cannot answer who it has enrolled, which is the one question it
// must always be able to answer.
func NewService(reg *Registry, pub *Publisher) (*Service, error) {
	if reg == nil {
		return nil, errors.New("federation: a service needs a registry")
	}
	return &Service{reg: reg, pub: pub}, nil
}

// Registry exposes the membership this service answers from, so a caller that
// has a Service does not also have to be handed a Registry.
func (s *Service) Registry() *Registry { return s.reg }

// CanPublish reports whether this root can issue a policy at all.
//
// It is a question the console asks rather than one it infers from a failed
// publish, because "this root cannot sign" and "the tree you named is not a
// package" send an operator to different pages.
func (s *Service) CanPublish() bool { return s.pub != nil }

// Enroll provisions a child cluster and returns its provisioning token, once.
func (s *Service) Enroll(id federation.ClusterID, name string) (string, error) {
	return s.reg.Enroll(id, name)
}

// Members lists every enrolled child, in identity order.
func (s *Service) Members() []Member { return s.reg.Members() }

// Member is one child's ledger state.
func (s *Service) Member(id federation.ClusterID) (Member, bool) { return s.reg.Member(id) }

// Publish issues the next policy version to one cluster.
//
// With no release key it refuses before touching the registry, so a keyless
// root cannot spend a version number on a decision it was never able to make.
// The ledger is the thing that must not lie here: HighestIssued is what the
// monotonic guarantee is built on, and burning one on a refusal would leave
// the cluster permanently one version ahead of any policy it can receive.
func (s *Service) Publish(ctx context.Context, id federation.ClusterID, req PublishRequest) (PublishResult, error) {
	if s.pub == nil {
		return PublishResult{}, ErrNoReleaseKey
	}
	return s.pub.Publish(ctx, id, req)
}

// Acknowledge records what a child answered about a version.
func (s *Service) Acknowledge(id federation.ClusterID, out federation.Outcome) error {
	return s.reg.Acknowledge(id, out)
}
