package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestRedirectsRefused is PLAN 7.1.7 (F7): the LLM client never follows a redirect (the
// Authorization header with the provider key would go to another host).
func TestRedirectsRefused(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redir.Close)
	hc := &http.Client{}
	c := New(hc)
	cfg := domain.LLMConfig{Provider: "openai", Model: "m1", BaseURL: redir.URL + "/v1"}
	if _, _, err := c.Review(context.Background(), cfg, "sk-test", sample()); err == nil {
		t.Fatal("review: a redirect was answered as success")
	}
	if _, err := c.Models(context.Background(), cfg, "sk-test"); err == nil {
		t.Fatal("models: a redirect was answered as success")
	}
	anth := domain.LLMConfig{Provider: "anthropic", Model: "claude-opus-5-5", BaseURL: redir.URL}
	if _, _, err := c.Review(context.Background(), anth, "sk-ant", sample()); err == nil {
		t.Fatal("anthropic: a redirect was answered as success")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the redirect target got %d request(s)", n)
	}
	if hc.CheckRedirect != nil {
		t.Fatal("the caller's http.Client was changed")
	}
}
