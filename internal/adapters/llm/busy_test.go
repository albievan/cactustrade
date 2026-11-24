package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestBusyProviderRetriedOnce: a provider answering "high demand" (503) is asked once more
// after a short pause; a second 503 is a clear, temporary error (still a rejection).
func TestBusyProviderRetriedOnce(t *testing.T) {
	old := retryAfterBusy
	retryAfterBusy = 10 * time.Millisecond
	defer func() { retryAfterBusy = old }()
	const ok = `{"choices":[{"message":{"content":"{\"verdict\":\"approve\",\"reason\":\"fine\"}"},"finish_reason":"stop"}]}`
	const busyBody = `[{"error":{"code":503,"message":"This model is currently experiencing high demand.","status":"UNAVAILABLE"}}]`
	for _, tt := range []struct {
		name      string
		answers   []int
		wantCalls int32
		wantErr   string
	}{
		{"busy then fine", []int{503, 200}, 2, ""},
		{"overloaded (529) then fine", []int{529, 200}, 2, ""},
		{"busy twice", []int{503, 503}, 2, "overloaded or having a problem right now"},
		{"not retried: quota", []int{429}, 1, "quota or rate limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				code := tt.answers[min(int(n), len(tt.answers))-1]
				w.WriteHeader(code)
				if code == 200 {
					_, _ = io.WriteString(w, ok)
					return
				}
				_, _ = io.WriteString(w, busyBody)
			}))
			defer srv.Close()
			c := New(srv.Client())
			cfg := domain.LLMConfig{Provider: "gemini", Model: "models/gemini-x", BaseURL: srv.URL}
			v, _, err := c.Review(context.Background(), cfg, "k-12345678", sample())
			if calls.Load() != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tt.wantCalls)
			}
			if tt.wantErr == "" {
				if err != nil || v.Kind != domain.VerdictApprove {
					t.Fatalf("review = %+v %v", v, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestBusyNotRetriedWithoutTime: no retry when the deadline would not leave room for it.
func TestBusyNotRetriedWithoutTime(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg := domain.LLMConfig{Provider: "gemini", Model: "m", BaseURL: srv.URL}
	if _, _, err := c.Review(ctx, cfg, "k-12345678", sample()); err == nil || calls.Load() != 1 {
		t.Fatalf("err %v, calls %d", err, calls.Load())
	}
}
