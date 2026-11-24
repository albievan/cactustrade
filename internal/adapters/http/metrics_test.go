package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// errStopLoop ends SuperviseLoop after one tick (its sleep between ticks fails).
var errStopLoop = errors.New("stop the loop")

// tickOnce runs one supervisor loop tick (every pass included).
func tickOnce(t *testing.T, svc *app.Service) {
	t.Helper()
	svc.SuperviseLoop(context.Background())
}

// oneTickSleep is a Deps.Sleep that advances the fake clock, except the supervisor loop's
// own wait between ticks, which ends the loop.
func oneTickSleep(clk func(time.Duration)) func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		if d == app.SupervisorInterval {
			return errStopLoop
		}
		clk(d)
		return ctx.Err()
	}
}

// metricsEnv is an agent test server whose service also feeds a second handler with its own
// registry and the Observers, as cmd wires them.
type metricsEnv struct {
	*agentTC
	clk     *fakes.Clock
	bus     *fakes.Bus
	obs     *Observers
	metrics *httptest.Server
	token   string
}

// Secrets planted where a careless metric could pick them up.
const (
	plantedAPIKey  = "sk-etoro-api-0123456789abcdef0123456789abcdef"
	plantedUserKey = "eyJhbGciOiJIUzI1NiJ9.userkey.c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0"
	plantedToken   = "f00dfeedf00dfeedf00dfeedf00dfeedf00dfeedf00dfeedf00dfeedf00dfeed"
)

func newMetricsEnv(t *testing.T) *metricsEnv {
	t.Helper()
	var clk *fakes.Clock
	bus := &fakes.Bus{}
	x := newAgentTCServer(t, func(d *app.Deps) {
		clk = d.Clock.(*fakes.Clock)
		d.Sleep = oneTickSleep(clk.Advance)
		d.Bus = bus
	}, Config{MetricsToken: plantedToken}, slog.New(slog.DiscardHandler))
	a, err := x.store.Accounts().Get(context.Background(), x.acct)
	if err != nil {
		t.Fatal(err)
	}
	ak, uk := domain.AccountKeyNames(a)
	if err := x.sec.Put(context.Background(), ak, plantedAPIKey); err != nil {
		t.Fatal(err)
	}
	if err := x.sec.Put(context.Background(), uk, plantedUserKey); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	obs := NewObservers(reg)
	ctx, cancel := context.WithCancel(context.Background())
	done := obs.CountOrders(ctx, x.svc)
	t.Cleanup(func() { cancel(); <-done })
	srv := httptest.NewServer(New(x.svc, slog.New(slog.DiscardHandler), Config{MetricsToken: plantedToken, OpenAPI: []byte("openapi: 3.0.3\n")}, reg))
	t.Cleanup(srv.Close)
	return &metricsEnv{agentTC: x, clk: clk, bus: bus, obs: obs, metrics: srv, token: plantedToken}
}

func (m *metricsEnv) scrape(t *testing.T, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, m.metrics.URL+"/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// sample is one exposition line: the metric name, its labels and the value.
type sample struct {
	name   string
	labels map[string]string
	value  string
}

var sampleLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*)\})? (\S+)$`)

var labelPair = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)

func parseExposition(t *testing.T, body string) []sample {
	t.Helper()
	var out []sample
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		mm := sampleLine.FindStringSubmatch(line)
		if mm == nil {
			t.Fatalf("unparsable line %q", line)
		}
		s := sample{name: mm[1], labels: map[string]string{}, value: mm[3]}
		for _, p := range labelPair.FindAllStringSubmatch(mm[2], -1) {
			s.labels[p[1]] = p[2]
		}
		out = append(out, s)
	}
	return out
}

func find(ss []sample, name string, labels ...string) (sample, bool) {
	for _, s := range ss {
		if s.name != name {
			continue
		}
		match := true
		for i := 0; i+1 < len(labels); i += 2 {
			if s.labels[labels[i]] != labels[i+1] {
				match = false
			}
		}
		if match {
			return s, true
		}
	}
	return sample{}, false
}

// keyPattern matches what a key, token or password could look like in a label value: long
// hex or base64 runs, JWT-ish dotted tokens, and the prefixes our and the providers' keys
// carry. A UUID (dash-separated hex groups of ≤ 12) does not match.
var keyPattern = regexp.MustCompile(`(?i)[a-f0-9]{24,}|[A-Za-z0-9+/_]{32,}={0,2}|ey[A-Za-z0-9_-]{8,}\.|ctk_|sk-|bearer|password|secret`)

// allowedLabels are the label sets of the trading series (PLAN 7.5): an account id plus
// values from fixed sets.
var allowedLabels = map[string][]string{
	"cactustrade_trading_healthy":                              nil,
	"cactustrade_supervisor_last_tick_timestamp_seconds":       nil,
	"cactustrade_agent_loop_running":                           nil,
	"cactustrade_account_healthy":                              {"account"},
	"cactustrade_account_info":                                 {"account", "mode"},
	"cactustrade_account_supervised":                           {"account"},
	"cactustrade_agent_state":                                  {"account", "state"},
	"cactustrade_agent_killed":                                 {"account"},
	"cactustrade_account_reconciled":                           {"account"},
	"cactustrade_supervisor_last_pass_timestamp_seconds":       {"account"},
	"cactustrade_supervisor_last_clean_pass_timestamp_seconds": {"account"},
	"cactustrade_lease_held":                                   {"account"},
	"cactustrade_orders_non_final":                             {"account", "state"},
	"cactustrade_real_gate_armed":                              {"account"},
	"cactustrade_llm_calls_total":                              {"outcome", "purpose"},
	"cactustrade_etoro_requests_total":                         {"class", "mode", "op"},
	"cactustrade_etoro_limiter_waits_total":                    {"quota"},
	"cactustrade_etoro_limiter_wait_seconds_total":             {"quota"},
	"cactustrade_order_transitions_total":                      {"account", "kind", "state"},
}

// TestMetricsTradingSeries (PLAN 7.5): the trading series are present with their values,
// their labels are bounded (account ids and fixed sets only), and no label or value looks
// like a key or carries a planted secret.
func TestMetricsTradingSeries(t *testing.T) {
	m := newMetricsEnv(t)
	m.ready(t)
	real := ""
	if code, mm, _ := m.do("POST", "/api/v1/accounts", `{"name":"Real one","mode":"real"}`, m.csrfHdr()...); code == 201 {
		real = mm["id"].(string)
	} else {
		t.Fatalf("create real account %d %v", code, mm)
	}
	tickOnce(t, m.svc)
	// the counters cmd feeds from the eToro client and the bus
	m.obs.EtoroRequest("portfolio", domain.ModeDemo, "2xx")
	m.obs.EtoroRequest("open_order", domain.ModeDemo, "429")
	m.obs.EtoroRequest("portfolio", "", "error") // an unknown mode is demo, never a new label value
	m.obs.LimiterWait("portfolio", 1500*time.Millisecond)
	ord := domain.Order{RequestID: "4f9c2a8e-1b7d-4c3e-9a6f-2d8b0e5c7a19", AccountID: m.acct, Mode: domain.ModeDemo, Kind: domain.OrderOpen, State: domain.OrderSent}
	m.bus.Publish(ports.Event{Type: app.BusOrder, Data: ord})
	m.bus.Publish(ports.Event{Type: app.BusOrder, Data: domain.Order{AccountID: m.acct, Kind: "weird", State: domain.OrderSent}}) // ignored
	m.bus.Publish(ports.Event{Type: app.BusEvent, Data: domain.Event{Title: plantedAPIKey}})                                      // not an order
	var body string
	waitFor(t, func() bool {
		_, body = m.scrape(t, m.token)
		return strings.Contains(body, "cactustrade_order_transitions_total")
	})
	ss := parseExposition(t, body)

	want := []struct {
		name   string
		labels []string
		value  string
	}{
		{"cactustrade_trading_healthy", nil, "1"},
		{"cactustrade_agent_loop_running", nil, "1"},
		{"cactustrade_account_healthy", []string{"account", m.acct}, "1"},
		{"cactustrade_account_info", []string{"account", m.acct, "mode", "demo"}, "1"},
		{"cactustrade_account_info", []string{"account", real, "mode", "real"}, "1"},
		{"cactustrade_account_supervised", []string{"account", m.acct}, "1"},
		{"cactustrade_account_supervised", []string{"account", real}, "0"},
		{"cactustrade_agent_state", []string{"account", m.acct, "state", "stopped"}, "1"},
		{"cactustrade_agent_state", []string{"account", m.acct, "state", "running"}, "0"},
		{"cactustrade_agent_killed", []string{"account", m.acct}, "0"},
		{"cactustrade_account_reconciled", []string{"account", m.acct}, "1"},
		{"cactustrade_lease_held", []string{"account", m.acct}, "1"},
		{"cactustrade_orders_non_final", []string{"account", m.acct, "state", "pending"}, "0"},
		{"cactustrade_real_gate_armed", []string{"account", real}, "0"},
		{"cactustrade_etoro_requests_total", []string{"op", "portfolio", "class", "2xx", "mode", "demo"}, "1"},
		{"cactustrade_etoro_requests_total", []string{"op", "portfolio", "class", "error", "mode", "demo"}, "1"},
		{"cactustrade_etoro_requests_total", []string{"op", "open_order", "class", "429", "mode", "demo"}, "1"},
		{"cactustrade_etoro_limiter_waits_total", []string{"quota", "portfolio"}, "1"},
		{"cactustrade_etoro_limiter_wait_seconds_total", []string{"quota", "portfolio"}, "1.5"},
		{"cactustrade_order_transitions_total", []string{"account", m.acct, "kind", "open", "state", "sent"}, "1"},
	}
	for _, w := range want {
		s, ok := find(ss, w.name, w.labels...)
		if !ok || s.value != w.value {
			t.Errorf("%s%v = %q (present %v), want %s", w.name, w.labels, s.value, ok, w.value)
		}
	}
	for _, name := range []string{"cactustrade_supervisor_last_pass_timestamp_seconds", "cactustrade_supervisor_last_clean_pass_timestamp_seconds", "cactustrade_supervisor_last_tick_timestamp_seconds"} {
		s, ok := find(ss, name)
		if !ok || s.value == "0" {
			t.Errorf("%s = %+v", name, s)
		}
	}
	if s, ok := find(ss, "cactustrade_order_transitions_total", "kind", "weird"); ok {
		t.Errorf("an unknown kind became a label value: %+v", s)
	}

	accounts := []string{m.acct, real}
	for _, s := range ss {
		if !strings.HasPrefix(s.name, "cactustrade_") || strings.HasPrefix(s.name, "cactustrade_http_") || s.name == "cactustrade_live_streams" {
			continue
		}
		allowed, known := allowedLabels[s.name]
		if !known {
			t.Errorf("series %s is not in the bounded-label table", s.name)
			continue
		}
		var names []string
		for k := range s.labels {
			names = append(names, k)
		}
		slices.Sort(names)
		if !slices.Equal(names, allowed) {
			t.Errorf("%s has labels %v, want %v", s.name, names, allowed)
		}
		if id, ok := s.labels["account"]; ok && !slices.Contains(accounts, id) {
			t.Errorf("%s: account label %q is not an account id", s.name, id)
		}
	}
	for _, s := range ss {
		for k, v := range s.labels {
			if keyPattern.MatchString(v) {
				t.Errorf("%s: label %s=%q looks like a key", s.name, k, v)
			}
		}
		if keyPattern.MatchString(s.value) {
			t.Errorf("%s: value %q looks like a key", s.name, s.value)
		}
	}
	for _, secret := range []string{plantedAPIKey, plantedUserKey, plantedToken, "a long password", m.csrf} {
		if strings.Contains(body, secret) {
			t.Errorf("the exposition contains a secret (%d bytes)", len(secret))
		}
	}
}

// TestMetricsNeedToken (PLAN 7.1.14): /metrics answers 401 without the right token, and
// always when none is configured.
func TestMetricsNeedToken(t *testing.T) {
	m := newMetricsEnv(t)
	for _, tok := range []string{"", "wrong", m.token + "x"} {
		if code, _ := m.scrape(t, tok); code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", tok, code)
		}
	}
	if code, _ := m.scrape(t, m.token); code != http.StatusOK {
		t.Fatalf("right token: %d", code)
	}
	open := httptest.NewServer(New(m.svc, slog.New(slog.DiscardHandler), Config{}, prometheus.NewRegistry()))
	defer open.Close()
	for _, hdr := range []string{"", "Bearer ", "Bearer x"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, open.URL+"/metrics", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no token configured, %q: %d", hdr, resp.StatusCode)
		}
	}
}

// TestMetricsStalledSupervisorHealth (PLAN 7.5): GET /api/v1/system/health is 200 while the
// supervisor passes; a supervisor that has not passed over an account for over 60 s → 503
// naming the account (in the body and on /metrics); admin only.
func TestMetricsStalledSupervisorHealth(t *testing.T) {
	m := newMetricsEnv(t)
	m.ready(t)
	tickOnce(t, m.svc)
	code, body, _ := m.do("GET", "/api/v1/system/health", "")
	if code != 200 || body["ok"] != true {
		t.Fatalf("healthy: %d %v", code, body)
	}
	accts := body["accounts"].([]any)
	if a := accts[0].(map[string]any); a["accountId"] != m.acct || a["name"] != "Main demo" || a["ok"] != true || a["lastSupervisorPass"] == nil {
		t.Fatalf("account %v", a)
	}

	m.clk.Advance(app.SupervisorStale + time.Second) // nothing passes meanwhile
	code, body, _ = m.do("GET", "/api/v1/system/health", "")
	if code != http.StatusServiceUnavailable || body["ok"] != false {
		t.Fatalf("stalled: %d %v", code, body)
	}
	problems := body["problems"].([]any)
	if !slices.ContainsFunc(problems, func(p any) bool {
		s, _ := p.(string)
		return strings.Contains(s, "account "+m.acct+": the last supervisor pass was 1m1s ago")
	}) {
		t.Fatalf("problems %v do not name the account", problems)
	}
	if a := body["accounts"].([]any)[0].(map[string]any); a["ok"] != false || !strings.Contains(a["problem"].(string), "supervisor pass") {
		t.Fatalf("account %v", a)
	}
	_, exp := m.scrape(t, m.token)
	ss := parseExposition(t, exp)
	if s, _ := find(ss, "cactustrade_account_healthy", "account", m.acct); s.value != "0" {
		t.Fatalf("account_healthy %+v", s)
	}
	if s, _ := find(ss, "cactustrade_trading_healthy"); s.value != "0" {
		t.Fatalf("trading_healthy %+v", s)
	}

	tickOnce(t, m.svc) // the supervisor passes again
	if code, body, _ := m.do("GET", "/api/v1/system/health", ""); code != 200 {
		t.Fatalf("recovered: %d %v", code, body)
	}

	// admin only
	for _, role := range []string{"viewer", "operator"} {
		if code, mm, _ := m.do("POST", "/api/v1/users", `{"username":"u`+role+`","password":"a long password `+role+`","role":"`+role+`"}`, m.csrfHdr()...); code != 201 {
			t.Fatalf("create %s %d %v", role, code, mm)
		}
	}
	for _, role := range []string{"viewer", "operator"} {
		m.c.Jar, _ = cookiejar.New(nil)
		m.login("u"+role, "a long password "+role)
		if code, _, _ := m.do("GET", "/api/v1/system/health", ""); code != http.StatusForbidden {
			t.Fatalf("%s: %d", role, code)
		}
	}
	if code := m.anon("GET", "/api/v1/system/health", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
}
