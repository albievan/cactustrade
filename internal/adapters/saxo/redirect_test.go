package saxo

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

// TestRedirectsRefused is PLAN 7.1.7 (F7): neither the sign-in nor the API follows a
// redirect (it would carry the app secret or a token to another host).
func TestRedirectsRefused(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redir.Close)
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	sec := &fakes.SecretStore{Now: func() time.Time { return now }}
	ctx := context.Background()
	_ = sec.Put(ctx, cred(simAcct, domain.SaxoAppKey), "appkey")
	_ = sec.Put(ctx, cred(simAcct, domain.SaxoAppSecret), "appsecret")
	_ = sec.Put(ctx, cred(simAcct, domain.SaxoRefreshToken), "refresh-0")
	hc := &http.Client{}
	c, err := New(Config{
		SimBaseURL: redir.URL + "/sim/openapi", SimAuthURL: redir.URL + "/auth", LiveBaseURL: redir.URL + "/openapi", LiveAuthURL: redir.URL + "/auth",
		HTTP: hc, Secrets: sec, Now: func() time.Time { return now },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Holdings(ctxFor(simAcct)); err == nil {
		t.Fatal("a redirect was answered as success")
	}
	_ = sec.Put(ctx, cred(simAcct, domain.SaxoDevToken), "devtoken-secret")
	if _, err := c.Holdings(ctxFor(simAcct)); err == nil {
		t.Fatal("a redirect was answered as success (developer token)")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the redirect target got %d request(s)", n)
	}
	if hc.CheckRedirect != nil {
		t.Fatal("the caller's http.Client was changed")
	}
}
