package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// How a node's agent reaches a model, and why none of this is a PiG import.
//
// The agent reads its provider configuration from two different scopes, and
// conflating them is the bug this file exists to prevent:
//
//   - the *project* scope, `<Cwd>/.pig`, which is where the package list and
//     the profile live. Cwd is the plugin bundle root — reviewed, signed,
//     digest-covered content. Nothing that varies per deployment may be
//     written into it.
//   - the *agent* scope, `<AgentDir>/models.json`, which is where provider
//     endpoints and credentials live. AgentDir is $PIG_CODING_AGENT_DIR, or
//     a default derived from the environment.
//
// The node owns the second scope outright and points the agent at it, because
// the default is not safe to rely on. Measured against PiG 0.3.0: with
// $PIG_CODING_AGENT_DIR unset and $HOME unset — which is what a systemd unit
// with no HOME= produces — DefaultAgentDir() discards the os.UserHomeDir()
// error and returns the *relative* path ".pig/agent", which the agent
// resolves against its working directory. On this node that is the plugin
// bundle root, so a credential file would land inside signed, digest-covered
// plugin content. Setting the variable explicitly is the only way to make the
// location a fact rather than a consequence of the service manager.
//
// The credential itself never reaches the filesystem. models.json names the
// environment variable ("$OPSKEEPER_EDGE_AGENT_TOKEN") and the token is
// passed in the agent process's environment. PiG resolves that reference at
// authentication time on every request and caches nothing, so a rotated token
// takes effect on the next turn without restarting the agent or rewriting a
// file. A token on disk would be a token an operator has to remember to
// rotate, on every host, by hand.

// agentModelConfigDirEnv names the node-owned agent configuration root.
//
// It is a separate variable from OPSKEEPER_EDGE_AGENT_DIR on purpose: that
// one is the *working* directory and therefore the package root, and this one
// is the agent's own scope. They used to be conflated by nobody, because
// there was no second one; conflating them now would put a credential inside
// the plugin tree.
const agentModelConfigDirEnv = "OPSKEEPER_EDGE_AGENT_CONFIG_DIR"

// defaultAgentModelConfigDir is where a node keeps its agent scope.
//
// It is a sibling of the package root rather than a child, so that the agent
// reading its configuration cannot walk into reviewed plugin content, and so
// that removing the plugin bundle cannot take the node's model configuration
// with it.
const defaultAgentModelConfigDir = "/var/lib/opskeeper-edge/agent-home"

// The environment contract. Three variables, because they answer three
// different questions and combining any two of them produces a state that is
// either ambiguous or unrecoverable.
const (
	// agentBaseURLEnv is the OpenAI-compatible endpoint the node's agent
	// talks to. Empty means "this node has no model configured", which is a
	// legitimate state — the agent's own scope is then left entirely alone,
	// so an operator who provisioned a provider by hand keeps it.
	agentBaseURLEnv = "OPSKEEPER_EDGE_AGENT_BASE_URL"

	// agentTokenEnv is both where the operator puts the credential and the
	// name the agent's configuration refers to. One name, two roles, on
	// purpose: a second variable holding the *name* of the credential
	// variable would let the two disagree, and the failure would be an agent
	// that cannot authenticate with a message naming a variable nobody set.
	agentTokenEnv = "OPSKEEPER_EDGE_AGENT_TOKEN"

	// agentModelEnv pins the model slug the node's provider serves, so the
	// node and the manager agree on which model answers.
	agentModelEnv = "OPSKEEPER_EDGE_AGENT_MODEL"
)

// agentModelProviderID is the provider name written into models.json.
//
// It is a constant rather than configuration because it is not a choice: it
// names the one provider the node knows how to reach, and an operator who
// wants a different one is configuring a different deployment. Making it
// configurable would only produce nodes whose console reports one provider
// and whose agent resolves another.
const agentModelProviderID = "opskeeper"

// agentModelConfig is a node's resolved model endpoint.
type agentModelConfig struct {
	// BaseURL is the OpenAI-compatible root, e.g. https://opskeeper.example.com/llm/v1.
	BaseURL string
	// Token is the credential. It is passed to the agent in the process
	// environment and never written to disk.
	Token string
	// Model is the slug the endpoint serves. Empty lets the endpoint's
	// default stand.
	Model string
	// Dir is the node-owned agent scope written to PIG_CODING_AGENT_DIR.
	Dir string
}

// modelsFile is the agent's own schema, and only the part this node fills in.
//
// The apiKey field is a reference, not a key. Writing the token here would
// put a credential in a file that is rewritten on every boot, lives next to
// other configuration, and gets copied by every "back up the node config"
// habit an operator has. PiG expands "$VAR" at authentication time, so the
// reference is both the safer file and the rotatable one.
type modelsFile struct {
	Providers map[string]modelsProvider `json:"providers"`
}

// modelsProvider is one entry of models.json.
type modelsProvider struct {
	Name    string         `json:"name"`
	BaseURL string         `json:"baseUrl"`
	APIKey  string         `json:"apiKey"`
	API     string         `json:"api,omitempty"`
	Models  []modelsModel  `json:"models,omitempty"`
	Headers map[string]any `json:"-"`
}

// modelsModel is one model the provider serves.
type modelsModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// agentModelConfigFromEnv resolves the node's model endpoint.
//
// The error cases are the interesting part, and they are all refusals rather
// than fallbacks:
//
//   - an endpoint with no credential is refused. Starting anyway produces a
//     node that authenticates, dials home, reports its metrics, loads its
//     plugins and then answers every question with "no API key found" —
//     indistinguishable, from the outside, from a model having an opinion.
//   - a credential with no endpoint is refused. The token would be handed to
//     whatever provider the agent happens to resolve, which is exactly the
//     "an agent handed those would be handed the ability to assert them"
//     mistake the gate socket comment warns about: the credential would leave
//     this node for a destination nobody chose.
//
// Both are configuration errors an operator can fix, and the caller treats an
// error here as "this node runs without an AI agent" — the edge still
// collects telemetry, which is the property that makes failing loudly safe.
func agentModelConfigFromEnv() (agentModelConfig, bool, error) {
	baseURL := strings.TrimSpace(os.Getenv(agentBaseURLEnv))
	token := strings.TrimSpace(os.Getenv(agentTokenEnv))
	dir := strings.TrimSpace(os.Getenv(agentModelConfigDirEnv))
	if dir == "" {
		dir = defaultAgentModelConfigDir
	}

	switch {
	case baseURL == "" && token == "":
		// Not configured, not an error. The agent's own configuration scope
		// is left untouched, so a node provisioned by hand keeps working.
		return agentModelConfig{}, false, nil
	case baseURL == "":
		return agentModelConfig{}, false, fmt.Errorf(
			"%s is set but %s is empty; the credential would be sent to whatever provider the "+
				"agent resolves on its own, which is not a destination anybody chose. "+
				"Set %s, or unset %s",
			agentTokenEnv, agentBaseURLEnv, agentBaseURLEnv, agentTokenEnv)
	case token == "":
		return agentModelConfig{}, false, fmt.Errorf(
			"%s=%s but %s is empty; the node would start, load its plugins, and answer every "+
				"question with no model behind it",
			agentBaseURLEnv, baseURL, agentTokenEnv)
	}

	return agentModelConfig{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		Model:   strings.TrimSpace(os.Getenv(agentModelEnv)),
		Dir:     dir,
	}, true, nil
}

// writeAgentModelConfig materialises the agent scope and returns its path.
//
// It is written whole and installed by rename, for the reason
// writeAgentSettings is: a node killed halfway through leaves either the old
// configuration or the new one, never a truncated file that names a provider
// with no endpoint.
//
// The directory is 0700 and the file 0600 even though the file holds no
// secret. It is the node's configuration root, and the next thing to land
// there will be something that does.
func writeAgentModelConfig(cfg agentModelConfig) (string, error) {
	if cfg.Dir == "" {
		return "", errors.New("agent model: no configuration directory")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return "", fmt.Errorf("agent model: create %s: %w", cfg.Dir, err)
	}

	models := []modelsModel{}
	if cfg.Model != "" {
		models = append(models, modelsModel{ID: cfg.Model, Name: cfg.Model})
	}
	doc := modelsFile{Providers: map[string]modelsProvider{
		agentModelProviderID: {
			Name:    "OpsKeeper",
			BaseURL: cfg.BaseURL,
			// The reference, never the value. See the type comment.
			APIKey:  "$" + agentTokenEnv,
			API:     "openai-completions",
			Models:  models,
			Headers: nil,
		},
	}}

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("agent model: encode models.json: %w", err)
	}
	body = append(body, '\n')

	final := filepath.Join(cfg.Dir, "models.json")
	tmp, err := os.CreateTemp(cfg.Dir, "models.json.*")
	if err != nil {
		return "", fmt.Errorf("agent model: stage models.json: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agent model: write models.json: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agent model: chmod models.json: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("agent model: close models.json: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", fmt.Errorf("agent model: install models.json: %w", err)
	}
	return final, nil
}

// agentModelEnvVars is what the agent process needs in order to find the
// configuration this node just wrote and the credential to use with it.
//
// The two are returned together rather than written into a map at the call
// site, because they are one fact: pointing the agent at a scope that does
// not exist, or handing it a scope without the credential, are the same
// misconfiguration wearing different clothes, and the code that assembles
// them should not be able to do one without the other.
//
// PIG_CODING_AGENT_DIR is PiG's own variable. It is a string here rather than
// an import of PiG's paths package on purpose — the same reason
// agentConfigDirName is: importing it would put PiG in the node's dependency
// graph for a constant, and the node plane must not name PiG. The delivery
// tests assert the two spellings stay in step.
func (c agentModelConfig) agentModelEnvVars() map[string]string {
	return map[string]string{
		"PIG_CODING_AGENT_DIR": c.Dir,
		agentTokenEnv:          c.Token,
	}
}
