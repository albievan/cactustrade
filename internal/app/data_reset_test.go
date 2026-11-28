package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// resetEnv is a gateEnv (a reconciled demo account and a reconciled real-money account, both
// the admin's, with keys) with a backup folder, a recipient, five qualifying demo days on the
// demo account and an admin e-mail address.
type resetEnv struct {
	*gateEnv
	backups *fakes.Backups
}

func newResetEnv(t *testing.T) *resetEnv {
	t.Helper()
	g := newGateEnv(t)
	b := &fakes.Backups{}
	g.svc.d.Backups = b
	if _, err := g.svc.SetBackupRecipients(g.ctx, g.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	u, err := g.store.Users().Get(g.ctx, g.admin.UserID)
	if err != nil {
		t.Fatal(err)
	}
	u.Email = "admin@example.com"
	if err := g.store.Users().Update(g.ctx, u); err != nil {
		t.Fatal(err)
	}
	g.seedEvidence(t, g.a.ID, 5, 1)
	return &resetEnv{gateEnv: g, backups: b}
}

func (r *resetEnv) reset(t *testing.T) (DataResetResult, error) {
	t.Helper()
	return r.svc.ResetAllData(r.ctx, r.admin, domain.DataResetPhrase, adminPassword, false)
}

// failedCheck returns the names of the checks a refusal failed.
func failedCheck(err error) []string {
	r, ok := errors.AsType[*domain.DataResetRefusal](err)
	if !ok {
		return nil
	}
	var out []string
	for _, c := range r.Failed {
		out = append(out, c.Name)
	}
	return out
}

func TestDataResetRoles(t *testing.T) {
	r := newResetEnv(t)
	op := r.user(t, "op", domain.RoleOperator)
	viewer := r.user(t, "viewer", domain.RoleViewer)
	key := r.admin
	key.Via = ViaAPIKey
	for _, p := range []Principal{op, viewer, key, {}} {
		if _, err := r.svc.DataResetPreview(r.ctx, p); err == nil {
			t.Fatalf("%s/%s may preview", p.Username, p.Via)
		}
		_, err := r.svc.ResetAllData(r.ctx, p, domain.DataResetPhrase, passwordOf(p), false)
		if !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("%s/%s: %v", p.Username, p.Via, err)
		}
	}
	if r.store.ResetCalls != 0 || len(r.backups.Writes) != 0 {
		t.Fatal("a refused caller reset or backed up")
	}
}

func TestResetAllDataPhraseAndPassword(t *testing.T) {
	r := newResetEnv(t)
	if _, err := r.svc.ResetAllData(r.ctx, r.admin, "RESET ALL DATA", adminPassword, false); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("wrong phrase: %v", err)
	}
	if _, err := r.svc.ResetAllData(r.ctx, r.admin, domain.DataResetPhrase, "wrong", false); !errors.Is(err, domain.ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	var details []string
	for _, a := range r.audits("data.reset_refused") {
		details = append(details, a.Details)
	}
	if strings.Join(details, "|") != "wrong phrase|wrong password" {
		t.Fatalf("audit %v", details)
	}
	if r.store.ResetCalls != 0 || len(r.backups.Writes) != 0 {
		t.Fatal("wiped or backed up without the phrase and password")
	}
}

// TestResetAllDataRefusals: every check refuses before anything is backed up or wiped.
func TestResetAllDataRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, r *resetEnv)
		check string
	}{
		{"running agent", func(t *testing.T, r *resetEnv) { r.setAgent(t, domain.AgentRunning) }, domain.CheckResetAgents},
		{"paused agent", func(t *testing.T, r *resetEnv) { r.setAgent(t, domain.AgentPaused) }, domain.CheckResetAgents},
		{"live lease of another process", func(t *testing.T, r *resetEnv) {
			if err := r.store.AgentLeases().Acquire(r.ctx, r.a.ID, "proc-other", r.clock.Now(), r.clock.Now().Add(domain.LeaseTTL)); err != nil {
				t.Fatal(err)
			}
		}, domain.CheckResetLeases},
		{"armed gate", func(t *testing.T, r *resetEnv) { r.arm(t) }, domain.CheckResetGates},
		{"broker down: unreconciled (fail closed)", func(_ *testing.T, r *resetEnv) { r.b.ReadErr = errors.New("eToro is down") }, domain.CheckResetReconciled},
		{"open position", func(t *testing.T, r *resetEnv) {
			r.openPosition(t, instNYSE, domain.Long)
			r.clock.Advance(20 * time.Minute)
		}, domain.CheckResetPositions},
		{"open order on Trading 212", func(t *testing.T, r *resetEnv) {
			r.t212Order(t, r.clock.Now().Add(-time.Hour), false)
		}, domain.CheckResetOrders},
		{"order younger than 15 minutes", func(t *testing.T, r *resetEnv) {
			r.t212Order(t, r.clock.Now().Add(-5*time.Minute), true)
		}, domain.CheckResetRecentOrders},
		{"no recipient", func(t *testing.T, r *resetEnv) {
			if err := r.store.Settings().Set(r.ctx, domain.SettingBackupRecipients, "[]", r.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, domain.CheckResetBackup},
		{"no backup folder", func(_ *testing.T, r *resetEnv) { r.svc.d.Backups = nil }, domain.CheckResetBackup},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newResetEnv(t)
			tc.setup(t, r)
			before, _ := r.store.DataReset().Counts(r.ctx)
			_, err := r.reset(t)
			if !errors.Is(err, domain.ErrConflict) || strings.Join(failedCheck(err), ",") != tc.check {
				t.Fatalf("err = %v, failed %v, want %s", err, failedCheck(err), tc.check)
			}
			if r.store.ResetCalls != 0 || len(r.backups.Writes) != 0 {
				t.Fatal("refused, but backed up or wiped")
			}
			if after, _ := r.store.DataReset().Counts(r.ctx); after["decisions"] != before["decisions"] || after["orders"] != before["orders"] {
				t.Fatalf("rows changed: %v → %v", before, after)
			}
			if a := r.audits("data.reset_refused"); len(a) != 1 || !strings.Contains(a[0].Details, tc.check) {
				t.Fatalf("audit %+v", a)
			}
			if len(r.audits("data.reset_started")) != 0 {
				t.Fatal("started although refused")
			}
		})
	}
}

// t212Order stores an order of a new Trading 212 practice account created at at: final
// (filled) or not (sent).
func (r *resetEnv) t212Order(t *testing.T, at time.Time, final bool) {
	t.Helper()
	a := domain.Account{ID: "acc-t212", Name: "Practice 212", OwnerID: r.admin.UserID, Mode: domain.ModeDemo, Platform: domain.PlatformTrading212, CreatedAt: at, UpdatedAt: at}
	if err := r.store.Accounts().Create(r.ctx, a); err != nil {
		t.Fatal(err)
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: "t212-1", AccountID: a.ID, Mode: domain.ModeDemo, Platform: domain.PlatformTrading212, Direction: domain.Long,
		Kind: domain.OrderOpen, Instrument: instNYSE, Ticker: "AAPL_US_EQ", Quantity: 1_000_000, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	if o, err = o.Transition(domain.OrderSent, at, ""); err != nil {
		t.Fatal(err)
	}
	if final {
		if o, err = o.Transition(domain.OrderFilled, at, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.store.Orders().Create(r.ctx, o); err != nil {
		t.Fatal(err)
	}
}

func TestResetAllDataBackupFails(t *testing.T) {
	r := newResetEnv(t)
	r.backups.WriteErr = errors.New("disk full")
	_, err := r.reset(t)
	if err == nil || !strings.Contains(err.Error(), "nothing was wiped") {
		t.Fatalf("err = %v", err)
	}
	if r.store.ResetCalls != 0 {
		t.Fatal("wiped without a backup")
	}
	if len(r.audits("data.reset_started")) != 1 || len(r.audits("data.reset_refused")) != 1 || len(r.audits("data.reset")) != 0 {
		t.Fatal("audit")
	}
}

func TestResetAllDataFailedWipe(t *testing.T) {
	r := newResetEnv(t)
	r.store.ResetErr = errors.New("database gone")
	if _, err := r.reset(t); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(r.audits("data.reset")) != 0 || len(r.audits("data.reset_refused")) != 1 {
		t.Fatal("audit")
	}
	if !r.svc.Reconciled(r.a.ID).OK {
		t.Fatal("a failed wipe unreconciled the account")
	}
}

func TestDataResetPreview(t *testing.T) {
	r := newResetEnv(t)
	pv, err := r.svc.DataResetPreview(r.ctx, r.admin)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.CanReset || len(pv.Checks) != 8 || !pv.BackupReady || pv.BackupFolder != "/backups" || pv.Phrase != domain.DataResetPhrase {
		t.Fatalf("preview %+v", pv)
	}
	if pv.Wiped["decisions"] != 5 || pv.Wiped["orders"] != 5 || pv.Kept["users"] != 1 || pv.Kept["watchlist"] != 0 {
		t.Fatalf("counts wiped %v kept %v", pv.Wiped, pv.Kept)
	}
	if _, ok := pv.Kept["orders"]; ok {
		t.Fatal("orders listed as kept")
	}
	if len(pv.RealAccounts) != 1 || pv.RealAccounts[0].AccountID != r.real.ID || pv.RealAccounts[0].DaysNow != 5 ||
		pv.RealAccounts[0].Required != 5 || pv.RealAccounts[0].DemoAccountID != r.a.ID {
		t.Fatalf("real accounts %+v", pv.RealAccounts)
	}
	r.setAgent(t, domain.AgentRunning)
	r.svc.d.Backups = nil
	pv, err = r.svc.DataResetPreview(r.ctx, r.admin)
	if err != nil || pv.CanReset || pv.BackupReady || pv.BackupWhy == "" || pv.BackupFolder != "" {
		t.Fatalf("refusing preview %+v %v", pv, err)
	}
}

func TestResetAllData(t *testing.T) {
	r := newResetEnv(t)
	// A second owner with an e-mail address and an account of their own.
	owner := r.user(t, "owner2", domain.RoleOperator)
	u, _ := r.store.Users().Get(r.ctx, owner.UserID)
	u.Email = "owner2@example.com"
	if err := r.store.Users().Update(r.ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Accounts().Create(r.ctx, domain.Account{ID: "acc-o2", Name: "Owner2", OwnerID: owner.UserID, Mode: domain.ModeDemo, Platform: domain.PlatformSaxo}); err != nil {
		t.Fatal(err)
	}
	bystander := r.user(t, "bystander", domain.RoleViewer)
	b, _ := r.store.Users().Get(r.ctx, bystander.UserID)
	b.Email = "bystander@example.com"
	_ = r.store.Users().Update(r.ctx, b)
	if err := r.store.Instruments().Upsert(r.ctx, domain.Instrument{ID: instNYSE, Symbol: "AAPL"}, domain.Eligibility{Instrument: instNYSE, RealLong: true}, r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Watchlist().Add(r.ctx, r.a.ID, instNYSE, "admin", r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(time.Minute)

	res, err := r.reset(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.BackupFile == "" || len(r.backups.Writes) != 1 || res.Counts["decisions"] != 5 || res.Counts["orders"] != 5 || res.WipedWatchlists {
		t.Fatalf("result %+v", res)
	}
	c, _ := r.store.DataReset().Counts(r.ctx)
	for _, tb := range domain.DataResetWipedTables() {
		if tb != "events" && c[tb] != 0 { // the reset's own event is new
			t.Errorf("%s: %d left", tb, c[tb])
		}
	}
	if c["watchlist"] != 1 {
		t.Fatal("the watchlist was not kept")
	}
	if len(r.audits("data.reset_started")) != 1 {
		t.Fatal("start not audited")
	}
	if a := r.audits("data.reset"); len(a) != 1 || !strings.Contains(a[0].Details, res.BackupFile) || !strings.Contains(a[0].Details, "watchlists kept") {
		t.Fatalf("done audit %+v", a)
	}
	if !hasAuditAction(r.store, "backup.run", "trigger reset") {
		t.Fatal("the backup was not taken with the reset trigger")
	}
	evs, _ := r.store.Events().List(r.ctx, nil, 10)
	if len(evs) == 0 || evs[0].Kind != "data.reset" || evs[0].Severity != domain.SeverityWarning {
		t.Fatalf("events %+v", evs)
	}
	ms, _ := r.store.Outbox().List(r.ctx, 50)
	to := map[string]int{}
	for _, m := range ms {
		if strings.Contains(m.Subject, "All trading data was reset") {
			to[m.To]++
		}
	}
	if to["admin@example.com"] != 1 || to["owner2@example.com"] != 1 || to["bystander@example.com"] != 0 {
		t.Fatalf("mails %v", to)
	}
	if v, err := r.store.Settings().Get(r.ctx, domain.HistorySinceKey(r.a.ID)); err != nil || v != res.At.Format(time.RFC3339Nano) {
		t.Fatalf("history floor %q %v", v, err)
	}
	// The evidence is gone: the real account needs new demo days.
	pv, err := r.svc.DataResetPreview(r.ctx, r.admin)
	if err != nil || len(pv.RealAccounts) != 1 || pv.RealAccounts[0].DaysNow != 0 {
		t.Fatalf("after: %+v %v", pv.RealAccounts, err)
	}
	// The final reconciliation ran: the accounts are reconciled again.
	if !r.svc.Reconciled(r.a.ID).OK {
		t.Fatalf("not reconciled again: %+v", r.svc.Reconciled(r.a.ID))
	}
}

// hasAuditAction reports an audit entry of action whose details contain s.
func hasAuditAction(st *fakes.Store, action, s string) bool {
	for _, a := range st.AuditEntries() {
		if a.Action == action && strings.Contains(a.Details, s) {
			return true
		}
	}
	return false
}

// TestResetAllDataUnreconciles: after the wipe every account is unreconciled; with the broker
// down the final reconciliation fails, the agent cannot start until a clean reconciliation.
func TestResetAllDataUnreconciles(t *testing.T) {
	r := newResetEnv(t)
	l := domain.DefaultRiskLimits
	l.TradingBudget = 1_000_00
	if _, err := r.svc.SetRiskLimits(r.ctx, r.admin, l); err != nil {
		t.Fatal(err)
	}
	r.store.OnReset = func() { r.b.ReadErr = errors.New("eToro is down") }
	if _, err := r.reset(t); err != nil {
		t.Fatal(err)
	}
	if rc := r.svc.Reconciled(r.a.ID); rc.OK {
		t.Fatalf("reconciled after the reset with the broker down: %+v", rc)
	}
	if _, err := r.svc.StartAgent(r.ctx, r.admin, r.a.ID); err == nil {
		t.Fatal("started while unreconciled")
	}
	r.b.ReadErr = nil
	if _, err := r.svc.ReconcileAll(r.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.StartAgent(r.ctx, r.admin, r.a.ID); err != nil {
		t.Fatalf("start after a clean reconciliation: %v", err)
	}
}

// TestResetAllDataGateNotInForce: a gate stored armed with an arming older than the reset is
// not in force (data_reset.at counts like restore.at), and the in-memory latch holds it too.
func TestResetAllDataGateNotInForce(t *testing.T) {
	r := newResetEnv(t)
	r.clock.Advance(time.Minute)
	res, err := r.reset(t)
	if err != nil {
		t.Fatal(err)
	}
	// Another process (or a stale write) stores the gate armed as of before the reset.
	armed := domain.LockedGate(r.real.ID).Arm(r.a.ID, r.admin.UserID, domain.GateEvidence{}, res.At.Add(-time.Second))
	if _, err := r.store.RealGates().Put(r.ctx, armed); err != nil {
		t.Fatal(err)
	}
	if r.svc.realGateArmed(r.ctx, r.real) {
		t.Fatal("a gate armed before the reset is in force")
	}
	at, err := r.svc.restoredAt(r.ctx)
	if err != nil || !at.Equal(res.At) {
		t.Fatalf("restoredAt = %s %v", at, err)
	}
	if err := r.store.Settings().Set(r.ctx, domain.SettingDataResetAt, "garbage", r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.restoredAt(r.ctx); err == nil {
		t.Fatal("an unreadable reset time was accepted")
	}
}

// TestResetAllDataLocksTheArmedGate: a gate stored armed but no longer in force (expired) is
// locked by the reset, which goes ahead.
func TestResetAllDataLocksAnExpiredGate(t *testing.T) {
	r := newResetEnv(t)
	r.arm(t)
	r.clock.Advance(domain.RealGateTTL + time.Hour)
	if _, err := r.svc.ReconcileAll(r.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reset(t); err != nil {
		t.Fatal(err)
	}
	if g := r.gate(t); g.State != domain.GateLocked {
		t.Fatalf("gate %+v", g)
	}
}

// TestResetAllDataDuringTheReset: while the reset runs (here: during its backup), a start,
// an automatic start and an arming are refused; a kill completes; the reset then finishes.
func TestResetAllDataDuringTheReset(t *testing.T) {
	r := newResetEnv(t)
	l := domain.DefaultRiskLimits
	l.TradingBudget = 1_000_00
	if _, err := r.svc.SetRiskLimits(r.ctx, r.admin, l); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var startErr, armErr, killErr error
	autoStarted := true
	r.backups.OnWrite = func() {
		once.Do(func() {
			_, startErr = r.svc.StartAgent(r.ctx, r.admin, r.a.ID)
			autoStarted = r.svc.autoStart(r.ctx, r.a)
			_, armErr = r.svc.ArmReal(r.ctx, r.admin, r.real.ID, r.a.ID, domain.ArmPhrase(r.real.Name), adminPassword)
			done := make(chan error, 1)
			go func() {
				_, err := r.svc.Kill(r.ctx, r.admin, r.a.ID, "during the reset")
				done <- err
			}()
			select {
			case killErr = <-done:
			case <-time.After(10 * time.Second):
				killErr = errors.New("the kill waited for the reset")
			}
		})
	}
	if _, err := r.reset(t); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(startErr, domain.ErrConflict) || !strings.Contains(startErr.Error(), msgResetting) {
		t.Fatalf("start during the reset: %v", startErr)
	}
	if autoStarted {
		t.Fatal("autonomy started an agent during the reset")
	}
	if !errors.Is(armErr, domain.ErrConflict) || !strings.Contains(armErr.Error(), msgResetting) {
		t.Fatalf("arm during the reset: %v", armErr)
	}
	if killErr != nil {
		t.Fatalf("kill during the reset: %v", killErr)
	}
	if st := r.svc.agentState(r.ctx, r.a.ID); st != domain.AgentKilled {
		t.Fatalf("agent %s", st)
	}
	if rs, err := r.store.RiskStates().Get(r.ctx, r.a.ID); err != nil || !rs.State.Killed {
		t.Fatalf("the reset cleared the kill: %+v %v", rs, err)
	}
	// After the reset, a start is no longer refused for the reset.
	if _, err := r.svc.StartAgent(r.ctx, r.admin, r.a.ID); err != nil && strings.Contains(err.Error(), msgResetting) {
		t.Fatalf("still resetting: %v", err)
	}
}

// TestResetAllDataOneAtATime: a second reset while one runs is refused.
func TestResetAllDataOneAtATime(t *testing.T) {
	r := newResetEnv(t)
	var once sync.Once
	var second error
	r.backups.OnWrite = func() {
		once.Do(func() { _, second = r.reset(t) })
	}
	if _, err := r.reset(t); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(second, domain.ErrConflict) {
		t.Fatalf("second reset: %v", second)
	}
}

// TestResetAllDataBusyAccount: an account whose control lock stays held is not waited for
// longer than dataResetLockWait: the reset is refused and nothing is wiped.
func TestResetAllDataBusyAccount(t *testing.T) {
	r := newResetEnv(t)
	locks := r.svc.rt.agent(r.real.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	_, err := r.reset(t)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err = %v", err)
	}
	if r.store.ResetCalls != 0 {
		t.Fatal("wiped without every lock")
	}
}

// sinceBroker records the since of every trade-history read.
type sinceBroker struct {
	ports.Broker
	mu    sync.Mutex
	since []time.Time
}

func (b *sinceBroker) TradeHistory(ctx context.Context, since time.Time) ([]domain.ClosedTrade, error) {
	b.mu.Lock()
	b.since = append(b.since, since)
	b.mu.Unlock()
	return b.Broker.TradeHistory(ctx, since)
}

// TestSyncTradesAfterReset: after a reset the history is read from the reset on (OD38), not
// 90 days back, also when an older stored close would start earlier.
func TestSyncTradesAfterReset(t *testing.T) {
	r := newResetEnv(t)
	sb := &sinceBroker{Broker: r.svc.d.Broker}
	r.svc.d.Broker = sb
	r.clock.Advance(time.Minute)
	res, err := r.reset(t)
	if err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(time.Hour)
	out, err := r.svc.SyncTrades(r.ctx, r.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Since.Equal(res.At) || len(sb.since) == 0 || !sb.since[len(sb.since)-1].Equal(res.At) {
		t.Fatalf("since %s, asked %v, want %s", out.Since, sb.since, res.At)
	}
	if err := r.store.Settings().Set(r.ctx, domain.HistorySinceKey(r.a.ID), "garbage", r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.SyncTrades(r.ctx, r.a.ID); err == nil {
		t.Fatal("an unreadable floor read the history")
	}
}
