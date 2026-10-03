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
}

// newFederationWiring assembles the root side.
//
// fbClient may be a disabled tunnel: the binding table is still built, the
// routes are still mounted, and every push fails with the tunnel's own
// disabled error rather than with a 404. A root whose tunnel is down is a
// root that has lost its way to its children, not a root that never had one.
//
// The registry is in memory. That is a real limitation rather than a
// simplification — a root that restarts forgets which versions it issued,
// and the monotonic guarantee only survives a restart through a durable
// Ledger — and it is why the Ledger port exists and no implementation of it
// ships yet. Wiring it is a data-layer change with no bearing on anything
// above it, which is the point of it being a port.
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
		handler:    handler,
		link:       link,
		service:    svc,
		canPublish: svc.CanPublish(),
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
	return w, nil
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
