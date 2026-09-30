package ports

import (
	"context"
	"encoding/json"

	"github.com/vincent-wuhan/opskeeper/core/domain"
)

// ToolSchema is a tool's model-facing declaration: the wire name, the
// blurb the model routes on, and the JSON Schema of the argument object.
//
// The contract keeps the routing hint separate from the description for the
// same reason the runtime does: a sibling tool's disambiguation belongs in a
// different system-prompt position than the tool's purpose statement, and a
// skill manifest must be able to override one without rewriting the other.
type ToolSchema struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	WhenToUse   string           `json:"when_to_use,omitempty"`
	Parameters  json.RawMessage  `json:"parameters,omitempty"`
	Class       domain.ToolClass `json:"class,omitempty"`
	// Origin records where the tool came from, so policy can treat a
	// runtime-discovered tool differently from a compiled-in builtin
	// without matching on wire names. Empty means builtin.
	Origin string `json:"origin,omitempty"`
}

// Origins for runtime-discovered tools.
const (
	OriginBuiltin = ""
	OriginMCP     = "mcp"
	OriginSkill   = "skill"
	OriginPlugin  = "plugin"
)

// ToolCall is a model's request to invoke a tool.
type ToolCall struct {
	ID   string
	Name string
	// Arguments is the raw JSON the model produced. It is not pre-validated
	// here: a tool parses it against its own schema, and a tool that wants
	// to coerce input before validation implements the optional preparer.
	Arguments json.RawMessage
}

// Tool is an executable capability.
//
// Invokable is the whole contract: everything else in OpsKeeper that needs
// a tool, a skill, or an MCP-backed capability adapts to it. A tool reports
// its own class and origin rather than having them inferred from its name,
// because name matching stops scaling once MCP servers, installed skills,
// and node plugins all contribute tools.
type Tool interface {
	// Schema returns the model-facing declaration. It must be safe to call
	// on a hot path: implementations cache after first load.
	Schema() ToolSchema
	// Invoke executes the tool and returns a JSON string fed back to the
	// model. Returning an error surfaces to the loop, which classifies it;
	// a tool that wants to report a domain failure to the model should
	// return a result describing that failure rather than an error.
	Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

// ArgumentPreparer is an optional interface a tool implements to normalise
// its raw input before schema validation runs. Models emit flat legacy
// shapes often enough that validating first turns a recoverable call into a
// hard error; normalising first keeps the tool's schema strict without
// making the model pay for it.
type ArgumentPreparer interface {
	PrepareArguments(args json.RawMessage) (json.RawMessage, error)
}

// ToolBag is the set of tools a turn may reach. The runtime filters it by
// caller role and agent profile before every turn, so a viewer can never
// reach a mutating tool regardless of what a profile requests.
type ToolBag interface {
	// Tools returns the currently admitted tools.
	Tools() []Tool
	// Names returns the admitted tool names.
	Names() []string
}
