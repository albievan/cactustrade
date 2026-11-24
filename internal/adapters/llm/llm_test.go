package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

func sample() ports.ReviewInput {
	return ports.ReviewInput{
		Candidate: domain.Candidate{
			Instrument: 1001, Playbook: "trend_breakout", Reason: "close above the 20-bar high",
			Entry: 189_200_000, StopLossRate: 187_000_000, TakeProfitRate: 193_600_000, SuggestedSize: 50_000,
			RegimeProbs: map[domain.Regime]float64{domain.RegimeTrending: 0.8, domain.RegimeChoppy: 0.2},
		},
		RecentCandles: []domain.Candle{{Close: 188_000_000}, {Close: 189_200_000}},
		Positions:     []domain.Position{{ID: 1}},
		TodayPnL:      -1250,
	}
}

func TestUserPrompt(t *testing.T) {
	p := UserPrompt(sample())
	for _, want := range []string{"instrument id: 1001", "trend_breakout", "entry: 189.2", "stop loss: 187", "size: $500.00", "choppy 0.20 trending 0.80", "1 open position", "$-12.50", "188, 189.2"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	for text, want := range map[string]domain.VerdictKind{
		`{"verdict":"approve","sizeMultiplier":1,"reason":"fine"}`:                       domain.VerdictApprove,
		"```json\n{\"verdict\":\"REDUCE\",\"sizeMultiplier\":0.5,\"reason\":\"x\"}\n```": domain.VerdictReduce,
		`{"verdict":"double","sizeMultiplier":2,"reason":"x"}`:                           domain.VerdictReject,
	} {
		v, err := parseVerdict(text)
		if err != nil || v.Kind != want {
			t.Errorf("%q → %+v %v", text, v, err)
		}
	}
	if _, err := parseVerdict("I think it is fine"); err == nil {
		t.Fatal("prose accepted")
	}
	for text, want := range map[string]domain.Verdict{
		`{"verdict":"reduce","sizeMultiplier":0.12345,"reason":"x"}`:   {Kind: domain.VerdictReduce, SizeBp: 1_234, Reason: "x"}, // rounded down
		`{"verdict":"reduce","sizeMultiplier":0.00001,"reason":"x"}`:   {Kind: domain.VerdictReject, Reason: "reduce to nothing: x"},
		`{"verdict":"reduce","sizeMultiplier":-2,"reason":"x"}`:        {Kind: domain.VerdictReject, Reason: "reduce to nothing: x"},
		`{"verdict":"reduce","sizeMultiplier":1e308,"reason":"x"}`:     {Kind: domain.VerdictReduce, SizeBp: domain.FullSize, Reason: "x"},
		`{"verdict":"approve","sizeMultiplier":3,"reason":"x"}`:        {Kind: domain.VerdictApprove, SizeBp: domain.FullSize, Reason: "x"},
		`{"verdict":"increase","sizeMultiplier":0.5,"reason":"x"}`:     {Kind: domain.VerdictReject, Reason: "x"},
		`{"verdict":"reduce","sizeMultiplier":0.99999,"reason":"x"}`:   {Kind: domain.VerdictReduce, SizeBp: 9_999, Reason: "x"},
		`{"verdict":"reduce","sizeMultiplier":0.0001,"reason":"x"}`:    {Kind: domain.VerdictReduce, SizeBp: 1, Reason: "x"},
		`{"verdict":"reject","sizeMultiplier":1,"reason":"no thanks"}`: {Kind: domain.VerdictReject, Reason: "no thanks"},
	} {
		if v, err := parseVerdict(text); err != nil || v != want {
			t.Errorf("%s → %+v %v, want %+v", text, v, err, want)
		}
	}
}

// TestMultiplierToBpNeverEnlarges: whatever float the model returns (NaN, ±Inf, huge,
// negative, tiny), the converted verdict never makes a trade larger.
func TestMultiplierToBpNeverEnlarges(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 8))
	specials := []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, math.Copysign(0, -1), 1, 1 + 1e-15, 1 - 1e-15, math.MaxFloat64, math.SmallestNonzeroFloat64, -1}
	for i := range 50_000 {
		f := r.NormFloat64() * 2
		if i < len(specials) {
			f = specials[i]
		}
		bp := multiplierToBp(f)
		if bp < 0 || bp > domain.FullSize || (f > 0 && f < 1 && float64(bp) > f*float64(domain.FullSize)) {
			t.Fatalf("multiplierToBp(%v) = %d", f, bp)
		}
		size := domain.Cents(r.Int64())
		kind := []domain.VerdictKind{domain.VerdictApprove, domain.VerdictReduce, domain.VerdictReject, "increase", ""}[r.IntN(5)]
		if got := domain.ApplyVerdict(size, domain.Verdict{Kind: kind, SizeBp: bp}); got < 0 || got > size {
			t.Fatalf("ApplyVerdict(%d, %s %v→%d bp) = %d", size, kind, f, bp, got)
		}
	}
}

func TestOpenAICompatible(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/chat/completions":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"verdict\":\"reduce\",\"sizeMultiplier\":0.4,\"reason\":\"wide stop\"}"},"finish_reason":"stop"}]}`)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"b-model"},{"id":"a-model"},{"id":"a-model"}]}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
		}
	}))
	defer srv.Close()
	c := New(srv.Client())
	cfg := domain.LLMConfig{Provider: "openai", Model: "m1", BaseURL: srv.URL + "/v1/"}
	v, call, err := c.Review(context.Background(), cfg, "sk-test", sample())
	if err != nil || v.Kind != domain.VerdictReduce || v.SizeBp != 4_000 || v.Reason != "wide stop" {
		t.Fatalf("verdict = %+v %v", v, err)
	}
	if call.Prompt == "" || call.Provider != "openai" || call.Model != "m1" || call.Error != "" || strings.Contains(call.Prompt+call.Response, "sk-test") {
		t.Fatalf("trace = %+v", call)
	}
	if auth != "Bearer sk-test" || got["model"] != "m1" || got["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("request auth %q body %v", auth, got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || !strings.Contains(msgs[0].(map[string]any)["content"].(string), "never increase") {
		t.Fatalf("messages = %v", msgs)
	}
	ms, err := c.Models(context.Background(), cfg, "sk-test")
	if err != nil || strings.Join(ms, ",") != "a-model,b-model" {
		t.Fatalf("models = %v %v", ms, err)
	}
	// errors: no key, unknown provider, HTTP error with the provider's message
	if _, _, err := c.Review(context.Background(), cfg, "", sample()); !errors.Is(err, domain.ErrCredentialsMissing) {
		t.Fatalf("no key: %v", err)
	}
	if _, _, err := c.Review(context.Background(), domain.LLMConfig{Provider: "x"}, "k", sample()); err == nil {
		t.Fatal("unknown provider")
	}
	if _, err := c.Models(context.Background(), domain.LLMConfig{Provider: "x"}, "k"); err == nil {
		t.Fatal("unknown provider models")
	}
	bad := cfg
	bad.BaseURL = srv.URL + "/nope"
	if _, _, err := c.Review(context.Background(), bad, "k", sample()); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("http error: %v", err)
	}
	if _, err := c.Models(context.Background(), bad, "k"); err == nil {
		t.Fatal("models http error")
	}
	// local Ollama needs no key
	local := domain.LLMConfig{Provider: "ollama_local", Model: "llama", BaseURL: srv.URL + "/v1"}
	if _, _, err := c.Review(context.Background(), local, "", sample()); err != nil {
		t.Fatalf("ollama local: %v", err)
	}
}

func TestOpenAIEmptyAndTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Slow") == "" && strings.HasSuffix(r.URL.Path, "/chat/completions") && r.URL.Query().Get("slow") == "" {
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}
	}))
	defer srv.Close()
	c := New(srv.Client())
	if _, _, err := c.Review(context.Background(), domain.LLMConfig{Provider: "openai", Model: "m", BaseURL: srv.URL}, "k", sample()); err == nil {
		t.Fatal("empty choices accepted")
	}
	if timeout(domain.LLMConfig{}) != time.Minute || timeout(domain.LLMConfig{TimeoutSeconds: 5}) != 5*time.Second {
		t.Fatal("timeout")
	}
	if New(nil).HTTP == nil {
		t.Fatal("default http client")
	}
}

func TestAnthropic(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	stop := "end_turn"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/messages"):
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"`+stop+`",
				"content":[{"type":"text","text":"{\"verdict\":\"approve\",\"sizeMultiplier\":1,\"reason\":\"consistent\"}"}],
				"usage":{"input_tokens":10,"output_tokens":5}}`)
		case strings.HasSuffix(r.URL.Path, "/v1/models"):
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-opus-5-5","type":"model","display_name":"Claude Opus 5.5","created_at":"2026-01-01T00:00:00Z"}],"has_more":false,"first_id":"a","last_id":"a"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.Client())
	c.anthropicOpts = []option.RequestOption{option.WithBaseURL(srv.URL)}
	cfg := domain.LLMConfig{Provider: "anthropic"}
	v, call, err := c.Review(context.Background(), cfg, "sk-ant-test", sample())
	if err != nil || v.Kind != domain.VerdictApprove || v.SizeBp != domain.FullSize {
		t.Fatalf("verdict = %+v %v", v, err)
	}
	if call.InputTokens != 10 || call.OutputTokens != 5 || call.Model != "claude-opus-5-5" || !strings.Contains(call.Response, "consistent") {
		t.Fatalf("trace = %+v", call)
	}
	if body["model"] != "claude-opus-5-5" || hdr.Get("Authorization") != "Bearer sk-ant-test" || !strings.Contains(hdr.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") || body["fallbacks"] != "default" {
		t.Fatalf("request: headers %v model %v key %q beta %q fallbacks %v", hdr, body["model"], hdr.Get("X-Api-Key"), hdr.Get("Anthropic-Beta"), body["fallbacks"])
	}
	oc := body["output_config"].(map[string]any)
	if oc["effort"] != "medium" || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("output_config = %v", oc)
	}
	if _, tools := body["tools"]; tools {
		t.Fatal("the reviewer must have no tools")
	}
	ms, err := c.Models(context.Background(), cfg, "sk-ant-test")
	if err != nil || len(ms) != 1 || ms[0] != "claude-opus-5-5" {
		t.Fatalf("models = %v %v", ms, err)
	}
	stop = "refusal"
	if _, _, err := c.Review(context.Background(), cfg, "sk-ant-test", sample()); err == nil {
		t.Fatal("a refusal must be an error (→ reject)")
	}
	withURL := domain.LLMConfig{Provider: "anthropic", Model: "claude-sonnet-5-5", BaseURL: srv.URL + "/nope"}
	if _, _, err := c.Review(context.Background(), withURL, "k", sample()); err == nil {
		t.Fatal("bad base URL")
	}
	if _, err := c.Models(context.Background(), domain.LLMConfig{Provider: "anthropic"}, ""); !errors.Is(err, domain.ErrCredentialsMissing) {
		t.Fatal("models without key")
	}
}
