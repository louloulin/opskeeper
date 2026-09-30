package main

import (
	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/edge/policygate"
	"github.com/vincent-wuhan/opskeeper/core/edge/toolbroker"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/pluginmanifest"
	"github.com/vincent-wuhan/opskeeper/internal/skill"
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

// toolAuthorizer builds the broker's second allow-list check.
//
// It reads the same registry the gate reads, narrowed to the same role
// ceiling, so the two cannot disagree about what is deployed. What it adds
// is the class: where the node holds the real executor, it knows the tool's
// actual permission class rather than the one a manifest claimed for it,
// and it hands that to the gate's under-declaring rule.
//
// That is the check that makes the manifest mean something. A package that
// declares host_restart_service as read is caught at the moment the model
// calls it, by the host, with an executor in hand — not reviewed into
// compliance by a human reading YAML, and not discovered after the
// restart. The gate reaches the same conclusion a moment earlier from the
// manifest alone; this one is what survives a package that replaced the
// courier extension to get past the first.
func toolAuthorizer(registry *policygate.Registry) toolbroker.Authorizer {
	return func(actor, toolName string) (bool, string) {
		call := policygate.Call{ToolName: toolName, Class: domain.ClassUnknown}
		if exec, ok := skill.Get(toolName); ok {
			call.Class = classOfSkill(exec.Metadata().EffectiveClass())
		}
		return registry.Policy(roleCeiling(actor)).Permitted(call)
	}
}

// classOfSkill maps a skill's permission class onto the tool classes the
// governance vocabulary uses.
//
// The two vocabularies are close but not identical — "safe" is about
// whether a skill has side effects, "read" is about what an operator would
// expect a tool named this to do — and the mapping is the conservative
// one in every ambiguous case. host_restart_service is ClassMutating and
// becomes write, which is what sends it to an approval queue instead of
// running because a manifest said it was harmless.
func classOfSkill(c skill.Class) domain.ToolClass {
	switch c {
	case skill.ClassSafe:
		return domain.ClassRead
	case skill.ClassMutating:
		return domain.ClassWrite
	case skill.ClassDangerous:
		return domain.ClassDestructive
	default:
		// An unrecognised class is treated as the most dangerous thing in
		// the system. A skill author who adds a class the host has not
		// learned to read gets the strictest reading, not a default.
		return domain.ClassDestructive
	}
}
