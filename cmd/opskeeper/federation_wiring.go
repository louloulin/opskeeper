package main

// The root's cluster federation, composed in one file.
//
// It is one file rather than a block in main() because what it assembles has
// four parts that must agree with each other — the registry, the publisher
// that may not exist, the tunnel-side binding table, and the HTTP routes —
// and the failure mode of getting one of them wrong is not a crash. It is a
// control plane that enrols child clusters and then quietly cannot reach any
// of them.

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/vincent-wuhan/opskeeper/core/floor/pluginmanifest"

	fedbiz "github.com/vincent-wuhan/opskeeper/core/manager/biz/federation"
	managerserverfed "github.com/vincent-wuhan/opskeeper/core/manager/server/federation"
	managersvcfedlink "github.com/vincent-wuhan/opskeeper/core/manager/service/federationlink"
	managersvcfb "github.com/vincent-wuhan/opskeeper/core/manager/service/frontierbound"
)

const (
	// federationReleaseKeyEnv carries the root's ed25519 seed, base64, and
	// is what lets this root sign a policy bundle at all.
	//
	// It is an env var rather than a settings row on purpose, and the
	// reason is the plan's own constraint about not creating a second
	// source of truth for credentials: a signing *identity* is not a
	// setting an operator edits in a console, and putting it in the same
	// table as display preferences would make "who am I" a runtime
	// preference. It stays unset on every root that does not federate,
	// which is the shape a single-cluster deployment has.
	federationReleaseKeyEnv = "OPSKEEPER_FEDERATION_RELEASE_KEY"
	// federationKeyIDEnv names the key. It is a label, not authority: a
	// child that does not hold this key refuses the bundle whatever the
	// root calls it.
	federationKeyIDEnv = "OPSKEEPER_FEDERATION_KEY_ID"
	// defaultFederationKeyID matches the id the shipped test keys use, so
	// an operator who sets only the seed does not also have to remember
	// to set a name.
	defaultFederationKeyID = "release-2026"
	// federationArtifactDirEnv is where this root writes a policy tree so
	// a child can fetch it.
	//
	// A file:// source is the zero-configuration path, and it is not a
	// placeholder: a root and its children sharing a mount is the shape
	// most on-prem multi-cluster deployments already have, and it needs no
	// web server. A deployment that fronts this directory over https
	// instead would have a Distributor of its own; this is the one that
	// works with nothing configured but a path.
	federationArtifactDirEnv = "OPSKEEPER_FEDERATION_ARTIFACT_DIR"
	// federationArtifactPrefixEnv is the path the *child* sees this
	// directory at, when it is not the same one.
	//
	// It is separate because those two are genuinely different in every
	// deployment where they differ — a container mount, a chroot, an NFS
	// path the operator chose to spell differently — and one variable
	// cannot be both the write path and the read path.
	federationArtifactPrefixEnv = "OPSKEEPER_FEDERATION_ARTIFACT_PREFIX"
)

// federationWiring is the assembled root side of the cluster channel.
type federationWiring struct {
	// handler serves the console routes. It is never nil: a root with no
	// release key still enrols children and answers what they enforce,
	// so mounting the routes is always right.
	handler *managerserverfed.Handler
	// link is the tunnel-side binding table. It is also the
	// ClusterHelloHandler the tunnel dispatches to, and the thing
	// subscribed to caller disconnections.
	link *managersvcfedlink.Links
	// service is the registry plus, when a release key was configured,
	// the publisher.
	service *fedbiz.Service
	// canPublish is the one line an operator's log needs on boot, because
	// "federation is mounted but cannot sign" is otherwise invisible.
	canPublish bool
	// canDeliver is the same for the other half. A root that can sign and
	// cannot deliver produces a version every time and tells no cluster
	// anything, and that reads as a working console until an operator
	// checks a cluster.
	canDeliver bool
	// artifactDir is where trees are written, empty when delivery is off.
	artifactDir string
}

// newFederationWiring assembles the root side.
//
// fbClient may be a disabled tunnel: the binding table is still built, the
// routes are still mounted, and every push fails with the tunnel's own
// disabled error rather than with a 404. A root whose tunnel is down is a
// root that has lost its way to its children, not a root that never had one.
//
// The registry is in memory, and until a Ledger implementation ships that is
// an availability problem rather than a bookkeeping one: a root that
// restarts forgets every member, so each child cluster is refused on its next
// hello with its own still-valid provisioning token, and recovery means
// re-enrolling them one at a time and handing each operator a new token.
// (Decision 145 measured this; the wire cannot say which of the two causes a
// refusal had, by design, so the log line at the hello boundary is where that
// shows up.)
//
// The Ledger port exists for exactly this and no implementation of it ships
// yet. Wiring one is a data-layer change with no bearing on anything above
// it, which is the point of it being a port — but "no bearing on anything
// above it" is not the same as "nothing to do", and the comment above used to
// read as though it were.
func newFederationWiring(fbClient *managersvcfb.Client, log *slog.Logger) (*federationWiring, error) {
	if fbClient == nil {
		return nil, fmt.Errorf("federation: no tunnel client to push over")
	}

	signer, err := federationReleaseSigner()
	if err != nil {
		return nil, err
	}

	reg := fedbiz.NewRegistry(nil)
	var pub *fedbiz.Publisher
	if signer != nil {
		if pub, err = fedbiz.NewPublisher(reg, signer); err != nil {
			return nil, err
		}
	}
	svc, err := fedbiz.NewService(reg, pub)
	if err != nil {
		return nil, err
	}

	link, err := managersvcfedlink.NewLink(fbClient, reg, managersvcfedlink.WithLogger(log))
	if err != nil {
		return nil, err
	}

	// Delivery is wired after the link because it is the link that carries
	// the push, and a distributor built first would be a tree with nowhere
	// to go.
	dist, err := federationDistributor()
	if err != nil {
		return nil, err
	}
	// The nil check is here and not only inside SetDelivery, and the reason
	// is worth stating because it is a Go trap rather than a design point:
	// federationDistributor returns a *FileDistributor, and handing a nil
	// one of those to an interface parameter produces a non-nil interface
	// holding a nil pointer. Every `svc.dist == nil` downstream would be
	// false and the first publish would dereference it.
	if dist == nil {
		log.Warn("federation: no artifact directory configured — versions can be issued and read, but no child can be told about one",
			slog.String("env", federationArtifactDirEnv),
		)
		svc.SetDelivery(link, nil, nil)
	} else {
		svc.SetDelivery(link, dist, dist)
	}
	// A caller that goes away must stop being reachable, because the
	// broker will hand the same number to somebody else. This is the one
	// subscription in the whole channel and it has no fallback: forget
	// it and a push eventually lands on a process that never said hello.
	fbClient.OnEdgeOffline(func(edgeID uint64) {
		if n := link.Forget(edgeID); n > 0 {
			log.Info("federation: released cluster bindings",
				slog.Uint64("caller", edgeID),
				slog.Int("clusters", n),
			)
		}
	})

	handler := managerserverfed.NewHandler(svc)
	handler.SetPusher(link)

	w := &federationWiring{
		handler:     handler,
		link:        link,
		service:     svc,
		canPublish:  svc.CanPublish(),
		canDeliver:  dist != nil,
		artifactDir: "",
	}
	if dist != nil {
		w.artifactDir = dist.Dir()
	}
	if !w.canPublish {
		log.Warn("federation: mounted without a release key — clusters can be enrolled and read, but no policy can be issued",
			slog.String("env", federationReleaseKeyEnv),
		)
	} else {
		log.Info("federation: release key loaded",
			slog.String("key_id", signer.KeyID()),
		)
	}
	if w.canDeliver {
		log.Info("federation: policy delivery configured",
			slog.String("artifact_dir", w.artifactDir),
		)
	}
	return w, nil
}

// federationDistributor builds the root's artifact distributor, or nil when
// no directory is configured.
//
// Nil is the answer for a root that federates membership but not policy, and
// it is a legitimate state rather than a misconfiguration: a single-cluster
// deployment, or one that wants the console to show what its children are
// enforcing without any way to change it. A publish on such a root still
// issues a version and reports that nobody was told, which is the honest
// pair of facts.
//
// A directory that is configured but unusable is an error, for the same
// reason a release key that will not load is: a deployment that was handed
// a path and cannot write to it has a real misconfiguration, and running on
// without saying so would leave an operator believing policy is being
// delivered when none is.
func federationDistributor() (*fedbiz.FileDistributor, error) {
	dir := strings.TrimSpace(os.Getenv(federationArtifactDirEnv))
	if dir == "" {
		return nil, nil
	}
	return fedbiz.NewFileDistributor(dir, os.Getenv(federationArtifactPrefixEnv))
}

// federationReleaseSigner loads the root's signing key, or nil when none is
// configured.
//
// A configured key that does not load is an error rather than a nil: a
// deployment that was handed a key and cannot use it has a real
// misconfiguration, and answering that by quietly running a keyless root
// would leave an operator believing policy is being signed when none is.
func federationReleaseSigner() (*pluginmanifest.Signer, error) {
	raw := strings.TrimSpace(os.Getenv(federationReleaseKeyEnv))
	if raw == "" {
		return nil, nil
	}
	keyID := strings.TrimSpace(os.Getenv(federationKeyIDEnv))
	if keyID == "" {
		keyID = defaultFederationKeyID
	}
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("federation: %s is not base64: %w", federationReleaseKeyEnv, err)
	}
	signer, err := pluginmanifest.NewSigner(keyID, ed25519.PrivateKey(seed))
	if err != nil {
		return nil, fmt.Errorf("federation: %s is not a usable release key: %w", federationReleaseKeyEnv, err)
	}
	return signer, nil
}
