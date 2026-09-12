package pirpc

import "strings"

// providerAPIKeyEnv maps a Pi provider id to the environment variable
// Pi reads its credential from. Transcribed from Pi's own
// docs/providers.md ("Environment Variables or Auth File") for
// v0.85.1; the upstream source of truth is packages/ai/src/
// env-api-keys.ts.
//
// Credentials travel through the environment and never through argv:
// argv is world-readable in /proc/<pid>/cmdline and lands in the
// audit chain, an env block does neither.
var providerAPIKeyEnv = map[string]string{
	"anthropic":                  "ANTHROPIC_API_KEY",
	"ant-ling":                   "ANT_LING_API_KEY",
	"azure-openai-responses":     "AZURE_OPENAI_API_KEY",
	"openai":                     "OPENAI_API_KEY",
	"deepseek":                   "DEEPSEEK_API_KEY",
	"nvidia":                     "NVIDIA_API_KEY",
	"google":                     "GEMINI_API_KEY",
	"amazon-bedrock":             "AWS_BEARER_TOKEN_BEDROCK",
	"mistral":                    "MISTRAL_API_KEY",
	"groq":                       "GROQ_API_KEY",
	"cerebras":                   "CEREBRAS_API_KEY",
	"cloudflare-ai-gateway":      "CLOUDFLARE_API_KEY",
	"cloudflare-workers-ai":      "CLOUDFLARE_API_KEY",
	"xai":                        "XAI_API_KEY",
	"openrouter":                 "OPENROUTER_API_KEY",
	"vercel-ai-gateway":          "AI_GATEWAY_API_KEY",
	"zai":                        "ZAI_API_KEY",
	"zai-coding-cn":              "ZAI_CODING_CN_API_KEY",
	"opencode":                   "OPENCODE_API_KEY",
	"opencode-go":                "OPENCODE_API_KEY",
	"radius":                     "RADIUS_API_KEY",
	"huggingface":                "HF_TOKEN",
	"fireworks":                  "FIREWORKS_API_KEY",
	"together":                   "TOGETHER_API_KEY",
	"baseten":                    "BASETEN_API_KEY",
	"kimi-coding":                "KIMI_API_KEY",
	"minimax":                    "MINIMAX_API_KEY",
	"minimax-cn":                 "MINIMAX_CN_API_KEY",
	"qwen-token-plan":            "QWEN_TOKEN_PLAN_API_KEY",
	"qwen-token-plan-cn":         "QWEN_TOKEN_PLAN_CN_API_KEY",
	"xiaomi":                     "XIAOMI_API_KEY",
	"xiaomi-token-plan-cn":       "XIAOMI_TOKEN_PLAN_CN_API_KEY",
	"xiaomi-token-plan-ams":      "XIAOMI_TOKEN_PLAN_AMS_API_KEY",
	"xiaomi-token-plan-sgp":      "XIAOMI_TOKEN_PLAN_SGP_API_KEY",
	"qwen-token-plan-individual": "QWEN_TOKEN_PLAN_API_KEY",
}

// ProviderAPIKeyEnv returns the environment variable Pi reads the API
// key from for the given provider id, or "" when the provider is
// unknown. An unknown provider is not an error: the operator may be
// using auth.json or a gateway that needs no key, and guessing a
// variable name would be worse than leaving it unset.
func ProviderAPIKeyEnv(provider string) string {
	return providerAPIKeyEnv[strings.ToLower(strings.TrimSpace(provider))]
}

// BuildEnv returns base plus the credential and process settings Pi
// needs. base is normally os.Environ().
//
// apiKey is placed under the provider's documented variable. When the
// provider is unknown, or apiKey is empty, nothing is added — Pi then
// falls back to auth.json or fails loudly on the first LLM call,
// which is the honest outcome.
//
// PI_OFFLINE is set when offline is true so Pi skips update checks
// even if --offline is dropped from argv by a wrapper.
func BuildEnv(base []string, provider, apiKey string, offline bool) []string {
	out := make([]string, 0, len(base)+2)
	key := ProviderAPIKeyEnv(provider)
	skip := map[string]bool{}
	if key != "" && apiKey != "" {
		skip[key] = true
	}
	if offline {
		skip["PI_OFFLINE"] = true
	}
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 && skip[kv[:i]] {
			// Drop an inherited value so the operator-configured one
			// wins deterministically instead of depending on which
			// duplicate the OS picks.
			continue
		}
		out = append(out, kv)
	}
	if key != "" && apiKey != "" {
		out = append(out, key+"="+apiKey)
	}
	if offline {
		out = append(out, "PI_OFFLINE=1")
	}
	return out
}
