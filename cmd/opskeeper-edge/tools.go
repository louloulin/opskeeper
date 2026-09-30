package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/vincent-wuhan/opskeeper/core/edge/toolbroker"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tunnel"
	"github.com/vincent-wuhan/opskeeper/internal/skill"
)

// The node's tool invoker: what the broker dispatches to.
//
// A node's read-only toolset is two kinds of tool wearing one name. The
// host_* probes are the node's own business — it can read its own kernel
// ring buffer without asking anyone. The topology and alert queries are
// the control plane's business: the graph and the rule table live in the
// manager, and the only honest way to reach them is to ask. So resolution
// is local first, then upcall, and the order is not a preference — a tool
// the node can answer itself should not spend a tunnel round trip, and a
// node that shadowed a control-plane tool with a local one would be a node
// answering questions about the fleet from its own machine.
//
// This is what makes the read-only profile real. The PiG extension inside
// the agent process holds no implementation at all; it routes. Every
// privileged operation on a node therefore happens here, in the edge,
// where it is permissioned by the skill registry, covered by tests written
// before any of this existed, and recorded.
//
// It lives in the composition root rather than in internal/edgeagent
// because it is the one place that knows both the skill framework and the
// broker protocol exist, and internal/edgeagent may not import core/edge.

// agentToolInvoker runs a tool the broker has already permitted.
type agentToolInvoker struct {
	// client is the tunnel up to the control plane. Required for the
	// control-plane half; without it those tools report an error rather
	// than being silently absent.
	client tunnel.Client
	// log records what ran. The gate's ledger records what was permitted;
	// this records what actually happened, which is the half a gate alone
	// cannot supply.
	log *slog.Logger
}

// Invoke runs one permitted call.
//
// Local first, then upcall. A name that is neither is not a tool this
// node has, and saying so is more useful than an empty result the model
// would then have to interpret.
func (t *agentToolInvoker) Invoke(ctx context.Context, c toolbroker.Call) (json.RawMessage, error) {
	if exec, ok := skill.Get(c.ToolName); ok {
		return t.runLocal(ctx, exec, c)
	}
	return t.upcall(ctx, c)
}

// runLocal executes a skill that lives on this node.
func (t *agentToolInvoker) runLocal(ctx context.Context, exec skill.Executor, c toolbroker.Call) (json.RawMessage, error) {
	// The parameters are the JSON the broker re-encoded from the model's
	// proposal, so the executor parses exactly what the host validated
	// rather than whatever the agent process claimed to send.
	out, err := exec.Execute(ctx, c.Arguments)
	if t.log != nil {
		t.log.Info("agent tool ran on the node",
			"tool", c.ToolName, "session", c.SessionID, "err", errString(err))
	}
	if err != nil {
		// A skill that fails is reported as a failure, not as an
		// exception the agent renders as an extension fault the model
		// never sees. A container that cannot read /proc/kmsg is a normal
		// answer, and the investigator persona needs to be able to read
		// it rather than be told the tool does not exist.
		return nil, fmt.Errorf("%s failed: %w", c.ToolName, err)
	}
	return out, nil
}

// upcall asks the control plane to run a tool it owns.
func (t *agentToolInvoker) upcall(ctx context.Context, c toolbroker.Call) (json.RawMessage, error) {
	if t.client == nil {
		return nil, fmt.Errorf(
			"%s is a control-plane tool and this node has no tunnel to the control plane", c.ToolName)
	}
	var resp tunnel.AgentToolResponse
	err := t.client.Call(ctx, tunnel.MethodAgentTool, tunnel.AgentToolRequest{
		SessionID: c.SessionID,
		Tool:      c.ToolName,
		Arguments: c.Arguments,
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("%s: the control plane could not be reached: %w", c.ToolName, err)
	}
	if resp.Error != "" {
		// The manager's refusal arrives as an error string rather than a
		// transport failure, so the model reads it as a decision rather
		// than as something to retry.
		return nil, fmt.Errorf("%s: %s", c.ToolName, resp.Error)
	}
	return resp.Result, nil
}

// errString renders an error for a log line without the ceremony.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
