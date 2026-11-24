package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// stmtEnv is an admin with an eToro demo account ("Main demo"), a statement renderer that
// records what it is given, a mail server (the deployment's fallback) and a fake mailer. The
// clock stands at Monday 2026-11-23 09:00 UTC.
type stmtEnv struct {
	*env
	ctx    context.Context
	r      *fakes.StatementRenderer
	mailer *fakes.Mailer
	a      domain.Account
}

func newStmtEnv(t *testing.T) *stmtEnv {
	t.Helper()
	e, _ := withBroker(t)
	s := &stmtEnv{env: e, ctx: context.Background(), r: &fakes.StatementRenderer{}, mailer: &fakes.Mailer{}}
	e.svc.d.Statements, e.svc.d.Mailer = s.r, s.mailer
	e.svc.d.MailFallback = func() (FallbackMail, bool) {
		return FallbackMail{Server: domain.SMTPSettings{Enabled: true, Host: "mail.example.com", Port: 587, TLS: domain.SMTPStartTLS, From: "ct@example.com"}}, true
	}
	v, err := e.svc.CurrentAccount(s.ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	s.a = v.Account
	return s
}

func weekly(recipients ...string) StatementScheduleInput {
	return StatementScheduleInput{Period: domain.PeriodWeekly, Recipients: recipients, IncludeCombined: true, IncludeAccounts: true, IncludeExchanges: true, Enabled: true}
}

func (s *stmtEnv) outbox(t *testing.T) []domain.OutboxMail {
	t.Helper()
	ms, err := s.store.Outbox().List(s.ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func at(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.UTC) }

// seed stores a week of records on Main demo and a Trading 212 ISA (GBP) for the week of
// 23–27 November 2026 (risk days from Sunday 22 November 22:00 UTC to Friday 27 22:00 UTC).
func (s *stmtEnv) seed(t *testing.T) domain.AccountView {
	t.Helper()
	ctx, st, id := s.ctx, s.store, s.a.ID
	for _, in := range []domain.Instrument{{ID: 1, Symbol: "AAPL", Market: domain.MarketNYSE}, {ID: 2, Symbol: "BARC.L"}} {
		if err := st.Instruments().Upsert(ctx, in, domain.Eligibility{Instrument: in.ID}, at(1, 0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	trade := func(pos int64, ins domain.InstrumentID, closed time.Time, net, fees domain.Cents, known, ours bool) {
		tr := domain.Trade{AccountID: id, Mode: domain.ModeDemo, Ours: ours, SyncedAt: closed, ClosedTrade: domain.ClosedTrade{
			PositionID: pos, Instrument: ins, ClosedAt: closed, NetProfit: net, Fees: fees, FeesKnown: known, Long: true, Leverage: 1,
		}}
		if err := st.Trades().Upsert(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	trade(1, 1, at(24, 15, 0), 30_00, 1_00, true, true)
	trade(2, 2, at(25, 10, 0), -10_00, 0, false, true) // fees unknown; BARC.L → LSE by its suffix
	trade(3, 1, at(25, 16, 0), 99_00, 0, true, false)  // not opened by cactusTrade
	trade(4, 1, at(28, 10, 0), 5_00, 0, true, true)    // Saturday: after the week
	trade(5, 1, at(22, 21, 59), 5_00, 0, true, true)   // before the week
	snap := func(acct string, when time.Time, cur string, eq domain.Cents) {
		if err := st.EquitySnapshots().Add(ctx, domain.EquitySnapshot{AccountID: acct, At: when, Currency: cur, Equity: eq}); err != nil {
			t.Fatal(err)
		}
	}
	snap(id, at(23, 9, 0), "USD", 10_000_00)
	snap(id, at(27, 21, 0), "USD", 10_020_00)
	snap(id, at(27, 22, 0), "USD", 99) // at the end: the next week
	isa, err := s.svc.CreateAccountOn(ctx, s.admin, domain.PlatformTrading212, "ISA", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	snap(isa.ID, at(23, 9, 0), "GBP", 1_000_00)
	snap(isa.ID, at(26, 9, 0), "GBP", 1_050_00)
	s.svc.d.Holdings = map[domain.Platform]ports.Holdings{domain.PlatformTrading212: &fakes.Holdings{
		ByAccount: map[string]domain.Holdings{isa.ID: {Platform: domain.PlatformTrading212, Currency: "GBP", Equity: 1_060_00}},
	}}
	for _, p := range []domain.AgentPosition{
		{AccountID: id, PositionID: 9, OpenRequestID: "r9", Instrument: 1, Direction: domain.Long, OpenedAt: at(27, 20, 0)},
		{AccountID: id, PositionID: 8, OpenRequestID: "r8", Instrument: 1, Direction: domain.Long, OpenedAt: at(26, 9, 0), ClosedAt: at(26, 15, 0)},
		{AccountID: id, PositionID: 7, OpenRequestID: "r7", Instrument: 2, Direction: domain.Short, OpenedAt: at(27, 19, 0), ClosedAt: at(28, 1, 0)},
	} {
		if err := st.AgentPositions().Add(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for i, d := range []domain.Decision{
		{ID: "d1", AccountID: id, At: at(24, 14, 0), Outcome: domain.DecisionRejectedRisk, Signal: domain.DecisionSignal{Direction: domain.Long}},
		{ID: "d2", AccountID: id, At: at(25, 14, 0), Outcome: domain.DecisionRejectedCost, Signal: domain.DecisionSignal{Direction: domain.Long}},
		{ID: "d3", AccountID: id, At: at(28, 14, 0), Outcome: domain.DecisionRejectedCost, Signal: domain.DecisionSignal{Direction: domain.Long}},
	} {
		if err := st.Decisions().Add(ctx, d); err != nil {
			t.Fatalf("decision %d: %v", i, err)
		}
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: "req-close", AccountID: id, Mode: domain.ModeDemo, Kind: domain.OrderClose, Direction: domain.Long,
		Instrument: 1, PositionID: 1,
	}, at(24, 14, 59))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Orders().Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, e := range []domain.Event{
		{ID: "e1", At: at(24, 16, 0), Kind: domain.EventKillSwitch, AccountID: id},
		{ID: "e2", At: at(25, 16, 0), Kind: domain.EventDailyStop, AccountID: id},
		{ID: "e3", At: at(20, 16, 0), Kind: domain.EventKillSwitch, AccountID: id}, // the week before
		{ID: "e4", At: at(24, 16, 0), Kind: domain.EventKillSwitch},                // system-wide
	} {
		if err := st.Events().Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.LLMCalls().Add(ctx, domain.LLMCall{ID: "c1", AccountID: id, Purpose: domain.LLMPurposeAgent, CostMicros: 5_000, At: at(24, 14, 0)}); err != nil {
		t.Fatal(err)
	}
	return isa
}

// TestStatementFiguresFromFixtures: the statement is built from the stored records of the
// period only: currencies apart, unknown fees apart, others' trades apart, positions open at
// the end, risk events, decisions, orders and LLM calls of the account.
func TestStatementFiguresFromFixtures(t *testing.T) {
	s := newStmtEnv(t)
	s.seed(t)
	s.clock.T = at(28, 7, 0)
	f, err := s.svc.PreviewStatement(s.ctx, s.admin, StatementPreviewRequest{Period: domain.PeriodWeekly, From: "2026-11-24", Scope: domain.ScopeCombined})
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "cactusTrade-statement-2026-w48-weekly-combined.pdf" || !strings.HasPrefix(string(f.Data), "%PDF-fake Combined statement") {
		t.Fatalf("file %q %q", f.Name, f.Data)
	}
	all := s.r.All()
	st := all[len(all)-1]
	tr := st.Trades
	if tr.Count != 2 || tr.Wins != 1 || tr.Losses != 1 || tr.Net != 20_00 || tr.Fees != 1_00 || tr.FeesUnknown != 1 || st.ForeignTrades != 1 {
		t.Fatalf("trades %+v foreign %d", tr, st.ForeignTrades)
	}
	if len(st.ByExchange) != 2 || st.ByExchange[0].Name != "LSE" || st.ByExchange[1].Name != "NYSE" {
		t.Fatalf("exchanges %+v", st.ByExchange)
	}
	if len(st.EquityTotals) != 2 || st.EquityTotals[0].Currency != "GBP" || st.EquityTotals[0].Closing != 1_050_00 ||
		st.EquityTotals[1].Currency != "USD" || st.EquityTotals[1].Opening != 10_000_00 || st.EquityTotals[1].Closing != 10_020_00 {
		t.Fatalf("equity totals (never summed across currencies) %+v", st.EquityTotals)
	}
	var ids []int64
	for _, p := range st.OpenPositions {
		ids = append(ids, p.PositionID)
	}
	if !slices.Equal(ids, []int64{7, 9}) || st.OpenPositions[0].Symbol != "BARC.L" || st.OpenPositions[0].Market != domain.MarketLSE {
		t.Fatalf("open positions %+v", st.OpenPositions)
	}
	if st.KillSwitches != 1 || st.DailyStops != 1 || st.DecisionsTotal != 2 || st.OrdersTotal != 1 || st.LLM.Calls != 1 || st.LLM.Cost != 5_000 {
		t.Fatalf("activity kill %d stop %d decisions %d orders %d llm %+v", st.KillSwitches, st.DailyStops, st.DecisionsTotal, st.OrdersTotal, st.LLM)
	}
	if len(st.Holdings) != 1 || st.Holdings[0].Holdings == nil || st.Holdings[0].Holdings.Equity != 1_060_00 {
		t.Fatalf("holdings %+v", st.Holdings)
	}

	// One account; one exchange.
	f, err = s.svc.PreviewStatement(s.ctx, s.admin, StatementPreviewRequest{Period: domain.PeriodWeekly, From: "2026-11-24", Scope: domain.ScopeAccount, AccountID: s.a.ID})
	if err != nil || f.Name != "cactusTrade-statement-2026-w48-weekly-account-main-demo.pdf" {
		t.Fatalf("account preview %q %v", f.Name, err)
	}
	if st := s.r.All()[len(s.r.All())-1]; len(st.Accounts) != 1 || st.Trades.Count != 2 {
		t.Fatalf("account statement %+v", st.Accounts)
	}
	if f, err = s.svc.PreviewStatement(s.ctx, s.admin, StatementPreviewRequest{Period: domain.PeriodWeekly, From: "2026-11-24", Scope: domain.ScopeExchange, Exchange: "LSE"}); err != nil ||
		f.Name != "cactusTrade-statement-2026-w48-weekly-exchange-lse.pdf" {
		t.Fatalf("exchange preview %q %v", f.Name, err)
	}
	if st := s.r.All()[len(s.r.All())-1]; st.Trades.Count != 1 || st.Trades.Losses != 1 || len(st.OpenPositions) != 1 {
		t.Fatalf("LSE statement %+v", st.Trades)
	}
	// The default is the last complete period (Saturday 07:00: this week).
	if f, err = s.svc.PreviewStatement(s.ctx, s.admin, StatementPreviewRequest{Period: domain.PeriodWeekly, Scope: domain.ScopeCombined}); err != nil || !strings.Contains(f.Name, "2026-w48") {
		t.Fatalf("default preview %q %v", f.Name, err)
	}
	// The daily statement of Friday.
	if _, err = s.svc.PreviewStatement(s.ctx, s.admin, StatementPreviewRequest{Period: domain.PeriodDaily, From: "2026-11-25", Scope: domain.ScopeCombined}); err != nil {
		t.Fatal(err)
	}
	if st := s.r.All()[len(s.r.All())-1]; st.Trades.Count != 1 || st.ForeignTrades != 1 || st.DailyStops != 1 || st.KillSwitches != 0 {
		t.Fatalf("daily %+v", st)
	}

	for name, req := range map[string]StatementPreviewRequest{
		"period":       {Period: "yearly", Scope: domain.ScopeCombined},
		"date":         {Period: domain.PeriodDaily, From: "25/11/2026", Scope: domain.ScopeCombined},
		"future":       {Period: domain.PeriodDaily, From: "2026-12-25", Scope: domain.ScopeCombined},
		"too old":      {Period: domain.PeriodDaily, From: "2024-01-02", Scope: domain.ScopeCombined},
		"scope":        {Period: domain.PeriodDaily, Scope: "all"},
		"exchange":     {Period: domain.PeriodDaily, Scope: domain.ScopeExchange, Exchange: "XETRA"},
		"account gone": {Period: domain.PeriodDaily, Scope: domain.ScopeAccount, AccountID: "nope"},
	} {
		if _, err := s.svc.PreviewStatement(s.ctx, s.admin, req); !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	s.svc.d.Statements = nil
	if _, err := s.svc.PreviewStatement(s.ctx, s.admin, StatementPreviewRequest{Period: domain.PeriodDaily, Scope: domain.ScopeCombined}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("without a renderer: %v", err)
	}
}

// TestStatementScheduleAccess is the access matrix: a user puts only accounts they own on a
// schedule (an administrator: any account they can see), manages only their own schedules
// (an administrator: every schedule), and only from the web UI.
func TestStatementScheduleAccess(t *testing.T) {
	s := newStmtEnv(t)
	ctx := s.ctx
	olivia := s.user(t, "olivia", domain.RoleOperator)
	victor := s.user(t, "victor", domain.RoleViewer)
	ov, err := s.svc.CreateAccount(ctx, olivia, "Olivia demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.svc.ShareAccount(ctx, s.admin, s.a.ID, "olivia", domain.AccessTrade); err != nil {
		t.Fatal(err)
	}
	if err := s.svc.ShareAccount(ctx, s.admin, s.a.ID, "victor", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	key := s.admin
	key.Via, key.APIKeyID = ViaAPIKey, "key-1"

	in := func(ids ...string) StatementScheduleInput {
		x := weekly("me@example.com")
		x.AccountIDs = ids
		return x
	}
	tests := []struct {
		name string
		p    Principal
		in   StatementScheduleInput
		want error
	}{
		{"admin, all accounts", s.admin, in(), nil},
		{"admin, own account", s.admin, in(s.a.ID), nil},
		{"admin, account not shared with them", s.admin, in(ov.ID), domain.ErrInvalid},
		{"operator, own account", olivia, in(ov.ID), nil},
		{"operator, account shared with trade access", olivia, in(s.a.ID), domain.ErrInvalid},
		{"viewer owning nothing", victor, in(), domain.ErrInvalid},
		{"API key", key, in(), domain.ErrForbidden},
		{"too many recipients", s.admin, StatementScheduleInput{
			Period: domain.PeriodDaily, IncludeCombined: true,
			Recipients: strings.Split("a@x.io b@x.io c@x.io d@x.io e@x.io f@x.io g@x.io h@x.io i@x.io j@x.io k@x.io", " "),
		}, domain.ErrInvalid},
		{"no statement chosen", s.admin, StatementScheduleInput{Period: domain.PeriodDaily, Recipients: []string{"a@x.io"}}, domain.ErrInvalid},
	}
	created := map[string]StatementScheduleView{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := s.svc.CreateStatementSchedule(ctx, tc.p, tc.in)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err == nil {
				created[tc.name] = v
			}
		})
	}
	if me, ok := errors.AsType[*domain.MissingError](func() error { _, err := s.svc.CreateStatementSchedule(ctx, victor, in()); return err }()); !ok || me.What != domain.MissingAccount || me.CanFix {
		t.Fatalf("viewer: %+v", me)
	}
	os := created["operator, own account"]
	if os.OwnerName != "olivia" || len(os.Accounts) != 1 || os.Accounts[0].Name != "Olivia demo" || os.LastPeriod != "2026-W47" || os.NextSendAt != at(28, 6, 0) {
		t.Fatalf("olivia's schedule %+v", os)
	}
	if all := created["admin, all accounts"]; len(all.Accounts) != 1 || all.Accounts[0].ID != s.a.ID {
		t.Fatalf("admin's accounts %+v", all.Accounts)
	}

	// Listing: own only; an administrator sees every schedule.
	if pg, err := s.svc.StatementSchedules(ctx, olivia); err != nil || len(pg.Schedules) != 1 || len(pg.Accounts) != 1 || !pg.MailServer || pg.CanFixMail {
		t.Fatalf("olivia's page %+v %v", pg, err)
	}
	if pg, err := s.svc.StatementSchedules(ctx, s.admin); err != nil || len(pg.Schedules) != 3 || !pg.CanFixMail {
		t.Fatalf("admin's page %d %v", len(pg.Schedules), err)
	}
	if pg, err := s.svc.StatementSchedules(ctx, victor); err != nil || len(pg.Schedules) != 0 || len(pg.Accounts) != 0 {
		t.Fatalf("victor's page %+v %v", pg, err)
	}
	if _, err := s.svc.StatementSchedules(ctx, key); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("API key list: %v", err)
	}

	// Someone else's schedule is not found; an administrator may manage it, but only with
	// the owner's accounts.
	upd := in(ov.ID)
	upd.Recipients = []string{"boss@example.com"}
	if _, err := s.svc.UpdateStatementSchedule(ctx, victor, os.ID, upd); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("viewer updates olivia's: %v", err)
	}
	if _, err := s.svc.SendStatementNow(ctx, victor, os.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("viewer sends olivia's: %v", err)
	}
	if err := s.svc.DeleteStatementSchedule(ctx, victor, os.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("viewer deletes olivia's: %v", err)
	}
	if _, err := s.svc.UpdateStatementSchedule(ctx, s.admin, os.ID, in(s.a.ID)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("admin puts an account olivia does not own on her schedule: %v", err)
	}
	v, err := s.svc.UpdateStatementSchedule(ctx, s.admin, os.ID, upd)
	if err != nil || v.Recipients[0] != "boss@example.com" || v.OwnerID != olivia.UserID || v.LastPeriod != os.LastPeriod {
		t.Fatalf("admin update %+v %v", v, err)
	}
	if _, err := s.svc.UpdateStatementSchedule(ctx, olivia, "nope", upd); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := s.svc.PreviewStatement(ctx, olivia, StatementPreviewRequest{Period: domain.PeriodDaily, Scope: domain.ScopeAccount, AccountID: s.a.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("olivia previews an account she does not own: %v", err)
	}
	if _, err := s.svc.PreviewStatement(ctx, key, StatementPreviewRequest{Period: domain.PeriodDaily, Scope: domain.ScopeCombined}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("API key preview: %v", err)
	}
	if err := s.svc.DeleteStatementSchedule(ctx, s.admin, os.ID); err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range s.store.AuditEntries() {
		if strings.HasPrefix(e.Action, "statement.") {
			actions = append(actions, e.Action)
		}
	}
	if strings.Join(actions, ",") != "statement.schedule_created,statement.schedule_created,statement.schedule_created,statement.schedule_updated,statement.schedule_deleted" {
		t.Fatalf("audit %v", actions)
	}
}

// TestSendDueStatementsOnce: a period is sent once, after it closes: not again on the next
// round, not after a restart, not twice when the record of it was lost after the e-mails
// were queued, and only the newest period after a long stop (a missed run sends once).
func TestSendDueStatementsOnce(t *testing.T) {
	s := newStmtEnv(t)
	s.seed(t)
	ctx := s.ctx
	v, err := s.svc.CreateStatementSchedule(ctx, s.admin, weekly("a@example.com", "b@example.com"))
	if err != nil || v.LastPeriod != "2026-W47" {
		t.Fatalf("%+v %v", v, err)
	}
	s.clock.T = at(28, 5, 59) // the week's statement is due at Saturday 06:00
	if n, err := s.svc.SendDueStatements(ctx); err != nil || n != 0 {
		t.Fatalf("before the send time: %d %v", n, err)
	}
	s.clock.T = at(28, 6, 0)
	if n, err := s.svc.SendDueStatements(ctx); err != nil || n != 2 {
		t.Fatalf("due: %d %v", n, err)
	}
	ms := s.outbox(t)
	if len(ms) != 2 || ms[0].ID != statementMailID(v.ID, "2026-W48", ms[0].To) || ms[0].Subject != "[cactusTrade] Weekly trading statement: 23–27 November 2026" {
		t.Fatalf("outbox %+v", ms)
	}
	var names []string
	for _, a := range ms[0].Attachments {
		names = append(names, a.Filename)
		if a.ContentType != "application/pdf" || !strings.HasPrefix(string(a.Data), "%PDF-fake") {
			t.Fatalf("attachment %+v", a)
		}
	}
	want := []string{
		"cactusTrade-statement-2026-w48-weekly-combined.pdf", "cactusTrade-statement-2026-w48-weekly-account-isa.pdf",
		"cactusTrade-statement-2026-w48-weekly-account-main-demo.pdf", "cactusTrade-statement-2026-w48-weekly-exchange-lse.pdf",
		"cactusTrade-statement-2026-w48-weekly-exchange-nyse.pdf",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("files %v", names)
	}
	if b := ms[0].Body; !strings.Contains(b, "cactusTrade closed 2 trades on 1 account (LSE 1, NYSE 1)") || !strings.Contains(b, want[0]) ||
		!strings.Contains(b, "not financial advice") || !strings.Contains(b, "admin set up a weekly trading statement") {
		t.Fatalf("body %s", b)
	}
	got, _ := s.store.StatementSchedules().Get(ctx, v.ID)
	if got.LastPeriod != "2026-W48" || !got.LastSentAt.Equal(at(28, 6, 0)) || got.LastError != "" {
		t.Fatalf("after the send %+v", got)
	}

	// The next round, and a restart (a new service on the same store), send nothing.
	if n, _ := s.svc.SendDueStatements(ctx); n != 0 {
		t.Fatalf("second round sent %d", n)
	}
	again, err := New(s.svc.d, s.svc.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := again.SendDueStatements(ctx); n != 0 {
		t.Fatalf("after a restart sent %d", n)
	}
	// The process stopped after queueing the e-mails but before recording the period: the
	// next round finds the same e-mails queued and only records it.
	if err := s.store.StatementSchedules().MarkPeriod(ctx, v.ID, "2026-W48", "2026-W47", at(28, 6, 0), ""); err != nil {
		t.Fatal(err)
	}
	if n, err := again.SendDueStatements(ctx); err != nil || n != 0 || len(s.outbox(t)) != 2 {
		t.Fatalf("lost record: sent %d %v, outbox %d", n, err, len(s.outbox(t)))
	}
	if got, _ := s.store.StatementSchedules().Get(ctx, v.ID); got.LastPeriod != "2026-W48" {
		t.Fatalf("not recorded again: %+v", got)
	}

	// Three weeks without the loop: only the newest week is sent, once.
	s.clock.T = at(28, 6, 0).AddDate(0, 0, 21)
	if n, err := s.svc.SendDueStatements(ctx); err != nil || n != 2 {
		t.Fatalf("after a long stop: %d %v", n, err)
	}
	if n, _ := s.svc.SendDueStatements(ctx); n != 0 || len(s.outbox(t)) != 4 {
		t.Fatalf("after a long stop, again: %d, outbox %d", n, len(s.outbox(t)))
	}
	if got, _ := s.store.StatementSchedules().Get(ctx, v.ID); got.LastPeriod != "2026-W51" {
		t.Fatalf("newest week %+v", got)
	}

	// Switched off: nothing; switched on again: from the next period.
	off := weekly("a@example.com", "b@example.com")
	off.Enabled = false
	if _, err := s.svc.UpdateStatementSchedule(ctx, s.admin, v.ID, off); err != nil {
		t.Fatal(err)
	}
	s.clock.T = s.clock.T.AddDate(0, 0, 7)
	if n, _ := s.svc.SendDueStatements(ctx); n != 0 {
		t.Fatalf("switched off sent %d", n)
	}
	if v, err := s.svc.UpdateStatementSchedule(ctx, s.admin, v.ID, weekly("a@example.com", "b@example.com")); err != nil || v.LastPeriod != "2026-W52" {
		t.Fatalf("switched on %+v %v", v, err)
	}
	if n, _ := s.svc.SendDueStatements(ctx); n != 0 {
		t.Fatalf("switched on sent a backlog: %d", n)
	}
}

// TestSendDueStatementsFailures: a renderer failure leaves the period open for the next round;
// statements too large for one e-mail, or a schedule whose accounts are all gone, are recorded
// with the reason and not retried; a disabled owner's schedule waits.
func TestSendDueStatementsFailures(t *testing.T) {
	s := newStmtEnv(t)
	ctx := s.ctx
	d := StatementScheduleInput{Period: domain.PeriodDaily, Recipients: []string{"a@example.com"}, IncludeCombined: true, IncludeAccounts: true, Enabled: true}
	v, err := s.svc.CreateStatementSchedule(ctx, s.admin, d)
	if err != nil || v.LastPeriod != "2026-11-20" { // Monday 09:00: the last complete day is Friday
		t.Fatalf("%+v %v", v, err)
	}
	s.clock.T = at(23, 22, 30)
	s.r.Err = errors.New("renderer broke")
	if n, _ := s.svc.SendDueStatements(ctx); n != 0 {
		t.Fatal("sent with a broken renderer")
	}
	if got, _ := s.store.StatementSchedules().Get(ctx, v.ID); got.LastPeriod != "2026-11-20" {
		t.Fatalf("a passing failure closed the period: %+v", got)
	}
	s.r.Err, s.r.Size = nil, 6<<20 // two files of 6 MiB: more than one e-mail carries
	if n, _ := s.svc.SendDueStatements(ctx); n != 0 || len(s.outbox(t)) != 0 {
		t.Fatal("sent statements over 10 MiB")
	}
	got, _ := s.store.StatementSchedules().Get(ctx, v.ID)
	if got.LastPeriod != "2026-11-23" || !strings.Contains(got.LastError, "too large") {
		t.Fatalf("too large: %+v", got)
	}
	s.r.Size = 0

	// Its account deleted: recorded with the reason.
	x := d
	x.AccountIDs = []string{s.a.ID}
	if _, err := s.svc.UpdateStatementSchedule(ctx, s.admin, v.ID, x); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Accounts().Delete(ctx, s.a.ID); err != nil {
		t.Fatal(err)
	}
	s.clock.T = at(24, 22, 30)
	if n, _ := s.svc.SendDueStatements(ctx); n != 0 {
		t.Fatal("sent without accounts")
	}
	if got, _ := s.store.StatementSchedules().Get(ctx, v.ID); got.LastPeriod != "2026-11-24" || !strings.Contains(got.LastError, "accounts") {
		t.Fatalf("no accounts: %+v", got)
	}

	// A disabled owner's schedule waits; without a renderer nothing runs.
	olivia := s.user(t, "olivia", domain.RoleOperator)
	if _, err := s.svc.CreateAccount(ctx, olivia, "Olivia demo", domain.ModeDemo); err != nil {
		t.Fatal(err)
	}
	ov, err := s.svc.CreateStatementSchedule(ctx, olivia, d)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := s.store.Users().Get(ctx, olivia.UserID)
	u.Disabled = true
	if err := s.store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	s.clock.T = at(25, 22, 30)
	if _, err := s.svc.SendDueStatements(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.store.StatementSchedules().Get(ctx, ov.ID); got.LastPeriod != "2026-11-24" {
		t.Fatalf("a disabled owner's schedule ran: %+v", got)
	}
	s.svc.d.Statements = nil
	if n, err := s.svc.SendDueStatements(ctx); n != 0 || err != nil {
		t.Fatalf("without a renderer: %d %v", n, err)
	}
}

// TestSendStatementNow: needs a mail server; sends the last complete period with fresh ids
// (whatever the schedule sent before) and leaves the schedule's own record alone; at most
// once a minute per schedule.
func TestSendStatementNow(t *testing.T) {
	s := newStmtEnv(t)
	s.seed(t)
	ctx := s.ctx
	v, err := s.svc.CreateStatementSchedule(ctx, s.admin, weekly("a@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	fb := s.svc.d.MailFallback
	s.svc.d.MailFallback = nil
	_, err = s.svc.SendStatementNow(ctx, s.admin, v.ID)
	if me, ok := errors.AsType[*domain.MissingError](err); !ok || me.What != domain.MissingEmailServer || !me.CanFix || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no mail server: %v", err)
	}
	s.svc.d.MailFallback = fb
	res, err := s.svc.SendStatementNow(ctx, s.admin, v.ID)
	if err != nil || res.Recipients != 1 || res.Window.Key != "2026-W47" || len(res.Files) != 3 { // the week before: combined, ISA, Main demo; no exchange had trades
		t.Fatalf("send now %+v %v", res, err)
	}
	if _, err := s.svc.SendStatementNow(ctx, s.admin, v.ID); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("twice in a minute: %v", err)
	}
	s.clock.Advance(time.Minute)
	if res, err := s.svc.SendStatementNow(ctx, s.admin, v.ID); err != nil || res.Recipients != 1 {
		t.Fatalf("a minute later %+v %v", res, err)
	}
	ms := s.outbox(t)
	if len(ms) != 2 || ms[0].ID == ms[1].ID {
		t.Fatalf("outbox %+v", ms)
	}
	if got, _ := s.store.StatementSchedules().Get(ctx, v.ID); got.LastPeriod != v.LastPeriod || !got.LastSentAt.IsZero() {
		t.Fatalf("send now changed the schedule's record: %+v", got)
	}
	if n := len(s.store.AuditEntries()); s.store.AuditEntries()[n-1].Action != "statement.sent_now" {
		t.Fatal("send now not audited")
	}

	// The outbox delivers the e-mail with its PDFs.
	if n, err := s.svc.DeliverMail(ctx); err != nil || n != 2 {
		t.Fatalf("DeliverMail = %d %v", n, err)
	}
	if sent := s.mailer.Sent; len(sent) != 2 || len(sent[0].Attachments) != 3 || sent[0].Attachments[0].ContentType != "application/pdf" {
		t.Fatalf("mailer got %+v", sent)
	}

	s.svc.d.Statements = nil
	if _, err := s.svc.SendStatementNow(ctx, s.admin, v.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("without a renderer: %v", err)
	}
}

// TestStatementsOutboxRefusesUnsafeAttachments: the outbox itself refuses files a mail must
// not carry, so nothing unsafe waits to be sent.
func TestStatementsOutboxRefusesUnsafeAttachments(t *testing.T) {
	s := newStmtEnv(t)
	m := domain.OutboxMail{Mail: domain.Mail{
		ID: "x", To: "a@example.com", Subject: "s", Body: "b",
		Attachments: []domain.Attachment{{Filename: "../x.pdf", ContentType: "application/pdf", Data: []byte("x")}},
	}}
	if err := s.store.Outbox().Enqueue(s.ctx, m); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsafe attachment queued: %v", err)
	}
}

// TestPreviewOfASchedule: a schedule's preview covers its accounts only, for its owner and an
// administrator; anyone else gets not found.
func TestPreviewOfASchedule(t *testing.T) {
	s := newStmtEnv(t)
	ctx := s.ctx
	olivia := s.user(t, "olivia", domain.RoleOperator)
	victor := s.user(t, "victor", domain.RoleViewer)
	ov, err := s.svc.CreateAccount(ctx, olivia, "Olivia demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	x := weekly("o@example.com")
	x.AccountIDs = []string{ov.ID}
	v, err := s.svc.CreateStatementSchedule(ctx, olivia, x)
	if err != nil {
		t.Fatal(err)
	}
	req := StatementPreviewRequest{Period: domain.PeriodWeekly, Scope: domain.ScopeCombined, ScheduleID: v.ID}
	for _, p := range []Principal{olivia, s.admin} {
		if _, err := s.svc.PreviewStatement(ctx, p, req); err != nil {
			t.Fatalf("%s: %v", p.Username, err)
		}
		if st := s.r.All()[len(s.r.All())-1]; len(st.Accounts) != 1 || st.Accounts[0].Name != "Olivia demo" {
			t.Fatalf("%s: accounts %+v", p.Username, st.Accounts)
		}
	}
	if _, err := s.svc.PreviewStatement(ctx, victor, req); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("victor: %v", err)
	}
}
