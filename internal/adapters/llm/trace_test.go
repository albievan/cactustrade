package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/albievan/cactustrade/internal/domain"
)

// fakeProvider answers both API styles: Anthropic's /v1/messages and the OpenAI-compatible
// /chat/completions. It records every request body it received.
type fakeProvider struct {
	mu     sync.Mutex
	bodies [][]byte
	// answer is the verdict text the model returns; status != 0 is an HTTP error.
	answer string
	status int
	block  bool // wait for the client to give up
	raw    string
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, b)
	answer, status, block, raw := f.answer, f.status, f.block, f.raw
	f.mu.Unlock()
	if block {
		<-r.Context().Done()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	if raw != "" {
		_, _ = io.WriteString(w, raw)
		return
	}
	text, _ := json.Marshal(answer)
	if strings.HasSuffix(r.URL.Path, "/v1/messages") {
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-fallback-1","stop_reason":"end_turn",
			"content":[{"type":"text","text":`+string(text)+`}],"usage":{"input_tokens":321,"output_tokens":45}}`)
		return
	}
	_, _ = io.WriteString(w, `{"model":"served-model-7","choices":[{"message":{"content":`+string(text)+`},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":123,"completion_tokens":17,"total_tokens":140}}`)
}

// set changes the fake's behaviour (under its lock: the server reads it concurrently).
func (f *fakeProvider) set(change func(f *fakeProvider)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeProvider) last() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return nil
	}
	return f.bodies[len(f.bodies)-1]
}

const testKey = "sk-test-0123456789abcdef"

// clientFor points a client at the fake for provider id.
func clientFor(t *testing.T, srv *httptest.Server, id domain.LLMProviderID) (*Client, domain.LLMConfig) {
	t.Helper()
	c := New(srv.Client())
	p, _ := domain.LLMProviderByID(id)
	if p.Anthropic {
		c.anthropicOpts = []option.RequestOption{option.WithBaseURL(srv.URL)}
		return c, domain.LLMConfig{Provider: id, Model: "claude-opus-5-5"}
	}
	return c, domain.LLMConfig{Provider: id, Model: "asked-model", BaseURL: srv.URL + "/v1"}
}

// TestReviewTraceEveryProvider: for every provider the trace's prompt is exactly the body the
// provider received, it has no tools, tool_choice or functions key, the usage and the served
// model are parsed, and neither the trace nor the body holds the key.
func TestReviewTraceEveryProvider(t *testing.T) {
	fp := &fakeProvider{answer: `{"verdict":"reduce","sizeMultiplier":0.5,"reason":"ok"}`}
	srv := httptest.NewServer(fp)
	defer srv.Close()
	for _, p := range domain.LLMProviders {
		t.Run(string(p.ID), func(t *testing.T) {
			c, cfg := clientFor(t, srv, p.ID)
			v, call, err := c.Review(context.Background(), cfg, testKey, sample())
			if err != nil || v.Kind != domain.VerdictReduce || v.SizeBp != 5_000 {
				t.Fatalf("verdict = %+v %v", v, err)
			}
			sent := fp.last()
			if call.Prompt != string(sent) {
				t.Fatalf("trace prompt differs from the body sent:\n%s\n%s", call.Prompt, sent)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(call.Prompt), &body); err != nil {
				t.Fatalf("prompt is not the JSON body: %v", err)
			}
			for _, k := range []string{"tools", "tool_choice", "functions", "function_call"} {
				if _, ok := body[k]; ok {
					t.Fatalf("the request has %q: the reviewer must have no tools", k)
				}
			}
			if !strings.Contains(call.Prompt, "instrument id: 1001") || !strings.Contains(call.Response, "ok") {
				t.Fatalf("trace = %+v", call)
			}
			wantIn, wantOut, wantModel := int64(123), int64(17), "served-model-7"
			if p.Anthropic {
				wantIn, wantOut, wantModel = 321, 45, "claude-fallback-1"
			}
			if call.InputTokens != wantIn || call.OutputTokens != wantOut || call.Model != wantModel || call.Provider != p.ID || call.Error != "" || call.Latency < 0 {
				t.Fatalf("trace = %+v", call)
			}
			if strings.Contains(call.Prompt+call.Response+call.Model+call.Error, testKey) || strings.Contains(string(sent), testKey) {
				t.Fatal("the key is in the trace or the body")
			}
			if call.ID != "" || call.AccountID != "" || call.Purpose != "" || call.CostMicros != 0 || !call.At.IsZero() {
				t.Fatalf("the adapter set the caller's fields: %+v", call)
			}
		})
	}
}

// TestReviewTraceOnFailure: a timeout, an HTTP error, an unusable answer and a body that is
// not JSON all return an error with a filled trace: prompt, error, latency and the raw
// response when there was one.
func TestReviewTraceOnFailure(t *testing.T) {
	for _, id := range []domain.LLMProviderID{"anthropic", "openai"} {
		t.Run(string(id), func(t *testing.T) {
			fp := &fakeProvider{}
			srv := httptest.NewServer(fp)
			defer srv.Close()
			c, cfg := clientFor(t, srv, id)
			tick := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
			var mu sync.Mutex
			c.now = func() time.Time { // each reading is 250 ms after the previous one
				mu.Lock()
				defer mu.Unlock()
				tick = tick.Add(250 * time.Millisecond)
				return tick
			}

			// a timeout: the caller's deadline ends the call
			fp.set(func(f *fakeProvider) { f.block = true })
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			_, call, err := c.Review(ctx, cfg, testKey, sample())
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) || call.Error == "" || call.Latency != 250*time.Millisecond || !strings.Contains(call.Prompt, "instrument id: 1001") {
				t.Fatalf("timeout: %v, trace %+v", err, call)
			}
			fp.set(func(f *fakeProvider) { f.block = false })

			// prose instead of JSON: an error, the raw answer kept
			fp.set(func(f *fakeProvider) { f.answer = "I think this trade looks fine." })
			_, call, err = c.Review(context.Background(), cfg, testKey, sample())
			if err == nil || !strings.Contains(call.Response, "looks fine") || call.Error != err.Error() || call.InputTokens == 0 {
				t.Fatalf("prose: %v, trace %+v", err, call)
			}

			// a body that is not JSON at all
			const html = "<html>gateway</html>"
			fp.set(func(f *fakeProvider) { f.raw = html })
			_, call, err = c.Review(context.Background(), cfg, testKey, sample())
			if err == nil || call.Response != html || call.Error == "" {
				t.Fatalf("html: %v, trace %+v", err, call)
			}

			// an HTTP error that echoes the key: kept, but redacted everywhere
			fp.set(func(f *fakeProvider) {
				f.status, f.raw = http.StatusUnauthorized, `{"error":{"message":"bad key `+testKey+`"},"type":"error"}`
			})
			_, call, err = c.Review(context.Background(), cfg, testKey, sample())
			if err == nil || strings.Contains(err.Error(), testKey) || strings.Contains(call.Response+call.Error, testKey) || !strings.Contains(call.Response, redacted) {
				t.Fatalf("401: %v, trace %+v", err, call)
			}
			if call.Model != cfg.Model || call.Provider != id {
				t.Fatalf("no answer: the trace keeps the asked model: %+v", call)
			}
		})
	}
}

// TestReviewTraceBeforeSending: errors before anything is sent still return a trace.
func TestReviewTraceBeforeSending(t *testing.T) {
	c := New(nil)
	_, call, err := c.Review(context.Background(), domain.LLMConfig{Provider: "nope", Model: "m"}, "", sample())
	if err == nil || call.Error == "" || call.Provider != "nope" || call.Model != "m" {
		t.Fatalf("unknown provider: %v %+v", err, call)
	}
	_, call, err = c.Review(context.Background(), domain.LLMConfig{Provider: "anthropic"}, "", sample())
	if !errors.Is(err, domain.ErrCredentialsMissing) || call.Model != domain.DefaultAnthropicModel || call.Error == "" {
		t.Fatalf("no key: %v %+v", err, call)
	}
}

// TestRecorderBodyWithoutGetBody: a request whose body cannot be re-read is still recorded
// and still sent whole.
func TestRecorderBodyWithoutGetBody(t *testing.T) {
	r := &recorder{}
	req, _ := http.NewRequest(http.MethodPost, "http://x", io.NopCloser(strings.NewReader(`{"a":1}`)))
	req.GetBody = nil
	var got string
	resp, err := r.middleware(req, func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		got = string(b)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	passed, _ := io.ReadAll(resp.Body) // the caller still reads the whole body
	_ = resp.Body.Close()
	var call domain.LLMCall
	r.into(&call)
	if got != `{"a":1}` || call.Prompt != `{"a":1}` || call.Response != `{"ok":true}` || string(passed) != `{"ok":true}` {
		t.Fatalf("recorded %+v, sent %q, passed on %q", call, got, passed)
	}
	failing := &recorder{}
	resp, err = failing.middleware(req, func(*http.Request) (*http.Response, error) { return nil, errors.New("down") })
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("transport error lost")
	}
}
