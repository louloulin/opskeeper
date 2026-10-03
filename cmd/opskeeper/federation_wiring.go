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
// The registry is in memory and now has a Ledger underneath it, which is what
// makes a restart survivable. Without one the root came back believing it had
// never enrolled a cluster and refused every child's own still-valid
// provisioning token, and recovery meant re-enrolling them one at a time and
// handing each operator a new token. (Decision 145 measured that; the wire
// cannot say which of the two causes a refusal had, by design, so the log
// line at the hello boundary is where that shows up.)
//
// The file is the shipping Ledger because the supported topology is a single
// root process whose whole membership is a handful of rows. The port names
// the more interesting stores — Postgres, the audit chain — and neither is
// wired; switching is a change to the two lines below and nothing else,
// which is the point of the port existing.
//
// Restore is called before anything is served, and a failure to read the
// ledger is fatal. A root that started anyway with an empty membership would
// be doing exactly the thing the ledger prevents, silently, at the moment an
// operator is relying on it.
func newFederationWiring(fbClient *managersvcfb.Client, log *slog.Logger) (*federationWiring, error) {
	if fbClient == nil {
		return nil, fmt.Errorf("federation: no tunnel client to push over")
	}

	signer, err := federationReleaseSigner()
	if err != nil {
		return nil, err
	}

	// Declared as the interface and assigned conditionally on purpose. A
	// nil *FileLedger handed to NewRegistry would not be a nil Ledger —
	// an interface holding a nil pointer is not nil — so every
	// `r.ledger != nil` in the registry would be true and the first
	// enrolment would dereference nothing.
	var ledger fedbiz.Ledger
	if usable := federationLedger(log); usable != nil {
		ledger = usable
	}
	reg := fedbiz.NewRegistry(ledger)
	if ledger != nil {
		// Before the publisher, the link, and the routes. A ledger
		// that exists but cannot be read proves there was a membership
		// to lose, and starting with an empty one would re-enrol every
		// cluster and rotate every token. That is the one case where
		// refusing to start is the answer that cannot be wrong.
		fileLedger, _ := ledger.(*fedbiz.FileLedger)
		if err := reg.Restore(); err != nil {
			return nil, fmt.Errorf("federation: restore: %w (ledger: %s)", err, fileLedger.Path())
		}
		log.Info("federation: ledger loaded",
			slog.String("path", fileLedger.Path()),
			slog.Int("clusters", len(reg.Members())),
		)
	}
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

// federationLedgerPath is where the root's membership survives a restart.
//
// The default sits under /var/lib/opskeeper with the rest of the manager's
// durable state. It is overridable because a root running from a container
// image with a read-only root filesystem needs somewhere else to put it, and
// an operator who cannot move it cannot run a root at all.
func federationLedgerPath() string {
	return firstNonEmpty(os.Getenv("OPSKEEPER_FEDERATION_LEDGER"),
		"/var/lib/opskeeper/federation/ledger.json")
}

// federationLedger returns the ledger the root should keep its clusters in,
// or nil — with one loud line — when the configured path cannot be used.
//
// Degrading beats refusing here, and the reason is asymmetry. A root with no
// ledger is what this code did before the ledger existed: federation works,
// and a restart forgets. A root that refuses to wire federation because it
// cannot write a file has taken away a working feature over a durability
// detail, and it would do so on every read-only image and every deployment
// that has not mounted a volume. The operator is told exactly what is wrong
// and what to do about it, at boot, rather than finding out from a refusal
// during a rollout.
func federationLedger(log *slog.Logger) *fedbiz.FileLedger {
	ledger := fedbiz.NewFileLedger(federationLedgerPath())
	if err := ledger.Probe(); err != nil {
		log.Error("federation: the ledger path is not usable, so federation will not survive a restart",
			slog.String("path", ledger.Path()),
			slog.String("remedy", "make the path writable, or point "+
				"OPSKEEPER_FEDERATION_LEDGER at one that is"),
			slog.Any("err", err),
		)
		return nil
	}
	return ledger
}
