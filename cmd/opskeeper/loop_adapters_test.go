package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	middlewareregistry "github.com/vincent-wuhan/opskeeper/internal/middleware/registry"
)

// clearLoopAdapterEnv pins every adapter DSN to a known state for the
// duration of a test. The wiring reads process environment, and a leftover
// variable from another test or from the developer's shell would make these
// assertions describe the shell rather than the code.
func clearLoopAdapterEnv(t *testing.T) {
	t.Helper()
	for _, src := range loopAdapterSources() {
		t.Setenv(src.env, "")
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNoAdapterDSNLeavesTheRegistryEmpty(t *testing.T) {
	clearLoopAdapterEnv(t)
	reg := middlewareregistry.NewRegistry()

	closers := wireLoopRemediationAdapters(context.Background(), discardLog(), reg)

	if got := len(reg.ListTools("")); got != 0 {
		t.Fatalf("registry has %d tools with nothing configured; the approved phase would dispatch without a target", got)
	}
	if len(closers) != 0 {
		t.Fatalf("got %d closers with nothing configured", len(closers))
	}
}

// A DSN that cannot be reached must not take the manager down: the other
// adapters are still wired and the run still refuses to dispatch the tools
// that are missing, which is the behaviour the invoker already has.
func TestUnreachableAdapterIsSkippedRatherThanFatal(t *testing.T) {
	clearLoopAdapterEnv(t)
	t.Setenv("OPSKEEPER_LOOP_K8S_DSN", "bogus://nope")
	t.Setenv("OPSKEEPER_LOOP_HOST_DSN", "local://")
	reg := middlewareregistry.NewRegistry()

	closers := wireLoopRemediationAdapters(context.Background(), discardLog(), reg)
	for _, closeAdapter := range closers {
		closeAdapter()
	}

	tools := reg.ListTools("")
	if len(tools) == 0 {
		t.Fatal("the reachable host adapter registered nothing; one bad DSN suppressed the whole registry")
	}
	for _, name := range tools {
		if strings.HasPrefix(name, "k8s.") {
			t.Fatalf("%s landed in the registry even though the k8s DSN could not be resolved", name)
		}
	}
}

func TestConfiguredHostAdapterRegistersItsTools(t *testing.T) {
	clearLoopAdapterEnv(t)
	t.Setenv("OPSKEEPER_LOOP_HOST_DSN", "local://")
	reg := middlewareregistry.NewRegistry()

	closers := wireLoopRemediationAdapters(context.Background(), discardLog(), reg)
	if len(closers) != 1 {
		t.Fatalf("got %d closers, want 1 for the one configured adapter", len(closers))
	}
	defer closers[0]()

	tools := reg.ListTools("")
	if len(tools) == 0 {
		t.Fatal("host adapter registered no tools")
	}
	for _, want := range []string{"host.garbage_collect", "host.restart_service"} {
		if _, ok := reg.LookupTool(want); !ok {
			t.Errorf("%s is a loop action and is not dispatchable after wiring the host adapter", want)
		}
	}
}

// The list is the deployment contract: these names are documented to
// operators, so a rename here is a breaking change and should be a
// deliberate one.
func TestLoopAdapterEnvNames(t *testing.T) {
	want := map[string]string{
		"postgres": "OPSKEEPER_LOOP_PG_DSN",
		"redis":    "OPSKEEPER_LOOP_REDIS_DSN",
		"k8s":      "OPSKEEPER_LOOP_K8S_DSN",
		"mq":       "OPSKEEPER_LOOP_MQ_DSN",
		"host":     "OPSKEEPER_LOOP_HOST_DSN",
	}
	got := map[string]string{}
	for _, src := range loopAdapterSources() {
		got[src.name] = src.env
	}
	if len(got) != len(want) {
		t.Fatalf("wired adapters = %v, want %v", got, want)
	}
	for name, env := range want {
		if got[name] != env {
			t.Errorf("adapter %s reads %s, want %s", name, got[name], env)
		}
	}
}
