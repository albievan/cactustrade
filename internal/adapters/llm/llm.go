// Package llm is the LLM reviewer. It sees one proposed trade and answers approve, reduce or
// reject. It has no tools, no broker client and no credentials but its own API key (passed
// in per call); depguard keeps the eToro adapter and the secret store out of this package
// (CLAUDE.md §2.5). Anything it cannot answer cleanly is an error, and the caller treats
// every error as a rejection.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// maxResponse bounds an OpenAI-compatible response body.
const maxResponse = 1 << 20

// Client implements ports.Reviewer for every provider in domain.LLMProviders.
type Client struct {
	HTTP *http.Client
	// now measures latency (nil = time.Now; tests pin it).
	now func() time.Time
	// anthropicOpts are extra SDK options (tests point the SDK at a local server).
	anthropicOpts []option.RequestOption
}

var _ ports.Reviewer = (*Client)(nil)

// New builds a client. It never follows a redirect (PLAN 7.1.7: the key would go to another
// host); h itself is not changed.
func New(h *http.Client) *Client {
	if h == nil {
		h = &http.Client{}
	}
	hc := *h
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{HTTP: &hc}
}

// systemPrompt is the reviewer's whole job description.
const systemPrompt = `You are the risk reviewer of an automated share-trading system. A statistical model has proposed one trade in a share: either a long (buying the real share) or a short (a CFD sold short), never leveraged, always with a stop-loss, and closed before the market closes the same day. A short loses when the price rises. A deterministic risk engine will check every limit after you; your job is judgement the numbers may miss: is the stated reason consistent with the recent prices, is the stop sensible, is anything about this trade surprising or worrying?

You can only approve the trade at its proposed size, reduce it (sizeMultiplier between 0 and 1), or reject it. You can never increase it or propose another trade. When in doubt, reduce or reject.

Answer with JSON only: {"verdict": "approve" | "reduce" | "reject", "sizeMultiplier": number, "reason": "one or two sentences"}.`

// verdictSchema is the structured-output schema for providers that support it.
var verdictSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"verdict":        map[string]any{"type": "string", "enum": []string{"approve", "reduce", "reject"}},
		"sizeMultiplier": map[string]any{"type": "number"},
		"reason":         map[string]any{"type": "string"},
	},
	"required":             []string{"verdict", "sizeMultiplier", "reason"},
	"additionalProperties": false,
}

type verdictJSON struct {
	Verdict        string  `json:"verdict"`
	SizeMultiplier float64 `json:"sizeMultiplier"`
	Reason         string  `json:"reason"`
}

// UserPrompt renders the trade for the reviewer (exported for the setup page's preview).
func UserPrompt(in ports.ReviewInput) string {
	c := in.Candidate
	var b strings.Builder
	dir := c.Direction
	if dir == "" {
		dir = domain.Long
	}
	fmt.Fprintf(&b, "Proposed trade: %s\n- instrument id: %d\n- playbook: %s\n- reason: %s\n", strings.ToUpper(string(dir)), c.Instrument, c.Playbook, c.Reason)
	fmt.Fprintf(&b, "- entry: %s, stop loss: %s, take profit: %s\n- size: $%s\n", c.Entry, c.StopLossRate, c.TakeProfitRate, c.SuggestedSize)
	if len(c.RegimeProbs) > 0 {
		keys := make([]string, 0, len(c.RegimeProbs))
		for r := range c.RegimeProbs {
			keys = append(keys, string(r))
		}
		slices.Sort(keys)
		b.WriteString("- regime probabilities:")
		for _, k := range keys {
			fmt.Fprintf(&b, " %s %.2f", k, c.RegimeProbs[domain.Regime(k)])
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Account: %d open position(s), today's P&L $%s\n", len(in.Positions), in.TodayPnL)
	if n := len(in.RecentCandles); n > 0 {
		from := max(0, n-30)
		b.WriteString("Recent closes (oldest first): ")
		for i, k := range in.RecentCandles[from:] {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k.Close.String())
		}
		b.WriteString("\n")
	}
	return b.String()
}

func parseVerdict(text string) (domain.Verdict, error) {
	text = strings.TrimSpace(text)
	// tolerate a fenced block from models without structured output
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	var v verdictJSON
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return domain.Verdict{}, fmt.Errorf("the reviewer's answer is not the expected JSON: %w", err)
	}
	return domain.ClampVerdict(domain.Verdict{
		Kind: domain.VerdictKind(strings.ToLower(strings.TrimSpace(v.Verdict))), SizeBp: multiplierToBp(v.SizeMultiplier), Reason: v.Reason,
	}), nil
}

// multiplierToBp converts the model's size multiplier to basis points at the edge (D23),
// rounding down so the conversion can only shrink: NaN, infinities of either sign, zero and
// negatives are 0 (a reduce to nothing is a rejection); 1 and above are the full size.
func multiplierToBp(f float64) domain.BasisPoints {
	switch {
	case math.IsNaN(f) || f <= 0:
		return 0
	case f >= 1:
		return domain.FullSize
	}
	return min(domain.BasisPoints(math.Floor(f*float64(domain.FullSize))), domain.FullSize)
}

func timeout(cfg domain.LLMConfig) time.Duration {
	if cfg.TimeoutSeconds > 0 {
		return time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

// Review asks the configured provider for a verdict and returns the call's trace (see
// ports.Reviewer), filled on every path. Neither the trace nor the error holds the key.
func (c *Client) Review(ctx context.Context, cfg domain.LLMConfig, apiKey string, in ports.ReviewInput) (domain.Verdict, domain.LLMCall, error) {
	call := domain.LLMCall{Provider: cfg.Provider, Model: cfg.Model}
	p, ok := domain.LLMProviderByID(cfg.Provider)
	if !ok {
		return finish(&call, apiKey, fmt.Errorf("llm: unknown provider %q", cfg.Provider))
	}
	if p.Anthropic && call.Model == "" {
		call.Model = domain.DefaultAnthropicModel
	}
	if p.KeyRequired && apiKey == "" {
		return finish(&call, apiKey, fmt.Errorf("llm: the %s API key is not set: %w", p.Label, domain.ErrCredentialsMissing))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	start := c.clock()
	var v domain.Verdict
	var err error
	if p.Anthropic {
		v, err = c.reviewAnthropic(ctx, cfg, apiKey, UserPrompt(in), &call)
	} else {
		v, err = c.reviewOpenAI(ctx, cfg, apiKey, UserPrompt(in), &call)
	}
	call.Latency = c.clock().Sub(start)
	if err != nil {
		return finish(&call, apiKey, err)
	}
	_, call, _ = finish(&call, apiKey, nil)
	return v, call, nil
}

// finish redacts the key from the trace (defence in depth: a provider could echo it in an
// error message), records err in it and returns the failed result (no verdict) with it.
func finish(call *domain.LLMCall, apiKey string, err error) (domain.Verdict, domain.LLMCall, error) {
	if len(apiKey) < minRedact {
		apiKey = "" // too short to be a real key; replacing it would garble the text
	}
	if err != nil {
		if apiKey != "" && strings.Contains(err.Error(), apiKey) {
			err = errors.New(strings.ReplaceAll(err.Error(), apiKey, redacted))
		}
		call.Error = err.Error()
	}
	if apiKey != "" {
		call.Prompt = strings.ReplaceAll(call.Prompt, apiKey, redacted)
		call.Response = strings.ReplaceAll(call.Response, apiKey, redacted)
		call.Model = strings.ReplaceAll(call.Model, apiKey, redacted)
	}
	return domain.Verdict{}, *call, err
}

// redacted replaces the API key wherever a provider echoed it; keys shorter than minRedact
// bytes are not real provider keys and are left alone.
const (
	redacted  = "[redacted]"
	minRedact = 8
)

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// --- Anthropic (official SDK) -------------------------------------------------------------

func (c *Client) anthropicClient(cfg domain.LLMConfig, apiKey string) anthropic.Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithHTTPClient(c.HTTP), option.WithMaxRetries(1)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.Endpoint()))
	}
	return anthropic.NewClient(append(opts, c.anthropicOpts...)...)
}

// recorder keeps the request and response bodies of one SDK call (the last attempt). It
// never looks at the headers, which carry the key.
type recorder struct {
	mu        sync.Mutex
	req, resp []byte
	seen      bool
}

func (r *recorder) middleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	var body []byte
	switch {
	case req.GetBody != nil:
		if rc, err := req.GetBody(); err == nil {
			body, _ = io.ReadAll(io.LimitReader(rc, maxResponse))
			_ = rc.Close()
		}
	case req.Body != nil:
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	}
	r.mu.Lock()
	r.req, r.resp, r.seen = body, nil, true
	r.mu.Unlock()
	resp, err := next(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	r.mu.Lock()
	r.resp = data
	r.mu.Unlock()
	// the SDK reads the same bytes, then whatever was beyond the limit
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(data), resp.Body), resp.Body}
	return resp, nil
}

func (r *recorder) into(call *domain.LLMCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen {
		call.Prompt = string(r.req)
	}
	call.Response = string(r.resp)
}

func (c *Client) reviewAnthropic(ctx context.Context, cfg domain.LLMConfig, apiKey, prompt string, call *domain.LLMCall) (domain.Verdict, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     call.Model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt))},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: verdictSchema},
		},
		// A refusal is re-served by a fallback model inside the same call.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
	}
	// the trace's prompt until the request is seen on the wire (then: the exact body)
	if b, err := json.Marshal(params); err == nil {
		call.Prompt = string(b)
	}
	rec := &recorder{}
	client := c.anthropicClient(cfg, apiKey)
	resp, err := client.Beta.Messages.New(ctx, params, option.WithMiddleware(rec.middleware))
	rec.into(call)
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("llm anthropic: %w", err)
	}
	if resp.Model != "" {
		call.Model = resp.Model
	}
	call.InputTokens, call.OutputTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return domain.Verdict{}, errors.New("llm anthropic: the model declined to review this trade")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	v, err := parseVerdict(text.String())
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("llm anthropic: %w", err)
	}
	return v, nil
}

// --- OpenAI-compatible chat completions ---------------------------------------------------

// do sends body (nil = none) and returns the response body, also with an error status (then
// with an error that explains it).
func (c *Client) do(ctx context.Context, method, url, apiKey string, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return data, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return data, fmt.Errorf("HTTP %d: %s", resp.StatusCode, providerMessage(resp.StatusCode, data))
	}
	return data, nil
}

// chatRequest is the whole request: system and user message and JSON mode. There is
// deliberately no tools, tool_choice or functions field (CLAUDE.md §2.5).
type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	ResponseFormat map[string]string `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) reviewOpenAI(ctx context.Context, cfg domain.LLMConfig, apiKey, prompt string, call *domain.LLMCall) (domain.Verdict, error) {
	body, err := json.Marshal(chatRequest{
		Model:          cfg.Model,
		Messages:       []chatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: prompt}},
		ResponseFormat: map[string]string{"type": "json_object"},
	})
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("llm %s: %w", cfg.Provider, err)
	}
	call.Prompt = string(body)
	data, err := c.do(ctx, http.MethodPost, cfg.Endpoint()+"/chat/completions", apiKey, body)
	call.Response = string(data)
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("llm %s: %w", cfg.Provider, err)
	}
	var resp chatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return domain.Verdict{}, fmt.Errorf("llm %s: the response is not JSON: %w", cfg.Provider, err)
	}
	if resp.Model != "" {
		call.Model = resp.Model
	}
	call.InputTokens, call.OutputTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	if len(resp.Choices) == 0 {
		return domain.Verdict{}, fmt.Errorf("llm %s: no answer", cfg.Provider)
	}
	v, err := parseVerdict(resp.Choices[0].Message.Content)
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("llm %s: %w", cfg.Provider, err)
	}
	return v, nil
}

// Models lists the provider's models for the setup page.
func (c *Client) Models(ctx context.Context, cfg domain.LLMConfig, apiKey string) ([]string, error) {
	p, ok := domain.LLMProviderByID(cfg.Provider)
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
	if p.KeyRequired && apiKey == "" {
		return nil, fmt.Errorf("llm: the %s API key is not set: %w", p.Label, domain.ErrCredentialsMissing)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out []string
	if p.Anthropic {
		client := c.anthropicClient(cfg, apiKey)
		iter := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
		for iter.Next() {
			out = append(out, iter.Current().ID)
		}
		if err := iter.Err(); err != nil {
			return nil, fmt.Errorf("llm anthropic: %w", err)
		}
	} else {
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		data, err := c.do(ctx, http.MethodGet, cfg.Endpoint()+"/models", apiKey, nil)
		if err == nil {
			err = json.Unmarshal(data, &resp)
		}
		if err != nil {
			return nil, fmt.Errorf("llm %s: %w", cfg.Provider, err)
		}
		for _, m := range resp.Data {
			out = append(out, m.ID)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

type providerError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// providerMessage turns an error response into one readable line: the provider's message
// (OpenAI style {"error":{...}} or Gemini's [{"error":{...}}]) and what it usually means.
func providerMessage(status int, data []byte) string {
	msg := strings.TrimSpace(string(data))
	var one providerError
	var many []providerError
	switch {
	case json.Unmarshal(data, &one) == nil && one.Error.Message != "":
		msg = one.Error.Message
	case json.Unmarshal(data, &many) == nil && len(many) > 0 && many[0].Error.Message != "":
		msg = many[0].Error.Message
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "the provider refused the API key (wrong key, or not allowed for this model): " + msg
	case http.StatusPaymentRequired:
		return "the provider wants payment (no credit on this key): " + msg
	case http.StatusNotFound:
		return "the provider does not know this model or address (pick one with \"Load models\"): " + msg
	case http.StatusTooManyRequests:
		return "the provider's quota or rate limit for this key is used up — not a cactusTrade problem; " +
			"check the plan and billing at the provider, wait, or choose a model your plan includes: " + msg
	}
	return msg
}
