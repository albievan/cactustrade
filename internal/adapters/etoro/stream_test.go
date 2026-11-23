package etoro

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/albievan/cactustrade/internal/domain"
)

// fakeWS is a scripted connection: the test pushes frames in and reads what was sent.
type fakeWS struct {
	in      chan []byte
	mu      sync.Mutex
	sent    []map[string]any
	pingErr error
	closed  bool
}

func newFakeWS() *fakeWS { return &fakeWS{in: make(chan []byte, 16)} }

func (f *fakeWS) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case <-ctx.Done():
		return false, nil, ctx.Err()
	case b, ok := <-f.in:
		if !ok {
			return false, nil, errors.New("closed by peer")
		}
		if len(b) == 1 && b[0] == 0 {
			return false, b, nil // a binary heartbeat frame
		}
		return true, b, nil
	}
}

func (f *fakeWS) Write(_ context.Context, b []byte) error {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	return nil
}

func (f *fakeWS) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeWS) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeWS) sentOps() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func ratePush(id, ask, bid string) []byte {
	content, _ := json.Marshal(map[string]string{"Ask": ask, "Bid": bid, "LastExecution": ask, "Date": "2026-11-23T15:00:00.1234567Z"})
	b, _ := json.Marshal(map[string]any{"messages": []map[string]any{{"topic": "instrument:" + id, "content": string(content), "id": "x", "type": "Trading.Instrument.Rate"}}})
	return b
}

func newStreamEnv(t *testing.T) (*env, *Stream, chan *fakeWS) {
	t.Helper()
	e := newEnv(t)
	e.c.cfg.Sleep = func(ctx context.Context, d time.Duration) error { // real (short) sleeps for the loop
		return sleepCtx(ctx, min(d, 5*time.Millisecond))
	}
	e.c.cfg.Now = time.Now
	conns := make(chan *fakeWS, 4)
	s := NewStream(e.c, func(context.Context) (domain.Account, bool) { return e.account(), true })
	s.PingEvery = 20 * time.Millisecond
	s.dial = func(context.Context, string) (wsConn, error) {
		f := newFakeWS()
		conns <- f
		return f, nil
	}
	return e, s, conns
}

func TestStreamAuthenticatesSubscribesAndCaches(t *testing.T) {
	_, s, conns := newStreamEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Watch(1001, 2002, 1001, 0)
	go s.Run(ctx)
	ws := <-conns
	waitFor(t, "authenticate", func() bool { return len(ws.sentOps()) == 1 })
	auth := ws.sentOps()[0]
	data := auth["data"].(map[string]any)
	if auth["operation"] != "Authenticate" || data["apiKey"] != "demo-api" || data["userKey"] != "demo-user" || auth["id"] == "" {
		t.Fatalf("authenticate = %v", auth)
	}
	ws.in <- []byte{0} // heartbeat: skipped
	ws.in <- []byte(`{"id":"x","operation":"Authenticate","success":true}`)
	waitFor(t, "subscribe", func() bool { return len(ws.sentOps()) == 2 })
	sub := ws.sentOps()[1]
	sd := sub["data"].(map[string]any)
	if sub["operation"] != "Subscribe" || sd["snapshot"] != true || strings.Join(toStrings(sd["topics"]), ",") != "instrument:1001,instrument:2002" {
		t.Fatalf("subscribe = %v", sub)
	}
	if ok, _ := s.Status(); !ok {
		t.Fatal("not connected")
	}
	ws.in <- ratePush("1001", "189.2", "189.1")
	ws.in <- []byte(`{"messages":[{"topic":"instrument:2002","content":"{\"Ask\":215.9,\"Bid\":215.5,\"Date\":\"2026-11-23T15:00:01Z\"}"}]}`)
	ws.in <- []byte(`{"messages":[{"topic":"instrument:3003","content":"{\"Ask\":\"abc\"}"},{"topic":"private:demo","content":"{}"}]}`)
	ws.in <- []byte(`not json`)
	ws.in <- []byte(`{"operation":"Subscribe","success":false,"errorCode":"Forbidden"}`)
	waitFor(t, "quotes", func() bool { _, _, ok := s.Latest(2002); return ok })
	q, _, _ := s.Latest(1001)
	if q.Bid != 189100000 || q.Ask != 189200000 || q.At.IsZero() {
		t.Fatalf("quote = %+v", q)
	}
	if _, _, ok := s.Latest(3003); ok {
		t.Fatal("a bad push was cached")
	}
	// a new instrument is subscribed on the open connection
	s.Watch(4004)
	waitFor(t, "second subscribe", func() bool { return len(ws.sentOps()) == 3 })
	if got := toStrings(ws.sentOps()[2]["data"].(map[string]any)["topics"]); len(got) != 1 || got[0] != "instrument:4004" {
		t.Fatalf("incremental subscribe = %v", got)
	}
}

func TestStreamReconnects(t *testing.T) {
	e, s, conns := newStreamEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	// 1: authentication refused → reconnect
	ws := <-conns
	ws.in <- []byte(`{"operation":"Authenticate","success":false,"errorCode":"InvalidKey","errorMessage":"bad"}`)
	// 2: peer closes after auth → reconnect
	ws = <-conns
	ws.in <- []byte(`{"operation":"Authenticate","success":true}`)
	close(ws.in)
	// 3: ping fails → reconnect
	ws = <-conns
	ws.mu.Lock()
	ws.pingErr = errors.New("no pong")
	ws.mu.Unlock()
	ws.in <- []byte(`{"operation":"Authenticate","success":true}`)
	// 4: the trading mode changes → reconnect with the new mode's keys
	ws = <-conns
	ws.in <- []byte(`{"operation":"Authenticate","success":true}`)
	waitFor(t, "connected", func() bool { ok, _ := s.Status(); return ok })
	e.mode.Store(domain.ModeReal) // the stream's account changes to one with its own keys
	ak, uk := domain.AccountKeyNames(e.account())
	_ = e.sec.Put(ctx, ak, "real-api")
	_ = e.sec.Put(ctx, uk, "real-user")
	ws = <-conns
	waitFor(t, "authenticate with real keys", func() bool { return len(ws.sentOps()) == 1 })
	if ws.sentOps()[0]["data"].(map[string]any)["apiKey"] != "real-api" {
		t.Fatal("the new mode's keys were not used")
	}
	cancel()
	waitFor(t, "closed", func() bool { ws.mu.Lock(); defer ws.mu.Unlock(); return ws.closed })
}

func TestStreamWaitsForAnAccount(t *testing.T) {
	_, s, conns := newStreamEnv(t)
	s.Account = func(context.Context) (domain.Account, bool) { return domain.Account{}, false }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if len(conns) != 0 {
		t.Fatal("dialled without an account")
	}
}

func TestStreamWaitsForKeys(t *testing.T) {
	e, s, conns := newStreamEnv(t)
	_ = e.sec.Delete(context.Background(), domain.CredEtoroDemoAPIKey)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if len(conns) != 0 {
		t.Fatal("dialled without keys")
	}
	if _, msg := s.Status(); !strings.Contains(msg, "not both set") {
		t.Fatalf("status = %q", msg)
	}
}

func TestFeedPrefersFreshStreamQuotes(t *testing.T) {
	e := newEnv(t)
	s := NewStream(e.c, func(context.Context) (domain.Account, bool) { return e.account(), true })
	f := NewFeed(e.c, s, 30*time.Second)
	now := e.c.cfg.Now()
	s.quotes[1001] = streamQuote{q: domain.Quote{Instrument: 1001, Bid: 1, Ask: 2}, received: now.Add(-5 * time.Second)}
	s.quotes[2002] = streamQuote{q: domain.Quote{Instrument: 2002, Bid: 1, Ask: 2}, received: now.Add(-time.Minute)} // stale
	qs, err := f.Quotes(e.ctx(), []domain.InstrumentID{2002, 1001})
	if err != nil || len(qs) != 2 {
		t.Fatalf("quotes = %+v %v", qs, err)
	}
	if qs[0].Instrument != 1001 || qs[0].Bid != 1 || qs[1].Instrument != 2002 || qs[1].Bid != 215500000 {
		t.Fatalf("fresh from the stream, stale from REST: %+v", qs)
	}
	if got := e.last().URL.Query().Get("instrumentIds"); got != "2002" {
		t.Fatalf("REST asked for %q; only the stale one should go to REST", got)
	}
	if !s.want[1001] || !s.want[2002] {
		t.Fatal("asked-for instruments must be added to the stream")
	}
	// REST down: fresh stream quotes are still served; nothing at all → error
	e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 503, "", nil }
	if qs, err := f.Quotes(e.ctx(), []domain.InstrumentID{1001, 2002}); err != nil || len(qs) != 1 {
		t.Fatalf("partial = %+v %v", qs, err)
	}
	if _, err := f.Quotes(e.ctx(), []domain.InstrumentID{2002}); err == nil {
		t.Fatal("no quote and REST down must be an error")
	}
}

// TestRealWebSocket runs the coder/websocket adapter against a local server.
func TestRealWebSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		_, b, err := c.Read(ctx)
		if err != nil || !strings.Contains(string(b), "Authenticate") {
			return
		}
		_ = c.Write(ctx, websocket.MessageBinary, []byte{1})
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"operation":"Authenticate","success":true}`))
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialWS(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, []byte(`{"operation":"Authenticate"}`)); err != nil {
		t.Fatal(err)
	}
	r, err := readReply(ctx, conn)
	if err != nil || r.Success == nil || !*r.Success {
		t.Fatalf("reply = %+v %v", r, err)
	}
	go func() { _, _ = readReply(ctx, conn) }() // a pong is only seen while someone reads
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dialWS(ctx, "ws://127.0.0.1:1/ws"); err == nil {
		t.Fatal("dial to a closed port")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
