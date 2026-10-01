package biz

import (
	"context"

	"github.com/vincent-wuhan/opskeeper/core/ports"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tunnel"
)

// Plugin distribution, on the edge side.
//
// The review is not here. It is on the other side of the PluginInstaller
// port, in the composition root, because the inputs to a review — the
// node's trust store, its policy ceiling, its granted scopes — are the
// operator's configuration and not this package's business. What is here
// is the transport: decode a request, hand it to the port, and translate
// the port's verdict into the wire shape.
//
// The translation is the part worth reading. A node's answer has three
// outcomes and the wire has to keep them apart: installed, refused,
// failed. Collapsing "refused" into "failed" would make a manager retry a
// signature rejection against every node in the fleet, and collapsing
// "refused" into "installed" would make it count a capability the node
// does not have.

// SetPluginInstaller wires the node's plugin store.
//
// It is post-construction and optional, for the same reason SetAgentBridge
// is: the store is built in main, where the trust store and the policy
// live, and after the Agent. A node with no installer registers none of
// the methods below, which the manager can tell apart from a node that has
// one and is refusing — a node that answers "unknown method" has not been
// configured, and a node that answers "refused" has.
func (a *Agent) SetPluginInstaller(installer ports.PluginInstaller) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pluginInstaller = installer
}

// pluginStore returns the installer, or nil.
func (a *Agent) pluginStore() ports.PluginInstaller {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pluginInstaller
}

// registerPluginHandlers wires plugin.install / plugin.remove /
// plugin.list. Called from registerUpgradeHandlers' sibling, and a no-op
// on a node with no store.
func (a *Agent) registerPluginHandlers() {
	if a.pluginStore() == nil {
		return
	}

	a.client.RegisterHandler(tunnel.MethodPluginInstall,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			var req tunnel.PluginInstallRequest
			if err := jsonDecode(body, &req); err != nil {
				return nil, err
			}
			// The store is read per call rather than captured. A node
			// that wires its store after the tunnel comes up would
			// otherwise capture nil and answer every install with "not
			// provisioned" for the life of the process.
			return jsonEncode(a.handlePluginInstall(ctx, req, a.pluginStore()), nil)
		})

	a.client.RegisterHandler(tunnel.MethodPluginRemove,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			var req tunnel.PluginRemoveRequest
			if err := jsonDecode(body, &req); err != nil {
				return nil, err
			}
			return jsonEncode(a.handlePluginRemove(ctx, req), nil)
		})

	a.client.RegisterHandler(tunnel.MethodPluginList,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			var req tunnel.PluginListRequest
			if err := jsonDecode(body, &req); err != nil {
				return nil, err
			}
			return jsonEncode(a.handlePluginList(ctx, req), nil)
		})
}

// handlePluginInstall implements MethodPluginInstall.
func (a *Agent) handlePluginInstall(ctx context.Context, req tunnel.PluginInstallRequest, store ports.PluginInstaller) tunnel.PluginInstallResponse {
	if store == nil {
		// Unreachable through the registered handler, which is only
		// registered when there is a store. Kept because a node that
		// skipped the check would answer a missing store with a nil
		// dereference, and that crash would be in the tunnel's read loop
		// rather than in the request that caused it.
		return tunnel.PluginInstallResponse{
			Status: tunnel.PluginStatusFailed,
			Plugin: req.Plugin, Version: req.Version,
			Reason: noPluginStore,
		}
	}

	state := store.Install(ctx, ports.PluginSpec{
		Name:      req.Plugin,
		Version:   req.Version,
		URL:       req.URL,
		SHA256:    req.SHA256,
		Signature: req.Signature,
		KeyID:     req.KeyID,
	})
	return installResponse(state, entriesOf(a, store))
}

// handlePluginRemove implements MethodPluginRemove.
//
// The version is checked by the store, not here. A rollback that raced a
// newer release has to be able to say "remove exactly what I put here",
// and a handler that ignored the version would remove the new one instead —
// which is the one outcome a rollback must never produce.
func (a *Agent) handlePluginRemove(ctx context.Context, req tunnel.PluginRemoveRequest) tunnel.PluginRemoveResponse {
	store := a.pluginStore()
	if store == nil {
		return tunnel.PluginRemoveResponse{
			Status: tunnel.PluginStatusFailed,
			Plugin: req.Plugin,
			Reason: noPluginStore,
		}
	}
	state := store.Remove(ctx, req.Plugin, req.Version)
	return tunnel.PluginRemoveResponse{
		Status:    statusOf(state),
		Plugin:    state.Name,
		Version:   state.Version,
		Reason:    firstNonEmpty(state.Refused, state.Note, state.Error),
		Installed: entriesOf(a, store),
	}
}

// handlePluginList implements MethodPluginList.
func (a *Agent) handlePluginList(_ context.Context, _ tunnel.PluginListRequest) tunnel.PluginListResponse {
	store := a.pluginStore()
	if store == nil {
		return tunnel.PluginListResponse{}
	}
	return tunnel.PluginListResponse{Installed: entriesOf(a, store)}
}

// installResponse renders a state on the wire.
//
// The active set rides along with every answer rather than needing a
// second round trip. A manager that has to ask again to find out what a
// refusal left behind is a manager making two decisions from two moments,
// and a package that arrived in between them belongs to neither.
func installResponse(state ports.PluginState, installed []tunnel.PluginEntry) tunnel.PluginInstallResponse {
	var replaced *tunnel.PluginEntry
	if state.Replaced != nil {
		replaced = &tunnel.PluginEntry{
			Plugin:  state.Replaced.Name,
			Version: state.Replaced.Version,
			Digest:  state.Replaced.Digest,
		}
	}
	return tunnel.PluginInstallResponse{
		Status:    statusOf(state),
		Plugin:    state.Name,
		Version:   state.Version,
		Digest:    state.Digest,
		Replaced:  replaced,
		Reason:    firstNonEmpty(state.Refused, state.Note, state.Error),
		Installed: installed,
	}
}

// statusOf maps a port state onto the wire's closed set.
//
// The mapping is in one place because the three outcomes are a safety
// distinction, not a formatting choice: a manager reads "refused" as
// "stop", "failed" as "try this node again later", and "installed" as
// "count it towards the wave". Getting one of them wrong turns a
// canary into a fleet-wide push or a stuck release.
func statusOf(state ports.PluginState) string {
	switch {
	case state.Error != "":
		return tunnel.PluginStatusFailed
	case state.Refused != "":
		return tunnel.PluginStatusRefused
	default:
		return tunnel.PluginStatusInstalled
	}
}

// entriesOf renders the node's active set for the wire.
func entriesOf(a *Agent, store ports.PluginInstaller) []tunnel.PluginEntry {
	infos := store.Installed()
	out := make([]tunnel.PluginEntry, 0, len(infos))
	for _, i := range infos {
		out = append(out, tunnel.PluginEntry{Plugin: i.Name, Version: i.Version, Digest: i.Digest})
	}
	if a != nil && a.log != nil {
		a.log.Debug("plugin set reported", "count", len(out))
	}
	return out
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// noPluginStore is what a node without a store says, in the one sentence
// an operator needs. It is a refusal rather than a failure: nothing about
// the request is wrong, and retrying it will not conjure a store.
const noPluginStore = "this node has no plugin store configured; it is not provisioned for plugins"
