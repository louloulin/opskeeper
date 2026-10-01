package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/vincent-wuhan/opskeeper/internal/manager/biz/aiops/agentkernel"
	aiopstools "github.com/vincent-wuhan/opskeeper/internal/manager/biz/aiops/tools"
	aiopstoolsbase "github.com/vincent-wuhan/opskeeper/internal/manager/biz/aiops/tools/basetool"
)

// TestEveryRegisteredMutatingToolHasADeclaredApprovalOwner is the drift
// detector the kernel's boot check relies on, run against the real registry
// rather than against a fixture.
//
// The kernel can only see a tool's class; it cannot see that cloud_bash
// blocks on its own approval card or that the coordination primitives have
// never been gated. So every mutating tool has to be declared, and a new one
// that nobody declared must fail here — at build time — instead of appearing
// in production as a second approval card for a single action.
func TestEveryRegisteredMutatingToolHasADeclaredApprovalOwner(t *testing.T) {
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))
	reg := aiopstools.NewRegistry(nil, nil, nil, nil, nil, nil, nil, log)
	bag := reg.BuildBaseTools()
	if bag == nil {
		t.Fatal("BuildBaseTools returned no bag")
	}

	gate := agentkernel.NewDeferredGate(nil, selfSettledToolNames())
	missing, err := agentkernel.UndeclaredMutatingTools(context.Background(), bag.SchemasForLLM(), gate.Declares)
	if err != nil {
		t.Fatalf("UndeclaredMutatingTools: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("mutating tools with no declared approval owner: %v\n"+
			"add each to selfSettledToolNames (with the mechanism that approves it) "+
			"or register it with the kernel gate where its executor is built", missing)
	}
}

// testWriter keeps the registry's error logs visible in a failing run
// without printing them for a passing one.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// TestThePostConstructionToolsAreDeclared covers the tools that are bolted
// onto the runtime AFTER it is built. They are the reason the declaration
// check runs on the final bag rather than on the registry's: cloud_bash is
// not in the registry at all, so a check that stopped there would pass while
// the most dangerous tool in the product went undeclared.
func TestThePostConstructionToolsAreDeclared(t *testing.T) {
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))
	tools := []aiopstoolsbase.BaseTool{
		// The proposer-backed shell tools (main.go's AppendToolBag).
		aiopstools.NewBashToolWithProposer(nil, nil, nil, nil, log),
		aiopstools.NewCloudBashTool(nil, log),
		aiopstools.NewInstallSkillTool(nil, log),
		aiopstools.NewServePageTool(nil, log),
		aiopstools.NewSendIMMessageTool(nil, log),
		// The coordination trio, added through the worker-spawner wiring.
		aiopstools.NewAgentTool(nil, nil, log),
		aiopstools.NewSendMessageTool(nil, log),
		aiopstools.NewTaskStopTool(nil, log),
	}

	gate := agentkernel.NewDeferredGate(nil, selfSettledToolNames())
	missing, err := agentkernel.UndeclaredMutatingTools(context.Background(), tools, gate.Declares)
	if err != nil {
		t.Fatalf("UndeclaredMutatingTools: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("post-construction mutating tools with no declared approval owner: %v", missing)
	}
}

// TestTheDeclarationListHasNoDuplicates keeps the boot log honest: the count
// it prints is read as "how many tools were declared", and a duplicate would
// make that number disagree with the bag.
func TestTheDeclarationListHasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range selfSettledToolNames() {
		if n == "" {
			t.Fatal("the declaration list contains an empty name")
		}
		if seen[n] {
			t.Fatalf("%q is declared twice", n)
		}
		seen[n] = true
	}
}
