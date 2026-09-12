package pirpc

import (
	"strings"
	"testing"
)

func TestProviderAPIKeyEnvKnownProviders(t *testing.T) {
	cases := map[string]string{
		"anthropic":  "ANTHROPIC_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"google":     "GEMINI_API_KEY",
		"deepseek":   "DEEPSEEK_API_KEY",
		"zai":        "ZAI_API_KEY",
		"openrouter": "OPENROUTER_API_KEY",
		" Anthropic": "ANTHROPIC_API_KEY",
	}
	for provider, want := range cases {
		if got := ProviderAPIKeyEnv(provider); got != want {
			t.Errorf("ProviderAPIKeyEnv(%q)=%q, want %q", provider, got, want)
		}
	}
}

func TestProviderAPIKeyEnvUnknownProviderIsEmpty(t *testing.T) {
	if got := ProviderAPIKeyEnv("some-private-gateway"); got != "" {
		t.Errorf("unknown provider should map to empty, got %q", got)
	}
}

func TestBuildEnvPlacesKeyUnderProviderVariable(t *testing.T) {
	env := BuildEnv([]string{"PATH=/usr/bin"}, "anthropic", "sk-ant-test", false)
	if !contains(env, "ANTHROPIC_API_KEY=sk-ant-test") {
		t.Fatalf("key not placed: %v", env)
	}
	if !contains(env, "PATH=/usr/bin") {
		t.Fatalf("base env dropped: %v", env)
	}
	if contains(env, "PI_OFFLINE=1") {
		t.Fatalf("PI_OFFLINE set without being asked: %v", env)
	}
}

func TestBuildEnvOverridesInheritedKey(t *testing.T) {
	env := BuildEnv([]string{"ANTHROPIC_API_KEY=stale", "PATH=/usr/bin"}, "anthropic", "fresh", false)
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			count++
			if kv != "ANTHROPIC_API_KEY=fresh" {
				t.Errorf("stale value survived: %q", kv)
			}
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one ANTHROPIC_API_KEY entry, got %d: %v", count, env)
	}
}

func TestBuildEnvLeavesInheritedKeyWhenNoneConfigured(t *testing.T) {
	// An operator using auth.json or the host's own env must not have
	// their credential erased just because OPSKEEPER_PI_LLM_API_KEY is
	// unset.
	env := BuildEnv([]string{"ANTHROPIC_API_KEY=host-key"}, "anthropic", "", false)
	if !contains(env, "ANTHROPIC_API_KEY=host-key") {
		t.Fatalf("inherited key was dropped: %v", env)
	}
}

func TestBuildEnvUnknownProviderDoesNotInventVariable(t *testing.T) {
	env := BuildEnv([]string{"PATH=/usr/bin"}, "private-gw", "secret", false)
	for _, kv := range env {
		if strings.Contains(kv, "secret") {
			t.Fatalf("credential leaked into %q for an unknown provider", kv)
		}
	}
}

func TestBuildEnvSetsOfflineMarker(t *testing.T) {
	env := BuildEnv([]string{"PI_OFFLINE=0"}, "", "", true)
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "PI_OFFLINE=") {
			count++
			if kv != "PI_OFFLINE=1" {
				t.Errorf("PI_OFFLINE=%q", kv)
			}
		}
	}
	if count != 1 {
		t.Fatalf("want one PI_OFFLINE entry, got %d: %v", count, env)
	}
}

func contains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
