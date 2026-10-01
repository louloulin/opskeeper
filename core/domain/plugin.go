package domain

// PluginAPIVersion is the schema version of pig-ops.yaml. The loader rejects
// a manifest whose apiVersion it does not recognise, so a manifest written
// for a future host never half-loads against an older host.
const PluginAPIVersion = "opskeeper.io/v1"

// PluginKind is the only kind this schema defines.
const PluginKind = "Plugin"

// DeploymentTarget names where a plugin is allowed to run. A plugin listing
// both targets ships its agent-side package to the node fleet and its
// control-plane half to the manager.
type DeploymentTarget string

const (
	// TargetEdge runs inside the per-node pig process, as PiG extensions,
	// skills, and MCP servers.
	TargetEdge DeploymentTarget = "edge"
	// TargetManager runs in the control plane.
	TargetManager DeploymentTarget = "manager"
)

// Targets is the declared set of deployment locations.
type Targets []DeploymentTarget

// Has reports whether t is in the set.
func (ts Targets) Has(t DeploymentTarget) bool {
	for _, v := range ts {
		if v == t {
			return true
		}
	}
	return false
}

// Valid reports whether every entry is a declared target and the set is
// non-empty. A manifest with no targets is refused: it would otherwise
// install successfully and run nowhere.
func (ts Targets) Valid() bool {
	if len(ts) == 0 {
		return false
	}
	for _, v := range ts {
		if v != TargetEdge && v != TargetManager {
			return false
		}
	}
	return true
}

// Scope is a permission a plugin declares it needs. The host injects
// credentials for exactly these scopes and nothing else, so a plugin cannot
// reach a credential it did not declare.
type Scope string

// Scopes used by the first-party plugins. Third parties may declare their
// own namespaced scopes; the host treats an unknown scope as unsatisfied
// rather than permissive.
const (
	ScopeHostRead   Scope = "host.read"
	ScopeHostWrite  Scope = "host.write"
	ScopeK8sRead    Scope = "k8s.read"
	ScopeK8sExec    Scope = "k8s.exec"
	ScopeDBRead     Scope = "db.read"
	ScopeDBWrite    Scope = "db.write"
	ScopeMQRead     Scope = "mq.read"
	ScopeMQWrite    Scope = "mq.write"
	ScopeMetricsRO  Scope = "observability.read"
	ScopeTopologyRO Scope = "topology.read"
	ScopeAlertRO    Scope = "alert.read"
	ScopeAlertWrite Scope = "alert.write"
)

// Scopes is a declared permission set.
type Scopes []Scope

// Has reports whether s is in the set.
func (ss Scopes) Has(s Scope) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Satisfies reports whether granted covers every scope in ss. This is the
// host's admission check: it is the only place a plugin's declared needs are
// compared against what the operator actually granted.
func (ss Scopes) Satisfies(granted Scopes) bool {
	for _, s := range ss {
		if !granted.Has(s) {
			return false
		}
	}
	return true
}

// AuditPolicy declares a plugin's relationship to the audit ledger. It is a
// declaration, not a grant: only the host writes the ledger, and no manifest
// field can widen that.
type AuditPolicy struct {
	// Emits is true when the plugin reports tool activity the host should
	// record. The host derives the ledger entry from its own gate events, so
	// a plugin cannot forge one.
	Emits bool `json:"emits" yaml:"emits"`
	// Mutates is a declaration of intent. It MUST be false; a manifest that
	// sets it true is rejected at load, because audit-ledger writes are not
	// delegable to a plugin.
	Mutates bool `json:"mutates" yaml:"mutates"`
}

// ApprovalPolicy declares how a plugin's mutating calls reach a human.
type ApprovalPolicy struct {
	// Required forces every mutating call through the host approval gate.
	Required bool `json:"required" yaml:"required"`
	// MaxBlastRadius is the widest reach the host may approve. The host
	// clamps to the narrower of this and the node's policy ceiling; a
	// plugin cannot widen it by declaration.
	MaxBlastRadius BlastRadius `json:"max_blast_radius,omitempty" yaml:"max_blast_radius,omitempty"`
}

// InstallPolicy declares how the host rolls the plugin out.
type InstallPolicy struct {
	// Strategy is "rolling" (drain and replace node by node) or "pin"
	// (install once, never auto-upgrade).
	Strategy string `json:"strategy,omitempty" yaml:"strategy,omitempty"`
	// MinEdgeVersion is the lowest node-agent version that can host this
	// plugin. A node below it is skipped during a rolling install rather
	// than being handed a package it cannot run.
	MinEdgeVersion string `json:"min_edge_version,omitempty" yaml:"min_edge_version,omitempty"`
	// MinPigVersion is the lowest PiG agent build that can host this
	// plugin. It is the other half of the compatibility matrix: a package
	// that uses a tool-registration API, a hook name or an event field
	// that its PiG version does not yet have installs cleanly and fails
	// the first time a turn needs it.
	//
	// It is a separate field from MinEdgeVersion because the two move
	// independently. A node fleet is upgraded on one cadence and the
	// agent binary inside it on another, so "the edge is new enough" and
	// "the agent is new enough" are different questions with different
	// fixes.
	MinPigVersion string `json:"min_pig_version,omitempty" yaml:"min_pig_version,omitempty"`
}

// Install strategy values.
const (
	InstallRolling = "rolling"
	InstallPin     = "pin"
)

// Valid reports whether the strategy is empty or a declared value.
func (i InstallPolicy) Valid() bool {
	return i.Strategy == "" || i.Strategy == InstallRolling || i.Strategy == InstallPin
}

// PluginMeta is the manifest identity block.
type PluginMeta struct {
	Name     string `json:"name" yaml:"name"`
	Version  string `json:"version" yaml:"version"`
	Vendor   string `json:"vendor,omitempty" yaml:"vendor,omitempty"`
	Homepage string `json:"homepage,omitempty" yaml:"homepage,omitempty"`
	// Signature is the host's admission record: the digest of the package
	// contents the manager approved. It is written by the control plane,
	// not by the plugin author.
	Signature string `json:"signature,omitempty" yaml:"signature,omitempty"`
}

// PluginSpec is the manifest's behaviour block.
type PluginSpec struct {
	Targets        Targets        `json:"targets" yaml:"targets"`
	SafetyLevel    SafetyLevel    `json:"safety_level" yaml:"safety_level"`
	Capabilities   []ToolClass    `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Tools          Tools          `json:"tools,omitempty" yaml:"tools,omitempty"`
	RequiredScopes Scopes         `json:"required_scopes,omitempty" yaml:"required_scopes,omitempty"`
	Audit          AuditPolicy    `json:"audit" yaml:"audit"`
	Approval       ApprovalPolicy `json:"approval,omitempty" yaml:"approval,omitempty"`
	Install        InstallPolicy  `json:"install,omitempty" yaml:"install,omitempty"`
}

// ToolDecl is one tool a plugin declares it will register with the agent.
//
// This is the field that makes the host's allow-list possible. The agent
// discovers a package's tools at run time, but the host has to know which
// tools exist *before* the agent starts in order to decide whether any of
// them may run — so the inventory is declared, and a tool the agent
// produces that is not on this list is refused rather than judged on the
// spot.
type ToolDecl struct {
	// Name is the tool identifier the agent will present. It must match
	// the name the extension registers exactly; the host compares names,
	// not prefixes, so a plugin cannot widen a bound tool's reach by
	// registering a near neighbour.
	Name string `json:"name" yaml:"name"`
	// Class is what this tool is allowed to do. The host treats it as a
	// ceiling for the calls the tool makes, never as a claim about them:
	// the call site is free to classify an individual call worse, and a
	// tool whose observed behaviour is worse than its declaration is
	// refused at the gate.
	Class ToolClass `json:"class" yaml:"class"`
}

// Tools is a declared tool inventory.
type Tools []ToolDecl

// Names returns the declared tool names in declaration order.
func (ts Tools) Names() []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

// Has reports whether the inventory declares the named tool.
func (ts Tools) Has(name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}

// HighestClass returns the most dangerous class the inventory declares.
func (ts Tools) HighestClass() ToolClass {
	if len(ts) == 0 {
		return ClassUnknown
	}
	classes := make([]ToolClass, 0, len(ts))
	for _, t := range ts {
		classes = append(classes, t.Class)
	}
	return Classify(classes...)
}

// PluginManifest is the parsed pig-ops.yaml.
//
// The Pi package manifest alongside it stays Pi-compatible: this file adds
// only the governance fields Pi has no vocabulary for. Loading a plugin
// means reading both, and the package manifest is what the agent runtime
// actually mounts.
type PluginManifest struct {
	APIVersion string     `json:"apiVersion" yaml:"apiVersion"`
	Kind       string     `json:"kind" yaml:"kind"`
	Metadata   PluginMeta `json:"metadata" yaml:"metadata"`
	Spec       PluginSpec `json:"spec" yaml:"spec"`
}

// HighestCapability returns the most dangerous class the plugin declares.
// An empty declaration yields ClassUnknown, which the admission check
// treats as destructive.
func (m PluginManifest) HighestCapability() ToolClass {
	if len(m.Spec.Capabilities) == 0 {
		return ClassUnknown
	}
	return Classify(m.Spec.Capabilities...)
}
