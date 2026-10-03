//go:build e2e

package testenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Frontier is a running tunnel broker, and the two addresses a deployment
// hands to the two processes that dial it.
//
// The split is the whole reason this file exists. The manager and the node
// are on opposite sides of the broker and neither can reach the other
// directly: 40011 is the servicebound side the manager dials *in* to, and
// 40012 is the edgebound side a node dials *in* to. A harness that handed
// both processes one address would be testing a topology nobody deploys,
// and the failure it hides is the one that matters — the manager's RPC
// registrations and the node's inbound calls travel over different
// listeners, so "the node is connected" and "the manager can call the node"
// are two different facts.
type Frontier struct {
	// ServiceAddr is host:port for the manager to dial (frontier's
	// servicebound listener, 40011 in the shipped config).
	ServiceAddr string
	// EdgeAddr is host:port for a node to dial (frontier's edgebound
	// listener, 40012 in the shipped config).
	EdgeAddr string
}

var (
	frontierOnce sync.Once
	frontierInst *Frontier
	frontierBox  tc.Container
	frontierErr  error
	// frontierNoImage records that the failure was the daemon refusing the
	// image, as opposed to a broker that started and then misbehaved. Only
	// the former is an environment problem, and only the former may be
	// reported in those words.
	frontierNoImage bool
)

// sharedFrontier brings up one tunnel broker per `go test` process.
//
// The config is the repository's own deploy/install/frontier.yaml rather
// than something written here. That file is what a deployment runs, so a
// harness with its own copy would be testing a broker configured in a way
// no deployment is — and the interesting failures in a broker topology are
// configuration failures (which port, whether the idservice allocates edge
// ids) rather than code failures.
func sharedFrontier(t *testing.T) *Frontier {
	t.Helper()
	frontierOnce.Do(func() {
		repo := repoRoot()
		if repo == "" {
			frontierErr = fmt.Errorf("cannot locate repo root from testenv source")
			return
		}
		configPath := filepath.Join(repo, "deploy", "install", "frontier.yaml")
		if _, err := os.Stat(configPath); err != nil {
			frontierErr = fmt.Errorf("frontier config: %w", err)
			return
		}
		if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "" {
			_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		container, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
			ContainerRequest: tc.ContainerRequest{
				Image: frontierImage(),
				ExposedPorts: []string{
					"40011/tcp",
					"40012/tcp",
				},
				Cmd: []string{"--config", "/usr/conf/frontier.yaml"},
				Files: []tc.ContainerFile{{
					HostFilePath:      configPath,
					ContainerFilePath: "/usr/conf/frontier.yaml",
					FileMode:          0o644,
				}},
				WaitingFor: wait.ForLog("edgebound server listening on").
					WithStartupTimeout(2 * time.Minute),
			},
			Started: true,
		})
		if err != nil {
			frontierErr = fmt.Errorf("frontier container: %w", err)
			frontierNoImage = daemonRefusedImage(err)
			return
		}
		host, err := container.Host(ctx)
		if err != nil {
			frontierErr = err
			return
		}
		servicePort, err := container.MappedPort(ctx, "40011/tcp")
		if err != nil {
			frontierErr = err
			return
		}
		edgePort, err := container.MappedPort(ctx, "40012/tcp")
		if err != nil {
			frontierErr = err
			return
		}
		frontierBox = container
		frontierInst = &Frontier{
			ServiceAddr: fmt.Sprintf("%s:%s", host, servicePort.Port()),
			EdgeAddr:    fmt.Sprintf("%s:%s", host, edgePort.Port()),
		}
	})
	if frontierErr != nil {
		if frontierNoImage {
			// Still a failure. The delivery acceptance has not been
			// delivered, and a red gate that says why is worth more than
			// a green one that means nothing.
			t.Fatal(frontierUnavailableMessage(frontierImage(), frontierErr))
		}
		t.Fatalf("testenv: %v", frontierErr)
	}
	return frontierInst
}

// SharedFrontier is sharedFrontier, exported for tests that need the
// addresses to hand to something other than a manager (a node, mainly).
func SharedFrontier(t *testing.T) *Frontier { return sharedFrontier(t) }

// defaultFrontierImage is the broker this harness runs.
//
// It is NOT the tag docker-compose.yml pins (v1.2.4), and the difference
// is deliberate rather than an oversight. The v-prefixed tag does not
// resolve through the registry mirror this machine is configured with, so
// a harness pinned to it cannot start a broker at all; 1.2.5 is the tag
// that is actually deployed.
//
// It is NOT reliably obtainable either. The mirror in use answers 403 for
// the whole singchia namespace, and a direct path to docker.io needs
// credentials this harness does not have, so on a machine without a cached
// copy the two delivery tests cannot run at all. That is an environment
// precondition and it is reported as one (see frontier_image.go) rather
// than as a failure of the thing under test.
//
// The divergence is stated here rather than papered over, because it is a
// real discrepancy between what the repository asks for and what runs:
// docker-compose.yml still pins v1.2.4, and whoever reconciles the two
// should decide which is right (and re-run this gate afterwards) rather
// than find out later that the composition and the acceptance test were
// testing different brokers.
const defaultFrontierImage = "docker.io/singchia/frontier:1.2.5"

// frontierImage resolves the broker image, overridable so a mirror or a
// locally built tag can be used without editing the test.
func frontierImage() string {
	if image := strings.TrimSpace(os.Getenv("OPSKEEPER_E2E_FRONTIER_IMAGE")); image != "" {
		return image
	}
	return defaultFrontierImage
}

// TerminateSharedFrontier kills the broker container. Called from TestMain
// alongside TerminateSharedMySQL, for the same reason: ryuk is disabled,
// so nothing else reaps it.
func TerminateSharedFrontier() {
	if frontierBox == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = frontierBox.Terminate(ctx)
	frontierBox = nil
}

// WithFrontier points a manager at a real tunnel broker instead of the
// disabled one the default harness uses.
//
// The default harness sets OPSKEEPER_FRONTIER_DISABLED and reaches edge
// flows through the in-process edgesim helper. That is the right trade for
// twenty tests and the wrong one for the delivery acceptance: the thing
// being accepted is a node process and a manager process talking across a
// broker, and a simulated edge answers none of the questions it is meant
// to.
func WithFrontier(f *Frontier) Option {
	return func(c *envConfig) {
		if c.extraEnv == nil {
			c.extraEnv = map[string]string{}
		}
		c.extraEnv["OPSKEEPER_FRONTIER_DISABLED"] = "false"
		c.extraEnv["OPSKEEPER_FRONTIER_ADDR"] = f.ServiceAddr
	}
}
