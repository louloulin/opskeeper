package main

import (
	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/pluginmanifest"
)

// The node's role ladder.
//
// The control plane has already decided who is asking — the tunnel is
// authenticated and IAM ran on the manager before the turn was sent — so
// what the node does with a role is not re-authenticate it but refuse to be
// talked past it. An agent that convinces the model to call a mutating tool
// does not change which rung it is standing on.
//
// The rungs are deliberately few and readable. A role an operator cannot
// name from memory is a role nobody will configure correctly during an
// incident, and the safe reading of an unrecognised one has to be the
// bottom of the ladder rather than the top.
const (
	// RoleAdmin is the SRE on call: it may run anything the node has
	// installed, still subject to approval for what changes a live system.
	RoleAdmin = "admin"
	// RoleOperator runs day-two work: reads and changes inside the systems
	// the node manages.
	RoleOperator = "user"
	// RoleViewer observes. Everything else is refused, and the refusal is
	// what stops a read-only console session from being talked into a
	// restart.
	RoleViewer = "viewer"
)

// roleCeiling maps a caller's role to the most dangerous class it may run.
//
// An empty or unrecognised role is read-only. That is the one default worth
// being emphatic about: the ceiling is what stops a mutating call, and a
// typo in a role name must not hand out the top of the ladder.
func roleCeiling(role string) domain.ToolClass {
	switch role {
	case RoleAdmin:
		return domain.ClassDestructive
	case RoleOperator:
		return domain.ClassWrite
	default:
		return domain.ClassRead
	}
}

// manifestsOf projects admitted packages to their governance manifests.
//
// The gate's allow-list is built from exactly the manifests that were
// admitted at boot, and from nothing else. A tool that appears in the
// agent at run time but in no manifest here is not in the registry, and a
// tool that is not in the registry is refused.
func manifestsOf(plugins []pluginmanifest.Plugin) []domain.PluginManifest {
	out := make([]domain.PluginManifest, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p.Manifest)
	}
	return out
}
