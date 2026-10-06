package pigagent

import (
	"regexp"
	"strings"
)

// inlineThinkBlock matches the <think>…</think> run some providers fold into
// the assistant's text content rather than exposing as a separate reasoning
// channel. MiniMax's M-series is one: it answers with
// "<think>reasoning</think>\n\nanswer" in the same string.
//
// It matters because every other layer already treats reasoning as something
// the operator did not ask to read. The Mapper drops thinking *deltas* on
// purpose (see events.go) because leaking reasoning into a shared incident
// view is not the point of an AIOps console. A provider that inlines it
// defeats that by presenting it as ordinary text, and from there it lands in
// chat_messages, in the transcript the next turn replays, and in every
// generated report — where an operator would reasonably read it as the
// system's own conclusion.
var inlineThinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// stripInlineThinking removes inline reasoning runs and tidies the seam they
// leave behind.
//
// It is deliberately narrow: only complete <think>…</think> pairs are
// removed, so a partial stream that has emitted an opening tag but not yet
// its closing one is left alone rather than silently swallowing the rest of
// the answer. Leading blank lines introduced by the removal are trimmed; a
// message that was nothing but reasoning keeps a non-empty body (the tag
// text) so the turn still records that the model answered with no prose.
func stripInlineThinking(s string) string {
	if !strings.Contains(s, "<think>") {
		return s
	}
	out := inlineThinkBlock.ReplaceAllString(s, "")
	out = strings.TrimLeft(out, "\n\r\t ")
	if strings.TrimSpace(out) == "" {
		return s
	}
	return out
}
