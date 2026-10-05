package chatruntime

import "github.com/vincent-wuhan/opskeeper/core/extension/biz/container"

// The plugin package shapes moved to core/extension/biz/container (decision
// 270) and the six loader entry points with them. Everything below is a
// re-export, not a copy: a copy is a second thing that can disagree, and the
// one this file replaces was a second thing that did — biz/marketplace used
// to carry its own LoadWarning for exactly that reason, with a comment
// defending it that was not true.
//
// The aliases are here for one reason only: this package and its tests have
// hundreds of references, and rewriting all of them would make the move
// indistinguishable from a rewrite. A caller outside this package should
// import the container package directly — marketplace does, and that is what
// removed its dependency on the chat runtime.
type (
	// Activation controls when a skill is mounted into the toolBag.
	Activation = container.Activation
	// Requires expresses host-level dependencies of a skill.
	Requires = container.Requires
	// CredentialRequirement is one credential slot declared by a skill/MCP.
	CredentialRequirement = container.CredentialRequirement
	// CredentialInject declares where a slot's fields go at exec time.
	CredentialInject = container.CredentialInject
	// CredentialFile is one credential file to materialize for a skill run.
	CredentialFile = container.CredentialFile
	// OpskeeperExt is the opskeeper-private extension subtree.
	OpskeeperExt = container.OpskeeperExt
	// SkillMetadata is the frontmatter subtree common to SKILL.md forms.
	SkillMetadata = container.SkillMetadata
	// ToolDecl is a single tool declared by a skill.
	ToolDecl = container.ToolDecl
	// Provenance records where a declaration came from.
	Provenance = container.Provenance
	// Skill is one SKILL.md as loaded from a package.
	Skill = container.Skill
	// Agent is one agent persona as loaded from a package.
	Agent = container.Agent
	// Pack is a loaded plugin container.
	Pack = container.Pack
	// LoadResult is what a directory walk returns.
	LoadResult = container.LoadResult
	// LoadWarning is a non-fatal load issue.
	LoadWarning = container.LoadWarning
	// ContainerKind labels which marker file was found.
	ContainerKind = container.ContainerKind
	// PluginManifest is the on-disk shape of a container's manifest.
	PluginManifest = container.PluginManifest
	// ContainerLoader adapts the loader to domain.ContainerLoader.
	ContainerLoader = container.ContainerLoader
	// LoadAllConfig is the input to LoadAll.
	LoadAllConfig = container.LoadAllConfig
)

const (
	// ContainerClaude is the `.claude-plugin/plugin.json` form.
	ContainerClaude = container.ContainerClaude
	// ContainerOpenclaw is the `openclaw.plugin.json` form.
	ContainerOpenclaw = container.ContainerOpenclaw
	// ContainerBareSkills is the skills.sh / vercel-labs/skills form.
	ContainerBareSkills = container.ContainerBareSkills
	// ContainerNone means no recognized layout was found.
	ContainerNone = container.ContainerNone
)

// The loader functions are re-exported as values rather than as wrappers.
// A wrapper would be a second implementation of the same call; a value is the
// same function, and the only thing given up is the ability to take its
// address in a method expression, which nothing in this repository does.
var (
	// DetectContainer probes a directory for plugin container markers.
	DetectContainer = container.DetectContainer
	// LoadPluginContainer loads a container of a known kind.
	LoadPluginContainer = container.LoadPluginContainer
	// LoadAll walks the skill / agent roots and returns everything found.
	LoadAll = container.LoadAll
)
