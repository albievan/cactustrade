package trading212

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestRedirectsRefused is PLAN 7.1.7 (F7): the client never follows a redirect (it would
// carry the Basic credentials to another host), whatever http.Client it is given.
func TestRedirectsRefused(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	sec := &fakes.SecretStore{}
	k, s := domain.AccountKeyNames(demoAcct)
	_ = sec.Put(context.Background(), k, "key")
	_ = sec.Put(context.Background(), s, "secret")
	hc := &http.Client{}
	c, err := New(Config{
		DemoBaseURL: redir.URL, LiveBaseURL: redir.URL, HTTP: hc, Secrets: sec,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Holdings(ctxFor(demoAcct)); err == nil {
		t.Fatal("a redirect was answered as success")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the redirect target got %d request(s)", n)
	}
	if hc.CheckRedirect != nil {
		t.Fatal("the caller's http.Client was changed")
	}
}
