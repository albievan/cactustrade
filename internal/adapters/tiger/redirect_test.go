package tiger

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestRedirectsRefused is PLAN 7.1.7 (F7): the gateway client never follows a redirect (it
// would carry the signed request and the Tiger ID to another host).
func TestRedirectsRefused(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redir.Close)
	sec := &fakes.SecretStore{}
	setCreds(t, sec, paper, pkcs8PEM(t))
	hc := &http.Client{}
	c, err := New(Config{
		GatewayURL: redir.URL + "/gateway", HTTP: hc, Secrets: sec,
		Now:   func() time.Time { return time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC) },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Holdings(ctxFor(paper)); err == nil {
		t.Fatal("a redirect was answered as success")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the redirect target got %d request(s)", n)
	}
	if hc.CheckRedirect != nil {
		t.Fatal("the caller's http.Client was changed")
	}
}
