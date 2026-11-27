package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Tests for the independent review of the real-mode gate (PLAN "Real-gate review
// (2026-11-24)"). Each started failing before its fix.

// realGateArmed reports whether the account's gate is in force now; an unknown state (a read
// error) is not armed. Test helper over realGateState.
func (s *Service) realGateArmed(ctx context.Context, a domain.Account) bool {
	armed, err := s.realGateState(ctx, a)
	return armed && err == nil
}

// failingPuts fails every secret write from the failAt-th on.
type failingPuts struct {
	ports.SecretStore
	mu     sync.Mutex
	n      int
	failAt int
}

func (f *failingPuts) Put(ctx context.Context, n domain.CredentialName, v string) error {
	f.mu.Lock()
	f.n++
	k := f.n
	f.mu.Unlock()
	if k >= f.failAt {
		return errors.New("secret store down")
	}
	return f.SecretStore.Put(ctx, n, v)
}

// TestKeyChangeFailingPartWayDisarms (review MAJOR): a key change that fails part-way (the
// first or the second of the two eToro keys cannot be written) still disarms the gate and
// unreconciles the account: one key may already be the new one.
func TestKeyChangeFailingPartWayDisarms(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("write %d fails", failAt), func(t *testing.T) {
			g := newGateEnv(t)
			g.arm(t)
			g.clock.Advance(time.Second)
			g.svc.d.Secrets = &failingPuts{SecretStore: g.svc.d.Secrets, failAt: failAt}
			if err := g.svc.SetAccountKeys(g.ctx, g.admin, g.real.ID, "new-api-key", "new-user-key"); err == nil {
				t.Fatal("the failed write was not reported")
			}
			gate := g.gate(t)
			if gate.State != domain.GateLocked || !strings.Contains(gate.DisarmReason, domain.DisarmKeys) || g.svc.realGateArmed(g.ctx, g.real) {
				t.Fatalf("gate = %+v, want disarmed for the key change", gate)
			}
			if g.svc.Reconciled(g.real.ID).OK {
				t.Fatal("the account is still reconciled after a key change that failed part-way")
			}
		})
	}
	// Nothing to write (validation fails first): the gate stays armed.
	g := newGateEnv(t)
	g.arm(t)
	if err := g.svc.SetAccountCredentials(g.ctx, g.admin, g.real.ID, map[string]string{"nope": "x"}); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if !g.svc.realGateArmed(g.ctx, g.real) || !g.svc.Reconciled(g.real.ID).OK {
		t.Fatal("a refused key change disarmed or unreconciled")
	}
}

// TestRealGateStateUnknown (review minor 1): a gate that cannot be read is "unknown", never
// "not armed": Status says so (the web shows a red banner), the health names the problem.
func TestRealGateStateUnknown(t *testing.T) {
	g := newGateEnv(t)
	g.arm(t)
	g.store.GatesErr = errors.New("database down")
	st, err := g.svc.Status(g.ctx, g.admin)
	if err != nil || !st.RealArmedUnknown || len(st.RealArmed) != 0 {
		t.Fatalf("status with the gate unreadable = %+v %v", st, err)
	}
	h := g.svc.TradingHealth(g.ctx)
	found := false
	for _, a := range h.Accounts {
		if a.AccountID == g.real.ID {
			found = true
			if !strings.Contains(a.Problem, "real gate") {
				t.Fatalf("health %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("health %+v", h)
	}
	// Someone who cannot see the real account learns nothing from it.
	demoOnly := g.user(t, "demo-only", domain.RoleOperator)
	if st, err := g.svc.Status(g.ctx, demoOnly); err != nil || st.RealArmedUnknown {
		t.Fatalf("a user without the real account: %+v %v", st, err)
	}
	g.store.GatesErr = nil
	if st, err := g.svc.Status(g.ctx, g.admin); err != nil || st.RealArmedUnknown || len(st.RealArmed) != 1 {
		t.Fatalf("status readable again = %+v %v", st, err)
	}
}

// TestStatusForDefaultPassword (review minor 1): a user who must still change the default
// password gets the banner's facts from Status (the change-password page shows the banner)
// and nothing else; every other use case stays refused.
func TestStatusForDefaultPassword(t *testing.T) {
	g := newGateEnv(t)
	g.arm(t)
	p := g.admin
	p.MustChangePassword = true
	st, err := g.svc.Status(g.ctx, p)
	if err != nil || len(st.RealArmed) != 1 || st.RealArmed[0].Name != "Live" {
		t.Fatalf("status = %+v %v", st, err)
	}
	if st.Version != "" || st.Build != "" || st.Account != nil || !st.StartedAt.IsZero() || st.Mode != domain.ModeDemo {
		t.Fatalf("status told a default-password user more than the banner: %+v", st)
	}
	if _, err := g.svc.Accounts(g.ctx, p); !errors.Is(err, domain.ErrPasswordChangeRequired) {
		t.Fatalf("accounts: %v", err)
	}
	if _, err := g.svc.Status(g.ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
}

// TestRealGateDisarmWriteFailureKills (review minor 3): a disarm that cannot be stored also
// stores a kill, so every process (not only this one's latch) refuses entries.
func TestRealGateDisarmWriteFailureKills(t *testing.T) {
	g := newGateEnv(t)
	g.arm(t)
	g.clock.Advance(time.Second)
	g.store.GatesErr = errors.New("database down")
	if _, err := g.svc.DisarmReal(g.ctx, g.admin, g.real.ID, "drill"); err == nil {
		t.Fatal("the failed write was not reported")
	}
	g.store.GatesErr = nil
	if g.gate(t).State != domain.GateArmed {
		t.Fatal("test setup: the gate write should have failed")
	}
	rs, err := g.store.RiskStates().Get(g.ctx, g.real.ID)
	if err != nil || !rs.State.Killed || !strings.Contains(rs.KillReason, "disarm") {
		t.Fatalf("stored risk state = %+v %v, want a kill", rs, err)
	}
	// Another replica: a new service on the same database, without this process's latches.
	other, err := New(Deps{Store: g.store, Secrets: g.sec, Hasher: g.svc.d.Hasher, Random: g.svc.d.Random, Clock: g.clock}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if killed, err := other.isKilled(g.ctx, g.real.ID); err != nil || !killed {
		t.Fatalf("another process sees killed = %v %v", killed, err)
	}
	if as := g.audits("agent.kill"); len(as) != 1 {
		t.Fatalf("kill audits %+v", as)
	}
	// A kill whose own disarm fails does not recurse.
	g2 := newGateEnv(t)
	g2.arm(t)
	g2.clock.Advance(time.Second)
	g2.store.GatesErr = errors.New("database down")
	if _, err := g2.svc.Kill(g2.ctx, g2.admin, g2.real.ID, "drill"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	g2.store.GatesErr = nil
	if as := g2.audits("agent.kill"); len(as) != 1 {
		t.Fatalf("kill audits %+v", as)
	}
}

// TestTripAnnouncesTheDisarmAfterwards (review minor 4): tripLocked latches and stores the
// disarm before returning but leaves the event and the e-mails to its caller, which sends
// them after the flatten.
func TestTripAnnouncesTheDisarmAfterwards(t *testing.T) {
	g := newGateEnv(t)
	u, err := g.store.Users().Get(g.ctx, g.admin.UserID)
	if err != nil {
		t.Fatal(err)
	}
	u.Email = "owner@example.test"
	if err := g.store.Users().Update(g.ctx, u); err != nil {
		t.Fatal(err)
	}
	g.arm(t)
	g.clock.Advance(time.Second)
	disarmNews := func() (events, mails int) {
		es, _ := g.store.Events().List(g.ctx, []string{g.real.ID}, 100)
		for _, e := range es {
			if e.Kind == "real.disarmed" {
				events++
			}
		}
		ms, _ := g.store.Outbox().List(g.ctx, 100)
		for _, m := range ms {
			if strings.Contains(m.Subject, "disarmed") {
				mails++
			}
		}
		return events, mails
	}
	locks := g.svc.rt.agent(g.real.ID)
	locks.ctl.Lock()
	_, announce, err := g.svc.tripLocked(g.ctx, g.real, g.admin, "drill")
	locks.ctl.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if g.gate(t).State != domain.GateLocked || g.svc.realGateArmed(g.ctx, g.real) {
		t.Fatal("the disarm was not stored before tripLocked returned")
	}
	if e, m := disarmNews(); e != 0 || m != 0 {
		t.Fatalf("announced before the flatten: %d events, %d mails", e, m)
	}
	announce()
	if e, m := disarmNews(); e != 1 || m != 1 {
		t.Fatalf("after announcing: %d events, %d mails", e, m)
	}

	// Kill sends them itself (after its flatten).
	g2 := newGateEnv(t)
	g2.arm(t)
	g2.clock.Advance(time.Second)
	if _, err := g2.svc.Kill(g2.ctx, g2.admin, g2.real.ID, "drill"); err != nil {
		t.Fatal(err)
	}
	es, _ := g2.store.Events().List(g2.ctx, []string{g2.real.ID}, 100)
	n := 0
	for _, e := range es {
		if e.Kind == "real.disarmed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("kill: %d disarm events", n)
	}
}

// slowGates makes every read of one account's gate wait for release (or its context).
type slowGates struct {
	ports.Store
	realID   string
	release  chan struct{}
	started  chan struct{}
	once     sync.Once
	reads    atomic.Int32
	deadline atomic.Bool
}

func (s *slowGates) RealGates() ports.RealGates { return slowGateRepo{s.Store.RealGates(), s} }

type slowGateRepo struct {
	ports.RealGates
	s *slowGates
}

func (r slowGateRepo) Get(ctx context.Context, id string) (domain.RealGate, error) {
	if id == r.s.realID {
		r.s.reads.Add(1)
		_, ok := ctx.Deadline()
		r.s.deadline.Store(ok)
		r.s.once.Do(func() { close(r.s.started) })
		select {
		case <-r.s.release:
		case <-ctx.Done():
			return domain.RealGate{}, ctx.Err()
		}
	}
	return r.RealGates.Get(ctx, id)
}

// TestSlowGateSweepDoesNotDelayExits (review minor 4): the supervisor tick starts the passes
// first and sweeps the gates on the side, with a timeout; a sweep still running is not
// started twice.
func TestSlowGateSweepDoesNotDelayExits(t *testing.T) {
	closeAt := time.Date(2026, 11, 23, 21, 0, 0, 0, time.UTC)
	x := supEnv(t, closeAt.Add(-time.Hour))
	x.openPosition(t, instNYSE, domain.Long)
	real := x.account(t, "Real", domain.ModeReal)
	x.clock.Advance(50 * time.Minute) // close-10 min: the position is due
	slow := &slowGates{Store: x.svc.d.Store, realID: real.ID, release: make(chan struct{}), started: make(chan struct{})}
	x.svc.d.Store = slow
	var wg sync.WaitGroup
	defer wg.Wait()
	released := false
	release := func() {
		if !released {
			released = true
			close(slow.release)
		}
	}
	defer release()
	tickDone := make(chan struct{})
	go func() { x.svc.superviseTick(x.ctx, &wg); close(tickDone) }()
	select {
	case <-tickDone:
	case <-time.After(5 * time.Second):
		release()
		<-tickDone
		t.Fatal("the tick waited for the gate sweep")
	}
	busy := func() bool {
		x.svc.rt.mu.Lock()
		defer x.svc.rt.mu.Unlock()
		return x.svc.rt.supervising[x.a.ID]
	}
	deadline := time.Now().Add(5 * time.Second)
	for busy() {
		if time.Now().After(deadline) {
			t.Fatal("the pass did not finish while the sweep was slow")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if x.b.Closes != 1 {
		t.Fatalf("closes = %d while the sweep is slow, want 1", x.b.Closes)
	}
	select {
	case <-slow.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep never ran")
	}
	if !slow.deadline.Load() {
		t.Fatal("the sweep has no timeout")
	}
	// Since PLAN 7.21 the real account's own supervision reads its gate too (in its own
	// goroutine, realSupervised): one sweep and one pass, both slow.
	for deadline := time.Now().Add(5 * time.Second); slow.reads.Load() < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d gate reads, want the sweep's and the real pass's", slow.reads.Load())
		}
	}
	// A second tick while the sweep (and the real pass) still run starts neither again.
	x.svc.superviseTick(x.ctx, &wg)
	time.Sleep(20 * time.Millisecond)
	if n := slow.reads.Load(); n != 2 {
		t.Fatalf("%d gate reads: a second sweep or pass started", n)
	}
	release()
	wg.Wait()
}
