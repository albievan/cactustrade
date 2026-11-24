package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// realGateTC is newAgentTC plus a reconciled real-money account "Live" with keys and real
// limits (the current account stays the demo).
type realGateTC struct {
	*agentTC
	real string
}

const realLimits = `{"tradingBudgetCents":100000,"dailyLossBp":100,"drawdownBp":500,"maxPositionBp":1000,"maxOpenPositions":4,` +
	`"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":30,"maxShortBp":0}`

func newRealGateTC(t *testing.T, log *slog.Logger) *realGateTC {
	t.Helper()
	return newRealGateTCConfig(t, log, Config{})
}

func newRealGateTCConfig(t *testing.T, log *slog.Logger, cfg Config) *realGateTC {
	t.Helper()
	x := newAgentTCServer(t, func(*app.Deps) {}, cfg, log)
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Live","mode":"real"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create real %d %v", code, m)
	}
	g := &realGateTC{agentTC: x, real: m["id"].(string)}
	x.keys(t, g.real)
	for _, s := range []agentStep{
		{"current: real", "PUT", "/api/v1/accounts/current", `{"accountId":"` + g.real + `"}`, x.csrfHdr(), 200, ""},
		{"real limits", "PUT", "/api/v1/risk/limits", realLimits, x.csrfHdr(), 200, ""},
		{"reconcile real", "POST", "/api/v1/agent/reconcile?accountId=" + g.real, "", x.csrfHdr(), 200, ""},
		{"current: demo", "PUT", "/api/v1/accounts/current", `{"accountId":"` + x.acct + `"}`, x.csrfHdr(), 200, ""},
	} {
		if code, m, _ := x.do(s.method, s.path, s.body, s.hdr...); code != s.code {
			t.Fatalf("%s: %d %v", s.name, code, m)
		}
	}
	return g
}

// seedEvidence gives the demo account n qualifying risk days ending yesterday.
func (g *realGateTC) seedEvidence(t *testing.T, n int) { g.seedEvidenceDays(t, 0, n) }

// seedEvidenceDays gives the demo account the qualifying risk days from+1 … from+n days
// before today.
func (g *realGateTC) seedEvidenceDays(t *testing.T, from, n int) {
	t.Helper()
	ctx := context.Background()
	start := domain.RiskDayStart(time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	for i := from; i < from+n; i++ {
		at := start.AddDate(0, 0, -(1 + i)).Add(17 * time.Hour)
		id := fmt.Sprintf("ev-%d", i)
		o, err := domain.NewOrder(domain.Order{
			RequestID: id, AccountID: g.acct, Mode: domain.ModeDemo, Kind: domain.OrderOpen, Direction: domain.Long,
			Instrument: 1001, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000,
		}, at)
		if err != nil {
			t.Fatal(err)
		}
		o.State, o.Sends, o.SentAt = domain.OrderFilled, 1, at
		if err := g.store.Orders().Create(ctx, o); err != nil {
			t.Fatal(err)
		}
		if err := g.store.Decisions().Add(ctx, domain.Decision{
			ID: "d-" + id, AccountID: g.acct, At: at, Signal: domain.DecisionSignal{Instrument: 1001, Direction: domain.Long},
			Risk: domain.DecisionRisk{Allowed: true, Size: 100_00}, Outcome: domain.DecisionOrdered, OrderRequestID: id,
		}); err != nil {
			t.Fatal(err)
		}
		if err := g.store.AgentPositions().Add(ctx, domain.AgentPosition{
			AccountID: g.acct, PositionID: int64(9_000 + i), OpenRequestID: id, Instrument: 1001, Direction: domain.Long,
			OpenedAt: at, ClosedAt: at.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func armBody(demo, confirm, password string) string {
	return fmt.Sprintf(`{"demoAccountId":%q,"confirm":%q,"password":%q}`, demo, confirm, password)
}

// TestRealGateEndpoints (PLAN 7.14): the status, every refusal of the arm (CSRF 403, wrong
// password 403, wrong phrase 422, a failing check 409, and with every check passing 403
// "Real trading is not enabled in this build"), the disarm, and no password in any log.
func TestRealGateEndpoints(t *testing.T) {
	logs := &lockedBuffer{}
	g := newRealGateTC(t, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	csrf := g.csrfHdr()
	base := "/api/v1/accounts/" + g.real + "/real-gate"
	const pw = "a long password"

	g.seedEvidenceDays(t, 0, 19) // one day short (review minor 5: the step's name is what it tests)
	code, m, _ := g.do("GET", base, "")
	if code != 200 || m["enabled"] != false || m["armed"] != false || m["phrase"] != "ENABLE REAL TRADING Live" || m["canArm"] != true ||
		m["canDisarm"] != true || m["demoAccountId"] != g.acct || m["gate"].(map[string]any)["state"] != "locked" {
		t.Fatalf("status %d %v", code, m)
	}
	failing := map[string]bool{}
	for _, c := range m["checks"].([]any) {
		c := c.(map[string]any)
		if c["pass"] != true {
			failing[c["name"].(string)] = true
		}
	}
	if len(failing) != 1 || !failing["demo_evidence"] || m["evidence"].(map[string]any)["count"] != 19.0 {
		t.Fatalf("failing checks %v, evidence %v", failing, m["evidence"])
	}
	for _, s := range []agentStep{
		{"without CSRF", "POST", base + "/arm", armBody(g.acct, "ENABLE REAL TRADING Live", pw), nil, 403, ""},
		{"without a body", "POST", base + "/arm", "", csrf, 415, ""},
		{"an unknown field", "POST", base + "/arm", `{"demoAccountId":"x","confirm":"y","password":"z","mode":"real"}`, csrf, 400, ""},
		{"wrong password", "POST", base + "/arm", armBody(g.acct, "ENABLE REAL TRADING Live", "not it"), csrf, 403, "the password is not correct"},
		{"wrong phrase", "POST", base + "/arm", armBody(g.acct, "ENABLE REAL TRADING", pw), csrf, 422, ""},
		{"19 days: one short of the evidence", "POST", base + "/arm", armBody(g.acct, "ENABLE REAL TRADING Live", pw), csrf, 409, "Real trading cannot be armed"},
		{"a demo account has no gate", "GET", "/api/v1/accounts/" + g.acct + "/real-gate", "", nil, 422, ""},
		{"an unknown account", "GET", "/api/v1/accounts/nope/real-gate", "", nil, 404, ""},
		{"disarm without CSRF", "POST", base + "/disarm", `{}`, nil, 403, ""},
	} {
		code, m, _ := g.do(s.method, s.path, s.body, s.hdr...)
		if code != s.code || (s.state != "" && m["title"] != s.state && m["detail"] != s.state) {
			t.Fatalf("%s: %d %v", s.name, code, m)
		}
	}
	g.seedEvidenceDays(t, 19, 1) // the 20th day
	code, m, _ = g.do("POST", base+"/arm", armBody(g.acct, " ENABLE REAL TRADING Live ", pw), csrf...)
	if code != 403 || m["title"] != "Real trading is not enabled in this build" {
		t.Fatalf("every check passing: %d %v", code, m)
	}
	code, m, _ = g.do("GET", base, "")
	if code != 200 || m["armed"] != false || m["gate"].(map[string]any)["state"] != "locked" || m["evidence"].(map[string]any)["count"] != 20.0 {
		t.Fatalf("status after %d %v", code, m)
	}
	for _, c := range m["checks"].([]any) {
		if c.(map[string]any)["pass"] != true {
			t.Fatalf("check %v", c)
		}
	}
	code, m, _ = g.do("POST", base+"/disarm", `{"reason":"drill"}`, csrf...)
	if code != 200 || m["state"] != "locked" || m["version"] != 0.0 {
		t.Fatalf("disarm %d %v", code, m)
	}
	if code, m, _ := g.do("GET", "/api/v1/system/status", ""); code != 200 || m["realArmed"] == nil || len(m["realArmed"].([]any)) != 0 {
		t.Fatalf("system status %d %v", code, m)
	}
	if strings.Contains(logs.String(), pw) || strings.Contains(logs.String(), "not it") {
		t.Fatalf("a password reached the log:\n%s", logs.String())
	}
	if n, err := g.store.RealGates().Get(context.Background(), g.real); err == nil {
		t.Fatalf("a gate row was stored: %+v", n)
	}
}

// TestRealGateAPIKeys: an API key may disarm (OD23), never arm (OD22).
func TestRealGateAPIKeys(t *testing.T) {
	g := newRealGateTC(t, slog.New(slog.DiscardHandler))
	g.seedEvidence(t, 20)
	code, m, _ := g.do("POST", "/api/v1/api-keys", `{"name":"ops","role":"admin","expiresInDays":30}`, g.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	key := m["secret"].(string)
	base := "/api/v1/accounts/" + g.real + "/real-gate"
	if code, body := g.bearer("POST", base+"/arm", armBody(g.acct, "ENABLE REAL TRADING Live", "a long password"), key); code != 403 ||
		!strings.Contains(body, "web session") {
		t.Fatalf("arm with a key %d %s", code, body)
	}
	if code, body := g.bearer("GET", base, "", key); code != 200 || !strings.Contains(body, `"canArm":false`) || !strings.Contains(body, `"canDisarm":true`) {
		t.Fatalf("status with a key %d %s", code, body)
	}
	if code, body := g.bearer("POST", base+"/disarm", `{"reason":"from a script"}`, key); code != 200 || !strings.Contains(body, `"state":"locked"`) {
		t.Fatalf("disarm with a key %d %s", code, body)
	}
	// A viewer cannot disarm, nor arm.
	if code, m, _ := g.do("POST", "/api/v1/users", `{"username":"vi","password":"vi long password","role":"viewer"}`, g.csrfHdr()...); code != 201 {
		t.Fatalf("create viewer %d %v", code, m)
	}
	if code, m, _ := g.do("PUT", "/api/v1/accounts/"+g.real+"/shares", `{"username":"vi","access":"trade"}`, g.csrfHdr()...); code != 204 && code != 200 {
		t.Fatalf("share %d %v", code, m)
	}
	g.c.Jar, _ = cookiejar.New(nil)
	g.login("vi", "vi long password")
	if code, m, _ := g.do("POST", base+"/disarm", `{}`, g.csrfHdr()...); code != 403 {
		t.Fatalf("viewer disarm %d %v", code, m)
	}
	if code, m, _ := g.do("POST", base+"/arm", armBody(g.acct, "ENABLE REAL TRADING Live", "vi long password"), g.csrfHdr()...); code != 403 {
		t.Fatalf("viewer arm %d %v", code, m)
	}
	if code, m, _ := g.do("GET", base, ""); code != 200 || m["canArm"] != false || m["canDisarm"] != false {
		t.Fatalf("viewer status %d %v", code, m)
	}
}

// TestRealGateWrongPasswordsAreLimited: arming counts like a sign-in (D28): after 5 wrong
// passwords the next attempt is 429 before the hash.
func TestRealGateWrongPasswordsAreLimited(t *testing.T) {
	g := newRealGateTC(t, slog.New(slog.DiscardHandler))
	base := "/api/v1/accounts/" + g.real + "/real-gate/arm"
	for i := range 5 {
		if code, m, _ := g.do("POST", base, armBody(g.acct, "ENABLE REAL TRADING Live", "guess"), g.csrfHdr()...); code != 403 ||
			m["detail"] != "the password is not correct" {
			t.Fatalf("attempt %d: %d %v", i+1, code, m)
		}
	}
	if code, m, h := g.do("POST", base, armBody(g.acct, "ENABLE REAL TRADING Live", "a long password"), g.csrfHdr()...); code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("6th attempt %d %v", code, m)
	}
}

// TestRateLimitNeverThrottlesDisarm (§2.6, D32): with every bucket exhausted for the address,
// the user and the API key, disarming still works from the web UI and with an API key.
func TestRateLimitNeverThrottlesDisarm(t *testing.T) {
	g := newRealGateTC(t, slog.New(slog.DiscardHandler))
	code, m, _ := g.do("POST", "/api/v1/api-keys", `{"name":"ops","role":"admin","expiresInDays":30}`, g.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	key, keyID := m["secret"].(string), m["apiKey"].(map[string]any)["id"].(string)
	_, me, _ := g.do("GET", "/api/v1/auth/me", "")
	uid := me["user"].(map[string]any)["id"].(string)
	for _, c := range []rateClass{classRead, classMutation, classExport, classStream} {
		for _, id := range []string{"ip:127.0.0.1", "user:" + uid, "key:" + keyID} {
			for {
				if ok, _ := g.h.rates.Allow(c, id); !ok {
					break
				}
			}
		}
	}
	base := "/api/v1/accounts/" + g.real + "/real-gate"
	if code, _, _ := g.do("GET", base, ""); code != 429 {
		t.Fatalf("reads not exhausted: %d", code)
	}
	if code, m, _ := g.do("POST", base+"/disarm", `{"reason":"now"}`, g.csrfHdr()...); code != 200 {
		t.Fatalf("disarm %d %v", code, m)
	}
	if code, body := g.bearer("POST", base+"/disarm", `{}`, key); code != 200 {
		t.Fatalf("disarm with a key %d %s", code, body)
	}
}

// TestRealRefusalHTTP (PLAN 7.17, S11): with a gate stored armed on a reconciled real account
// with keys, real limits and its agent forced to running, the banner shows it, and the API
// still refuses to start, force close or show a start control; nothing reaches the broker.
func TestRealRefusalHTTP(t *testing.T) {
	t.Setenv("CACTUSTRADE_REAL_TRADING", "on") // OD28's opt-in (7.23): nothing reads it yet
	g := newRealGateTC(t, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	armed := domain.LockedGate(g.real).Arm(g.acct, "someone", domain.GateEvidence{Days: []domain.RiskDate{"2026-11-20"}, Count: 1, First: "2026-11-20", Last: "2026-11-20"}, now)
	if _, err := g.store.RealGates().Put(ctx, armed); err != nil {
		t.Fatal(err)
	}
	if err := g.store.Settings().Set(ctx, "agent.state."+g.real, "running", now); err != nil {
		t.Fatal(err)
	}
	code, m, _ := g.do("GET", "/api/v1/system/status", "")
	if ra, _ := m["realArmed"].([]any); code != 200 || len(ra) != 1 || ra[0].(map[string]any)["name"] != "Live" {
		t.Fatalf("status %d %v", code, m)
	}
	code, m, _ = g.do("GET", "/api/v1/accounts/"+g.real+"/real-gate", "")
	if code != 200 || m["armed"] != true {
		t.Fatalf("gate %d %v", code, m)
	}
	code, m, _ = g.do("GET", "/api/v1/agent?accountId="+g.real, "")
	if ctl, _ := m["controls"].(map[string]any); code != 200 || ctl["gated"] != true || ctl["start"] != false || ctl["forceClose"] != false {
		t.Fatalf("agent %d %v", code, m)
	}
	if err := g.store.Settings().Set(ctx, "agent.state."+g.real, "stopped", now); err != nil {
		t.Fatal(err)
	}
	for _, s := range []agentStep{
		{"start", "POST", "/api/v1/agent/start?accountId=" + g.real, "", g.csrfHdr(), 403, "Real trading needs the phase-7 gate"},
		{"force close", "POST", "/api/v1/agent/force-close?accountId=" + g.real, forceBody, g.csrfHdr(), 403, "Real trading needs the phase-7 gate"},
		{"force close preview", "GET", "/api/v1/agent/force-close?accountId=" + g.real, "", nil, 403, ""},
	} {
		code, m, _ := g.do(s.method, s.path, s.body, s.hdr...)
		if code != s.code || (s.state != "" && m["title"] != s.state) {
			t.Fatalf("%s: %d %v", s.name, code, m)
		}
	}
	if g.b.Opens+g.b.Closes+g.b.Cancels != 0 {
		t.Fatalf("the broker saw %d opens, %d closes, %d cancels", g.b.Opens, g.b.Closes, g.b.Cancels)
	}
	// Disarming it works and the banner goes.
	if code, m, _ := g.do("POST", "/api/v1/accounts/"+g.real+"/real-gate/disarm", `{"reason":"matrix"}`, g.csrfHdr()...); code != 200 || m["state"] != "locked" {
		t.Fatalf("disarm %d %v", code, m)
	}
	if code, m, _ := g.do("GET", "/api/v1/system/status", ""); code != 200 || len(m["realArmed"].([]any)) != 0 {
		t.Fatalf("status after %d %v", code, m)
	}
}

// TestRealBannerUnknownAndSwagger (review minor 1): an unreadable gate is reported as unknown
// in the status, and the Swagger UI page (served by the server, outside the React app)
// carries the same banner for a signed-in user: armed, unknown, or none.
func TestRealBannerUnknownAndSwagger(t *testing.T) {
	web := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"api-docs.html": {Data: []byte("<html><body><div id=\"swagger-ui\"></div></body></html>")},
	}
	g := newRealGateTCConfig(t, slog.New(slog.DiscardHandler), Config{Web: web})
	docs := func(c *http.Client) string {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), "GET", g.srv.URL+"/api/docs", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("docs %d %s", resp.StatusCode, b)
		}
		return string(b)
	}
	if body := docs(g.c); strings.Contains(body, "REAL MONEY") || strings.Contains(body, "unknown") {
		t.Fatalf("nothing armed: %s", body)
	}
	// Armed (stored directly: this build cannot arm), with a name that needs escaping.
	if code, m, _ := g.do("PATCH", "/api/v1/accounts/"+g.real, `{"name":"Live <b>&"}`, g.csrfHdr()...); code != 200 {
		t.Fatalf("rename %d %v", code, m)
	}
	ev := domain.GateEvidence{Days: []domain.RiskDate{"2026-11-20"}, Count: 1, First: "2026-11-20", Last: "2026-11-20"}
	if _, err := g.store.RealGates().Put(context.Background(), domain.LockedGate(g.real).Arm(g.acct, "u", ev, time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if body := docs(g.c); !strings.Contains(body, "REAL MONEY — real trading armed on: Live &lt;b&gt;&amp;") || strings.Contains(body, "<b>&") ||
		!strings.Contains(body, `<div id="swagger-ui">`) {
		t.Fatalf("armed: %s", body)
	}
	anon := &http.Client{Transport: g.c.Transport}
	if body := docs(anon); strings.Contains(body, "REAL MONEY") {
		t.Fatalf("an anonymous visitor got the banner: %s", body)
	}
	// Unreadable: unknown in the status and on the page.
	g.store.GatesErr = errors.New("database down")
	if code, m, _ := g.do("GET", "/api/v1/system/status", ""); code != 200 || m["realArmedUnknown"] != true || len(m["realArmed"].([]any)) != 0 {
		t.Fatalf("status with the gate unreadable %d %v", code, m)
	}
	if body := docs(g.c); !strings.Contains(body, "real-trading state unknown") {
		t.Fatalf("unknown: %s", body)
	}
	g.store.GatesErr = nil
}
