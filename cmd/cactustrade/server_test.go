package main

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/api"
	"github.com/albievan/cactustrade/internal/adapters/eventbus"
	httpapi "github.com/albievan/cactustrade/internal/adapters/http"
	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// scaledTimeouts are the production timeouts scaled down (PLAN 6.9: 200/300 ms).
func scaledTimeouts() httpTimeouts {
	return httpTimeouts{ReadHeader: 200 * time.Millisecond, Read: 200 * time.Millisecond, Write: 300 * time.Millisecond, Idle: time.Second}
}

// liveEnv is the real service with a live bus behind the production web server (scaled
// timeouts) over TLS, HTTP/1.1 or HTTP/2, and an operator API key of the account's owner.
type liveEnv struct {
	*tradingEnv
	ts  *httptest.Server
	srv *http.Server
	key string
}

func newLiveEnv(t *testing.T, h2 bool, wrap func(http.Handler) http.Handler) *liveEnv {
	t.Helper()
	e := newTradingEnvWith(t, func(d *app.Deps) { d.Bus = eventbus.New() })
	log := slog.New(slog.DiscardHandler)
	// The stream's own write timeout is scaled too (10 s → 100 ms): a deadline left behind
	// after a write would reset the idle HTTP/2 stream long before the next ping.
	h := httpapi.New(e.svc, log, httpapi.Config{CookieSecure: true, OpenAPI: api.OpenAPI, Live: httpapi.LiveLimits{WriteTimeout: 100 * time.Millisecond}}, nil)
	srv := newHTTPServerWith("", h, scaledTimeouts(), log)
	if wrap != nil {
		srv.Handler = wrap(srv.Handler)
	}
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config = srv
	ts.EnableHTTP2 = h2
	ts.StartTLS()
	t.Cleanup(ts.Close)
	k, err := e.svc.CreateAPIKey(context.Background(), e.p, "dashboard", domain.RoleOperator, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &liveEnv{tradingEnv: e, ts: ts, srv: srv, key: k.Secret}
}

func (e *liveEnv) request(ctx context.Context, method, path string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, method, e.ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	return e.ts.Client().Do(req)
}

// sse is an open live stream's messages ("event data" pairs, joined by a space).
type sse struct {
	resp *http.Response
	msgs chan string
}

func (e *liveEnv) stream(t *testing.T, wantProto int) *sse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := e.request(ctx, http.MethodGet, "/api/v1/live?accountId="+e.v.ID)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != wantProto {
		t.Fatalf("stream: %d over HTTP/%d", resp.StatusCode, resp.ProtoMajor)
	}
	s := &sse{resp: resp, msgs: make(chan string, 256)}
	go func() {
		defer close(s.msgs)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		var ev string
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				s.msgs <- ev + " " + strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return s
}

// wait returns the first message starting with prefix and containing substr (fails when
// the stream ends or nothing comes in time).
func (s *sse) wait(t *testing.T, prefix, substr string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m, open := <-s.msgs:
			if !open {
				t.Fatalf("the stream ended before %q", prefix)
			}
			if strings.HasPrefix(m, prefix) && strings.Contains(m, substr) {
				return m
			}
		case <-deadline:
			t.Fatalf("no %q message", prefix)
		}
	}
}

// waitAll waits until, for every i, a message starting with prefixes[i] and containing
// substrs[i] has arrived, in any order.
func (s *sse) waitAll(t *testing.T, prefixes, substrs []string) {
	t.Helper()
	left := len(prefixes)
	got := make([]bool, len(prefixes))
	deadline := time.After(5 * time.Second)
	for left > 0 {
		select {
		case m, open := <-s.msgs:
			if !open {
				t.Fatalf("the stream ended; still waiting for %v", prefixes)
			}
			for i := range prefixes {
				if !got[i] && strings.HasPrefix(m, prefixes[i]) && strings.Contains(m, substrs[i]) {
					got[i] = true
					left--
				}
			}
		case <-deadline:
			t.Fatalf("missing messages: %v of %v", got, prefixes)
		}
	}
}

func protos() []struct {
	name  string
	h2    bool
	major int
} {
	return []struct {
		name  string
		h2    bool
		major int
	}{{"HTTP/1.1", false, 1}, {"HTTP/2", true, 2}}
}

// TestStreamOutlivesServerTimeouts (PLAN 6.9): on HTTP/1.1 and HTTP/2, a live stream that
// has been quiet for more than 3× ReadTimeout and 3× WriteTimeout still delivers events,
// and the kill switch works while it is open.
func TestStreamOutlivesServerTimeouts(t *testing.T) {
	for _, p := range protos() {
		t.Run(p.name, func(t *testing.T) {
			e := newLiveEnv(t, p.h2, nil)
			s := e.stream(t, p.major)
			s.wait(t, "snapshot ", "")
			to := scaledTimeouts()
			time.Sleep(3*max(to.Read, to.Write) + 100*time.Millisecond) // no write meanwhile (ping 15 s, snapshot 5 s)
			resp, err := e.request(context.Background(), http.MethodPost, "/api/v1/agent/kill?accountId="+e.v.ID)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("kill: %d", resp.StatusCode)
			}
			// the agent state (a snapshot, at once) and the kill event, in either order, well
			// before the next periodic snapshot (5 s)
			start := time.Now()
			s.waitAll(t, []string{"event ", "snapshot "}, []string{"kill", `"killed":true`})
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("the kill reached the stream after %v", d)
			}
		})
	}
}

// TestShutdownWithOpenStreams (PLAN 6.9, 4.14-b): Shutdown returns within a second with 3
// open streams (each gets bye "shutdown"), and a kill request in flight still completes
// before it returns.
func TestShutdownWithOpenStreams(t *testing.T) {
	for _, p := range protos() {
		t.Run(p.name, func(t *testing.T) {
			inKill := make(chan struct{})
			var mu sync.Mutex
			var killDone time.Time
			slowKill := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api/v1/agent/kill" {
						next.ServeHTTP(w, r)
						return
					}
					close(inKill)
					time.Sleep(150 * time.Millisecond) // in flight while the shutdown starts
					next.ServeHTTP(w, r)
					mu.Lock()
					killDone = time.Now()
					mu.Unlock()
				})
			}
			e := newLiveEnv(t, p.h2, slowKill)
			var streams []*sse
			for range 3 {
				s := e.stream(t, p.major)
				s.wait(t, "snapshot ", "")
				streams = append(streams, s)
			}
			killed := make(chan int, 1)
			go func() {
				resp, err := e.request(context.Background(), http.MethodPost, "/api/v1/agent/kill?accountId="+e.v.ID)
				if err != nil {
					killed <- 0
					return
				}
				_ = resp.Body.Close()
				killed <- resp.StatusCode
			}()
			<-inKill
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := e.srv.Shutdown(ctx); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			returned := time.Now()
			if d := returned.Sub(start); d >= time.Second {
				t.Fatalf("shutdown took %v with 3 open streams", d)
			}
			if code := <-killed; code != http.StatusOK {
				t.Fatalf("kill during the shutdown: %d", code)
			}
			mu.Lock()
			done := killDone
			mu.Unlock()
			if done.IsZero() || done.After(returned) {
				t.Fatalf("shutdown returned before the kill completed (kill %v, shutdown %v)", done, returned)
			}
			if rs, err := e.st.RiskStates().Get(context.Background(), e.v.ID); err != nil || !rs.State.Killed {
				t.Fatalf("kill not stored: %+v %v", rs, err)
			}
			for _, s := range streams {
				s.wait(t, "bye ", "shutdown")
			}
		})
	}
}
