package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vincent-wuhan/opskeeper/core/ports"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tunnel"
)

// EdgeCaller is the manager's cloud→edge dispatcher, as this package needs
// it. It is the same shape frontierbound.Client.Call has, declared here so
// the rollout logic does not import the transport and can be driven by a
// scripted fleet in tests.
type EdgeCaller interface {
	Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error)
}

// NodeFleet is a Node backed by the manager's tunnel.
//
// It is deliberately thin: marshal, call, unmarshal, translate. Everything
// that decides *whether* a node should get a package lives on the node, and
// everything that decides *when* lives in Rollout. What is left here is the
// one thing neither of them can do, which is speak to a node that is not in
// this process.
type NodeFleet struct {
	caller EdgeCaller
}

// NewNodeFleet builds the adapter. A nil caller is a programming error and
// is refused by the methods rather than answering "refused" for every node,
// because an unwired control plane looks exactly like a fleet-wide policy
// rejection from the outside.
func NewNodeFleet(caller EdgeCaller) *NodeFleet { return &NodeFleet{caller: caller} }

// Install asks one node to take a package.
func (f *NodeFleet) Install(ctx context.Context, edgeID uint64, spec ports.PluginSpec) Outcome {
	if f == nil || f.caller == nil {
		return Outcome{Status: StatusFailed, Reason: "the control plane has no tunnel to the fleet"}
	}
	body, err := json.Marshal(tunnel.PluginInstallRequest{
		Plugin:    spec.Name,
		Version:   spec.Version,
		URL:       spec.URL,
		SHA256:    spec.SHA256,
		Signature: spec.Signature,
		KeyID:     spec.KeyID,
	})
	if err != nil {
		return Outcome{Status: StatusFailed, Reason: "malformed install request: " + err.Error()}
	}
	raw, err := f.caller.Call(ctx, edgeID, tunnel.MethodPluginInstall, body)
	if err != nil {
		// A transport error is an answer we did not get, not a refusal. It
		// is reported as failed so the operator sees the node — but see
		// Outcome and answered(): a wave that has one of these still moves,
		// which is why the node is named in the log and in Status.Failed.
		return Outcome{Status: StatusFailed, Reason: "no answer from the node: " + err.Error()}
	}
	var resp tunnel.PluginInstallResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Outcome{Status: StatusFailed, Reason: "unreadable answer from the node: " + err.Error()}
	}
	out := Outcome{
		Plugin:   resp.Plugin,
		Status:   resp.Status,
		Digest:   resp.Digest,
		Reason:   resp.Reason,
		Set:      infosOf(resp.Installed),
		Replaced: replacedOf(resp.Replaced),
	}
	if out.Reason == "" && out.Status == StatusRefused {
		out.Reason = "the node refused the package and gave no reason"
	}
	return out
}

// Remove asks one node to give a package back.
func (f *NodeFleet) Remove(ctx context.Context, edgeID uint64, name, version string) Outcome {
	if f == nil || f.caller == nil {
		return Outcome{Status: StatusFailed, Reason: "the control plane has no tunnel to the fleet"}
	}
	body, err := json.Marshal(tunnel.PluginRemoveRequest{Plugin: name, Version: version})
	if err != nil {
		return Outcome{Status: StatusFailed, Reason: "malformed remove request: " + err.Error()}
	}
	raw, err := f.caller.Call(ctx, edgeID, tunnel.MethodPluginRemove, body)
	if err != nil {
		return Outcome{Status: StatusFailed, Reason: "no answer from the node: " + err.Error()}
	}
	var resp tunnel.PluginRemoveResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Outcome{Status: StatusFailed, Reason: "unreadable answer from the node: " + err.Error()}
	}
	return Outcome{
		Plugin: resp.Plugin,
		Status: resp.Status,
		Reason: resp.Reason,
		Set:    infosOf(resp.Installed),
	}
}

// Installed asks one node what it is running.
func (f *NodeFleet) Installed(ctx context.Context, edgeID uint64) ([]ports.PluginInfo, error) {
	if f == nil || f.caller == nil {
		return nil, fmt.Errorf("the control plane has no tunnel to the fleet")
	}
	body, err := json.Marshal(tunnel.PluginListRequest{})
	if err != nil {
		return nil, fmt.Errorf("plugin.list: marshal: %w", err)
	}
	raw, err := f.caller.Call(ctx, edgeID, tunnel.MethodPluginList, body)
	if err != nil {
		return nil, err
	}
	var resp tunnel.PluginListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("plugin.list: unmarshal: %w", err)
	}
	return infosOf(resp.Installed), nil
}

// infosOf renders the wire's package entries.
func infosOf(entries []tunnel.PluginEntry) []ports.PluginInfo {
	if len(entries) == 0 {
		return nil
	}
	out := make([]ports.PluginInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, ports.PluginInfo{Name: e.Plugin, Version: e.Version, Digest: e.Digest})
	}
	return out
}

// replacedOf renders what the node said it overwrote.
//
// A nil entry means "there was nothing there", and that is not the same as
// an entry with an empty version: the manager derives "restore nothing, just
// remove" from the former and "restore the version named here" from the
// latter, and collapsing the two would make the second case a delete.
func replacedOf(entry *tunnel.PluginEntry) *ports.PluginInfo {
	if entry == nil {
		return nil
	}
	return &ports.PluginInfo{Name: entry.Plugin, Version: entry.Version, Digest: entry.Digest}
}
