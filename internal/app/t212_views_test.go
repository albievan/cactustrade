package app

import (
	"errors"
	"slices"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// TestT212Views (R45.9): the agent panel, the agent cards, the live view, the limits view,
// the watchlist's matches and the account list work for a Trading 212 practice account, in
// its currency; a real Trading 212 account stays view-only.
func TestT212Views(t *testing.T) {
	x := newT212X(t)
	o, err := x.place(t, "req-1")
	if err != nil {
		t.Fatal(err)
	}

	v, err := x.svc.AgentStatusView(x.ctx, x.owner, x.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	acct, _ := x.sim.Account(ports.WithAccount(x.ctx, x.a))
	if !v.EquityKnown || v.Equity != acct.Equity || v.Currency != "GBP" || v.OpenPositions != 1 {
		t.Fatalf("panel %+v (sim equity %s)", v, acct.Equity)
	}
	if _, err := x.svc.AgentStatusView(x.ctx, x.owner, x.t212Rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("real Trading 212 panel: %v", err)
	}

	cards, err := x.svc.AgentCards(x.ctx, x.owner)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, c := range cards {
		ids[c.Account.ID] = c.Currency
	}
	if cur, ok := ids[x.a.ID]; !ok || cur != "GBP" {
		t.Fatalf("cards %v: the practice account in GBP", ids)
	}
	if _, ok := ids[x.t212Rl.ID]; ok {
		t.Fatal("a real Trading 212 account has an agent card")
	}
	if ids[x.etoro.ID] != "USD" {
		t.Fatalf("eToro card currency %q", ids[x.etoro.ID])
	}

	if err := x.svc.WarmLiveView(x.ctx, x.owner, x.a.ID); err != nil {
		t.Fatal(err)
	}
	ls, err := x.svc.LiveSnapshot(x.ctx, x.owner, x.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ls.PortfolioKnown || ls.Currency != "GBP" || len(ls.Positions) != 1 || !ls.Positions[0].Ours || len(ls.RecentOrders) == 0 {
		t.Fatalf("live %+v", ls)
	}
	if _, err := x.svc.LiveSnapshot(x.ctx, x.owner, x.t212Rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("real Trading 212 live view: %v", err)
	}

	rv, err := x.svc.RiskLimitsView(x.ctx, x.owner)
	if err != nil || rv.Platform != domain.PlatformTrading212 || rv.Currency != "GBP" || rv.FractionsVerified != domain.T212FractionsVerified {
		t.Fatalf("limits view %+v %v", rv, err)
	}

	ms, err := x.svc.WatchlistMatches(x.ctx, x.owner)
	if err != nil || ms[1].Status != MatchConfirmed || ms[1].Ticker != barc || ms[1].Name == "" || ms[1].Currency != "GBX" {
		t.Fatalf("matches %+v %v", ms, err)
	}
	if _, err := x.svc.SetCurrentAccount(x.ctx, x.owner, x.etoro.ID); err != nil {
		t.Fatal(err)
	}
	if ms, err := x.svc.WatchlistMatches(x.ctx, x.owner); err != nil || len(ms) != 0 {
		t.Fatalf("eToro matches %+v %v", ms, err)
	}

	vs, err := x.svc.Accounts(x.ctx, x.owner)
	if err != nil {
		t.Fatal(err)
	}
	ex := x.svc.AccountExtras(x.ctx, vs)
	if ex[x.a.ID].PriceSource != x.etoro.ID || ex[x.a.ID].Currency != "GBP" || ex[x.etoro.ID].Currency != "USD" || ex[x.t212Rl.ID] != (AccountExtra{}) {
		t.Fatalf("extras %+v", ex)
	}

	// The orders page words Trading 212 orders by what they do.
	views, err := x.svc.Orders(x.ctx, x.owner, x.a.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[domain.OrderKind]string{}
	for _, ov := range views {
		labels[ov.Kind] = ov.KindLabel() + " | " + ov.StateLabel()
		if ov.Resolvable() {
			t.Fatalf("%s resolvable", ov.RequestID)
		}
	}
	if labels[domain.OrderOpen] != "Buy BARC.L (BARCl_EQ) | filled" || labels[domain.OrderStop] != "Sell stop for BARC.L (BARCl_EQ) | resting at Trading 212" {
		t.Fatalf("labels %v (order %s)", labels, o.RequestID)
	}
}

// TestT212OrderLabels: every kind and the resolvable rule.
func TestT212OrderLabels(t *testing.T) {
	base := domain.Order{Platform: domain.PlatformTrading212, Ticker: barc, Instrument: 1, TargetOrderID: 42}
	tests := []struct {
		kind       domain.OrderKind
		state      domain.OrderState
		id         int64
		label      string
		stateLabel string
		resolvable bool
	}{
		{domain.OrderOpen, domain.OrderUnknown, 0, "Buy BARC.L (BARCl_EQ)", "unknown", true},
		{domain.OrderOpen, domain.OrderUnknown, 7, "Buy BARC.L (BARCl_EQ)", "unknown", false},
		{domain.OrderStop, domain.OrderFilled, 7, "Sell stop for BARC.L (BARCl_EQ)", "fired (sold)", false},
		{domain.OrderClose, domain.OrderFilled, 7, "Sell BARC.L (BARCl_EQ)", "sold", false},
		{domain.OrderClose, domain.OrderPartial, 7, "Sell BARC.L (BARCl_EQ)", "partly sold", false},
		{domain.OrderCancel, domain.OrderFilled, 0, "Cancel request for BARC.L (BARCl_EQ) (Trading 212 order 42)", "accepted by Trading 212", false},
	}
	for _, tc := range tests {
		o := base
		o.Kind, o.State, o.EtoroOrderID = tc.kind, tc.state, tc.id
		v := OrderView{Order: o, Symbol: "BARC.L"}
		if v.KindLabel() != tc.label || v.StateLabel() != tc.stateLabel || v.Resolvable() != tc.resolvable {
			t.Errorf("%s %s: %q %q %v", tc.kind, tc.state, v.KindLabel(), v.StateLabel(), v.Resolvable())
		}
	}
	c := OrderView{Order: domain.Order{Platform: domain.PlatformTrading212, Kind: domain.OrderCancel, State: domain.OrderRejected, TargetOrderID: 42, Reason: "pending for over 1m0s"}}
	if r := c.ReasonLabel(); !slices.Contains([]string{"Trading 212 refused the request to cancel order 42: the order was still not filled after 1 minute (cactusTrade cancels an entry order Trading 212 has not filled by then, so its cash is not held up)"}, r) {
		t.Errorf("reason %q", r)
	}
	// eToro orders read as before.
	e := OrderView{Order: domain.Order{Kind: domain.OrderCancel, State: domain.OrderFilled, TargetOrderID: 42}, Symbol: "GOOG"}
	if e.StateLabel() != "accepted by eToro" || e.Resolvable() {
		t.Errorf("eToro cancel %q", e.StateLabel())
	}
}

// TestT212ResolveOrderView: the owner says an unknown order was not placed; the view names it.
func TestT212ResolveOrderView(t *testing.T) {
	x := newT212X(t)
	o, err := domain.NewOrder(domain.Order{
		RequestID: "lost-1", AccountID: x.a.ID, Mode: domain.ModeDemo, Kind: domain.OrderOpen, Platform: domain.PlatformTrading212,
		Ticker: barc, Quantity: 3_000_000, Direction: domain.Long, Instrument: 1, Amount: 700, Entry: 200_000_000, StopLoss: 194_000_000,
	}, x.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := x.store.Orders().Create(x.ctx, o); err != nil {
		t.Fatal(err)
	}
	sent, _ := o.Transition(domain.OrderSent, x.clock.Now(), "")
	if err := x.store.Orders().Update(x.ctx, o, sent); err != nil {
		t.Fatal(err)
	}
	lost, _ := sent.Transition(domain.OrderUnknown, x.clock.Now(), "lost")
	if err := x.store.Orders().Update(x.ctx, sent, lost); err != nil {
		t.Fatal(err)
	}
	api := x.owner
	api.Via = ViaAPIKey
	if _, err := x.svc.ResolveOrder(x.ctx, api, "lost-1", 0, true); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("with an API key: %v", err)
	}
	v, err := x.svc.ResolveOrder(x.ctx, x.owner, "lost-1", 0, true)
	if err != nil || v.State != domain.OrderAbandoned || v.Symbol != "BARC.L" {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := x.svc.ResolveOrder(x.ctx, x.owner, "lost-1", 0, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("again: %v", err)
	}
}
