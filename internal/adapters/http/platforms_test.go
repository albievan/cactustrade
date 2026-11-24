package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

func TestPlatformEndpoints(t *testing.T) {
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	sec := &fakes.SecretStore{Now: clk.Now}
	conn := &fakes.OAuthConnector{}
	tiger := &fakes.Holdings{ByAccount: map[string]domain.Holdings{}}
	saxo := &fakes.Holdings{Errs: map[string]error{}}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: sec, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk,
		Holdings:   map[domain.Platform]ports.Holdings{domain.PlatformTiger: tiger, domain.PlatformSaxo: saxo},
		Connectors: map[domain.Platform]ports.OAuthConnector{domain.PlatformSaxo: conn},
	}, app.Config{Version: "9.9.9", PublicURL: "https://trader.example:8453"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(New(svc, slog.New(slog.DiscardHandler), Config{CookieSecure: true, OpenAPI: []byte("openapi: 3.0.3\n")}, nil))
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	x := &tc{t: t, srv: srv, c: c, store: st, svc: svc}
	if code, _, _ := x.do("GET", "/api/v1/platforms", ""); code != 401 {
		t.Fatalf("anonymous platforms %d", code)
	}
	x.setupAndLogin()
	csrf := []string{"X-CSRF-Token", x.csrf}

	var platforms []struct {
		Platform    string           `json:"platform"`
		Label       string           `json:"label"`
		ViewOnly    bool             `json:"viewOnly"`
		Connectable bool             `json:"connectable"`
		Credentials []map[string]any `json:"credentials"`
	}
	if code := x.getJSON("/api/v1/platforms", &platforms); code != 200 || len(platforms) != 4 {
		t.Fatalf("platforms %d %+v", code, platforms)
	}
	if p := platforms[0]; p.Platform != "etoro" || p.ViewOnly || p.Connectable {
		t.Fatalf("eToro %+v", p)
	}
	if p := platforms[2]; p.Platform != "saxo" || p.Label != "Saxo" || !p.ViewOnly || !p.Connectable || len(p.Credentials) != 4 || p.Credentials[2]["setByFlow"] != true {
		t.Fatalf("Saxo %+v", p)
	}
	if p := platforms[3]; p.Platform != "tiger" || p.Label != "Tiger Brokers" || !p.ViewOnly || p.Connectable || p.Credentials[2]["multiline"] != true {
		t.Fatalf("Tiger %+v", p)
	}

	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Tiger paper","mode":"demo","platform":"tiger"}`, csrf...)
	if code != 201 || m["platform"] != "tiger" {
		t.Fatalf("create Tiger %d %v", code, m)
	}
	tg := m["id"].(string)
	code, m, _ = x.do("POST", "/api/v1/accounts", `{"name":"Saxo SIM","mode":"demo","platform":"saxo"}`, csrf...)
	if code != 201 || m["platform"] != "saxo" {
		t.Fatalf("create Saxo %d %v", code, m)
	}
	sx := m["id"].(string)

	// credentials by key, PEM with line breaks; values and the old fields are exclusive
	body := `{"values":{"tiger_id":"20150001","account":"21234567","private_key":"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----"}}`
	if code, m, _ := x.do("PUT", "/api/v1/accounts/"+tg+"/keys", body, csrf...); code != 204 {
		t.Fatalf("set Tiger keys %d %v", code, m)
	}
	if v, _ := sec.Get(context.Background(), domain.CredentialName("tiger."+tg+".private_key")); !strings.Contains(v, "\nAAAA\n") {
		t.Fatalf("PEM stored as %q", v)
	}
	for _, bad := range []string{
		`{"values":{"api_key":"x"}}`, `{"values":{"tiger_id":"x"},"apiKey":"y"}`, `{"values":{"account":"a\nb"}}`,
	} {
		if code, m, _ := x.do("PUT", "/api/v1/accounts/"+tg+"/keys", bad, csrf...); code != 400 {
			t.Fatalf("%s: %d %v", bad, code, m)
		}
	}
	if code, m, _ := x.do("PUT", "/api/v1/accounts/"+sx+"/keys", `{"values":{"refresh_token":"rt"}}`, csrf...); code != 400 || m["field"] != "refresh_token" {
		t.Fatalf("typed refresh token %d %v", code, m)
	}
	if code, _, _ := x.do("PUT", "/api/v1/accounts/"+sx+"/keys", `{"apiKey":"app","userKey":"secret"}`, csrf...); code != 204 {
		t.Fatalf("Saxo app key and secret %d", code)
	}
	var keys struct {
		APIKeySet          bool             `json:"apiKeySet"`
		Complete           bool             `json:"complete"`
		Connectable        bool             `json:"connectable"`
		ConnectRedirectURI string           `json:"connectRedirectUri"`
		Credentials        []map[string]any `json:"credentials"`
	}
	if code := x.getJSON("/api/v1/accounts/"+sx+"/keys", &keys); code != 200 || !keys.APIKeySet || keys.Complete || !keys.Connectable ||
		keys.ConnectRedirectURI != "https://trader.example:8453/api/v1/oauth/saxo/callback" || len(keys.Credentials) != 4 ||
		keys.Credentials[0]["set"] != true || keys.Credentials[0]["updatedAt"] == nil || keys.Credentials[2]["set"] != false {
		t.Fatalf("Saxo keys %d %+v", code, keys)
	}
	for _, cr := range keys.Credentials {
		for k := range cr {
			if k == "value" {
				t.Fatal("a credential value was returned")
			}
		}
	}
	keys.ConnectRedirectURI = ""
	if code := x.getJSON("/api/v1/accounts/"+tg+"/keys", &keys); code != 200 || !keys.Complete || keys.Connectable || keys.ConnectRedirectURI != "" {
		t.Fatalf("Tiger keys %d %+v", code, keys)
	}

	// connecting: only a Saxo account; the answer carries Saxo's address with our state
	if code, _, _ := x.do("GET", "/api/v1/accounts/"+tg+"/saxo/connect", ""); code != 400 {
		t.Fatalf("connect a Tiger account %d", code)
	}
	code, m, _ = x.do("GET", "/api/v1/accounts/"+sx+"/saxo/connect", "")
	if code != 200 || m["redirectUri"] != "https://trader.example:8453/api/v1/oauth/saxo/callback" {
		t.Fatalf("connect %d %v", code, m)
	}
	au, _ := url.Parse(m["authorizeUrl"].(string))
	state := au.Query().Get("state")
	if state == "" {
		t.Fatalf("no state in %s", au)
	}
	// the callback is public (the session cookie does not come back from Saxo)
	noFollow := &http.Client{Transport: c.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	callback := func(q string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL+"/api/v1/oauth/saxo/callback?"+q, http.NoBody)
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	if code, loc := callback("code=the-code&state=" + url.QueryEscape(state)); code != 303 || loc != "/accounts?connected="+sx {
		t.Fatalf("callback %d %s", code, loc)
	}
	if len(conn.Exchanged) != 1 || conn.Exchanged[0] != sx+":the-code:https://trader.example:8453/api/v1/oauth/saxo/callback" {
		t.Fatalf("exchanged %v", conn.Exchanged)
	}
	if code, loc := callback("code=again&state=" + url.QueryEscape(state)); code != 303 || loc != "/accounts?connect_error=expired" {
		t.Fatalf("replayed state %d %s", code, loc)
	}
	_, m, _ = x.do("GET", "/api/v1/accounts/"+sx+"/saxo/connect", "")
	au, _ = url.Parse(m["authorizeUrl"].(string))
	if code, loc := callback("error=access_denied&state=" + url.QueryEscape(au.Query().Get("state"))); code != 303 || loc != "/accounts?connect_error=refused" {
		t.Fatalf("refused %d %s", code, loc)
	}
	_, m, _ = x.do("GET", "/api/v1/accounts/"+sx+"/saxo/connect", "")
	au, _ = url.Parse(m["authorizeUrl"].(string))
	if code, loc := callback("code=bad%20code&state=" + url.QueryEscape(au.Query().Get("state"))); code != 303 || loc != "/accounts?connect_error=failed" {
		t.Fatalf("bad code %d %s", code, loc)
	}
	// forged states are rate limited like sign-ins
	for range 25 {
		callback("code=c&state=forged")
	}
	if _, loc := callback("code=c&state=forged"); loc != "/accounts?connect_error=failed" {
		t.Fatalf("not limited: %s", loc)
	}

	// the overview: a lapsed Saxo sign-in says "reconnect"; a position without an
	// account-currency value is "unknown"
	saxo.Errs[sx] = errReconnect
	tiger.ByAccount[tg] = domain.Holdings{
		Platform: domain.PlatformTiger, Currency: "JPY", Cash: 1, Equity: 2, AsOf: clk.T,
		Positions: []domain.Holding{{Symbol: "7203", Quantity: 1_000_000, Unknown: true, Long: true}},
	}
	var ov struct {
		Accounts []struct {
			Account   map[string]any `json:"account"`
			Holdings  map[string]any `json:"holdings"`
			Error     string         `json:"error"`
			Reconnect bool           `json:"reconnect"`
		} `json:"accounts"`
	}
	if code := x.getJSON("/api/v1/overview", &ov); code != 200 || len(ov.Accounts) != 2 {
		t.Fatalf("overview %d %+v", code, ov)
	}
	for _, a := range ov.Accounts {
		switch a.Account["platform"] {
		case "saxo":
			if !a.Reconnect || a.Holdings != nil || !strings.HasPrefix(a.Error, "Reconnect to Saxo") {
				t.Fatalf("Saxo %+v", a)
			}
		case "tiger":
			pos := a.Holdings["positions"].([]any)[0].(map[string]any)
			if a.Reconnect || pos["unknown"] != true || a.Holdings["currency"] != "JPY" {
				t.Fatalf("Tiger %+v", a)
			}
		}
	}
	// view-only accounts are refused by every trading path
	if code, m, _ := x.do("PUT", "/api/v1/accounts/current", `{"accountId":"`+sx+`"}`, csrf...); code != 403 || m["title"] != "View-only account" {
		t.Fatalf("switch to Saxo %d %v", code, m)
	}
	if code, _, _ := x.do("POST", "/api/v1/accounts/"+tg+"/test", "", csrf...); code != 403 {
		t.Fatalf("test a Tiger account %d", code)
	}
}

var errReconnect = func() error {
	return &wrapped{msg: "Saxo sign-in lapsed", errs: []error{domain.ErrReconnect, domain.ErrUnavailable}}
}()

type wrapped struct {
	msg  string
	errs []error
}

func (w *wrapped) Error() string   { return w.msg }
func (w *wrapped) Unwrap() []error { return w.errs }
