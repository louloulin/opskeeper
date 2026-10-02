// wire.go owns the OpenAI-compatible chat-completions HTTP surface.
//
// Why this file exists rather than an SDK import: the previous implementation
// used github.com/sashabaranov/go-openai, whose CreateChatCompletion validates
// the request *before it is sent* — it refuses MaxTokens on a reasoning
// model, validates schema, and rejects a known reasoning model carrying a
// sampling param. Those checks are client-side errors, and at our call site
// they are indistinguishable from a provider 400. That matters because the
// reactive sampling retry in Chat() learns by *interpreting a rejection*: it
// only strips the params when a rejection arrives without having sent them
// on a first attempt in the first place. A client-side refusal of a model we
// had classified as non-reasoning would consume the single retry without
// ever reaching the provider, so the model would never be learned and every
// later call would fail the same way. Owning the request path removes that
// failure mode at the root: every error the client sees is now a real
// response or a real transport failure.
//
// The structs below are a deliberate subset of the API — only the fields
// this package sets or reads. Unknown response fields are ignored, which is
// what lets one client front OpenAI, Zhipu, Gemini's compat endpoint and a
// local Ollama without a per-provider branch.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// wireRequest is the POST body for /chat/completions.
//
// Temperature is a pointer, not a float32 with omitempty. A reasoning model
// must see the field *absent* — sending an explicit 0 is also rejected by
// some gateways — and omitempty cannot express that because 0 is the zero
// value. A pointer distinguishes "unset" from "set to 0".
type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Temperature *float32      `json:"temperature,omitempty"`
}

// wireMessage is one entry in the messages array.
//
// Name/ToolCallID are omitempty because a "user" message with an empty
// tool_call_id is rejected by strict gateways; the removed SDK's struct tags
// carried the same omitempty, so the wire output is unchanged.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}

// wireToolCall is an assistant-requested invocation.
type wireToolCall struct {
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type"`
	Function wireToolCallFunction `json:"function"`
}

// wireToolCallFunction carries the name plus the argument blob as a *string*.
// Arguments is deliberately a string, not a json.RawMessage: the API defines
// it as a JSON-encoded string, and some gateways double-encode an object.
type wireToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// wireTool is one entry in the tools array.
type wireTool struct {
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

// wireToolFunction is the function definition. Parameters is a
// json.RawMessage so a caller-supplied JSON Schema is embedded verbatim
// rather than being re-marshalled through `any`, which would reorder keys
// (harmless) or drop unknown keywords (not harmless — a provider that
// supports a newer JSON Schema keyword would stop receiving it).
type wireToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// wireResponse is the subset of a chat completion we consume.
type wireResponse struct {
	Choices []wireChoice `json:"choices"`
	Usage   wireUsage    `json:"usage"`
}

// wireChoice is one completion candidate. Only index 0 is read: the API
// returns one choice unless `n` is set, and this client never sets it.
type wireChoice struct {
	Message wireMessage `json:"message"`
}

// wireUsage is the token accounting we bill against.
type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// wireHTTPError is a non-2xx response.
//
// Error() reproduces the removed SDK's "error, status code: N, status: S,
// message: M, body: B" format on purpose. isSamplingParamError matches on
// the *message text* of a 400, and the reactive retry was verified against
// that text; keeping the format means the retry keeps working and the
// existing tests keep asserting the same user-visible failure.
type wireHTTPError struct {
	statusCode int
	status     string
	message    string
	body       []byte
}

func (e *wireHTTPError) Error() string {
	return fmt.Sprintf(
		"error, status code: %d, status: %s, message: %s, body: %s",
		e.statusCode, e.status, e.message, e.body,
	)
}

// postChatCompletion sends one chat-completions request and returns the
// decoded response.
//
// The body is marshalled here, on every call, rather than being pre-encoded
// by the caller: the retry in Chat() mutates the request between attempts,
// and a body cached across attempts would resend the parameters the
// provider just rejected — silently defeating the retry.
func (c *openaiClient) postChatCompletion(ctx context.Context, ep wireEndpoint, apiKey string, req *wireRequest) (wireResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		// Reaching here means a caller built a struct this package cannot
		// serialise, which is a bug in this package, not in the provider.
		return wireResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.url, bytes.NewReader(body))
	if err != nil {
		return wireResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// Set only when non-empty: a keyless local server (Ollama, LM Studio)
	// accepts a placeholder key, but an empty "Bearer " header is malformed
	// and some gateways reject it outright.
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := ep.client.Do(httpReq)
	if err != nil {
		return wireResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return wireResponse{}, newWireHTTPError(resp)
	}

	var out wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return wireResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// newWireHTTPError reads the body and extracts the provider's error message.
//
// The body is read with a cap: a gateway that answers with a multi-megabyte
// HTML error page must not be copied into the error string, which is
// logged, wrapped and (on the AIOps path) surfaced to a user.
func newWireHTTPError(resp *http.Response) error {
	const maxErrBody = 64 << 10
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))

	message := ""
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		message = envelope.Error.Message
	}

	return &wireHTTPError{
		statusCode: resp.StatusCode,
		status:     resp.Status,
		message:    message,
		body:       raw,
	}
}
