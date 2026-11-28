package storetest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// resetWipedTables are the tables a data reset empties (PLAN R49.2).
var resetWipedTables = []string{"orders", "agent_positions", "trades", "decisions", "llm_calls", "equity_snapshots", "events", "reviews"}

// runReset fills every table, then resets: a failure halfway changes nothing; a reset
// empties the wiped tables, keeps every other row (settings minus the derived keys), locks an
// armed gate and stamps the history floors; asked, it empties the watchlists too.
func runReset(t *testing.T, s ports.Store, ownerID string, t0 time.Time) {
	t.Run("data_reset", func(t *testing.T) {
		ctx := context.Background()
		dr := s.DataReset()
		fillEveryTable(t, s, ownerID, t0)
		before, err := dr.Counts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for name, n := range before {
			if n == 0 {
				t.Errorf("table %s is empty: the suite must fill every table", name)
			}
		}
		as, err := s.Accounts().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(as))
		for _, a := range as {
			ids = append(ids, a.ID)
		}
		at := t0.Add(48 * time.Hour)

		// A failure halfway (an unknown account's floor, after the deletes) rolls back.
		if _, err := dr.Reset(ctx, ports.ResetSpec{At: at, WipeWatchlists: true, AccountIDs: append(append([]string(nil), ids...), "no-such-account")}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("failing reset = %v", err)
		}
		if _, err := dr.Reset(ctx, ports.ResetSpec{}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("reset without a time = %v", err)
		}
		if after, err := dr.Counts(ctx); err != nil || !maps.Equal(before, after) {
			t.Fatalf("a failed reset changed rows:\nbefore %v\nafter  %v (%v)", before, after, err)
		}
		if g, err := s.RealGates().Get(ctx, "acc-reset-real"); err != nil || g.State != domain.GateArmed {
			t.Fatalf("a failed reset touched the gate: %+v %v", g, err)
		}
		if v, err := s.Settings().Get(ctx, "research.state.acc-reset"); err != nil || v != "x" {
			t.Fatalf("a failed reset deleted a setting: %q %v", v, err)
		}

		got, err := dr.Reset(ctx, ports.ResetSpec{At: at, AccountIDs: ids})
		if err != nil {
			t.Fatal(err)
		}
		after, err := dr.Counts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range resetWipedTables {
			if after[name] != 0 || got[name] != before[name] {
				t.Errorf("%s: %d left, %d reported deleted of %d", name, after[name], got[name], before[name])
			}
		}
		if got["real_gates_locked"] != 1 || got["settings"] != 3 {
			t.Errorf("reported %v", got)
		}
		for name, n := range before {
			switch {
			case slices.Contains(resetWipedTables, name):
			case name == "settings":
				if want := n - 3 + 1 + int64(len(ids)); after[name] != want {
					t.Errorf("settings = %d, want %d", after[name], want)
				}
			case after[name] != n:
				t.Errorf("kept table %s: %d → %d", name, n, after[name])
			}
		}
		st := s.Settings()
		for k, want := range map[string]string{
			"agent.aggressiveness_adapt.acc-reset": "", "research.state.acc-reset": "", "events.seen." + ownerID: "",
			"agent.aggressivenessXadapt.acc-reset": "kept", // "_" is escaped, not a wildcard
			"watchlist.signals.acc-reset":          "s",    // goes with the watchlist only
			"agent.state.acc-reset":                "stopped",
			domain.SettingDataResetAt:              at.Format(time.RFC3339Nano),
			domain.HistorySinceKey("acc-reset"):    at.Format(time.RFC3339Nano),
		} {
			v, err := st.Get(ctx, k)
			switch {
			case want == "" && !errors.Is(err, domain.ErrNotFound):
				t.Errorf("%s survived: %q %v", k, v, err)
			case want != "" && (err != nil || v != want):
				t.Errorf("%s = %q %v, want %q", k, v, err, want)
			}
		}
		for _, id := range ids {
			if _, err := st.Get(ctx, domain.HistorySinceKey(id)); err != nil {
				t.Errorf("no history floor for %s: %v", id, err)
			}
		}
		g, err := s.RealGates().Get(ctx, "acc-reset-real")
		if err != nil || g.State != domain.GateLocked || g.DisarmReason != domain.DisarmDataReset || g.Version != 2 ||
			!g.DisarmedAt.Equal(at) || g.DisarmedBy != "" || g.ArmedBy != ownerID {
			t.Fatalf("gate after the reset = %+v %v", g, err)
		}
		if rs, err := s.RiskStates().Get(ctx, "acc-reset"); err != nil || !rs.State.Killed || rs.State.HighWaterMark != 12_000_00 {
			t.Fatalf("risk state after the reset = %+v %v", rs, err)
		}

		// Again, with the watchlists: they and their signals go; data_reset.at is replaced.
		at2 := at.Add(time.Hour)
		got, err = dr.Reset(ctx, ports.ResetSpec{At: at2, WipeWatchlists: true, AccountIDs: ids})
		if err != nil {
			t.Fatal(err)
		}
		after2, _ := dr.Counts(ctx)
		if after2["watchlist"] != 0 || got["watchlist"] != after["watchlist"] || got["settings"] != 1 || got["real_gates_locked"] != 0 {
			t.Fatalf("watchlist reset: counts %v, reported %v", after2, got)
		}
		if after2["settings"] != after["settings"]-1 {
			t.Fatalf("settings %d → %d", after["settings"], after2["settings"])
		}
		if v, err := st.Get(ctx, domain.SettingDataResetAt); err != nil || v != at2.Format(time.RFC3339Nano) {
			t.Fatalf("data_reset.at = %q %v", v, err)
		}
		if _, err := st.Get(ctx, "watchlist.signals.acc-reset"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("signals survived the watchlist: %v", err)
		}

		// The suite leaves every table filled (the backup tests dump what Run wrote).
		fillTradingData(t, s, "acc-reset", ownerID, t0)
		refilled, _ := dr.Counts(ctx)
		for name, n := range refilled {
			if n == 0 {
				t.Errorf("table %s is empty after the refill", name)
			}
		}
	})
}

// mustDo fails the test on err, naming what was written.
func mustDo(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// fillEveryTable writes at least one row into every table of the schema, for accounts of a
// new user (acc-reset, demo; acc-reset-real, real money with an armed gate).
func fillEveryTable(t *testing.T, s ports.Store, ownerID string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	must := func(what string, err error) { t.Helper(); mustDo(t, what, err) }
	u := domain.User{ID: "u-reset", Username: "resetter", Role: domain.RoleOperator, PasswordHash: "h", CreatedAt: t0, UpdatedAt: t0}
	must("user", s.Users().Create(ctx, u))
	demo := domain.Account{ID: "acc-reset", Name: "Reset demo", OwnerID: u.ID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
	realAcct := domain.Account{ID: "acc-reset-real", Name: "Reset real", OwnerID: u.ID, Mode: domain.ModeReal, CreatedAt: t0, UpdatedAt: t0}
	for _, a := range []domain.Account{demo, realAcct} {
		must("account", s.Accounts().Create(ctx, a))
	}
	must("share", s.Accounts().SetShare(ctx, domain.AccountShare{AccountID: demo.ID, UserID: ownerID, Access: domain.AccessView, CreatedAt: t0}))
	must("session", s.Sessions().Create(ctx, domain.Session{TokenHash: "h-reset", UserID: u.ID, CSRFToken: "c", CreatedAt: t0, LastSeen: t0, ExpiresAt: t0.Add(time.Hour)}))
	must("api key", s.APIKeys().Create(ctx, domain.APIKey{ID: "k-reset", OwnerID: u.ID, Name: "x", Role: domain.RoleViewer, SecretHash: "s-reset", CreatedAt: t0}))
	must("secret", s.Secrets().Put(ctx, ports.SecretRecord{Name: "reset_secret", Ciphertext: []byte{1, 2, 3}, UpdatedAt: t0}))
	must("audit", s.Audit().Append(ctx, domain.AuditEntry{At: t0, Actor: "system", Action: "test.reset"}))
	must("setting", s.Settings().Set(ctx, "agent.aggressivenessXadapt.acc-reset", "kept", t0))
	must("setting", s.Settings().Set(ctx, "agent.state.acc-reset", "stopped", t0))
	aapl := domain.Instrument{ID: 1001, Symbol: "AAPL", Name: "Apple", Type: "Stocks", ExchangeID: 4, Market: domain.MarketNYSE, Currency: "USD"}
	must("instrument", s.Instruments().Upsert(ctx, aapl, domain.Eligibility{Instrument: 1001, Symbol: "AAPL", RealLong: true, CheckedAt: t0}, t0))
	must("theme", s.Themes().Create(ctx, domain.Theme{ID: "th-reset", AccountID: demo.ID, Name: "Reset", Instruments: []domain.InstrumentID{1001}, CreatedAt: t0, UpdatedAt: t0}))
	must("outbox", s.Outbox().Enqueue(ctx, domain.OutboxMail{Mail: domain.Mail{
		ID: "m-reset", To: "a@example.com", Subject: "s", Body: "b",
		Attachments: []domain.Attachment{{Filename: "r.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4\n")}},
	}, CreatedAt: t0, NextAt: t0}))
	must("password reset", s.PasswordResets().Create(ctx, domain.PasswordReset{TokenHash: strings.Repeat("r", 64), UserID: u.ID, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}))
	_, err := s.RiskStates().Put(ctx, domain.AccountRiskState{
		AccountID: demo.ID, UpdatedAt: t0, KillReason: "test", KilledAt: t0,
		State: domain.RiskState{Day: domain.RiskDay(t0), StartOfDayEquity: 10_000_00, HighWaterMark: 12_000_00, Killed: true},
	})
	must("risk state", err)
	must("block", s.InstrumentBlocks().Block(ctx, domain.InstrumentBlock{AccountID: demo.ID, Instrument: 1001, Reason: "x", At: t0}))
	must("lease", s.AgentLeases().Acquire(ctx, demo.ID, "proc-reset", t0, t0.Add(domain.LeaseTTL)))
	_, err = s.RealGates().Put(ctx, domain.LockedGate(realAcct.ID).Arm(demo.ID, ownerID, domain.GateEvidence{}, t0))
	must("gate", err)
	must("schedule", s.StatementSchedules().Create(ctx, domain.StatementSchedule{
		ID: "sch-reset", OwnerID: u.ID, Period: domain.PeriodDaily, AccountIDs: []string{demo.ID}, Recipients: []string{"r@example.com"}, CreatedAt: t0, UpdatedAt: t0,
	}))
	must("t212 catalogue", s.T212Instruments().ReplaceAll(ctx, catalogue(3, "R"), t0))
	must("match", s.InstrumentMatches().Put(ctx, domain.InstrumentMatch{AccountID: demo.ID, Instrument: 1001, Ticker: "AAPL_US_EQ", UpdatedAt: t0}))
	fillTradingData(t, s, demo.ID, ownerID, t0)
}

// fillTradingData writes a row into every table a reset wipes, the watchlist and the
// derived settings of account acct. It can run again after a reset, so the suite leaves
// every table filled (the backup tests dump and restore it).
func fillTradingData(t *testing.T, s ports.Store, acct, ownerID string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	must := func(what string, err error) { t.Helper(); mustDo(t, what, err) }
	for k, v := range map[string]string{
		"agent.aggressiveness_adapt." + acct: "x", "research.state." + acct: "x", "events.seen." + ownerID: "x", "watchlist.signals." + acct: "s",
	} {
		must("setting", s.Settings().Set(ctx, k, v, t0))
	}
	must("watchlist", s.Watchlist().Add(ctx, acct, 1001, "resetter", t0))
	must("event", s.Events().Append(ctx, domain.Event{ID: "e-reset", At: t0, Kind: "order.filled", Severity: domain.SeverityInfo, Title: "x", AccountID: acct}))
	must("review", s.Reviews().Upsert(ctx, domain.Review{AccountID: acct, Instrument: 1001, Symbol: "AAPL", At: t0, Outcome: domain.ReviewNoSignal, Summary: "x"}))
	must("equity", s.EquitySnapshots().Add(ctx, domain.EquitySnapshot{AccountID: acct, At: t0, Currency: "USD", Cash: 1, Equity: 2}))
	must("order", s.Orders().Create(ctx, newOpen(t, "req-reset", acct, domain.Long, t0)))
	must("position", s.AgentPositions().Add(ctx, domain.AgentPosition{AccountID: acct, PositionID: 77, OpenRequestID: "req-reset", Instrument: 1001, Direction: domain.Long, OpenedAt: t0}))
	must("llm call", s.LLMCalls().Add(ctx, domain.LLMCall{ID: "call-reset", AccountID: acct, Purpose: domain.LLMPurposeAgent, CostMicros: domain.CostUnknown, At: t0}))
	must("decision", s.Decisions().Add(ctx, domain.Decision{
		ID: "dec-reset", AccountID: acct, At: t0, Outcome: domain.DecisionPending,
		Signal: domain.DecisionSignal{Instrument: 1001, Symbol: "AAPL", Direction: domain.Long, Playbook: "trend_breakout", Reason: "x", Entry: 2, StopLoss: 1, TakeProfit: 3, SuggestedSize: 1},
	}))
	must("trade", s.Trades().Upsert(ctx, domain.Trade{
		AccountID: acct, Mode: domain.ModeDemo, SyncedAt: t0,
		ClosedTrade: domain.ClosedTrade{PositionID: 77, Instrument: 1001, OpenedAt: t0.Add(-time.Hour), ClosedAt: t0, Long: true, Leverage: 1},
	}))
}
