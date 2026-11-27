package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// sseMsg is one server-sent message (event "ping" for a comment line).
type sseMsg struct {
	event string
	data  string
}

// sseStream is an open live stream read in the background.
type sseStream struct {
	t      *testing.T
	resp   *http.Response
	msgs   chan sseMsg
	cancel context.CancelFunc
}

// openSSE opens path with client c (extra headers in hdr) and reads its messages.
func openSSE(t *testing.T, c *http.Client, url string, hdr ...string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	s := &sseStream{t: t, resp: resp, msgs: make(chan sseMsg, 1024), cancel: cancel}
	if resp.StatusCode != http.StatusOK {
		return s
	}
	go func() {
		defer close(s.msgs)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		var m sseMsg
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == ": ping":
				s.msgs <- sseMsg{event: "ping"}
			case strings.HasPrefix(line, "event: "):
				m.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				m.data = strings.TrimPrefix(line, "data: ")
			case line == "" && m.event != "":
				s.msgs <- m
				m = sseMsg{}
			}
		}
	}()
	return s
}

func (s *sseStream) close() {
	s.cancel()
	_ = s.resp.Body.Close()
}

// next returns the next message other than a ping (ok false: the stream ended or timed out).
func (s *sseStream) next(timeout time.Duration) (sseMsg, bool) {
	s.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m, open := <-s.msgs:
			if !open {
				return sseMsg{}, false
			}
			if m.event == "ping" {
				continue
			}
			return m, true
		case <-deadline:
			return sseMsg{}, false
		}
	}
}

// until returns every message up to and including the first that matches (fails when the
// stream ends or times out first).
func (s *sseStream) until(timeout time.Duration, match func(sseMsg) bool) []sseMsg {
	s.t.Helper()
	var seen []sseMsg
	deadline := time.Now().Add(timeout)
	for {
		m, ok := s.next(time.Until(deadline))
		if !ok {
			s.t.Fatalf("no matching message; got %v", seen)
		}
		seen = append(seen, m)
		if match(m) {
			return seen
		}
	}
}

func isEvent(name string) func(sseMsg) bool { return func(m sseMsg) bool { return m.event == name } }

func decodeData[T any](t *testing.T, m sseMsg) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(m.data), &v); err != nil {
		t.Fatalf("%s data %q: %v", m.event, m.data, err)
	}
	return v
}

// liveTC is the agent test server with a recording bus and the given stream limits.
type liveTC struct {
	*agentTC
	bus   *fakes.Bus
	admin app.Principal
	logs  *syncBuffer
}

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func newLiveTC(t *testing.T, lim LiveLimits) *liveTC {
	t.Helper()
	bus := &fakes.Bus{}
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	x := newAgentTCServer(t, func(d *app.Deps) { d.Bus = bus }, Config{Live: lim, MetricsToken: "mt"}, log)
	u, err := x.store.Users().GetByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	return &liveTC{agentTC: x, bus: bus, admin: app.Principal{UserID: u.ID, Username: u.Username, Role: u.Role, Via: app.ViaSession}, logs: logs}
}

func (x *liveTC) url(account string) string { return x.srv.URL + "/api/v1/live?accountId=" + account }

// user creates a user with role and returns a signed-in client (its CSRF token in the
// client's jar).
func (x *liveTC) user(t *testing.T, name string, role domain.Role) *http.Client {
	c, _ := x.userCSRF(t, name, role)
	return c
}

// userCSRF is user with the session's CSRF token.
func (x *liveTC) userCSRF(t *testing.T, name string, role domain.Role) (*http.Client, string) {
	t.Helper()
	if _, err := x.svc.CreateUser(context.Background(), x.admin, name, "a-long-password-1", role); err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: x.c.Transport}
	c.Jar, _ = cookiejar.New(nil)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, x.srv.URL+"/api/v1/auth/login",
		strings.NewReader(`{"username":"`+name+`","password":"a-long-password-1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m struct {
		CsrfToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s: %d %v", name, resp.StatusCode, err)
	}
	return c, m.CsrfToken
}

// account creates another demo account of the admin with keys.
func (x *liveTC) account(t *testing.T, name string) string {
	t.Helper()
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"`+name+`","mode":"demo"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create account %d %v", code, m)
	}
	id := m["id"].(string)
	x.keys(t, id)
	return id
}

func TestLiveStreamSnapshotEventsAndHeaders(t *testing.T) {
	x := newLiveTC(t, LiveLimits{Snapshot: time.Hour, Ping: 50 * time.Millisecond})
	s := openSSE(t, x.c, x.url(x.acct))
	if s.resp.StatusCode != 200 {
		t.Fatalf("status %d", s.resp.StatusCode)
	}
	h := s.resp.Header
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-store" || h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers %v", h)
	}
	m, ok := s.next(5 * time.Second)
	if !ok || m.event != "snapshot" {
		t.Fatalf("first message %v %v", m, ok)
	}
	snap := decodeData[LiveSnapshot](t, m)
	// WarmLiveView read the portfolio on connect: the figures are there and fresh
	if snap.Account.Id != x.acct || !snap.PortfolioKnown || snap.Stale || snap.Figures == nil || snap.Figures.EquityCents != 1_000_000 {
		t.Fatalf("snapshot %+v", snap)
	}
	// a ping arrives as a comment line
	if !waitPing(s, 5*time.Second) {
		t.Fatal("no ping")
	}

	// the kill switch works while the stream is open, and the stream shows it
	if code, m, _ := x.do("POST", "/api/v1/agent/kill?accountId="+x.acct, "", x.csrfHdr()...); code != 200 {
		t.Fatalf("kill %d %v", code, m)
	}
	seen := s.until(5*time.Second, func(m sseMsg) bool {
		return m.event == "snapshot" && decodeData[LiveSnapshot](t, m).Killed
	})
	var killed bool
	for _, m := range seen {
		if m.event == "event" {
			ev := decodeData[LiveEvent](t, m)
			killed = killed || (ev.AccountId == x.acct && strings.Contains(ev.Event.Kind, "kill"))
		}
	}
	if !killed {
		t.Fatalf("no kill event before the snapshot: %v", seen)
	}

	// an order of the account is forwarded with its symbol from the snapshot
	x.bus.Publish(ports.Event{Type: app.BusOrder, Data: domain.Order{RequestID: "req-1", AccountID: x.acct, Kind: domain.OrderOpen, State: domain.OrderSent}})
	seen = s.until(5*time.Second, isEvent("order"))
	if o := decodeData[LiveOrder](t, seen[len(seen)-1]); o.AccountId != x.acct || o.Order.RequestId != "req-1" || o.Order.State != "sent" {
		t.Fatalf("order %+v", o)
	}
}

func waitPing(s *sseStream, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case m, open := <-s.msgs:
			if !open {
				return false
			}
			if m.event == "ping" {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// TestLiveStreamViewerOfANeverGetsB: a viewer of account A gets A's events and system-wide
// ones, never B's, and cannot open B's stream.
func TestLiveStreamViewerOfANeverGetsB(t *testing.T) {
	x := newLiveTC(t, LiveLimits{Snapshot: time.Hour})
	b := x.account(t, "Other demo")
	viewer := x.user(t, "viewer1", domain.RoleViewer)
	if err := x.svc.ShareAccount(context.Background(), x.admin, x.acct, "viewer1", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	if s := openSSE(t, viewer, x.url(b)); s.resp.StatusCode != http.StatusNotFound {
		t.Fatalf("B's stream: %d", s.resp.StatusCode)
	}
	s := openSSE(t, viewer, x.url(x.acct))
	if m, ok := s.next(5 * time.Second); !ok || m.event != "snapshot" {
		t.Fatalf("first %v", m)
	}
	// B: a kill through the API, an order and an event straight on the bus
	if code, m, _ := x.do("POST", "/api/v1/agent/kill?accountId="+b, "", x.csrfHdr()...); code != 200 {
		t.Fatalf("kill B %d %v", code, m)
	}
	x.bus.Publish(ports.Event{Type: app.BusOrder, Data: domain.Order{RequestID: "b-order", AccountID: b}})
	x.bus.Publish(ports.Event{Type: app.BusEvent, Data: domain.Event{ID: "b-event", AccountID: b, Kind: "x", Title: "B"}})
	x.bus.Publish(ports.Event{Type: app.BusPortfolio, Data: app.PortfolioRead{AccountID: b}})
	// a system-wide event, then A's marker
	x.bus.Publish(ports.Event{Type: app.BusEvent, Data: domain.Event{ID: "sys", Kind: "system.reloaded", Title: "S"}})
	x.bus.Publish(ports.Event{Type: app.BusEvent, Data: domain.Event{ID: "a-marker", AccountID: x.acct, Kind: "x", Title: "A"}})
	seen := s.until(5*time.Second, func(m sseMsg) bool { return m.event == "event" && strings.Contains(m.data, "a-marker") })
	var sys bool
	for _, m := range seen {
		if strings.Contains(m.data, b) || strings.Contains(m.data, "b-order") || strings.Contains(m.data, "b-event") {
			t.Fatalf("B leaked to A's viewer: %v", m)
		}
		sys = sys || strings.Contains(m.data, `"id":"sys"`)
	}
	if !sys {
		t.Fatalf("no system-wide event: %v", seen)
	}
}

// TestLiveStreamTicksAndDecisions (R26): a viewer of A gets A's tick summaries and
// decisions as "tick" and "decision" messages, never B's.
func TestLiveStreamTicksAndDecisions(t *testing.T) {
	x := newLiveTC(t, LiveLimits{Snapshot: time.Hour})
	b := x.account(t, "Other demo")
	viewer := x.user(t, "viewer1", domain.RoleViewer)
	if err := x.svc.ShareAccount(context.Background(), x.admin, x.acct, "viewer1", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	s := openSSE(t, viewer, x.url(x.acct))
	if m, ok := s.next(5 * time.Second); !ok || m.event != "snapshot" {
		t.Fatalf("first %v", m)
	}
	at := time.Date(2026, 11, 24, 15, 0, 5, 0, time.UTC)
	tick := func(acct string) app.TickSummary {
		return app.TickSummary{
			AccountID: acct, At: at, Checked: 3, Ideas: 1, Orders: 1,
			Reasons: map[app.TraceStep]int{app.StepSession: 1, app.StepSignal: 1},
			Shares: []app.TickShare{
				{Instrument: 101, Symbol: "AAPL", Entered: true, Detail: "long $480"},
				{Instrument: 104, Symbol: "MSFT", Step: app.StepSignal, Detail: "no signal: flat"},
			},
		}
	}
	decision := func(acct, id string) app.DecisionView {
		return app.DecisionView{Decision: domain.Decision{
			ID: id, AccountID: acct, At: at, Outcome: domain.DecisionOrdered, OrderRequestID: "req-" + id,
			Signal:  domain.DecisionSignal{Instrument: 101, Symbol: "AAPL", Direction: domain.Long, Playbook: "trend_breakout"},
			Verdict: domain.DecisionVerdict{Kind: domain.VerdictApprove, SizeBp: domain.FullSize},
			Risk:    domain.DecisionRisk{Allowed: true, Size: 480_00},
		}, OrderState: domain.OrderFilled}
	}
	x.bus.Publish(ports.Event{Type: app.BusTick, Data: tick(b)})
	x.bus.Publish(ports.Event{Type: app.BusDecision, Data: decision(b, "b-dec")})
	x.bus.Publish(ports.Event{Type: app.BusTick, Data: tick(x.acct)})
	x.bus.Publish(ports.Event{Type: app.BusDecision, Data: decision(x.acct, "a-dec")})
	seen := s.until(5*time.Second, isEvent("decision"))
	var ticks []LiveTick
	for _, m := range seen {
		if strings.Contains(m.data, b) || strings.Contains(m.data, "b-dec") {
			t.Fatalf("B leaked to A's viewer: %v", m)
		}
		if m.event == "tick" {
			ticks = append(ticks, decodeData[LiveTick](t, m))
		}
	}
	if len(ticks) != 1 {
		t.Fatalf("ticks %+v", ticks)
	}
	if tk := ticks[0]; tk.AccountId != x.acct || !tk.At.Equal(at) || tk.Checked != 3 || tk.Ideas != 1 || tk.Orders != 1 ||
		tk.Reasons["session"] != 1 || tk.Reasons["signal"] != 1 || len(tk.Shares) != 2 ||
		tk.Shares[0] != (LiveTickShare{InstrumentId: 101, Symbol: "AAPL", Entered: true, Detail: "long $480"}) ||
		tk.Shares[1] != (LiveTickShare{InstrumentId: 104, Symbol: "MSFT", Step: "signal", Detail: "no signal: flat"}) {
		t.Fatalf("tick %+v", tk)
	}
	d := decodeData[LiveDecision](t, last(seen))
	if d.AccountId != x.acct || d.Decision.Id != "a-dec" || d.Decision.Outcome != "ordered" || d.Decision.OrderRequestId != "req-a-dec" ||
		d.Decision.OrderState != "filled" || d.Decision.Verdict != "approve" || !d.Decision.RiskAllowed || d.Decision.RiskSizeCents != 480_00 ||
		d.Decision.Playbook != "trend_breakout" || d.Decision.Symbol != "AAPL" {
		t.Fatalf("decision %+v", d)
	}
}

func TestLiveStreamAccessErrors(t *testing.T) {
	x := newLiveTC(t, LiveLimits{})
	anon := &http.Client{Transport: x.c.Transport}
	tests := []struct {
		name string
		c    *http.Client
		url  string
		hdr  []string
		code int
	}{
		{"signed out", anon, x.url(x.acct), nil, http.StatusUnauthorized},
		{"bad API key", anon, x.url(x.acct), []string{"Authorization", "Bearer ctk_nope_nope"}, http.StatusUnauthorized},
		{"unknown account", x.c, x.url("nope"), nil, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openSSE(t, tt.c, tt.url, tt.hdr...)
			if s.resp.StatusCode != tt.code || !strings.HasPrefix(s.resp.Header.Get("Content-Type"), "application/problem+json") {
				t.Fatalf("status %d %s", s.resp.StatusCode, s.resp.Header.Get("Content-Type"))
			}
		})
	}
}

// TestLiveStreamAPIKeyAndRevocation: a viewer API key opens a stream; revoking it ends the
// stream with bye within one recheck. Nothing secret is logged.
func TestLiveStreamAPIKeyAndRevocation(t *testing.T) {
	x := newLiveTC(t, LiveLimits{Recheck: 100 * time.Millisecond, Snapshot: time.Hour})
	ctx := context.Background()
	k, err := x.svc.CreateAPIKey(ctx, x.admin, "dashboard", domain.RoleViewer, 0)
	if err != nil {
		t.Fatal(err)
	}
	anon := &http.Client{Transport: x.c.Transport}
	s := openSSE(t, anon, x.url(x.acct), "Authorization", "Bearer "+k.Secret)
	if m, ok := s.next(5 * time.Second); !ok || m.event != "snapshot" {
		t.Fatalf("API key stream: %d %v", s.resp.StatusCode, m)
	}
	if err := x.svc.RevokeAPIKey(ctx, x.admin, k.Key.ID); err != nil {
		t.Fatal(err)
	}
	seen := s.until(5*time.Second, isEvent("bye"))
	if n := decodeData[LiveNotice](t, seen[len(seen)-1]); n.Reason != liveSignedOut {
		t.Fatalf("bye %+v", n)
	}
	if _, ok := s.next(2 * time.Second); ok {
		t.Fatal("the stream went on after bye")
	}
	_, secret, _ := strings.Cut(strings.TrimPrefix(k.Secret, app.APIKeyPrefix), "_")
	logs := x.logs.String()
	if strings.Contains(logs, secret) || strings.Contains(logs, "Bearer") {
		t.Fatalf("the API key reached the log: %s", logs)
	}
	if !strings.Contains(logs, `"path":"/api/v1/live"`) {
		t.Fatalf("the stream was not logged: %s", logs)
	}
}

// TestLiveStreamSessionRecheck: signing out, a password change (sessions end) or the share
// being removed ends the stream with bye within one recheck; no cookie is logged.
func TestLiveStreamSessionRecheck(t *testing.T) {
	tests := []struct {
		name   string
		end    func(t *testing.T, x *liveTC, viewer *http.Client, csrf, viewerID string)
		reason string
	}{
		{"signed out", func(t *testing.T, x *liveTC, viewer *http.Client, csrf, _ string) {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, x.srv.URL+"/api/v1/auth/logout", nil)
			req.Header.Set("X-CSRF-Token", csrf)
			resp, err := viewer.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("logout %d", resp.StatusCode)
			}
		}, liveSignedOut},
		{"password changed by an admin", func(t *testing.T, x *liveTC, _ *http.Client, _, id string) {
			pw := "another-long-password-2"
			if _, err := x.svc.UpdateUser(context.Background(), x.admin, id, app.UserUpdate{Password: &pw}); err != nil {
				t.Fatal(err)
			}
		}, liveSignedOut},
		{"share removed", func(t *testing.T, x *liveTC, _ *http.Client, _, id string) {
			if err := x.svc.UnshareAccount(context.Background(), x.admin, x.acct, id); err != nil {
				t.Fatal(err)
			}
		}, liveNoAccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newLiveTC(t, LiveLimits{Recheck: 100 * time.Millisecond, Snapshot: time.Hour})
			viewer, csrf := x.userCSRF(t, "viewer1", domain.RoleViewer)
			if err := x.svc.ShareAccount(context.Background(), x.admin, x.acct, "viewer1", domain.AccessView); err != nil {
				t.Fatal(err)
			}
			s := openSSE(t, viewer, x.url(x.acct))
			if m, ok := s.next(5 * time.Second); !ok || m.event != "snapshot" {
				t.Fatalf("first %d %v", s.resp.StatusCode, m)
			}
			cookies := viewer.Jar.Cookies(s.resp.Request.URL)
			tt.end(t, x, viewer, csrf, userID(t, x, "viewer1"))
			seen := s.until(5*time.Second, isEvent("bye"))
			if n := decodeData[LiveNotice](t, seen[len(seen)-1]); n.Reason != tt.reason {
				t.Fatalf("bye %+v", n)
			}
			for _, c := range cookies {
				if c.Value != "" && strings.Contains(x.logs.String(), c.Value) {
					t.Fatal("the session cookie reached the log")
				}
			}
			if strings.Contains(x.logs.String(), SessionCookie) {
				t.Fatal("the cookie name reached the log")
			}
		})
	}
}

func userID(t *testing.T, x *liveTC, name string) string {
	t.Helper()
	u, err := x.store.Users().GetByUsername(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// TestLiveStreamCaps: 4 streams per user, the 5th is 429 until one closes; the server-wide
// cap applies across users; the gauge counts open streams.
func TestLiveStreamCaps(t *testing.T) {
	x := newLiveTC(t, LiveLimits{Total: 6, Snapshot: time.Hour})
	var open []*sseStream
	for i := range 4 {
		s := openSSE(t, x.c, x.url(x.acct))
		if s.resp.StatusCode != 200 {
			t.Fatalf("stream %d: %d", i+1, s.resp.StatusCode)
		}
		open = append(open, s)
	}
	s := openSSE(t, x.c, x.url(x.acct))
	if s.resp.StatusCode != http.StatusTooManyRequests || s.resp.Header.Get("Retry-After") == "" {
		t.Fatalf("5th stream: %d", s.resp.StatusCode)
	}
	if g := gauge(t, x); g != 4 {
		t.Fatalf("gauge %v", g)
	}
	open[0].close()
	waitFor(t, func() bool { return gauge(t, x) == 3 })
	if s := openSSE(t, x.c, x.url(x.acct)); s.resp.StatusCode != 200 {
		t.Fatalf("after one closed: %d", s.resp.StatusCode)
	}
	// server-wide: 4 of the admin + 2 of another user = 6; the 7th is refused for anyone
	if err := x.svc.ShareAccount(context.Background(), x.admin, x.acct, "viewer2", domain.AccessView); err == nil {
		t.Fatal("shared with a user that does not exist")
	}
	other := x.user(t, "viewer2", domain.RoleViewer)
	if err := x.svc.ShareAccount(context.Background(), x.admin, x.acct, "viewer2", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if s := openSSE(t, other, x.url(x.acct)); s.resp.StatusCode != 200 {
			t.Fatalf("other user's stream %d: %d", i+1, s.resp.StatusCode)
		}
	}
	if s := openSSE(t, other, x.url(x.acct)); s.resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over the server cap: %d", s.resp.StatusCode)
	}
}

// gauge reads cactustrade_live_streams from /metrics (and checks the histogram skips the
// stream's route).
func gauge(t *testing.T, x *liveTC) float64 {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, x.srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer mt")
	resp, err := x.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	var v float64 = -1
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "request_duration_seconds") && strings.Contains(line, `route="GET /api/v1/live"`) {
			t.Fatalf("the stream is in the latency histogram: %s", line)
		}
		if rest, ok := strings.CutPrefix(line, "cactustrade_live_streams "); ok {
			if err := json.Unmarshal([]byte(rest), &v); err != nil {
				t.Fatal(err)
			}
		}
	}
	return v
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveStreamLifetimeAndShutdown(t *testing.T) {
	x := newLiveTC(t, LiveLimits{Lifetime: 300 * time.Millisecond, Snapshot: time.Hour})
	s := openSSE(t, x.c, x.url(x.acct))
	if n := decodeData[LiveNotice](t, last(s.until(5*time.Second, isEvent("bye")))); n.Reason != liveLifetime {
		t.Fatalf("bye %+v", n)
	}

	y := newLiveTC(t, LiveLimits{Snapshot: time.Hour})
	streams := []*sseStream{openSSE(t, y.c, y.url(y.acct)), openSSE(t, y.c, y.url(y.acct))}
	for _, s := range streams {
		if m, ok := s.next(5 * time.Second); !ok || m.event != "snapshot" {
			t.Fatalf("first %v", m)
		}
	}
	y.h.CloseStreams()
	y.h.CloseStreams() // idempotent
	for _, s := range streams {
		if n := decodeData[LiveNotice](t, last(s.until(5*time.Second, isEvent("bye")))); n.Reason != liveShutdown {
			t.Fatalf("bye %+v", n)
		}
	}
	if s := openSSE(t, y.c, y.url(y.acct)); s.resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after shutdown: %d", s.resp.StatusCode)
	}
	waitFor(t, func() bool { return gauge(t, y) == 0 })
}

func last(ms []sseMsg) sseMsg { return ms[len(ms)-1] }

// flushWriter is a ResponseWriter that records what is written (no deadlines: the stream
// carries on without them).
type flushWriter struct {
	mu  sync.Mutex
	h   http.Header
	buf bytes.Buffer
}

func (w *flushWriter) Header() http.Header { return w.h }
func (w *flushWriter) WriteHeader(int)     {}
func (w *flushWriter) Flush()              {}
func (w *flushWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(b)
}

func (w *flushWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestLiveStreamResync: a stream whose bus buffer was full (an event may have been dropped
// for it) says resync, drops what is buffered and sends a fresh snapshot; a closed bus ends
// the stream.
func TestLiveStreamResync(t *testing.T) {
	x := newLiveTC(t, LiveLimits{Snapshot: time.Hour})
	tests := []struct {
		name    string
		fill    int
		close   bool
		resync  bool
		forward int
	}{
		{"full buffer", 4, false, true, 1},
		{"room left", 2, false, false, 2},
		{"bus closed", 0, true, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &flushWriter{h: http.Header{}}
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, x.url(x.acct), nil)
			st := &liveStream{
				s: x.h, w: w, r: req, rc: http.NewResponseController(w), p: x.admin, account: x.acct,
				filter: map[string]bool{x.acct: true}, symbols: map[int64]liveTag{},
			}
			events := make(chan ports.Event, 4)
			for i := range tt.fill {
				events <- ports.Event{Type: app.BusOrder, Data: domain.Order{RequestID: "r" + string(rune('a'+i)), AccountID: x.acct}}
			}
			if tt.close {
				close(events)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { st.loop(ctx, events); close(done) }()
			if tt.close {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("a closed bus did not end the stream")
				}
				cancel()
				return
			}
			want := "event: order"
			if tt.resync {
				want = "event: snapshot"
			}
			waitFor(t, func() bool {
				return strings.Count(w.String(), "event: order") >= tt.forward && strings.Contains(w.String(), want)
			})
			cancel()
			<-done
			out := w.String()
			if got := strings.Count(out, "event: order"); got != tt.forward {
				t.Fatalf("%d orders forwarded, want %d:\n%s", got, tt.forward, out)
			}
			if strings.Contains(out, "event: resync") != tt.resync {
				t.Fatalf("resync %v:\n%s", !tt.resync, out)
			}
			if tt.resync && strings.Index(out, "event: resync") > strings.Index(out, "event: snapshot") {
				t.Fatalf("the snapshot must follow the resync:\n%s", out)
			}
		})
	}
}
