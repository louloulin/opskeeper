package main

import (
	"os"
	"path/filepath"
	"testing"
)

// An acceptance command that cannot fail is a ceremony. Every offline check
// here is broken in a fixture and asserted to go red, and every needs-input
// check is asserted to be reported MISSING rather than silently passed.

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "deploy/install/edge/build-edge-bundle.sh",
		"STAGED=(\n"+
			"  \"promtail                 0755 /usr/local/lib/opskeeper-edge/promtail                 promtail-${ARCH}                      optional\"\n"+
			"  \"pig                      0755 /usr/local/lib/opskeeper-edge/pig                     pig-${ARCH}                           required\"\n"+
			")\n")
	write(t, root, "deploy/Dockerfile.opskeeper-edge",
		"RUN go build -o /out/pig cmd/pig\nCOPY --from=builder /out/pig /opskeeper-edge/pig\nENV OPSKEEPER_EDGE_AGENT_BIN=/opskeeper-edge/pig\n")
	write(t, root, "deploy/install/edge/install-edge.sh",
		"# Spawning `pig --version` here costs milliseconds.\n"+
			"if ! v=\"$(\"$AGENT_BIN\" --version 2>&1)\"; then\n"+
			"    log_error \"installed $AGENT_BIN but it does not run: $v\"\n"+
			"    exit 1\n"+
			"fi\n")
	write(t, root, "deploy/install/edge/opskeeper-edge.env.example",
		"OPSKEEPER_EDGE_MODEL_API_KEY=${OPSKEEPER_PROVIDER_KEY}\n")
	write(t, root, "core/floor/delivery/edge_agent_assets.go", `const modelsTemplate = "{\"apiKey\": \"$VAR\"}"`)
	write(t, root, "Makefile", "edge-credential-check:\n")
	// The two machine-dependent checks get nothing: no pig binary anywhere
	// under the fixture, no OPSKEEPER_ACCEPTANCE_PROVIDER_KEY.
	return root
}

func byID(t *testing.T, id string) check {
	t.Helper()
	for _, c := range checks {
		if c.id == id {
			return c
		}
	}
	t.Fatalf("no check %s", id)
	return check{}
}

func statusOf(t *testing.T, root, id string) status {
	t.Helper()
	ok, reason := byID(t, id).run(root)
	switch {
	case ok:
		return statusPass
	case reason != "":
		return statusFail
	case byID(t, id).needs != "":
		return statusMissing
	default:
		return statusFail
	}
}

func TestTheFixturePassesEverythingItCan(t *testing.T) {
	root := fixture(t)
	for _, id := range []string{"A1", "A2", "A3", "A4", "A5"} {
		if got := statusOf(t, root, id); got != statusPass {
			t.Errorf("%s = %s on the fixture", id, got)
		}
	}
	// The machine-dependent ones must report MISSING, never pass: a chain
	// that skipped them silently and exited zero would be reporting success
	// it did not verify.
	for _, id := range []string{"A6", "A8"} {
		if got := statusOf(t, root, id); got != statusMissing {
			t.Errorf("%s = %s, want MISSING when its input is absent", id, got)
		}
	}
}

// Each offline check, broken in exactly one way.
func TestEveryOfflineCheckCanGoRed(t *testing.T) {
	cases := []struct {
		id, rel, body string
	}{
		{"A1", "deploy/install/edge/build-edge-bundle.sh",
			"STAGED=(\n  \"pig  0755 /usr/local/lib/opskeeper-edge/pig  pig-${ARCH}  optional\"\n)\n"},
		{"A2", "deploy/Dockerfile.opskeeper-edge", "FROM scratch\n"},
		{"A3", "deploy/install/edge/install-edge.sh",
			"# Spawning `pig --version` here costs milliseconds.\necho installing\n"},
		{"A4", "deploy/install/edge/opskeeper-edge.env.example",
			"OPSKEEPER_EDGE_MODEL_API_KEY=sk-live-realllly-not-a-placeholder\n"},
		{"A5", "Makefile", "build:\n"},
	}
	if len(cases) != 5 {
		t.Fatalf("%d cases for 5 offline checks", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			root := fixture(t)
			write(t, root, tc.rel, tc.body)
			if got := statusOf(t, root, tc.id); got != statusFail {
				t.Errorf("%s = %s after breaking %s, want FAIL", tc.id, got, tc.rel)
			}
		})
	}
}

// A missing file is a failure, not a skip: the artifact this repository is
// supposed to ship is not optional.
func TestAMissingArtifactFailsRatherThanSkips(t *testing.T) {
	root := fixture(t)
	if err := os.Remove(filepath.Join(root, "deploy/Dockerfile.opskeeper-edge")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := statusOf(t, root, "A2"); got != statusFail {
		t.Errorf("A2 = %s with the Dockerfile absent, want FAIL", got)
	}
}

// A needs-input check that runs must be able to pass, or it is not a check.
func TestTheCredentialCheckPassesWhenTheKeyIsThere(t *testing.T) {
	root := fixture(t)
	t.Setenv("OPSKEEPER_ACCEPTANCE_PROVIDER_KEY", "test-key")
	if got := statusOf(t, root, "A8"); got != statusPass {
		t.Errorf("A8 = %s with a key present, want pass", got)
	}
}

// The command's whole claim is the distinction between 1 and 3, so the
// classification of every check is asserted on the real tree too.
func TestTheRealTreeHasNoSilentPass(t *testing.T) {
	for _, r := range run("../..") {
		if r.st == statusPass && byID(t, r.id).needs != "" {
			continue // a needs-input check may legitimately pass here
		}
		if r.st == statusPass {
			continue
		}
		if r.reason == "" {
			t.Errorf("%s is %s with no reason; every non-pass names what is missing", r.id, r.st)
		}
	}
}
