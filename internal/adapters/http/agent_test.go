package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// nyseOpen is a calendar with every market trading 14:30–21:00 UTC.
type nyseOpen struct{}

func (nyseOpen) Session(_ domain.Market, at time.Time) (time.Time, time.Time, bool, error) {
	y, mo, d := at.UTC().Date()
	return time.Date(y, mo, d, 14, 30, 0, 0, time.UTC), time.Date(y, mo, d, 21, 0, 0, 0, time.UTC), true, nil
}

type agentTC struct {
	*tc
	h    *Server
	b    *fakes.SimBroker
	sec  *fakes.SecretStore
	acct string // the demo account's id
}

// newAgentTC is a signed-in admin with a demo account "Main demo" that has keys, on a
// simulated eToro account with $10 000, in the NYSE session.
func newAgentTC(t *testing.T) *agentTC {
	t.Helper()
	return newAgentTCWith(t, func(*app.Deps) {})
}

// newAgentTCWith is newAgentTC with the service's dependencies changed by edit.
func newAgentTCWith(t *testing.T, edit func(*app.Deps)) *agentTC {
	t.Helper()
	return newAgentTCServer(t, edit, Config{}, slog.New(slog.DiscardHandler))
}

// newAgentTCServer is newAgentTCWith with the HTTP adapter's settings (cfg; OpenAPI and
// CookieSecure are set here) and its logger.
func newAgentTCServer(t *testing.T, edit func(*app.Deps), cfg Config, log *slog.Logger) *agentTC {
	t.Helper()
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)}
	sec := &fakes.SecretStore{Now: clk.Now}
	b := &fakes.SimBroker{Now: clk.Now, Cash: 10_000_00}
	deps := app.Deps{
		Store: st, Secrets: sec, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk,
		Broker: b, Market: &fakes.Broker{}, Calendar: nyseOpen{},
		Sleep: func(ctx context.Context, d time.Duration) error { clk.Advance(d); return ctx.Err() },
	}
	edit(&deps)
	svc, err := app.New(deps, app.Config{Version: "9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAgentLoop(true) // as after a clean start-up (6.13)
	cfg.CookieSecure, cfg.OpenAPI = true, []byte("openapi: 3.0.3\n")
	h := New(svc, log, cfg, nil)
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	t.Cleanup(h.CloseStreams) // before srv.Close (cleanups run last first): open streams end
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	x := &agentTC{tc: &tc{t: t, srv: srv, c: c, store: st, svc: svc}, b: b, sec: sec, h: h}
	x.setupAndLogin()
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Main demo","mode":"demo"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create account %d %v", code, m)
	}
	x.acct = m["id"].(string)
	x.keys(t, x.acct)
	return x
}

func (x *agentTC) csrfHdr() []string { return []string{"X-CSRF-Token", x.csrf} }

func (x *agentTC) keys(t *testing.T, id string) {
	t.Helper()
	a, err := x.store.Accounts().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ak, uk := domain.AccountKeyNames(a)
	for _, n := range []domain.CredentialName{ak, uk} {
		if err := x.sec.Put(context.Background(), n, "test-key"); err != nil {
			t.Fatal(err)
		}
	}
}

// ready sets a $5 000 budget and reconciles the account.
func (x *agentTC) ready(t *testing.T) {
	t.Helper()
	limits := `{"tradingBudgetCents":500000,"dailyLossBp":200,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":5,` +
		`"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":5,"maxShortBp":2500}`
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", limits, x.csrfHdr()...); code != 200 {
		t.Fatalf("limits %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/reconcile", "", x.csrfHdr()...); code != 200 || m["ok"] != true {
		t.Fatalf("reconcile %d %v", code, m)
	}
}

// bearer sends a request with an API key instead of the session cookie.
func (x *agentTC) bearer(method, path, body, key string) (int, string) {
	x.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, x.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Transport: x.c.Transport}).Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type agentStep struct {
	name   string
	method string
	path   string
	body   string
	hdr    []string
	code   int
	state  string
}

func TestAgentEndpointsLifecycle(t *testing.T) {
	x := newAgentTC(t)
	csrf := x.csrfHdr()

	// before anything: stopped, not reconciled, controls for the admin in a web session
	code, m, _ := x.do("GET", "/api/v1/agent", "")
	if code != 200 || m["state"] != "stopped" || m["reconciliation"].(map[string]any)["ok"] != false || m["equityKnown"] != true ||
		m["equityCents"] != 1_000_000.0 || m["resetPhrase"] != "RESET Main demo" {
		t.Fatalf("agent %d %v", code, m)
	}
	ctl := m["controls"].(map[string]any)
	for _, k := range []string{"start", "pause", "stop", "kill", "reset"} {
		if ctl[k] != true {
			t.Fatalf("controls %v", ctl)
		}
	}
	if ctl["gated"] != false || len(m["flatten"].([]any)) != len(domain.Markets) {
		t.Fatalf("agent %v", m)
	}

	steps := []agentStep{
		{"start without CSRF", "POST", "/api/v1/agent/start", "", nil, 403, ""},
		{"start with a wrong CSRF token", "POST", "/api/v1/agent/start", "", []string{"X-CSRF-Token", "nope"}, 403, ""},
		{"start without a budget", "POST", "/api/v1/agent/start", "", csrf, 422, ""},
	}
	run := func(steps []agentStep) {
		t.Helper()
		for _, s := range steps {
			code, m, _ := x.do(s.method, s.path, s.body, s.hdr...)
			if code != s.code || (s.state != "" && m["state"] != s.state) {
				t.Fatalf("%s: %d %v", s.name, code, m)
			}
		}
	}
	run(steps)
	limits := `{"tradingBudgetCents":500000,"dailyLossBp":200,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":5,` +
		`"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":5,"maxShortBp":2500}`
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", limits, csrf...); code != 200 {
		t.Fatalf("limits %d %v", code, m)
	}
	run([]agentStep{
		{"start before reconciliation", "POST", "/api/v1/agent/start", "", csrf, 409, ""},
		{"reconcile without CSRF", "POST", "/api/v1/agent/reconcile", "", nil, 403, ""},
		{"reconcile", "POST", "/api/v1/agent/reconcile", "", csrf, 200, ""},
		{"start", "POST", "/api/v1/agent/start", "", csrf, 200, "running"},
		{"start again", "POST", "/api/v1/agent/start", "", csrf, 200, "running"},
		{"limits while running", "PUT", "/api/v1/risk/limits", limits, csrf, 409, ""},
		{"pause without CSRF", "POST", "/api/v1/agent/pause", "", nil, 403, ""},
		{"pause", "POST", "/api/v1/agent/pause", "", csrf, 200, "paused"},
		{"pause a paused agent", "POST", "/api/v1/agent/pause", "", csrf, 200, "paused"},
		{"start a paused agent", "POST", "/api/v1/agent/start", "", csrf, 200, "running"},
		{"stop without CSRF", "POST", "/api/v1/agent/stop", "", nil, 403, ""},
		{"stop", "POST", "/api/v1/agent/stop", "", csrf, 200, "stopped"},
		{"pause a stopped agent", "POST", "/api/v1/agent/pause", "", csrf, 409, ""},
		{"kill without CSRF", "POST", "/api/v1/agent/kill", `{"reason":"x"}`, nil, 403, ""},
		{"kill", "POST", "/api/v1/agent/kill", `{"reason":"test"}`, csrf, 200, ""},
		{"kill without a body", "POST", "/api/v1/agent/kill", "", csrf, 200, ""},
		{"kill with an unknown field", "POST", "/api/v1/agent/kill", `{"bogus":1}`, csrf, 400, ""},
		{"start while killed", "POST", "/api/v1/agent/start", "", csrf, 409, ""},
		{"stop while killed", "POST", "/api/v1/agent/stop", "", csrf, 409, ""},
		{"reset without CSRF", "POST", "/api/v1/agent/reset", `{"confirm":"RESET Main demo"}`, nil, 403, ""},
		{"reset with the wrong phrase", "POST", "/api/v1/agent/reset", `{"confirm":"reset main demo"}`, csrf, 422, ""},
		{"reset", "POST", "/api/v1/agent/reset", `{"confirm":"RESET Main demo"}`, csrf, 200, "stopped"},
		{"reset when not killed", "POST", "/api/v1/agent/reset", `{"confirm":"RESET Main demo"}`, csrf, 409, ""},
		{"unknown account", "POST", "/api/v1/agent/stop?accountId=nope", "", csrf, 404, ""},
	})

	code, m, _ = x.do("GET", "/api/v1/agent", "")
	if code != 200 || m["state"] != "stopped" || m["killed"] != false || m["reconciliation"].(map[string]any)["ok"] != true {
		t.Fatalf("agent after reset %d %v", code, m)
	}
	var orders []map[string]any
	if code := x.getJSON("/api/v1/orders", &orders); code != 200 || len(orders) != 0 {
		t.Fatalf("orders %d %v", code, orders)
	}
	for _, bad := range []string{"0", "501", "x"} {
		if code, _, _ := x.do("GET", "/api/v1/orders?limit="+bad, ""); code != 400 {
			t.Fatalf("orders limit=%s: %d", bad, code)
		}
	}
	if x.b.Opens != 0 {
		t.Fatalf("an order was opened: %d", x.b.Opens)
	}
}

func TestAgentAPIKeysMayKillPauseStopButNeverStartOrReset(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	code, m, _ := x.do("POST", "/api/v1/api-keys", `{"name":"ops","role":"admin","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, body := x.bearer("GET", "/api/v1/agent", "", key); code != 200 ||
		!strings.Contains(body, `"start":false`) || !strings.Contains(body, `"reset":false`) || !strings.Contains(body, `"kill":true`) {
		t.Fatalf("agent with a key %d %s", code, body)
	}
	if code, body := x.bearer("POST", "/api/v1/agent/start", "", key); code != 403 {
		t.Fatalf("start with a key %d %s", code, body)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/start", "", x.csrfHdr()...); code != 200 {
		t.Fatalf("start in the web UI %d %v", code, m)
	}
	for _, s := range []struct {
		path  string
		body  string
		code  int
		state string
	}{
		{"/api/v1/agent/pause", "", 200, `"state":"paused"`},
		{"/api/v1/agent/stop", "", 200, `"state":"stopped"`},
		{"/api/v1/agent/reconcile", "", 200, `"ok":true`},
		{"/api/v1/agent/kill", `{"reason":"from a script"}`, 200, `"already":false`},
		{"/api/v1/agent/reset", `{"confirm":"RESET Main demo"}`, 403, ""},
	} {
		if code, body := x.bearer("POST", s.path, s.body, key); code != s.code || !strings.Contains(body, s.state) {
			t.Fatalf("%s with a key: %d %s", s.path, code, body)
		}
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/reset", `{"confirm":"RESET Main demo"}`, x.csrfHdr()...); code != 200 {
		t.Fatalf("reset in the web UI %d %v", code, m)
	}
}

func TestAgentRolesAndRealAccounts(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	// a real account: shown, but nothing starts there (phase 7)
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Real","mode":"real"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create real %d %v", code, m)
	}
	realID := m["id"].(string)
	x.keys(t, realID)
	if code, m, _ := x.do("GET", "/api/v1/agent?accountId="+realID, ""); code != 200 || m["controls"].(map[string]any)["gated"] != true ||
		m["controls"].(map[string]any)["start"] != false {
		t.Fatalf("real agent %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/start?accountId="+realID, "", x.csrfHdr()...); code != 403 || m["title"] != "Real trading needs the phase-7 gate" {
		t.Fatalf("start real %d %v", code, m)
	}

	// a demo account without keys
	code, m, _ = x.do("POST", "/api/v1/accounts", `{"name":"No keys","mode":"demo"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create demo %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/start?accountId="+m["id"].(string), "", x.csrfHdr()...); code != 503 {
		t.Fatalf("start without keys %d %v", code, m)
	}

	// a viewer the demo account is shared with (trade access) may look, not act
	if code, m, _ := x.do("POST", "/api/v1/users", `{"username":"vi","password":"vi long password","role":"viewer"}`, x.csrfHdr()...); code != 201 {
		t.Fatalf("create viewer %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/accounts/"+x.acct+"/shares", `{"username":"vi","access":"trade"}`, x.csrfHdr()...); code != 204 && code != 200 {
		t.Fatalf("share %d %v", code, m)
	}
	x.c.Jar, _ = cookiejar.New(nil)
	x.login("vi", "vi long password")
	code, m, _ = x.do("GET", "/api/v1/agent?accountId="+x.acct, "")
	if code != 200 {
		t.Fatalf("viewer agent %d %v", code, m)
	}
	for k, v := range m["controls"].(map[string]any) {
		if v != false {
			t.Fatalf("viewer control %s = %v", k, v)
		}
	}
	for _, p := range []string{"start", "pause", "stop", "kill", "reconcile"} {
		if code, m, _ := x.do("POST", "/api/v1/agent/"+p+"?accountId="+x.acct, "", x.csrfHdr()...); code != 403 {
			t.Fatalf("viewer %s %d %v", p, code, m)
		}
	}
	if code, _, _ := x.do("GET", "/api/v1/orders?accountId="+x.acct, ""); code != 200 {
		t.Fatalf("viewer orders %d", code)
	}
}
