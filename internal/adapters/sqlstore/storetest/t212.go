package storetest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// runT212 covers migration 00016 (PLAN R45.4): Trading 212 fields on orders and agent
// positions, the instrument catalogue (atomic replace) and the per-account instrument
// matches.
func runT212(t *testing.T, s ports.Store, ownerID string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	acct := domain.Account{ID: "acc-t212", Name: "Practice", OwnerID: ownerID, Mode: domain.ModeDemo, Platform: domain.PlatformTrading212, CreatedAt: t0, UpdatedAt: t0}
	if err := s.Accounts().Create(ctx, acct); err != nil {
		t.Fatal(err)
	}
	t.Run("t212_orders", func(t *testing.T) { runT212Orders(t, s, acct.ID, t0) })
	t.Run("t212_positions", func(t *testing.T) { runT212Positions(t, s, acct.ID, t0) })
	t.Run("t212_catalogue", func(t *testing.T) { runT212Catalogue(t, s, t0) })
	t.Run("instrument_matches", func(t *testing.T) { runMatches(t, s, acct.ID, t0) })
}

func runT212Orders(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	or := s.Orders()
	mk := func(o domain.Order) domain.Order {
		t.Helper()
		o.AccountID, o.Mode, o.Platform, o.Direction = acct, domain.ModeDemo, domain.PlatformTrading212, domain.Long
		o, err := domain.NewOrder(o, t0)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	orders := []domain.Order{
		mk(domain.Order{RequestID: "t-buy", Kind: domain.OrderOpen, Instrument: 1001, Ticker: "BARCl_EQ", Quantity: 12_000_000, Amount: 2_424, Entry: 200_000_000, StopLoss: 190_000_000, TakeProfit: 215_000_000}),
		mk(domain.Order{RequestID: "t-stop", Kind: domain.OrderStop, Instrument: 1001, Ticker: "BARCl_EQ", Quantity: 12_000_000, PositionID: 9_123_456_789, StopLoss: 190_000_000}),
		mk(domain.Order{RequestID: "t-sell", Kind: domain.OrderClose, Instrument: 1001, Ticker: "BARCl_EQ", Quantity: 12_000_000, PositionID: 9_123_456_789}),
		mk(domain.Order{RequestID: "t-cancel", Kind: domain.OrderCancel, TargetOrderID: 9_123_456_790}),
	}
	for _, o := range orders {
		if err := or.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
		if g, err := or.Get(ctx, o.RequestID); err != nil || g != o {
			t.Fatalf("Get = %+v %v\nwant %+v", g, err, o)
		}
	}
	// The fields survive a transition (the broker's id is stored in etoro_order_id).
	sent, _ := orders[1].Transition(domain.OrderSent, t0.Add(time.Second), "")
	if err := or.Update(ctx, orders[1], sent); err != nil {
		t.Fatal(err)
	}
	pend, _ := sent.Transition(domain.OrderPending, t0.Add(2*time.Second), "")
	pend.EtoroOrderID = 9_123_456_790
	if err := or.Update(ctx, sent, pend); err != nil {
		t.Fatal(err)
	}
	if g, _ := or.Get(ctx, pend.RequestID); g != pend || g.Kind != domain.OrderStop || g.Ticker != "BARCl_EQ" {
		t.Fatalf("after update: %+v", g)
	}
	if l, _ := or.ListOpen(ctx, acct); len(l) != 4 {
		t.Fatalf("ListOpen = %d", len(l))
	}
	long := orders[0]
	long.RequestID, long.Ticker = "t-long", strings.Repeat("x", 65)
	if err := or.Create(ctx, long); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ticker of 65: %v", err)
	}
	// An eToro order written without a platform reads back as eToro; so does a row from before
	// migration 00016 (the column default).
	e, err := domain.NewOrder(domain.Order{
		RequestID: "e-blank", AccountID: acct, Mode: domain.ModeDemo, Kind: domain.OrderClose, PositionID: 5, Instrument: 7,
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := or.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	if g, _ := or.Get(ctx, e.RequestID); g.Platform != domain.PlatformEtoro || g.Ticker != "" || g.Quantity != 0 {
		t.Fatalf("blank platform read back as %+v", g)
	}
	if d, ok := s.(interface{ DB() *sql.DB }); ok {
		_, err := d.DB().ExecContext(ctx, "INSERT INTO orders (request_id, account_id, mode, kind, state, created_at, updated_at, target_order_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			"e-old", acct, "demo", "cancel", "created", t0.UnixMicro(), t0.UnixMicro(), 3)
		if err != nil {
			t.Fatal(err)
		}
		if g, err := or.Get(ctx, "e-old"); err != nil || g.Platform != domain.PlatformEtoro || g.OnPlatform() != domain.PlatformEtoro || g.Ticker != "" {
			t.Fatalf("an old row read back as %+v %v", g, err)
		}
	}
}

func runT212Positions(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	ap := s.AgentPositions()
	p := domain.AgentPosition{
		AccountID: acct, PositionID: 9_123_456_789, OpenRequestID: "t-buy", Instrument: 1001, Direction: domain.Long, OpenedAt: t0,
		Ticker: "BARCl_EQ", Quantity: 12_500_000,
	}
	if err := ap.Add(ctx, p); err != nil {
		t.Fatal(err)
	}
	if g, err := ap.Get(ctx, acct, p.PositionID); err != nil || g != p {
		t.Fatalf("Get = %+v %v", g, err)
	}
	p.ClosedAt, p.CloseRequestID = t0.Add(time.Hour), "t-sell"
	if err := ap.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if g, _ := ap.Get(ctx, acct, p.PositionID); g != p {
		t.Fatalf("after update: %+v", g)
	}
	// SetCloseRequest writes only the close order: a closed position stays closed (review B3).
	if err := ap.SetCloseRequest(ctx, acct, p.PositionID, "t-sell-2"); err != nil {
		t.Fatal(err)
	}
	if g, _ := ap.Get(ctx, acct, p.PositionID); g.CloseRequestID != "t-sell-2" || !g.ClosedAt.Equal(p.ClosedAt) {
		t.Fatalf("after SetCloseRequest: %+v", g)
	}
	if err := ap.SetCloseRequest(ctx, acct, 404, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetCloseRequest of an unknown position: %v", err)
	}
	p.CloseRequestID = "t-sell-2"
	bad := p
	bad.PositionID, bad.Ticker = 2, strings.Repeat("y", 65)
	if err := ap.Add(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ticker of 65: %v", err)
	}
	runT212Sold(t, s, acct, t0)
}

// runT212Sold: a Trading 212 position's Sold is the filled quantity of its own stops and sales
// (migration 00017), stored with each order's state; buys, cancels, eToro orders and other
// positions' orders do not count (D44).
func runT212Sold(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	const pid int64 = 777_000_001
	p := domain.AgentPosition{
		AccountID: acct, PositionID: pid, OpenRequestID: "s-buy", Instrument: 1001, Direction: domain.Long, OpenedAt: t0.Add(time.Minute),
		Ticker: "VOD_l_EQ", Quantity: 10_000_000,
	}
	if err := s.AgentPositions().Add(ctx, p); err != nil {
		t.Fatal(err)
	}
	or := s.Orders()
	// fill moves an order to state with Filled set, in the same write.
	fill := func(o domain.Order, state domain.OrderState, filled domain.Micros) {
		t.Helper()
		o, err := domain.NewOrder(o, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := or.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
		sent, err := o.Transition(domain.OrderSent, t0.Add(time.Second), "")
		if err != nil {
			t.Fatal(err)
		}
		if err := or.Update(ctx, o, sent); err != nil {
			t.Fatal(err)
		}
		next, err := sent.Transition(state, t0.Add(2*time.Second), "")
		if err != nil {
			t.Fatal(err)
		}
		next.Filled, next.EtoroOrderID = filled, 1
		if err := or.Update(ctx, sent, next); err != nil {
			t.Fatal(err)
		}
		if g, _ := or.Get(ctx, o.RequestID); g.Filled != filled {
			t.Fatalf("%s: Filled read back as %s", o.RequestID, g.Filled)
		}
	}
	t212 := func(id string, k domain.OrderKind, q domain.Micros, positionID int64) domain.Order {
		return domain.Order{
			RequestID: id, AccountID: acct, Mode: domain.ModeDemo, Kind: k, Platform: domain.PlatformTrading212, Direction: domain.Long,
			Instrument: 1001, Ticker: "VOD_l_EQ", Quantity: q, PositionID: positionID, StopLoss: 90_000_000,
		}
	}
	buy := t212("s-buy", domain.OrderOpen, 10_000_000, 0)
	buy.Amount, buy.Entry, buy.StopLoss = 1_000, 100_000_000, 90_000_000
	fill(buy, domain.OrderFilled, 10_000_000)                                                 // a buy: not a sale
	fill(t212("s-stop", domain.OrderStop, 10_000_000, pid), domain.OrderCancelled, 2_000_000) // a stop partly filled, then cancelled
	fill(t212("s-sell1", domain.OrderClose, 8_000_000, pid), domain.OrderCancelled, 3_000_000)
	fill(t212("s-sell2", domain.OrderClose, 5_000_000, pid), domain.OrderPartial, 1_000_000)  // still working
	fill(t212("s-other", domain.OrderClose, 4_000_000, pid+1), domain.OrderFilled, 4_000_000) // another position
	fill(domain.Order{RequestID: "s-etoro", AccountID: acct, Mode: domain.ModeDemo, Kind: domain.OrderClose, PositionID: pid, Instrument: 7},
		domain.OrderFilled, 9_000_000) // an eToro order of the same position id
	const want domain.Micros = 6_000_000
	if g, err := s.AgentPositions().Get(ctx, acct, pid); err != nil || g.Sold != want || g.Unsold() != 4_000_000 {
		t.Fatalf("Get: Sold %s Unsold %s (%v), want %s", g.Sold, g.Unsold(), err, want)
	}
	for name, list := range map[string]func() ([]domain.AgentPosition, error){
		"ListOpen":  func() ([]domain.AgentPosition, error) { return s.AgentPositions().ListOpen(ctx, acct) },
		"ListSince": func() ([]domain.AgentPosition, error) { return s.AgentPositions().ListSince(ctx, acct, t0) },
	} {
		l, err := list()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, g := range l {
			if g.PositionID == pid {
				found = true
				if g.Sold != want {
					t.Fatalf("%s: Sold %s, want %s", name, g.Sold, want)
				}
			} else if g.PositionID == 9_123_456_789 && g.Sold != 0 {
				t.Fatalf("%s: position %d sold %s", name, g.PositionID, g.Sold)
			}
		}
		if !found {
			t.Fatalf("%s: position %d missing", name, pid)
		}
	}
}

func catalogue(n int, prefix string) []domain.T212Instrument {
	out := make([]domain.T212Instrument, 0, n)
	for i := range n {
		out = append(out, domain.T212Instrument{
			Ticker: fmt.Sprintf("%s%05d_US_EQ", prefix, i), ISIN: fmt.Sprintf("US%010d", i), Name: fmt.Sprintf("Company %d", i),
			ShortName: fmt.Sprintf("%s%05d", prefix, i), Currency: "USD", Type: "STOCK", MaxOpenQuantity: domain.Micros(i) * 1_000_000,
		})
	}
	return out
}

func runT212Catalogue(t *testing.T, s ports.Store, t0 time.Time) {
	ctx := context.Background()
	r := s.T212Instruments()
	if _, err := r.FetchedAt(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty catalogue: %v", err)
	}
	// 20 000 instruments in one replace, plus two London tickers that differ only in case,
	// a long name and an odd short name.
	cat := catalogue(20_000, "A")
	cat = append(cat,
		domain.T212Instrument{Ticker: "BARCl_EQ", ISIN: "GB0031348658", Name: "Barclays " + strings.Repeat("é", 300), ShortName: " barc ", Currency: "GBX", Type: "STOCK", MaxOpenQuantity: 1},
		domain.T212Instrument{Ticker: "BARCL_EQ", ShortName: "BARCL", Currency: "EUR", Type: "STOCK"},
		domain.T212Instrument{Ticker: "BARC_US_EQ", ShortName: "BARC", Currency: "USD", Type: "ETF"},
	)
	if err := r.ReplaceAll(ctx, cat, t0); err != nil {
		t.Fatal(err)
	}
	if at, err := r.FetchedAt(ctx); err != nil || !at.Equal(t0) {
		t.Fatalf("FetchedAt = %v %v", at, err)
	}
	g, err := r.Get(ctx, "A19999_US_EQ")
	if err != nil || g.ISIN != "US0000019999" || g.ShortName != "A19999" || g.MaxOpenQuantity != 19_999_000_000 || !g.FetchedAt.Equal(t0) || g.Type != "STOCK" {
		t.Fatalf("Get = %+v %v", g, err)
	}
	if g, err := r.Get(ctx, "BARCl_EQ"); err != nil || g.Currency != "GBX" || g.ShortName != "BARC" || len([]rune(g.Name)) > 255 {
		t.Fatalf("Get BARCl_EQ = %+v %v", g, err)
	}
	if g, err := r.Get(ctx, "BARCL_EQ"); err != nil || g.Currency != "EUR" {
		t.Fatalf("tickers that differ in case are not kept apart: %+v %v", g, err)
	}
	if _, err := r.Get(ctx, "barcl_eq"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get is not exact: %v", err)
	}
	l, err := r.ByShortName(ctx, "Barc")
	if err != nil || len(l) != 2 || l[0].Ticker != "BARC_US_EQ" || l[1].Ticker != "BARCl_EQ" {
		t.Fatalf("ByShortName = %+v %v", l, err)
	}
	if l, _ := r.ByShortName(ctx, "nothing"); len(l) != 0 {
		t.Fatal("ByShortName found nothing's instruments")
	}

	// A replace that fails half way (a ticker twice, at the end) leaves the old catalogue.
	next := append(catalogue(20_000, "B"), domain.T212Instrument{Ticker: "B00001_US_EQ"})
	if err := r.ReplaceAll(ctx, next, t0.Add(time.Hour)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate ticker: %v", err)
	}
	if _, err := r.Get(ctx, "B00000_US_EQ"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("a failed replace left new rows")
	}
	if _, err := r.Get(ctx, "A00000_US_EQ"); err != nil {
		t.Fatalf("a failed replace lost the old rows: %v", err)
	}
	if at, _ := r.FetchedAt(ctx); !at.Equal(t0) {
		t.Fatal("a failed replace changed the fetch time")
	}
	for _, bad := range []string{"", strings.Repeat("z", 65)} {
		if err := r.ReplaceAll(ctx, []domain.T212Instrument{{Ticker: bad}}, t0); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("ticker %q: %v", bad, err)
		}
	}
	// A successful replace drops what is no longer listed.
	if err := r.ReplaceAll(ctx, catalogue(3, "C"), t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "A00000_US_EQ"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("an old instrument survived the replace")
	}
	if at, _ := r.FetchedAt(ctx); !at.Equal(t0.Add(2 * time.Hour)) {
		t.Fatal("FetchedAt after replace")
	}
	if err := r.ReplaceAll(ctx, nil, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FetchedAt(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("an emptied catalogue has a time")
	}
}

func runMatches(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	m := s.InstrumentMatches()
	mt := domain.InstrumentMatch{AccountID: acct, Instrument: 1001, Ticker: "BARCl_EQ", ISIN: "GB0031348658", Method: domain.MatchMethodAuto, UpdatedAt: t0}
	if err := m.Put(ctx, mt); err != nil {
		t.Fatal(err)
	}
	if g, err := m.Get(ctx, acct, 1001); err != nil || g != mt || g.Confirmed() {
		t.Fatalf("Get = %+v %v", g, err)
	}
	// Confirm (the upsert updates), and the same values again (MariaDB: 0 rows changed).
	mt.ConfirmedBy, mt.ConfirmedAt, mt.UpdatedAt = "owner-id", t0.Add(time.Minute), t0.Add(time.Minute)
	for range 2 {
		if err := m.Put(ctx, mt); err != nil {
			t.Fatal(err)
		}
	}
	if g, _ := m.Get(ctx, acct, 1001); g != mt || !g.Confirmed() {
		t.Fatalf("after confirm: %+v", g)
	}
	other := domain.InstrumentMatch{AccountID: acct, Instrument: 7, Ticker: "AAPL_US_EQ", UpdatedAt: t0}
	if err := m.Put(ctx, other); err != nil {
		t.Fatal(err)
	}
	if l, err := m.List(ctx, acct); err != nil || len(l) != 2 || l[0] != other || l[1] != mt {
		t.Fatalf("List = %+v %v", l, err)
	}
	if err := m.Put(ctx, domain.InstrumentMatch{AccountID: "no-such-account", Instrument: 1, UpdatedAt: t0}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	if err := m.Put(ctx, domain.InstrumentMatch{AccountID: acct, Instrument: 2, Ticker: strings.Repeat("t", 65), UpdatedAt: t0}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ticker of 65: %v", err)
	}
	if _, err := m.Get(ctx, acct, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("Get unknown")
	}
	if err := m.Delete(ctx, acct, 7); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, acct, 7); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// Matches go with their account.
	if err := s.Accounts().Delete(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if l, err := m.List(ctx, acct); err != nil || len(l) != 0 {
		t.Fatalf("matches survived their account: %+v %v", l, err)
	}
}
