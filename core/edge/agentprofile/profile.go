// Package agentprofile writes the node's agent profile: the one file that
// decides what the model is even offered on a node.
//
// Everything else on this node argues about what a tool call is *allowed*
// to do. The allow-list in core/edge/policygate is built from the admitted
// manifests, the courier extension in core/pig/extensions/opskeeper-gate
// carries every call across the socket to be judged there, and the host
// holds the only approval authority. This file is upstream of all of that,
// and it is a different question: not "may this call run" but "does the
// model know this tool exists".
//
// That question has an answer today and the answer is wrong. The agent is
// launched as `pig --mode rpc` with no profile, so it starts with PiG's
// stock built-ins active - read, bash, powershell, edit, write, grep, find,
// ls. A node's agent is a process running with the node's privileges, so
// `bash` on it is arbitrary code execution on the host, offered to a model
// whose whole purpose is to be talked into running things.
//
// The gate does refuse those calls, because an unbound tool is blocked. But
// refusing is not the same as not offering, and the difference is a whole
// class of wasted and misleading behaviour:
//
//   - The model is shown a tool it can never successfully use, so it tries
//     it, reads a refusal, and tries something else at random. The
//     transcript fills with attempts against a capability the design says
//     does not exist.
//   - Every such attempt is a real round trip: a socket write, a host
//     lookup, a refusal, a frame back. During an incident that is latency
//     spent on a decision that was never in doubt.
//   - An operator reading the transcript sees the agent reaching for bash
//     on a production node, and has to reason about whether that was
//     allowed. It was not - but the transcript does not say so at the point
//     where it would have been cheap to say so.
//
// The profile removes them from the menu instead, which is the only place
// the removal is actually complete. PiG intersects what a profile selects
// with what the runtime registered, so `tools: []` means "no built-ins",
// and an extension the profile does not name keeps all of its tools - which
// is what makes this safe to write without enumerating the plugins: the
// packages a node has admitted are still exactly the tools it can call, and
// the profile only subtracts the host's own shell.
//
// Two other fields are set, and both are the same subtraction again.
//
// `discovery.extensions: []` and `discovery.skills: []` turn off ambient
// discovery of the `workspace` and `user` scopes. Without it, a file
// dropped into the agent's home directory is a skill the model may invoke,
// and the host's allow-list has nothing to say about it because it never
// passed a review. This is why the file is written into the agent's own
// config directory rather than discovered: a node's skill set is a
// reviewed set, and the only way to keep it that way is to stop the agent
// from finding anything the review did not admit.
//
// It is worth being precise about what this does NOT do, because the
// distinction is the whole point of the design. It does not grant
// anything. The profile is a subtraction from a default that already
// included too much; the set of tools the node can actually run is still
// exactly the manifests it admitted, still enforced at the gate, still
// audited in the host's ledger. A node whose profile was deleted would
// boot an agent that offers bash and is refused at the gate - less safe to
// read, not more capable. The profile is a second line drawn under a
// boundary that already exists, not the boundary itself.
package agentprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the profile's filename inside the agent's config directory.
//
// It carries a prefix rather than being named `piglet.yaml` because the
// agent's own config directory is a place other things also write, and a
// generic filename in a directory that decides what runs on a host is a
// name something else will eventually take.
const FileName = "opskeeper-node.piglet.yaml"

// Name is the profile's name inside the file.
//
// It is the node's identity in the agent's own diagnostics, so it says
// what the node is rather than what the file is. An operator reading
// `pig status` on a node sees this and should be able to conclude from the
// name alone that it is looking at an OpsKeeper node agent.
const Name = "opskeeper-node"

// Write renders the node profile into dir and returns its path.
//
// The file is written whole, to a temporary name, and renamed - the same
// discipline writeAgentSettings uses for the package list, and for the same
// reason. A node killed halfway through this leaves either the old profile
// or the new one, never a truncated YAML that the agent would refuse to
// parse at startup. A node that fails to start its agent is a node with no
// incident response, so this write is as load-bearing as any other file on
// the host and is not allowed to be partial.
//
// The permission is 0640 for the same reason as the settings file: the
// agent reads it as the user it runs as, root can read it, and the rest of
// the host cannot. It is not a secret, but it is a statement of what runs
// on this machine and there is no reason for an unprivileged local account
// to be able to find and edit it.
func Write(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("agentprofile: no directory to write the profile into")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("agentprofile: create %s: %w", dir, err)
	}

	final := filepath.Join(dir, FileName)
	tmp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return "", fmt.Errorf("agentprofile: stage the profile: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(Render()); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentprofile: write the profile: %w", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentprofile: chmod the profile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("agentprofile: close the profile: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", fmt.Errorf("agentprofile: install the profile at %s: %w", final, err)
	}
	return final, nil
}

// Render is the profile itself, as it is written to disk.
//
// It is exported so a test can assert the file's content without writing
// it, and so the review surface is a function rather than a fixture: the
// profile is what the node runs, and a reviewer should be able to read it
// here rather than go looking for a generated file on some host.
//
// It is a literal rather than a template because there is nothing in it
// that varies per node: the profile is the same subtraction everywhere, and
// a generator here would be machinery for producing a constant.
func Render() string {
	return `# The OpsKeeper node agent profile — GENERATED, do not edit.
#
# This file decides what the model is OFFERED on this node. It does not
# decide what a call may do: that is the host's allow-list, built from the
# admitted plugin manifests and enforced at core/edge/policygate, which
# refuses anything this node did not admit. This file is upstream of all of
# that, and asks the different question — whether the model knows a tool
# exists at all.
#
# It exists because without it the agent starts with PiG's stock built-ins
# active, and one of them is a shell.

name: ` + Name + `
description: "OpsKeeper node agent. Its tools are the opskeeper-* plugin extensions, and every one of them is a route to the host, not an implementation in this process."

# No PiG built-in tools. This removes read, bash, powershell, edit, write,
# grep, find and ls.
#
# The agent is a separate process running with this node's privileges, so
# bash here is arbitrary code execution on the host. The design already
# refuses those calls — an unbound tool is blocked at the gate — but
# refusing is not the same as not offering, and the difference costs a round
# trip per attempt, fills an incident transcript with calls that could never
# have worked, and leaves an operator looking at an agent reaching for a
# shell and having to work out whether that was allowed.
#
# The empty list is exact, not additive. An extension this profile does not
# name keeps every tool it registered, so the plugins this node admitted
# are still exactly the tools the model can call. This field only subtracts
# the host's own shell.
tools: []

# No ambient discovery. Without this, a skill dropped into the agent's home
# directory is a tool the model may invoke, and the host's allow-list has
# nothing to say about it because it never passed a review. A node's skill
# set is a reviewed set, and keeping it that way means the agent is not
# allowed to find anything the review did not admit.
#
# This does not affect the packages in settings.json: those are the reviewed
# ones, and they keep loading exactly as before.
discovery:
  extensions: []
  skills: []
`
}
