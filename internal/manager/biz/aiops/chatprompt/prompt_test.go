package chatprompt

import (
	"strings"
	"testing"
)

func TestTheReminderIsABareTagPairAroundTheBaselineRules(t *testing.T) {
	// Providers and tests key on the literal tag, and the assembler decides
	// whether to inject at all by testing for the prefix. Prose around the
	// block would break both.
	got := SystemReminder(Turn{})
	if !strings.HasPrefix(got, "<"+SystemReminderTag+">") || !strings.HasSuffix(got, "</"+SystemReminderTag+">") {
		t.Fatalf("reminder is not a bare tag pair: %q", got)
	}
	// The baseline rules are always present: a turn with no locale, no
	// persona and no hints is the common case, not an empty one.
	for _, want := range []string{"同一工具失败两次", "工具结果是事实"} {
		if !strings.Contains(got, want) {
			t.Fatalf("baseline rule %q missing from %q", want, got)
		}
	}
}

func TestTheReminderLeadsWithTheLanguageEveryTurn(t *testing.T) {
	// The system prompt scrolls out of attention in a long session; the
	// language has to be re-stated in the block, and it leads the block
	// because it governs the reading of everything after it.
	got := SystemReminder(Turn{Locale: "en-US"})
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "Respond in English") {
		t.Fatalf("the language directive does not lead the block: %q", got)
	}
	if !strings.Contains(got, "If the same tool fails twice") {
		t.Fatalf("the English baseline rules are missing: %q", got)
	}
}

func TestTheReminderDeclaresSearchOffOnlyWhenItIs(t *testing.T) {
	// A model that keeps calling a tool the turn disabled burns its
	// iteration budget on refusals; a model told search is off when it is
	// on stops using a capability it was granted.
	if off := SystemReminder(Turn{}); !strings.Contains(off, "web_search 已被关闭") {
		t.Fatalf("search was off but the block does not say so: %q", off)
	}
	if on := SystemReminder(Turn{WebSearchEnabled: true}); strings.Contains(on, "web_search 已被关闭") {
		t.Fatalf("search was on but the block disables it: %q", on)
	}
}

func TestTheReminderCarriesThePersonaThenTheHints(t *testing.T) {
	got := SystemReminder(Turn{
		AgentReminder: "never restart a database without a failover plan",
		DynamicHints:  []string{"get_topology failed twice in a row", "   ", "iteration 27 of 30 — summarize"},
	})
	if !strings.Contains(got, "- never restart a database without a failover plan") {
		t.Fatalf("the persona reminder is missing: %q", got)
	}
	persona := strings.Index(got, "never restart a database")
	hint := strings.Index(got, "get_topology failed twice")
	if persona < 0 || hint < 0 || persona > hint {
		t.Fatalf("persona and hints are out of order: %q", got)
	}
	// A blank hint is skipped rather than emitted as an empty bullet, which
	// reads to the model as a rule it should be able to see but cannot.
	if strings.Contains(got, "- \n") || strings.Contains(got, "-  \n") {
		t.Fatalf("a blank hint became an empty bullet: %q", got)
	}
}

func TestAnUnrecognisedLocaleAddsNoDirective(t *testing.T) {
	// Guessing a language from an unrecognised tag answers an operator in a
	// language they did not ask for, and nothing anywhere says why.
	if NormalizeLocale("fr-FR") != "" || NormalizeLocale("") != "" || NormalizeLocale("   ") != "" {
		t.Fatalf("an unrecognised locale normalised to a language")
	}
	for _, tc := range []struct{ in, want string }{
		{"zh-CN", "zh"}, {"zh_CN", "zh"}, {"ZH", "zh"},
		{"en-US", "en"}, {"en_GB", "en"}, {"en", "en"},
		{"zh-Hant", "zh"}, {"pt-BR", ""},
	} {
		if got := NormalizeLocale(tc.in); got != tc.want {
			t.Fatalf("NormalizeLocale(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if d := LanguageDirective("fr-FR"); d != "" {
		t.Fatalf("a directive was invented for an unknown locale: %q", d)
	}
	if d := LanguageDirective("zh-CN"); !strings.Contains(d, "用中文回复") {
		t.Fatalf("the Chinese directive is missing: %q", d)
	}
}

func TestTheReminderDirectiveIsShorterThanTheSystemOne(t *testing.T) {
	// The reminder is re-injected every turn; using the full system-prompt
	// directive there would spend the attention budget it exists to protect.
	for _, locale := range []string{"en-US", "zh-CN"} {
		full := LanguageDirective(locale)
		short := ReminderLanguageDirective(locale)
		if full == "" || short == "" {
			t.Fatalf("locale %q produced an empty directive", locale)
		}
		if len(short) >= len(full) {
			t.Fatalf("locale %q: reminder directive (%d) is not shorter than the system one (%d)",
				locale, len(short), len(full))
		}
	}
}
