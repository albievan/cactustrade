package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// lockedBuffer collects log output from several goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ours records a position as cactusTrade's and puts it at the simulated broker; foreign puts
// one there that cactusTrade did not open.
func (x *agentTC) ours(t *testing.T, accountID string, id int64) {
	t.Helper()
	x.b.Positions = append(x.b.Positions, domain.Position{ID: id, Instrument: 1001, Amount: 100_00, OpenRate: 100_000_000, StopLossRate: 97_000_000, RealAsset: true, Long: true, Leverage: 1})
	if err := x.store.AgentPositions().Add(context.Background(), domain.AgentPosition{
		AccountID: accountID, PositionID: id, OpenRequestID: "open-" + accountID, Instrument: 1001, Direction: domain.Long,
	}); err != nil {
		t.Fatal(err)
	}
}

const forceBody = `{"password":"a long password"}`

func TestForceCloseEndpoint(t *testing.T) {
	logs := &lockedBuffer{}
	x := newAgentTCServer(t, func(*app.Deps) {}, Config{}, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	x.ready(t)
	csrf := x.csrfHdr()
	if code, m, _ := x.do("POST", "/api/v1/agent/start", "", csrf...); code != 200 {
		t.Fatalf("start %d %v", code, m)
	}
	x.ours(t, x.acct, 501)
	x.b.Positions = append(x.b.Positions, domain.Position{ID: 502, Instrument: 1002, Amount: 50_00, RealAsset: true, Long: true, Leverage: 1})

	code, m, _ := x.do("GET", "/api/v1/agent", "")
	if code != 200 || m["controls"].(map[string]any)["forceClose"] != true {
		t.Fatalf("agent %d %v", code, m)
	}
	if code, m, _ := x.do("GET", "/api/v1/agent/force-close?accountId="+x.acct, ""); code != 200 ||
		m["summary"] != "closes 1 position on Main demo" || m["positions"] != 1.0 {
		t.Fatalf("preview %d %v", code, m)
	}
	for _, s := range []agentStep{
		{"without CSRF", "POST", "/api/v1/agent/force-close", forceBody, nil, 403, ""},
		{"without a body", "POST", "/api/v1/agent/force-close", "", csrf, 415, ""},
		{"with an unknown field", "POST", "/api/v1/agent/force-close", `{"password":"x","confirm":"y"}`, csrf, 400, ""},
		{"wrong password", "POST", "/api/v1/agent/force-close", `{"password":"not it"}`, csrf, 403, ""},
	} {
		if code, m, _ := x.do(s.method, s.path, s.body, s.hdr...); code != s.code {
			t.Fatalf("%s: %d %v", s.name, code, m)
		}
	}
	if x.b.Closes != 0 {
		t.Fatalf("%d closes before the right password", x.b.Closes)
	}
	code, m, _ = x.do("POST", "/api/v1/agent/force-close", forceBody, csrf...)
	if code != 200 || m["closing"] != 1.0 || m["cancelled"] != 0.0 || m["state"] != "stopped" || m["name"] != "Main demo" {
		t.Fatalf("force close %d %v", code, m)
	}
	if x.b.Closes != 1 || len(x.b.Positions) != 1 || x.b.Positions[0].ID != 502 {
		t.Fatalf("closes %d, positions %+v", x.b.Closes, x.b.Positions)
	}
	// stopped, not killed: the limits change and the agent starts without a reset
	code, m, _ = x.do("GET", "/api/v1/agent", "")
	if code != 200 || m["state"] != "stopped" || m["killed"] != false {
		t.Fatalf("agent after %d %v", code, m)
	}
	x.ready(t)
	if code, m, _ := x.do("POST", "/api/v1/agent/start", "", csrf...); code != 200 {
		t.Fatalf("start again %d %v", code, m)
	}
	if strings.Contains(logs.String(), "a long password") || strings.Contains(logs.String(), "not it") {
		t.Fatalf("a password reached the log:\n%s", logs.String())
	}
}

func TestForceCloseWrongPasswordsAreLimited(t *testing.T) {
	x := newAgentTC(t)
	csrf := x.csrfHdr()
	for i := range 5 {
		code, m, _ := x.do("POST", "/api/v1/agent/force-close", `{"password":"guess"}`, csrf...)
		if code != 403 || m["detail"] != "the password is not correct" {
			t.Fatalf("attempt %d: %d %v", i+1, code, m)
		}
	}
	// counted like failed sign-ins: the next attempt is refused before the hash, even right
	if code, m, h := x.do("POST", "/api/v1/agent/force-close", forceBody, csrf...); code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("6th attempt %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/force-close-all", forceBody, csrf...); code != 429 {
		t.Fatalf("all accounts after 5 wrong %d %v", code, m)
	}
}

func TestForceCloseRefusalsDoNotCount(t *testing.T) {
	// refusals that are not a wrong password are refunded: six refusals of an unknown account
	// in a row do not lock the admin out
	x := newAgentTC(t)
	for range 6 {
		if code, m, _ := x.do("POST", "/api/v1/agent/force-close?accountId=nope", forceBody, x.csrfHdr()...); code != 404 {
			t.Fatalf("unknown account %d %v", code, m)
		}
	}
	// A real-money account is force-closable since PLAN 7.21 (exits always work): with
	// nothing of ours there, nothing is sent.
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Real","mode":"real"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create real %d %v", code, m)
	}
	realID := m["id"].(string)
	x.keys(t, realID)
	if code, m, _ := x.do("GET", "/api/v1/agent?accountId="+realID, ""); code != 200 ||
		m["controls"].(map[string]any)["forceClose"] != true || m["controls"].(map[string]any)["forceCloseBlockedReason"] != nil {
		t.Fatalf("real controls %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/force-close?accountId="+realID, forceBody, x.csrfHdr()...); code != 200 || m["closing"] != 0.0 {
		t.Fatalf("real %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/force-close", forceBody, x.csrfHdr()...); code != 200 {
		t.Fatalf("demo after the refusals %d %v", code, m)
	}
	if x.b.Closes+x.b.Cancels+x.b.Opens != 0 {
		t.Fatal("a broker call for nothing to close")
	}
}

func TestForceCloseRolesAndAPIKeys(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	code, m, _ := x.do("POST", "/api/v1/api-keys", `{"name":"ops","role":"admin","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, body := x.bearer("GET", "/api/v1/agent", "", key); code != 200 || !strings.Contains(body, `"forceClose":false`) {
		t.Fatalf("agent with a key %d %s", code, body)
	}
	for _, p := range []string{"/api/v1/agent/force-close", "/api/v1/agent/force-close-all"} {
		if code, body := x.bearer("POST", p, forceBody, key); code != 403 {
			t.Fatalf("%s with a key %d %s", p, code, body)
		}
		if code, body := x.bearer("GET", p, "", key); code != 403 {
			t.Fatalf("preview %s with a key %d %s", p, code, body)
		}
	}
	// an operator with trade access may kill, never force close
	if code, m, _ := x.do("POST", "/api/v1/users", `{"username":"op","password":"op long password","role":"operator"}`, x.csrfHdr()...); code != 201 {
		t.Fatalf("create operator %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/accounts/"+x.acct+"/shares", `{"username":"op","access":"trade"}`, x.csrfHdr()...); code != 204 && code != 200 {
		t.Fatalf("share %d %v", code, m)
	}
	x.c.Jar, _ = cookiejar.New(nil)
	x.login("op", "op long password")
	if code, m, _ := x.do("GET", "/api/v1/agent?accountId="+x.acct, ""); code != 200 || m["controls"].(map[string]any)["forceClose"] != false ||
		m["controls"].(map[string]any)["kill"] != true {
		t.Fatalf("operator controls %d %v", code, m)
	}
	for _, p := range []string{"/api/v1/agent/force-close?accountId=" + x.acct, "/api/v1/agent/force-close-all"} {
		if code, m, _ := x.do("POST", p, `{"password":"op long password"}`, x.csrfHdr()...); code != 403 {
			t.Fatalf("operator %s %d %v", p, code, m)
		}
	}
}

func TestForceCloseAllEndpoint(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Second","mode":"demo"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create %d %v", code, m)
	}
	second := m["id"].(string)
	x.keys(t, second)
	x.ours(t, x.acct, 601)
	x.ours(t, second, 602)
	var plans []map[string]any
	if code := x.getJSON("/api/v1/agent/force-close-all", &plans); code != 200 || len(plans) != 2 {
		t.Fatalf("preview %d %v", code, plans)
	}
	if code, _, _ := x.do("POST", "/api/v1/agent/force-close-all", `{"password":"wrong"}`, x.csrfHdr()...); code != 403 || x.b.Closes != 0 {
		t.Fatalf("wrong password %d, closes %d", code, x.b.Closes)
	}
	code, _, _ = x.do("POST", "/api/v1/agent/force-close-all", forceBody, x.csrfHdr()...)
	if code != 200 || x.b.Closes != 2 || len(x.b.Positions) != 0 {
		t.Fatalf("all %d, closes %d, positions %+v", code, x.b.Closes, x.b.Positions)
	}
}

func TestAgentsListAndWatchlistCounts(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Second","mode":"demo"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create %d %v", code, m)
	}
	in := domain.Instrument{ID: 1001, Symbol: "EXMP", Type: "Stocks", Market: domain.MarketNYSE}
	if err := x.store.Instruments().Upsert(context.Background(), in, domain.Eligibility{Instrument: 1001, RealLong: true}, x.svc.Now()); err != nil {
		t.Fatal(err)
	}
	if err := x.store.Watchlist().Add(context.Background(), m["id"].(string), 1001, "admin", x.svc.Now()); err != nil {
		t.Fatal(err)
	}
	var cards []map[string]any
	if code := x.getJSON("/api/v1/agents", &cards); code != 200 || len(cards) != 2 {
		t.Fatalf("agents %d %v", code, cards)
	}
	first := cards[0]
	if first["current"] != true || first["state"] != "stopped" || first["limitsEditable"] != true ||
		first["controls"].(map[string]any)["forceClose"] != true || cards[1]["current"] != false {
		t.Fatalf("cards %v", cards)
	}
	var counts []map[string]any
	if code := x.getJSON("/api/v1/watchlist/counts", &counts); code != 200 || len(counts) != 2 ||
		counts[0]["count"] != 0.0 || counts[1]["count"] != 1.0 || counts[1]["account"].(map[string]any)["name"] != "Second" {
		t.Fatalf("counts %d %v", code, counts)
	}
	if code := x.anon("GET", "/api/v1/agents", ""); code != 401 {
		t.Fatalf("anonymous agents %d", code)
	}
}
