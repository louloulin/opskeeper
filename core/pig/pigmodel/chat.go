// chat.go runs one completion and owns every conversion between OpsKeeper's
// transcript vocabulary and PiG's.
//
// It exists so the PiG boundary is a boundary rather than a convention. A
// caller that built a transcript itself would hold ai.TranscriptContext, and
// holding that type is holding a PiG API: the next PiG release that renames a
// content block would then break that caller at compile time, which is the
// repository-wide rebuild this module was created to prevent. The host names
// a request in ports vocabulary; this file is the only place the two shapes
// meet.
//
// The conversions are total and they are checked, not assumed. PiG validates a
// transcript before a provider sees it, and it validates the parts operations
// depend on: a tool result with no tools in the request, or tool arguments
// that are not a JSON object, is refused as a malformed request. A
// conversion bug therefore surfaces here, naming the message index, instead
// of as a provider 400 the console cannot explain.
package pigmodel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MichaelKinsy/PiG/ai"

	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// Complete resolves a request to a model, runs it, and returns the reply in
// ports vocabulary.
//
// A nil model or a provider that returns no stream is an error rather than an
// empty reply: every caller of this function bills the request or reports a
// turn to an operator, and both are lies told about a call that never
// happened.
func (r *Registry) Complete(ctx context.Context, req ports.LLMRequest) (*ports.LLMResponse, error) {
	model, opts, err := r.Model(ctx, req.Selection)
	if err != nil {
		return nil, fmt.Errorf("pigmodel: resolve model: %w", err)
	}

	transcript, err := BuildTranscript(req)
	if err != nil {
		return nil, err
	}

	stream, err := model.Provider.Stream(ctx, transcript, opts)
	if err != nil {
		return nil, fmt.Errorf("pigmodel: chat completion: %w", err)
	}

	settled, err := drainAssistant(stream)
	if err != nil {
		return nil, err
	}

	return assistantFromPiG(settled)
}

// BuildTranscript converts a port request into PiG's normalized transcript.
//
// Exported because a caller that needs to inspect what would be sent — the
// prompt a run is about to issue, for a dry run or an audit row — must be able
// to ask without issuing the call. It is exported rather than reached through
// a getter so that the type it would return never appears in this package's
// API at all: the returned value is what a caller renders, never what it
// stores.
func BuildTranscript(req ports.LLMRequest) (ai.TranscriptContext, error) {
	msgs := make([]ai.Message, 0, len(req.Messages))
	for i, m := range req.Messages {
		converted, err := messageToPiG(m)
		if err != nil {
			return ai.TranscriptContext{}, fmt.Errorf("pigmodel: message[%d]: %w", i, err)
		}
		msgs = append(msgs, converted)
	}

	tools := make([]ai.ToolSchema, 0, len(req.Tools))
	for i, t := range req.Tools {
		params, err := decodeSchema(t.Parameters)
		if err != nil {
			return ai.TranscriptContext{}, fmt.Errorf("pigmodel: tool[%d] %q parameters: %w", i, t.Name, err)
		}
		tools = append(tools, ai.ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
	}

	return ai.NormalizeContext(ai.Context{Messages: msgs, Tools: tools}), nil
}

// messageToPiG maps one message across the boundary.
func messageToPiG(m ports.LLMMessage) (ai.Message, error) {
	switch m.Role {
	case "system":
		return ai.SystemMessage{Content: ai.SystemText(m.Content)}, nil

	case "user":
		return ai.UserMessage{Content: ai.UserText(m.Content)}, nil

	case "assistant":
		msg := ai.AssistantMessage{}
		if m.Content != "" {
			msg.Content = append(msg.Content, ai.TextContent{Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			args, err := decodeToolArguments(tc.Arguments)
			if err != nil {
				return nil, fmt.Errorf("tool call %q: %w", tc.Name, err)
			}
			msg.Content = append(msg.Content, ai.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: args,
			})
		}
		return msg, nil

	case "tool":
		if m.ToolCallID == "" {
			// An orphaned tool result cannot be correlated to the call it
			// answers. Providers reject the transcript, and the resulting
			// error names no message index — so it is caught here, where it
			// can.
			return nil, fmt.Errorf("tool result for %q has no tool call id", m.ToolName)
		}
		return ai.ToolResultMessage{
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			Content:    []ai.ToolResultMessageContent{ai.TextContent{Text: m.Content}},
		}, nil
	}
	return nil, fmt.Errorf("unknown role %q", m.Role)
}

// decodeToolArguments turns a stored argument blob into PiG's JSON object.
//
// Absent arguments become an empty object rather than a null: PiG validates
// that the field is an object, and "no arguments" is a legal object while
// null is not.
func decodeToolArguments(raw json.RawMessage) (ai.JsonObject, error) {
	if len(raw) == 0 {
		return ai.JsonObject{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
	}
	if out == nil {
		return ai.JsonObject{}, nil
	}
	return ai.JsonObject(out), nil
}

// decodeSchema validates a caller-supplied JSON Schema and returns it in the
// map form PiG's tool schema carries. An empty schema becomes
// object-with-no-properties, which is what a parameterless tool means.
func decodeSchema(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	return out, nil
}

// drainAssistant consumes a PiG stream to its terminal message.
//
// Streaming has no consumer on this path: the contract is one complete
// assistant turn, so the deltas are deliberately discarded rather than
// forwarded. PiG's stream already applies backpressure by dropping frames for
// a slow reader, so draining without work is cheap.
func drainAssistant(stream *ai.AssistantMessageEventStream) (*ai.AssistantMessage, error) {
	if stream == nil {
		return nil, fmt.Errorf("pigmodel: provider returned no stream")
	}
	settled, err := stream.ResultContext(context.Background())
	if err != nil {
		return nil, fmt.Errorf("pigmodel: chat completion: %w", err)
	}
	if settled == nil {
		return nil, fmt.Errorf("pigmodel: empty choices in response")
	}
	return settled, nil
}

// assistantFromPiG maps the settled message back onto the port vocabulary.
func assistantFromPiG(msg *ai.AssistantMessage) (*ports.LLMResponse, error) {
	out := &ports.LLMResponse{StopReason: ports.StopEndTurn}
	for _, block := range msg.Content {
		switch b := block.(type) {
		case ai.TextContent:
			out.Content += b.Text
		case ai.ToolCall:
			args, err := json.Marshal(b.Arguments)
			if err != nil {
				return nil, fmt.Errorf("pigmodel: encode tool call %q arguments: %w", b.Name, err)
			}
			out.ToolCalls = append(out.ToolCalls, ports.ToolCall{
				ID:   b.ID,
				Name: b.Name,
				// An absent argument set is a parameterless call, which
				// is an empty object rather than absent JSON: the host
				// stores this blob and replays it verbatim.
				Arguments: nonNullJSON(args),
			})
		}
		// Other block kinds (thinking, images, provider-specific content)
		// are dropped rather than mapped to a guess. The tool executor
		// reads only text and tool calls; synthesising content for the
		// rest would put text into the transcript the model never said.
	}
	if len(out.ToolCalls) > 0 {
		out.StopReason = ports.StopToolUse
	}
	out.Usage = usageFromPiG(msg)
	return out, nil
}

// nonNullJSON keeps a marshalled empty object from becoming the four bytes
// "null". json.Marshal of a nil map is null, and a stored null argument set
// replays as a request PiG refuses.
func nonNullJSON(b []byte) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(b)
}

// usageFromPiG converts PiG's token accounting.
//
// PiG reports input, output, and cache separately, and a provider-reported
// total that need not equal their sum. All of it is carried across: the
// total goes to ReportedTotal rather than being recomputed, because reasoning
// tokens are billed inside the provider's total and appear in neither input
// nor output, so recomputing would under-report the bill.
func usageFromPiG(msg *ai.AssistantMessage) ports.Usage {
	// ObserveUsage already answers a nil message with a zero Usage, so a
	// provider that reported nothing at all lands here as an honest zero
	// rather than as a guess.
	u := msg.ObserveUsage()
	return ports.Usage{
		InputTokens:      u.Input,
		OutputTokens:     u.Output,
		CacheReadTokens:  u.CacheRead,
		CacheWriteTokens: u.CacheWrite,
		ReportedTotal:    u.TotalTokens,
		CostUSD:          u.Cost.Total,
	}
}
