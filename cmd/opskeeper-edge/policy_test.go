package main

import (
	"strings"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/edge/policygate"
	"github.com/vincent-wuhan/opskeeper/internal/skill"
	// The executors have to be registered for skill.Get to find them.
	_ "github.com/vincent-wuhan/opskeeper/internal/skill/builtin"
)

// manifestOf builds a governance manifest declaring exactly the named
// tools at exactly the named classes, which is all the allow-list is made
// of.
func manifestOf(plugin string, tools ...domain.ToolDecl) []domain.PluginManifest {
	return []domain.PluginManifest{{
		Metadata: domain.PluginMeta{Name: plugin},
		Spec:     domain.PluginSpec{Tools: tools},
	}}
}

func registryOf(t *testing.T, manifests ...domain.PluginManifest) *policygate.Registry {
	t.Helper()
	r, err := policygate.RegistryFromManifests(manifests)
	if err != nil {
		t.Fatalf("RegistryFromManifests: %v", err)
	}
	return r
}

func TestTheBrokerPermitsAToolTheManifestDeclaresAsRead(t *testing.T) {
	auth := toolAuthorizer(registryOf(t,
		manifestOf("p", domain.ToolDecl{Name: "host_dmesg", Class: domain.ClassRead})...))

	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer, "", "nonsense"} {
		permitted, reason := auth(role, "host_dmesg")
		if !permitted {
			t.Errorf("role %q: %s", role, reason)
		}
	}
}

func TestTheBrokerRefusesAToolNoManifestDeclares(t *testing.T) {
	// This is the check a package cannot get past by shipping an
	// undeclared tool: the model can be told it exists, and it is still
	// refused at the only place that matters.
	auth := toolAuthorizer(registryOf(t,
		manifestOf("p", domain.ToolDecl{Name: "host_dmesg", Class: domain.ClassRead})...))

	permitted, reason := auth(RoleAdmin, "host_reboot")
	if permitted {
		t.Fatal("a tool no manifest declares was permitted")
	}
	if !strings.Contains(reason, "not in this node's tool set") {
		t.Errorf("reason = %q, want the allow-list refusal", reason)
	}
}

// A package that declares host_restart_service as read has lied at install
// time. The node holds the real executor, so it knows the truth, and the
// whole point of asking the gate with that truth is that a human reading
// YAML is not the only thing standing between a manifest and a restart.
func TestTheBrokerRefusesAToolWhoseRealClassIsWorseThanTheManifestClaims(t *testing.T) {
	if _, ok := skill.Get("host_restart_service"); !ok {
		t.Skip("host_restart_service is not registered in this build")
	}
	auth := toolAuthorizer(registryOf(t,
		manifestOf("liar", domain.ToolDecl{Name: "host_restart_service", Class: domain.ClassRead})...))

	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer} {
		permitted, reason := auth(role, "host_restart_service")
		if permitted {
			t.Errorf("role %q ran a tool its package under-declared", role)
			continue
		}
		if !strings.Contains(reason, "host_restart_service") {
			t.Errorf("role %q: reason = %q, want it to name the offending tool", role, reason)
		}
	}
}

func TestTheBrokerAppliesTheRoleCeilingToToolsItCannotCrossCheck(t *testing.T) {
	// A control-plane tool has no local executor, so the broker has no
	// independent class for it and the manifest's word stands. The role
	// ceiling is therefore the only thing narrowing it, and it has to
	// narrow it for the role the host resolved.
	auth := toolAuthorizer(registryOf(t,
		manifestOf("p",
			domain.ToolDecl{Name: "get_topology", Class: domain.ClassRead},
			domain.ToolDecl{Name: "draft_config_change", Class: domain.ClassWrite},
		)...))

	if permitted, reason := auth(RoleViewer, "get_topology"); !permitted {
		t.Errorf("a viewer was refused a read tool: %s", reason)
	}
	if permitted, _ := auth(RoleViewer, "draft_config_change"); permitted {
		t.Error("a viewer ran a write tool")
	}
	if permitted, reason := auth(RoleOperator, "draft_config_change"); !permitted {
		t.Errorf("an operator was refused a write tool: %s", reason)
	}
	if permitted, _ := auth(RoleAdmin, "draft_config_change"); !permitted {
		t.Error("an admin was refused a write tool")
	}
}

func TestAnUnrecognisedRoleGetsTheBottomOfTheLadder(t *testing.T) {
	// The ceiling is what stops a mutating call. A role the host cannot
	// read has to land at the bottom, or a typo in a role name hands out
	// the top of the ladder to whoever typed it.
	auth := toolAuthorizer(registryOf(t,
		manifestOf("p", domain.ToolDecl{Name: "draft_config_change", Class: domain.ClassWrite})...))

	for _, role := range []string{"", "root", "Admin", "ADMIN", "superuser", "system"} {
		if permitted, _ := auth(role, "draft_config_change"); permitted {
			t.Errorf("role %q was treated as privileged", role)
		}
	}
}

func TestASkillClassTheHostHasNotLearnedToReadIsTreatedAsTheWorst(t *testing.T) {
	// A skill author who adds a class the host does not know gets the
	// strictest reading, not a default that happens to be permissive.
	if got := classOfSkill(skill.Class("brand-new")); got != domain.ClassDestructive {
		t.Errorf("classOfSkill(unknown) = %q, want destructive", got)
	}
	// And the three known classes map the conservative way.
	for in, want := range map[skill.Class]domain.ToolClass{
		skill.ClassSafe:      domain.ClassRead,
		skill.ClassMutating:  domain.ClassWrite,
		skill.ClassDangerous: domain.ClassDestructive,
	} {
		if got := classOfSkill(in); got != want {
			t.Errorf("classOfSkill(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRoleCeilingPutsUnknownRolesAtTheBottom(t *testing.T) {
	// Stated separately from the authoriser test so the ladder itself is
	// pinned: everything else is a consequence of this function.
	if got := roleCeiling(RoleAdmin); got != domain.ClassDestructive {
		t.Errorf("admin ceiling = %q, want destructive", got)
	}
	if got := roleCeiling(RoleOperator); got != domain.ClassWrite {
		t.Errorf("operator ceiling = %q, want write", got)
	}
	for _, role := range []string{RoleViewer, "", "nope"} {
		if got := roleCeiling(role); got != domain.ClassRead {
			t.Errorf("role %q ceiling = %q, want read", role, got)
		}
	}
}
