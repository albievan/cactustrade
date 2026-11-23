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
	"net/http"
	"slices"
	"strings"
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
	// anthropicOpts are extra SDK options (tests point the SDK at a local server).
	anthropicOpts []option.RequestOption
}

var _ ports.Reviewer = (*Client)(nil)

// New builds a client.
func New(h *http.Client) *Client {
	if h == nil {
		h = &http.Client{}
	}
	return &Client{HTTP: h}
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
		Kind: domain.VerdictKind(strings.ToLower(strings.TrimSpace(v.Verdict))), SizeMultiplier: v.SizeMultiplier, Reason: v.Reason,
	}), nil
}

func timeout(cfg domain.LLMConfig) time.Duration {
	if cfg.TimeoutSeconds > 0 {
		return time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

// Review asks the configured provider for a verdict.
func (c *Client) Review(ctx context.Context, cfg domain.LLMConfig, apiKey string, in ports.ReviewInput) (domain.Verdict, error) {
	p, ok := domain.LLMProviderByID(cfg.Provider)
	if !ok {
		return domain.Verdict{}, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
	if p.KeyRequired && apiKey == "" {
		return domain.Verdict{}, fmt.Errorf("llm: the %s API key is not set: %w", p.Label, domain.ErrCredentialsMissing)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	if p.Anthropic {
		return c.reviewAnthropic(ctx, cfg, apiKey, UserPrompt(in))
	}
	return c.reviewOpenAI(ctx, cfg, apiKey, UserPrompt(in))
}

// --- Anthropic (official SDK) -------------------------------------------------------------

func (c *Client) anthropicClient(cfg domain.LLMConfig, apiKey string) anthropic.Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithHTTPClient(c.HTTP), option.WithMaxRetries(1)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.Endpoint()))
	}
	return anthropic.NewClient(append(opts, c.anthropicOpts...)...)
}

func (c *Client) reviewAnthropic(ctx context.Context, cfg domain.LLMConfig, apiKey, prompt string) (domain.Verdict, error) {
	model := cfg.Model
	if model == "" {
		model = domain.DefaultAnthropicModel
	}
	client := c.anthropicClient(cfg, apiKey)
	resp, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     model,
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
	})
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("llm anthropic: %w", err)
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return domain.Verdict{}, errors.New("llm anthropic: the model declined to review this trade")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return parseVerdict(text.String())
}

// --- OpenAI-compatible chat completions ---------------------------------------------------

func (c *Client) do(ctx context.Context, method, url, apiKey string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
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
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(data, out)
}

func (c *Client) reviewOpenAI(ctx context.Context, cfg domain.LLMConfig, apiKey, prompt string) (domain.Verdict, error) {
	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		},
		"response_format": map[string]string{"type": "json_object"},
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := c.do(ctx, http.MethodPost, cfg.Endpoint()+"/chat/completions", apiKey, body, &resp); err != nil {
		return domain.Verdict{}, fmt.Errorf("llm %s: %w", cfg.Provider, err)
	}
	if len(resp.Choices) == 0 {
		return domain.Verdict{}, fmt.Errorf("llm %s: no answer", cfg.Provider)
	}
	return parseVerdict(resp.Choices[0].Message.Content)
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
		if err := c.do(ctx, http.MethodGet, cfg.Endpoint()+"/models", apiKey, nil, &resp); err != nil {
			return nil, fmt.Errorf("llm %s: %w", cfg.Provider, err)
		}
		for _, m := range resp.Data {
			out = append(out, m.ID)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
