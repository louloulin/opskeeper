// Package llm is the real OpenAI-backed chat/tool-calling client.
//
// Red line: no provider abstraction — interface follows OpenAI's
// shape. The wire format is owned in wire.go; no LLM SDK is imported, so
// nothing between this package and the provider can reject a request the
// caller built (see wire.go for why that matters).
//
// Red line: Prom metric labels MUST NOT contain user_id / org_id /
// session_id. Allowed labels: model, kind, result.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vincent-wuhan/opskeeper/core/manager/pkg/zhipuauth"
)

// Sentinel errors.
var (
	// ErrBudgetExceeded is returned when the daily token budget is hit.
	ErrBudgetExceeded = errors.New("llm: budget exceeded")
	// ErrNoAPIKey is returned by the noop client when OPENAI_API_KEY is unset.
	ErrNoAPIKey = errors.New("llm: OPENAI_API_KEY not set")
)

// defaultTimeout is the fallback for callers that build a Chat request
// without putting a deadline on their context. 120s is the project-wide
// unification floor — short enough that a stuck request still gives up
// on a human-grade timescale, long enough that the slowest mainstream
// reasoning model finishes a tool-rich turn without false-failing
// (Anthropic Opus 4.x extended, DeepSeek v4 reasoning, GPT-5.x). The
// 30s prior default broke once the cluster default moved to DeepSeek.
const defaultTimeout = 120 * time.Second

// Config is the LLM client configuration.
//
// BaseURL is optional and lets us point at Azure / Fireworks / a local vLLM
// without reshaping the interface. Timeout applies when the caller's ctx has
// no deadline; default is 30s.
type Config struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
}

// Message is one entry in the chat completions messages array. The shape is
// OpenAI-flavored on purpose (no provider abstraction).
//
// Role semantics:
//   - "user" : user prompt; Content is required.
//   - "assistant" : model output; Content may be empty when ToolCalls is set.
//   - "tool" : tool result; ToolCallID references the assistant tool call;
//     ToolName is an optional hint for logs.
//   - "system" : system prompt; Content is required.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
}

// ToolCall is one tool invocation requested by the assistant.
type ToolCall struct {
	ID   string          // provider-assigned id, e.g. "call_abc"
	Name string          // tool name
	Args json.RawMessage // arguments JSON blob as produced by the model
}

// ToolSchema is the JSON-Schema description of a tool exposed to the model.
// Parameters is passed through as-is (JSON Schema draft-07).
type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Usage captures token counts as reported by OpenAI.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ChatReq is the input to Client.Chat.
//
// Provider is the optional provider override (e.g. "openai", "anthropic",
// "zhipu", "gemini"). Empty → router uses the default provider. The
// non-multi-provider single-client path ignores Provider.
type ChatReq struct {
	Model       string
	Provider    string
	Messages    []Message
	Tools       []ToolSchema
	Temperature float32
	UserID      uint64 // optional; used for budget scoping + logging only
}

// ChatResp is the output of Client.Chat.
type ChatResp struct {
	Assistant Message // role=assistant; may have empty Content + non-empty ToolCalls
	Usage     Usage
}

// BudgetChecker gates Chat requests against a token budget. Called with an
// estimated prompt size BEFORE the network call; Record is called AFTER
// success with the actual Usage. A nil BudgetChecker means no limit.
type BudgetChecker interface {
	Check(ctx context.Context, userID uint64, estPromptTokens int) error
	Record(ctx context.Context, userID uint64, usage Usage) error
}

// Client is the LLM client surface consumed by the AIOps agent.
type Client interface {
	Chat(ctx context.Context, req ChatReq) (*ChatResp, error)
}

// Resolver supplies the LLM credentials at call time. The seam exists so
// admin-editable settings (system_settings table, biz/setting service)
// can override the env-derived bootstrap values without restarting the
// manager. An empty string from any field means "fall back to the env-
// configured value".
//
// The implementation is expected to be cheap (an in-memory cache lookup);
// a small TTL cache lives inside the LLM client so even a slow Resolver
// does not block hot Chat() paths.
type Resolver interface {
	Resolve(ctx context.Context) (apiKey, model, baseURL string, err error)
}

// New builds a Client.
//
// If cfg.APIKey is empty, a noop client is returned whose Chat always fails
// with ErrNoAPIKey (useful for local dev without OPENAI_API_KEY).
//
// Metrics are registered on reg; pass nil to register on the default
// registerer (a warn is logged once).
//
// NOTE: logger is derived from slog.Default() to keep the 3-arg signature
// frozen in The agent-loop caller can still inject its own slog
// attrs via the ctx-carried logger if desired.
func New(cfg Config, budget BudgetChecker, reg *prometheus.Registry) Client {
	return NewWithResolver(cfg, nil, budget, reg)
}

// NewWithResolver is New with an optional dynamic credential source. The
// resolver, when non-nil, is queried before each Chat call (with a small
// internal TTL cache) and its non-empty fields override cfg. Empty fields
// fall back to cfg, which itself was env-seeded at startup.
//
// When the effective API key is empty (neither resolver nor cfg has one),
// Chat returns ErrNoAPIKey — the same behaviour as the noop client.
func NewWithResolver(cfg Config, resolver Resolver, budget BudgetChecker, reg *prometheus.Registry) Client {
	log := slog.Default().With(slog.String("component", "llm"))

	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}

	// With no resolver and no env key, fall back to noop so callers see a
	// clean ErrNoAPIKey instead of a confusing 401 from the provider.
	if resolver == nil && cfg.APIKey == "" {
		log.Warn("OPENAI_API_KEY empty and no Resolver wired — returning noop client; Chat will fail with ErrNoAPIKey")
		return &noopClient{}
	}

	return &openaiClient{
		cfg:        cfg,
		resolver:   resolver,
		budget:     budget,
		metrics:    newMetrics(reg, log),
		log:        log,
		resolveTTL: 60 * time.Second,
	}
}

// openaiClient is the OpenAI-compatible client. It keeps the name the
// removed SDK's wrapper had (rather than e.g. httpClient) because the
// sibling files — router.go, budget_callback.go — refer to it by name in
// their own comments, and a rename would leave those comments pointing at
// a type that no longer exists.
type openaiClient struct {
	cfg      Config
	resolver Resolver
	budget   BudgetChecker
	metrics  *metrics
	log      *slog.Logger

	// Endpoints are keyed by (apiKey, baseURL) so a settings change
	// transparently swaps the credential and the URL without us having to
	// rebuild anything on every Chat. The URL and the *http.Client are
	// cached as one value on purpose: caching only the client would let a
	// key rotation keep POSTing to the previous gateway's address.
	ephMu    sync.Mutex
	ephCache map[wireKey]wireEndpoint

	// Resolver TTL cache so hot paths don't pay the DB round-trip per call.
	resolveTTL time.Duration
	resolveMu  sync.Mutex
	resolved   resolvedCreds
	resolvedAt time.Time

	// noSampling records models discovered at runtime to reject custom
	// sampling params (temperature / top_p / n / penalties) — the OpenAI
	// reasoning families (o-series, gpt-5.x) fix these at 1/0 and 400 on any
	// other value. Seeded reactively from CreateChatCompletion's "params
	// fixed at 1" rejection so every later call for that model proactively
	// omits the params instead of eating a failed round-trip. Read alongside
	// the isReasoningModel name heuristic. Keyed by the raw model string.
	noSamplingMu sync.RWMutex
	noSampling   map[string]bool
}

// wireKey identifies one endpoint. apiKey participates so a rotated key
// builds a fresh entry; baseURL participates because the URL a call posts
// to is derived from it, and a stale URL would silently keep talking to
// the previous gateway.
type wireKey struct {
	apiKey  string
	baseURL string
}

// wireEndpoint is a resolved (URL, client) pair, returned together because
// a caller holding only the client could not tell which host it reaches.
type wireEndpoint struct {
	url    string
	client *http.Client
}

// defaultBaseURL is the address used when neither the resolver nor the
// bootstrap config names one. It matches the OpenAI v1 endpoint the removed
// SDK defaulted to, so an operator who had not configured a base URL keeps
// talking to the same place after the swap.
const defaultBaseURL = "https://api.openai.com/v1"

type resolvedCreds struct {
	apiKey  string
	model   string
	baseURL string
}

// effectiveCreds returns the credentials for the next Chat. Resolver values
// override cfg per-field; missing/empty resolver fields fall back to the
// env-seeded cfg. The result is cached for resolveTTL.
func (c *openaiClient) effectiveCreds(ctx context.Context) (string, string, string, error) {
	if c.resolver == nil {
		return c.cfg.APIKey, c.cfg.Model, c.cfg.BaseURL, nil
	}
	c.resolveMu.Lock()
	defer c.resolveMu.Unlock()
	if !c.resolvedAt.IsZero() && time.Since(c.resolvedAt) < c.resolveTTL {
		return c.resolved.apiKey, c.resolved.model, c.resolved.baseURL, nil
	}
	apiKey, model, baseURL, err := c.resolver.Resolve(ctx)
	if err != nil {
		// Soft-fail: log and fall back to cfg so a transient DB hiccup
		// does not break the chat surface.
		c.log.Warn("resolver failed; falling back to env-seeded cfg", slog.Any("err", err))
		apiKey, model, baseURL = "", "", ""
	}
	if apiKey == "" {
		apiKey = c.cfg.APIKey
	}
	if model == "" {
		model = c.cfg.Model
	}
	if baseURL == "" {
		baseURL = c.cfg.BaseURL
	}
	c.resolved = resolvedCreds{apiKey: apiKey, model: model, baseURL: baseURL}
	c.resolvedAt = time.Now()
	return apiKey, model, baseURL, nil
}

// endpointFor returns the cached URL and HTTP client for the (apiKey,
// baseURL) pair. The cache tops out at a handful of entries even across a
// year of settings edits, so it is never evicted.
//
// For Zhipu (open.bigmodel.cn) the client carries a transport that rewrites
// Authorization to a freshly-signed JWT on every request. Zhipu's v4
// endpoints reject the raw <id>.<secret> key with a 401, so the transport —
// not the static header set in wire.go — is what makes Zhipu work. The
// (apiKey, baseURL) key therefore also decides *which transport* a call
// uses, which is why a rotated key must miss the cache rather than reuse
// the previous client.
func (c *openaiClient) endpointFor(apiKey, baseURL string) wireEndpoint {
	baseURL = normalizeOpenAIBaseURL(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	k := wireKey{apiKey: apiKey, baseURL: baseURL}
	c.ephMu.Lock()
	defer c.ephMu.Unlock()
	if c.ephCache == nil {
		c.ephCache = make(map[wireKey]wireEndpoint)
	}
	if ep, ok := c.ephCache[k]; ok {
		return ep
	}
	hc := &http.Client{}
	if zhipuauth.LooksLikeZhipuURL(baseURL) && zhipuauth.LooksLikeZhipuKey(apiKey) {
		hc.Transport = &zhipuJWTTransport{apiKey: apiKey, base: http.DefaultTransport}
	}
	ep := wireEndpoint{url: baseURL + "/chat/completions", client: hc}
	c.ephCache[k] = ep
	return ep
}

// normalizeOpenAIBaseURL prepares a user-supplied base URL for our own
// client. A request is built as TrimRight(baseURL,"/") +
// "/chat/completions" — no "/v1" version segment is inserted. OpenAI's own
// default base URL already ends in "/v1", and so does every hosted
// provider's documented endpoint, so a configured base URL that carries
// that segment works unchanged.
//
// Operators pointing the Custom (OpenAI-compatible) provider at a local
// Ollama / LM Studio / vLLM box routinely paste just the bare address
// they use everywhere else — e.g. "http://192.168.8.5:11434". The SDK
// then POSTs to ".../chat/completions", which Ollama serves nothing on
// (its OpenAI-compatible route is "/v1/chat/completions"), so the request
// 404s and the chat surfaces a "stream error". When the base URL has no
// path of its own we append "/v1" so the request lands on the
// OpenAI-compatible route. A URL that already carries a path (".../v1",
// ".../openai", a gateway prefix) is trusted verbatim — named providers
// whose defaults include "/v1" are therefore untouched.
func normalizeOpenAIBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		// Malformed / scheme-less input: leave as-is so the transport
		// surfaces the real error rather than us silently reshaping garbage.
		return s
	}
	if strings.Trim(u.Path, "/") == "" {
		u.Path = "/v1"
		return u.String()
	}
	return s
}

// zhipuJWTTransport rewrites the Authorization header on every outbound
// request to a freshly-signed Zhipu JWT (TTL 1h), replacing the Bearer
// header postChatCompletion sets. Signing per request is what keeps the
// token fresh across a long-lived client that is reused for many turns.
type zhipuJWTTransport struct {
	apiKey string
	base   http.RoundTripper
}

func (t *zhipuJWTTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := zhipuauth.SignJWT(t.apiKey, time.Hour)
	if err != nil {
		return nil, err
	}
	// Clone first — Go's RoundTripper contract forbids mutating the
	// request the caller passed in (some HTTP middleware reuses it).
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(cloned)
}

// Chat implements Client.
func (c *openaiClient) Chat(ctx context.Context, req ChatReq) (*ChatResp, error) {
	// Resolve effective credentials for this call. Resolver overrides cfg;
	// empty fields fall back to cfg (env-seeded at startup).
	apiKey, defaultModel, baseURL, _ := c.effectiveCreds(ctx)
	if apiKey == "" {
		// No env key, no DB-seeded key. Match the noop-client contract so
		// the caller sees a single sentinel.
		return nil, ErrNoAPIKey
	}
	model := req.Model
	if model == "" {
		model = defaultModel
	}

	// 1. Budget gate BEFORE any network call.
	if c.budget != nil {
		if err := c.budget.Check(ctx, req.UserID, EstimatePromptTokens(req.Messages)); err != nil {
			c.metrics.requestsTotal.WithLabelValues(model, "budget_exceeded").Inc()
			// Never log user content — we only note the fact and the user bucket.
			c.log.Warn("llm budget check refused",
				slog.Uint64("user_id", req.UserID),
				slog.String("model", model),
			)
			return nil, err
		}
	}

	// 2. Translate to the OpenAI wire shape.
	wireReq, err := c.toWireReq(req, model)
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}

	// 3. Bound ctx to cfg.Timeout if caller provided no deadline.
	callCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}

	// 4. Issue the request through the endpoint matching the resolved
	// creds. The body is marshalled inside postChatCompletion on every
	// attempt, so the retry below sends the stripped body rather than a
	// cached copy of the rejected one.
	ep := c.endpointFor(apiKey, baseURL)
	start := time.Now()
	wireResp, err := c.postChatCompletion(callCtx, ep, apiKey, &wireReq)

	// Reactive self-heal for reasoning models the name heuristic did not
	// catch (custom gateway aliases like "gpt-5.6-sol"). These fix
	// temperature/top_p/n at 1 and penalties at 0, and 400 on any other
	// value. If that's what we hit AND we actually sent a sampling param,
	// remember the model, strip the params, and retry once. Safe: this is
	// still the single completion call — no tool has executed yet, so the
	// no-retry-on-tools rule below does not apply.
	if err != nil && isSamplingParamError(err) && hasCustomSampling(wireReq) {
		c.rememberNoSampling(model)
		stripSamplingParams(&wireReq)
		c.log.Warn("llm: model rejects custom sampling params; retrying without them",
			slog.String("model", model))
		wireResp, err = c.postChatCompletion(callCtx, ep, apiKey, &wireReq)
	}

	dur := time.Since(start)
	c.metrics.requestSeconds.WithLabelValues(model).Observe(dur.Seconds())

	if err != nil {
		// 5. Error path — no Record, no retry (tools not idempotent).
		c.metrics.requestsTotal.WithLabelValues(model, "error").Inc()
		c.log.Error("llm chat completion failed",
			slog.String("model", model),
			slog.Duration("duration", dur),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("llm: chat completion: %w", err)
	}

	if len(wireResp.Choices) == 0 {
		c.metrics.requestsTotal.WithLabelValues(model, "error").Inc()
		return nil, fmt.Errorf("llm: empty choices in response")
	}

	// 6. Translate response back.
	assistant, err := decodeWireMessage(wireResp.Choices[0].Message)
	if err != nil {
		c.metrics.requestsTotal.WithLabelValues(model, "error").Inc()
		return nil, fmt.Errorf("llm: decode assistant message: %w", err)
	}
	usage := Usage{
		PromptTokens:     wireResp.Usage.PromptTokens,
		CompletionTokens: wireResp.Usage.CompletionTokens,
		TotalTokens:      wireResp.Usage.TotalTokens,
	}

	c.metrics.tokensTotal.WithLabelValues(model, "prompt").Add(float64(usage.PromptTokens))
	c.metrics.tokensTotal.WithLabelValues(model, "completion").Add(float64(usage.CompletionTokens))
	c.metrics.requestsTotal.WithLabelValues(model, "success").Inc()

	// 7. Record actual usage for the budget.
	if c.budget != nil {
		if rerr := c.budget.Record(ctx, req.UserID, usage); rerr != nil {
			// Recording failures must not fail the user's request.
			c.log.Warn("llm budget record failed",
				slog.Uint64("user_id", req.UserID),
				slog.Any("err", rerr),
			)
		}
	}

	// 8. Structured log — NEVER the message content; only shape + usage.
	c.log.Info("llm chat completion",
		slog.String("model", model),
		slog.Uint64("user_id", req.UserID),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int("total_tokens", usage.TotalTokens),
		slog.Int("tool_calls", len(assistant.ToolCalls)),
		slog.Duration("duration", dur),
	)

	return &ChatResp{Assistant: assistant, Usage: usage}, nil
}

// toWireReq translates the public ChatReq into the request body we POST.
//
// It returns a value type, so the reactive retry in Chat mutates this
// copy. The body itself is marshalled fresh on each attempt inside
// postChatCompletion — if the JSON were built here and reused, the retry
// would resend the temperature the provider had just rejected.
func (c *openaiClient) toWireReq(req ChatReq, model string) (wireRequest, error) {
	// Reasoning models (o-series, gpt-5.x) fix temperature/top_p/n at 1 and
	// 400 on any other value, so we must NOT send a temperature for them.
	// The field is a pointer precisely so the zero value can be omitted
	// while a real 0.0 could still be sent if a caller ever wanted it.
	//
	// isReasoningModel is the fast path for known families; noSampling is the
	// reactively-learned set for gateway aliases the heuristic missed.
	var temp *float32
	if !isReasoningModel(model) && !c.modelRejectsSampling(model) {
		t := req.Temperature
		if t == 0 {
			t = 0.1
		}
		temp = &t
	}

	msgs := make([]wireMessage, 0, len(req.Messages))
	for i, m := range req.Messages {
		if m.Role == "" {
			// A role-less message is a caller bug. Sending it would have
			// the provider reject the whole batch with an opaque 400 that
			// names no message index; naming it here is the difference
			// between a one-line fix and a bisect.
			return wireRequest{}, fmt.Errorf("message[%d]: role is empty", i)
		}
		msgs = append(msgs, encodeWireMessage(m))
	}

	var tools []wireTool
	if len(req.Tools) > 0 {
		tools = make([]wireTool, 0, len(req.Tools))
		for i, t := range req.Tools {
			// Validate the schema is real JSON here rather than letting the
			// encoder emit it verbatim: json.RawMessage is written through
			// unvalidated, so a malformed schema would surface as a provider
			// 400 that names no tool. Failing at build time names it.
			var params json.RawMessage
			if len(t.Parameters) > 0 {
				var tmp any
				if err := json.Unmarshal(t.Parameters, &tmp); err != nil {
					return wireRequest{}, fmt.Errorf("tool[%d] %q parameters: %w", i, t.Name, err)
				}
				params = json.RawMessage(t.Parameters)
			}
			tools = append(tools, wireTool{
				Type: "function",
				Function: wireToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  params,
				},
			})
		}
	}

	return wireRequest{
		Model:       model,
		Messages:    msgs,
		Tools:       tools,
		Temperature: temp,
	}, nil
}

// encodeWireMessage maps one public Message onto the wire shape. Tool call
// arguments travel as the raw JSON the model produced; only an absent
// argument blob is coerced to "{}" so a provider that requires an object
// still receives one.
func encodeWireMessage(m Message) wireMessage {
	out := wireMessage{
		Role:       m.Role,
		Content:    m.Content,
		Name:       m.ToolName,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]wireToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			args := string(tc.Args)
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireToolCallFunction{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
	}
	return out
}

// decodeWireMessage maps the assistant message out of a response onto the
// public Message. It cannot fail today, but it keeps the (Message, error)
// shape the caller's error path already reports through, so a future field
// that does need validation has a place to surface without a signature
// change rippling into Chat.
func decodeWireMessage(m wireMessage) (Message, error) {
	out := Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolName:   m.Name,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]ToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			// A provider that omits arguments must not hand the tool
			// executor a nil blob: json.RawMessage(nil) is not valid JSON,
			// and the executor would fail on decode instead of on the
			// model's actual intent. Empty is "no arguments".
			args := json.RawMessage(tc.Function.Arguments)
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			})
		}
	}
	return out, nil
}

// isReasoningModel reports whether model is an OpenAI-style reasoning model
// that fixes its sampling params (temperature / top_p / n at 1, penalties at
// 0) and rejects any override with a 400. Matched by name family so the
// common cases skip a doomed round-trip; anything this misses is still caught
// reactively by isSamplingParamError + the noSampling cache.
//
// Covered families:
//   - OpenAI o-series: o1, o1-mini, o3, o3-mini, o4-mini, …
//   - GPT-5 family: gpt-5, gpt-5.5, gpt-5-mini, gpt-5.6-sol, … (all reasoning)
//   - Any name tagged "reasoner"/"reasoning" (e.g. deepseek-reasoner)
func isReasoningModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	switch {
	case m == "o1" || m == "o3" || m == "o4":
		return true
	case strings.HasPrefix(m, "o1-") || strings.HasPrefix(m, "o3-") || strings.HasPrefix(m, "o4-"):
		return true
	case strings.HasPrefix(m, "gpt-5"):
		return true
	case strings.Contains(m, "reasoner") || strings.Contains(m, "reasoning"):
		return true
	}
	return false
}

// modelRejectsSampling reports whether we've reactively learned that model
// 400s on custom sampling params. Complements the isReasoningModel heuristic.
func (c *openaiClient) modelRejectsSampling(model string) bool {
	if model == "" {
		return false
	}
	c.noSamplingMu.RLock()
	defer c.noSamplingMu.RUnlock()
	return c.noSampling[model]
}

// rememberNoSampling records that model rejects custom sampling params so
// later calls omit them up front.
func (c *openaiClient) rememberNoSampling(model string) {
	if model == "" {
		return
	}
	c.noSamplingMu.Lock()
	defer c.noSamplingMu.Unlock()
	if c.noSampling == nil {
		c.noSampling = make(map[string]bool)
	}
	c.noSampling[model] = true
}

// isSamplingParamError reports whether err is a provider 400 rejecting a
// sampling param (temperature/top_p/n/penalties) as unsupported/fixed — the
// signature of a reasoning model. Matched on message text because the shape
// differs across the OpenAI-compatible gateways we front (dmxapi, Azure, …).
func isSamplingParamError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "temperature") &&
		!strings.Contains(msg, "top_p") &&
		!strings.Contains(msg, "sampling") {
		return false
	}
	return strings.Contains(msg, "fixed at 1") ||
		strings.Contains(msg, "beta-limitations") ||
		strings.Contains(msg, "only the default") ||
		strings.Contains(msg, "does not support") ||
		strings.Contains(msg, "unsupported value") ||
		strings.Contains(msg, "unsupported_value")
}

// hasCustomSampling reports whether req carries any sampling param that a
// reasoning model would reject. Guards the reactive retry so we only re-issue
// when stripping the params can actually change the outcome.
func hasCustomSampling(req wireRequest) bool {
	return req.Temperature != nil
}

// stripSamplingParams drops every sampling param so the `omitempty`/pointer
// encoding leaves them off the wire, letting a reasoning model apply its
// fixed defaults. It must zero *every* field the encoder can emit: a
// leftover param reproduces the 400 the retry exists to escape.
func stripSamplingParams(req *wireRequest) {
	req.Temperature = nil
}

// EstimatePromptTokens is a cheap pre-call estimate: ~4 chars per token is a
// common rule of thumb for English, plus a fixed overhead per message for
// role/tool framing. Good enough to gate budgets; real billing is the Usage
// we get back.
//
// Exported because two implementations of the same Client interface have to
// agree on it. The HTTP client in this file and the PiG-backed one in
// core/manager/llmpig both gate a request on the same number, and a budget
// that is enforced against one estimate and spent against the other is a
// budget nobody can reason about.
func EstimatePromptTokens(msgs []Message) int {
	const perMsgOverhead = 4
	total := 0
	for _, m := range msgs {
		total += perMsgOverhead
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Name) / 4
			total += len(tc.Args) / 4
		}
	}
	return total
}
