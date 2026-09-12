package pirpc

import (
	"errors"
	"fmt"
	"strings"
)

// LaunchOptions describes how to start Pi in RPC mode.
//
// Every flag emitted by Args exists in `pi --help` for
// @earendil-works/pi-coding-agent@0.85.1. Nothing here is inferred
// from a plan document; the previous iteration's `--mode http
// --bind --port` argv was, and Pi rejects it.
type LaunchOptions struct {
	// Node is the interpreter used to run Script. Empty → "node".
	// Kept separate from Script because the vendored monorepo ships
	// a .js entry point, not an executable: a single "node foo.js"
	// string in a config file cannot be exec'd and was a real bug
	// in the previous OPSKEEPER_PI_BIN default.
	Node string

	// Script is the path to Pi's CLI bundle. For the vendored
	// submodule this is
	// vendor/pi/packages/coding-agent/dist/bundle/cli.js — the
	// package's own package.json declares bin.pi =
	// "dist/bundle/cli.js" relative to the package root.
	// Required unless Bin is set.
	Script string

	// Bin runs Pi directly (an installed `pi` shim or a
	// single-file build) instead of Node + Script. Mutually
	// exclusive with Script.
	Bin string

	// Provider / Model select the upstream model. Empty → Pi's own
	// configured default.
	Provider string
	Model    string

	// SystemPromptFile is appended to the system prompt.
	// --append-system-prompt accepts a file path, which is how the
	// rendered opskeeper SYSTEM.md reaches Pi without patching the
	// vendored tree.
	SystemPromptFile string

	// SkillDirs are explicit skill roots (--skill). opskeeper keeps
	// its skills in its own pi-skills/ tree rather than inside the
	// submodule, so they must be passed explicitly.
	SkillDirs []string

	// Tools is an allowlist of tool names (--tools). Empty → Pi's
	// full built-in set. For a read-only diagnosis session the edge
	// passes e.g. read,grep,find,ls. This is Pi's own least
	// privilege switch; it complements, and does not replace,
	// cmdpolicy on the edge side.
	Tools []string

	// ExcludeTools is a denylist (--exclude-tools). Applied by Pi
	// on top of Tools.
	ExcludeTools []string

	// SessionDir stores session transcripts (--session-dir). Empty
	// with Ephemeral=false → Pi's default location.
	SessionDir string

	// Ephemeral passes --no-session: nothing is persisted. Use for
	// probe sessions so a liveness check does not litter the
	// session store.
	Ephemeral bool

	// SessionName sets the display name (--name).
	SessionName string

	// NoExtensions, NoSkills, NoPromptTemplates and NoContextFiles
	// disable discovery of host-local resources. An edge sidecar
	// wants all four on unless the operator opted in: whatever is
	// installed in the host's ~/.pi would otherwise silently join
	// the ops session and change its behaviour.
	NoExtensions      bool
	NoSkills          bool
	NoPromptTemplates bool
	NoContextFiles    bool

	// Offline passes --offline (equivalent to PI_OFFLINE=1) to skip
	// startup network calls such as model-catalog refresh.
	Offline bool

	// ExtraArgs is appended verbatim for flags an extension
	// registers. Reserved for operator escape hatches.
	ExtraArgs []string
}

// Command returns the executable and argv for the configured launch.
func (o LaunchOptions) Command() (string, []string, error) {
	if o.Bin != "" && o.Script != "" {
		return "", nil, errors.New("pirpc: set either Bin or Script, not both")
	}
	if o.Bin != "" {
		return o.Bin, o.Args(), nil
	}
	if o.Script == "" {
		return "", nil, errors.New("pirpc: Script or Bin required")
	}
	if strings.ContainsAny(o.Script, " \t") {
		// A path with an embedded space is legal; a "node x.js"
		// two-token string is not, and that is the mistake this
		// guard catches early instead of at fork time.
		return "", nil, fmt.Errorf("pirpc: Script %q looks like a command line, not a path", o.Script)
	}
	node := o.Node
	if node == "" {
		node = "node"
	}
	return node, append([]string{o.Script}, o.Args()...), nil
}

// Args returns Pi's own flags, excluding the interpreter and script
// path. Order is stable so tests and audit records can compare argv.
func (o LaunchOptions) Args() []string {
	args := []string{"--mode", "rpc"}
	if o.Provider != "" {
		args = append(args, "--provider", o.Provider)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.SystemPromptFile != "" {
		args = append(args, "--append-system-prompt", o.SystemPromptFile)
	}
	for _, dir := range o.SkillDirs {
		if dir != "" {
			args = append(args, "--skill", dir)
		}
	}
	if len(o.Tools) > 0 {
		args = append(args, "--tools", strings.Join(o.Tools, ","))
	}
	if len(o.ExcludeTools) > 0 {
		args = append(args, "--exclude-tools", strings.Join(o.ExcludeTools, ","))
	}
	if o.Ephemeral {
		args = append(args, "--no-session")
	} else if o.SessionDir != "" {
		args = append(args, "--session-dir", o.SessionDir)
	}
	if o.SessionName != "" {
		args = append(args, "--name", o.SessionName)
	}
	if o.NoExtensions {
		args = append(args, "--no-extensions")
	}
	if o.NoSkills {
		args = append(args, "--no-skills")
	}
	if o.NoPromptTemplates {
		args = append(args, "--no-prompt-templates")
	}
	if o.NoContextFiles {
		args = append(args, "--no-context-files")
	}
	if o.Offline {
		args = append(args, "--offline")
	}
	return append(args, o.ExtraArgs...)
}
