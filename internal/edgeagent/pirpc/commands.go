package pirpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// State is the subset of Pi's get_state payload the edge acts on.
// Extra fields Pi adds are ignored by design.
type State struct {
	Model               *Model `json:"model"`
	ThinkingLevel       string `json:"thinkingLevel"`
	IsStreaming         bool   `json:"isStreaming"`
	IsCompacting        bool   `json:"isCompacting"`
	SessionID           string `json:"sessionId"`
	SessionFile         string `json:"sessionFile"`
	SessionName         string `json:"sessionName"`
	MessageCount        int    `json:"messageCount"`
	PendingMessageCount int    `json:"pendingMessageCount"`
}

// Model identifies the upstream model Pi is configured with. A nil
// Model in State means Pi booted without a usable provider — the
// process is alive but cannot reason, which the edge reports as
// degraded rather than dead.
type Model struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl"`
}

// BashResult is the payload of a direct RPC bash command.
type BashResult struct {
	Output         string `json:"output"`
	ExitCode       int    `json:"exitCode"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath"`
}

// StreamingBehavior selects what happens when a prompt arrives while
// Pi is already streaming. Sending a prompt with no behaviour during
// streaming is rejected by Pi, so callers must be explicit.
type StreamingBehavior string

const (
	// BehaviorSteer delivers the message after the current
	// assistant turn finishes its tool calls, before the next LLM
	// call. This is the one to use to interrupt a runaway
	// diagnosis.
	BehaviorSteer StreamingBehavior = "steer"

	// BehaviorFollowUp delivers the message only once Pi stops.
	BehaviorFollowUp StreamingBehavior = "followUp"
)

// GetState round-trips get_state. It is the edge's real liveness
// probe: it proves the child is not just alive at the process level
// but is reading stdin, parsing JSONL and answering — which a PID
// check cannot show.
func (s *Session) GetState(ctx context.Context) (*State, error) {
	resp, err := s.Do(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		return nil, err
	}
	var st State
	if err := decodeData(resp, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Bash runs a command through Pi's direct bash channel. Output is
// added to Pi's context for the next prompt, which is how the edge
// hands host facts to the model without letting the model choose the
// command. It needs no LLM call, so it also works on a Pi that has
// no provider credentials.
//
// This is NOT a policy boundary: whether a command may run at all is
// decided by cmdpolicy before this is called.
func (s *Session) Bash(ctx context.Context, command string) (*BashResult, error) {
	if command == "" {
		return nil, errors.New("pirpc: empty bash command")
	}
	resp, err := s.Do(ctx, map[string]any{"type": "bash", "command": command})
	if err != nil {
		return nil, err
	}
	var res BashResult
	if err := decodeData(resp, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Prompt sends a user prompt. behavior may be empty when Pi is idle;
// during streaming it must be BehaviorSteer or BehaviorFollowUp.
// A successful return means Pi accepted or queued the prompt — the
// outcome arrives on the event stream, not in this response.
func (s *Session) Prompt(ctx context.Context, message string, behavior StreamingBehavior) error {
	cmd := map[string]any{"type": "prompt", "message": message}
	if behavior != "" {
		cmd["streamingBehavior"] = string(behavior)
	}
	_, err := s.Do(ctx, cmd)
	return err
}

// Abort stops the current operation and resolves once Pi is idle.
func (s *Session) Abort(ctx context.Context) error {
	_, err := s.Do(ctx, map[string]any{"type": "abort"})
	return err
}

// AbortBash stops a running direct bash command.
func (s *Session) AbortBash(ctx context.Context) error {
	_, err := s.Do(ctx, map[string]any{"type": "abort_bash"})
	return err
}

// LastAssistantText returns the text of Pi's last assistant message,
// which is how a skill run's conclusion is collected for the audit
// chain without reassembling streaming deltas.
func (s *Session) LastAssistantText(ctx context.Context) (string, error) {
	resp, err := s.Do(ctx, map[string]any{"type": "get_last_assistant_text"})
	if err != nil {
		return "", err
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := decodeData(resp, &data); err != nil {
		return "", err
	}
	return data.Text, nil
}

func decodeData(resp *Response, out any) error {
	if len(resp.Data) == 0 {
		return fmt.Errorf("pirpc: %s: response carried no data", resp.Command)
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("pirpc: %s: decode data: %w", resp.Command, err)
	}
	return nil
}
