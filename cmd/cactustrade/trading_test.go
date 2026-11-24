package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// orderHook is a trader that records the order of the calls (the ordering hook of PLAN 4.14).
type orderHook struct {
	mu        sync.Mutex
	calls     []string
	reconErr  error
	resumeErr error
	started   chan string // a loop has started
}

func newOrderHook() *orderHook { return &orderHook{started: make(chan string, 4)} }

func (h *orderHook) note(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, s)
}

func (h *orderHook) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

func (h *orderHook) ReconcileAll(context.Context) ([]app.Reconciliation, error) {
	h.note("reconcile")
	return []app.Reconciliation{{AccountID: "a", OK: true}, {AccountID: "b", Reason: "test"}}, h.reconErr
}

func (h *orderHook) ResumeAgents(context.Context) ([]app.ResumeOutcome, error) {
	h.note("resume")
	return []app.ResumeOutcome{{AccountID: "a", State: domain.AgentRunning}}, h.resumeErr
}

func (h *orderHook) loop(ctx context.Context, name string) {
	h.note(name)
	h.started <- name
	<-ctx.Done()
	h.note(name + " ended")
}

func (h *orderHook) SuperviseLoop(ctx context.Context) { h.loop(ctx, "supervise") }
func (h *orderHook) AgentLoop(ctx context.Context)     { h.loop(ctx, "agent") }

func (h *orderHook) SetAgentLoop(running bool) {
	if running {
		h.note("loop on")
		return
	}
	h.note("loop off")
}

func (h *orderHook) ReleaseLeases(context.Context) error {
	h.note("release")
	return nil
}

func waitStarted(t *testing.T, h *orderHook, n int) {
	t.Helper()
	for range n {
		select {
		case <-h.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("loops did not start: %v", h.seen())
		}
	}
}

func TestStartTradingOrder(t *testing.T) {
	tests := []struct {
		name      string
		reconErr  error
		resumeErr error
		loops     []string
		loopNote  string // what SetAgentLoop was told (6.13)
	}{
		{"clean start", nil, nil, []string{"supervise", "agent"}, "loop on"},
		{"reconciliation failed: still resume (agents come back paused) and supervise", errors.New("db down"), nil, []string{"supervise", "agent"}, "loop on"},
		{"resume failed: exits only, no agent loop", nil, errors.New("db down"), []string{"supervise"}, "loop off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newOrderHook()
			h.reconErr, h.resumeErr = tt.reconErr, tt.resumeErr
			stop := startTrading(context.Background(), h, slog.New(slog.DiscardHandler))
			waitStarted(t, h, len(tt.loops))
			stop()
			got := h.seen()
			// reconcile, then resume, then the loops (in either order), and the leases are
			// released only after every loop has ended
			if len(got) < 2 || got[0] != "reconcile" || got[1] != "resume" || got[len(got)-1] != "release" {
				t.Fatalf("calls = %v", got)
			}
			mid := got[2 : len(got)-1]
			for _, l := range []string{"supervise", "agent"} {
				want := slices.Contains(tt.loops, l)
				if slices.Contains(mid, l) != want || slices.Contains(mid, l+" ended") != want {
					t.Fatalf("loop %s in %v, want started=%v", l, got, want)
				}
				if want && slices.Index(mid, l) > slices.Index(mid, l+" ended") {
					t.Fatalf("calls = %v", got)
				}
			}
			// the agent loop is reported exactly once, and "on" before the loop starts
			notes := slices.DeleteFunc(slices.Clone(mid), func(c string) bool { return !strings.HasPrefix(c, "loop ") })
			if !slices.Equal(notes, []string{tt.loopNote}) {
				t.Fatalf("SetAgentLoop calls %v, want [%s] (all calls %v)", notes, tt.loopNote, got)
			}
			if tt.loopNote == "loop on" && slices.Index(mid, "loop on") > slices.Index(mid, "agent") {
				t.Fatalf("loop reported running after it started: %v", got)
			}
		})
	}
}

func TestStartTradingStoppedDuringStartup(t *testing.T) {
	h := newOrderHook()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shut down before the start-up finished: no loop starts, leases are released
	stop := startTrading(ctx, h, slog.New(slog.DiscardHandler))
	stop()
	got := h.seen()
	if slices.Contains(got, "supervise") || slices.Contains(got, "agent") || got[len(got)-1] != "release" {
		t.Fatalf("calls = %v", got)
	}
}

func TestLeaseHolderID(t *testing.T) {
	r := &fakes.Random{}
	a, b := leaseHolderID("host", r), leaseHolderID("host", r)
	if a == b || !strings.HasPrefix(a, "host-") {
		t.Fatalf("ids %q %q", a, b)
	}
	if long := leaseHolderID(strings.Repeat("h", 200), r); len(long) > maxLeaseHolder {
		t.Fatalf("id %q is %d long", long, len(long))
	}
	if id := leaseHolderID("", r); strings.HasPrefix(id, "-") || id == "" {
		t.Fatalf("id %q", id)
	}
}

// tradingEnv is the real service on fakes with one demo account that has keys.
type tradingEnv struct {
	svc *app.Service
	st  *fakes.Store
	clk *fakes.Clock
	b   *fakes.SimBroker
	p   app.Principal
	v   domain.AccountView
}

func newTradingEnv(t *testing.T) *tradingEnv {
	t.Helper()
	ctx := context.Background()
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)}
	sec := &fakes.SecretStore{Now: clk.Now}
	b := &fakes.SimBroker{Now: clk.Now, Cash: 10_000_00}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: sec, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk,
		Broker: b, Market: &fakes.Broker{}, Calendar: nil,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done() // one round of each loop, then wait for the shutdown
			return ctx.Err()
		},
	}, app.Config{Version: "test", LeaseHolder: "this-process"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureDefaultAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := st.Users().GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin.MustChangePassword = false
	if err := st.Users().Update(ctx, admin); err != nil {
		t.Fatal(err)
	}
	p := app.Principal{UserID: admin.ID, Username: admin.Username, Role: domain.RoleAdmin, Via: app.ViaSession}
	v, err := svc.CreateAccount(ctx, p, "Demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	ak, uk := domain.AccountKeyNames(v.Account)
	for _, n := range []domain.CredentialName{ak, uk} {
		if err := sec.Put(ctx, n, "test-key"); err != nil {
			t.Fatal(err)
		}
	}
	return &tradingEnv{svc: svc, st: st, clk: clk, b: b, p: p, v: v}
}

// waitLease waits for the first supervisor pass, which takes the account's lease.
func (e *tradingEnv) waitLease(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if l, err := e.st.AgentLeases().Get(context.Background(), e.v.ID); err == nil && l.Holder == "this-process" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the supervisor did not take the lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestStartTradingWithTheService runs the real use cases on fakes: the start-up
// reconciliation happens before the first supervisor pass (seen as the first portfolio read
// after it), and the lease this process took is released at shutdown.
func TestStartTradingWithTheService(t *testing.T) {
	ctx := context.Background()
	e := newTradingEnv(t)
	if e.svc.Reconciled(e.v.ID).OK {
		t.Fatal("reconciled before the start")
	}
	stop := startTrading(ctx, e.svc, slog.New(slog.DiscardHandler))
	// the first supervisor pass takes the lease; reconciliation must have happened before
	e.waitLease(t)
	if !e.svc.Reconciled(e.v.ID).OK {
		t.Fatalf("not reconciled before the supervisor: %+v", e.svc.Reconciled(e.v.ID))
	}
	stop()
	if err := e.st.AgentLeases().Acquire(ctx, e.v.ID, "successor", e.clk.Now(), e.clk.Now().Add(domain.LeaseTTL)); err != nil {
		t.Fatalf("the lease was not released at shutdown: %v", err)
	}
	if e.b.Opens != 0 || e.b.Closes != 0 {
		t.Fatalf("orders at start-up: opens %d closes %d", e.b.Opens, e.b.Closes)
	}
}

// failingResume is the real service whose OD5 resume fails (e.g. the database is down).
type failingResume struct{ *app.Service }

func (failingResume) ResumeAgents(context.Context) ([]app.ResumeOutcome, error) {
	return nil, errors.New("db down")
}

// TestStartAgentNeedsTheAgentLoop (PLAN 6.13): StartAgent is refused while start-up has not
// decided and after a failed resume (no agent loop), with the reason on the panel; after a
// clean start it gets past that check.
func TestStartAgentNeedsTheAgentLoop(t *testing.T) {
	tests := []struct {
		name     string
		trader   func(*app.Service) trader
		loopRuns bool
	}{
		{"clean start", func(s *app.Service) trader { return s }, true},
		{"failed resume", func(s *app.Service) trader { return failingResume{s} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			e := newTradingEnv(t)
			if _, err := e.svc.StartAgent(ctx, e.p, e.v.ID); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "starting up") {
				t.Fatalf("start before start-up decided: %v", err)
			}
			stop := startTrading(ctx, tt.trader(e.svc), slog.New(slog.DiscardHandler))
			defer stop()
			e.waitLease(t) // the supervisor runs either way: exits still work
			_, err := e.svc.StartAgent(ctx, e.p, e.v.ID)
			view, verr := e.svc.AgentStatusView(ctx, e.p, e.v.ID)
			if verr != nil {
				t.Fatal(verr)
			}
			if tt.loopRuns {
				// past the loop check: refused only for the missing budget
				if !errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrUnavailable) {
					t.Fatalf("start after a clean start-up: %v", err)
				}
				if !view.Controls.Start || view.StartBlocked != "" {
					t.Fatalf("controls %+v blocked %q", view.Controls, view.StartBlocked)
				}
				return
			}
			if !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "resume failed") {
				t.Fatalf("start after a failed resume: %v", err)
			}
			if view.Controls.Start || !strings.Contains(view.StartBlocked, "resume failed") {
				t.Fatalf("controls %+v blocked %q", view.Controls, view.StartBlocked)
			}
			if !view.Controls.Kill || !view.Controls.Stop {
				t.Fatalf("kill and stop must stay available: %+v", view.Controls)
			}
		})
	}
}
