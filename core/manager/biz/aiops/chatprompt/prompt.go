// Package chatprompt composes the per-turn prompt fragments the host
// injects into a conversation.
//
// It is a host package with no kernel in it. The text it produces is policy —
// which rules are re-asserted every turn, how the UI locale is honoured, when
// search is declared off — and policy cannot live in a kernel adapter, or a
// kernel swap silently changes what the model was told. It used to live in
// the eino graph package, where it would have been deleted along with the
// graph.
package chatprompt

import "strings"

// SystemReminderTag wraps the per-turn reminder block. It is exported because
// providers and tests both key on the literal tag: a provider that has a
// special treatment for it, and a test that asserts the block is a bare tag
// pair rather than prose.
const SystemReminderTag = "system-reminder"

// Turn is everything the per-turn reminder is composed from.
type Turn struct {
	// Locale is the console language ("en-US" / "zh-CN"). An unrecognised
	// value adds no directive rather than guessing one: guessing wrong
	// answers a Chinese operator in English.
	Locale string
	// WebSearchEnabled declares whether the search tool is reachable this
	// turn. When false the reminder says so, because a model that keeps
	// calling a disabled tool burns its iteration budget on refusals.
	WebSearchEnabled bool
	// AgentReminder is the active persona's anti-drift line. Empty for the
	// default coordinator.
	AgentReminder string
	// DynamicHints are runtime-computed lines ("tool X failed twice in a
	// row", "iteration budget is nearly spent").
	DynamicHints []string
}

// SystemReminder renders the per-turn <system-reminder> block.
//
// The block is re-injected ahead of every user turn rather than appended to
// the system prompt, because the system prompt is the cached prefix: a rule
// placed there is cached with it and fades out of attention in a long
// session, which is the opposite of what a per-turn reminder is for.
//
// The baseline rules are always present, so the block is never empty — the
// empty branch exists for a caller that drops the baseline, and keeps the
// assembler able to skip the injection cleanly.
func SystemReminder(t Turn) string {
	lines := []string{}
	if NormalizeLocale(t.Locale) == "en" {
		lines = append(lines,
			"- If the same tool fails twice, change approach instead of repeating it.",
			"- device_id / alert_id must be numeric IDs (@-mentions have already been resolved).",
			"- Tool results are facts; do not invent data when evidence is missing.",
			"- call_budget_exceeded only applies to the current user turn; a new user message may call tools again.",
		)
	} else {
		lines = append(lines,
			"- 同一工具失败两次后请换思路，不要重复调用",
			"- device_id / alert_id 必须是数字 ID（@-mention 已经为你解析）",
			"- 工具结果是事实，不要在没有数据时编造",
			"- call_budget_exceeded 只限制当前用户消息；新消息可重新调用工具",
		)
	}
	// Re-assert the response language every turn (the system prompt scrolls
	// out of attention in long sessions). Prepended so it leads the block.
	if dir := ReminderLanguageDirective(t.Locale); dir != "" {
		lines = append([]string{"- " + dir}, lines...)
	}
	if !t.WebSearchEnabled {
		lines = append(lines, "- web_search 已被关闭，本轮不要调用")
	}
	if r := strings.TrimSpace(t.AgentReminder); r != "" {
		lines = append(lines, "- "+r)
	}
	for _, h := range t.DynamicHints {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		lines = append(lines, "- "+h)
	}
	if len(lines) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lines)+2)
	parts = append(parts, "<"+SystemReminderTag+">")
	parts = append(parts, lines...)
	parts = append(parts, "</"+SystemReminderTag+">")
	return strings.Join(parts, "\n")
}

// LanguageDirective maps a UI locale to an explicit "answer in this language"
// instruction. An empty or unrecognised locale yields "" (no directive), which
// is the back-compatible behaviour.
//
// It covers tool-call narration explicitly because that is what drifts back to
// Chinese first when the persona text is Chinese.
func LanguageDirective(locale string) string {
	switch NormalizeLocale(locale) {
	case "en":
		return "Respond in English. Everything you write to the user — prose, explanations, headings, and the narration around every tool call — must be in English. Tool descriptions, knowledge-base snippets, persona text, and logs may be in Chinese; render their MEANING in English and never echo raw Chinese to the user. Translate domain terms to their English equivalents (e.g. \"0号病人\" → \"patient zero\", \"根因\" → \"root cause\", \"告警\" → \"alert\", \"巡检\" → \"inspection\"). Leave only proper nouns, identifiers, hostnames, file paths, code, and raw command output verbatim."
	case "zh":
		return "用中文回复：你的所有叙述、解释、标题，以及每次工具调用前后的说明都必须用中文。工具描述、知识库片段、日志可能是英文，把含义用中文表达即可；标识符、主机名、文件路径、代码、命令原始输出保持原样。"
	}
	return ""
}

// ReminderLanguageDirective is the short form used inside the reminder block,
// where the full directive would dominate an attention budget it is meant to
// protect.
func ReminderLanguageDirective(locale string) string {
	switch NormalizeLocale(locale) {
	case "en":
		return "Respond in English; translate Chinese prompt/tool context by meaning, but keep identifiers/paths/commands verbatim."
	case "zh":
		return "用中文回复；标识符、主机名、路径、代码和命令输出保持原样。"
	}
	return ""
}

// NormalizeLocale reduces a console locale to the language it selects.
//
// An unrecognised value normalises to "" — no language at all — rather than
// defaulting to the operator's likely language. A wrong default is a silent
// defect: the answer arrives in a language the operator did not ask for and
// nothing anywhere says why.
func NormalizeLocale(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	l = strings.ReplaceAll(l, "_", "-")
	switch {
	case strings.HasPrefix(l, "en"):
		return "en"
	case strings.HasPrefix(l, "zh"):
		return "zh"
	default:
		return ""
	}
}
