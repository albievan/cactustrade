package fakes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

const share = domain.Micros(1_000_000)

var (
	t212Practice = domain.Account{ID: "t212", Platform: domain.PlatformTrading212, Mode: domain.ModeDemo}
	t212Real     = domain.Account{ID: "t212r", Platform: domain.PlatformTrading212, Mode: domain.ModeReal}
)

func newT212Sim() *T212Sim {
	return &T212Sim{
		Now: func() time.Time { return simT0 }, Currency: "GBP", Cash: 100_000, // £1 000
		Catalogue: []domain.T212Instrument{{Ticker: "BARCl_EQ", ShortName: "BARC", Currency: "GBX", Type: "STOCK", ISIN: "GB0031348658", MaxOpenQuantity: 1e12}},
		Prices:    map[string]domain.Micros{"BARCl_EQ": 200_000_000}, // 200 p
	}
}

func buy(q domain.Micros) domain.T212Intent {
	return domain.T212Intent{RequestID: "r", Ticker: "BARCl_EQ", Quantity: q, Side: domain.T212Buy, Type: domain.T212Market}
}

func sell(q domain.Micros) domain.T212Intent {
	return domain.T212Intent{RequestID: "s", Ticker: "BARCl_EQ", Quantity: q, Side: domain.T212Sell, Type: domain.T212Market}
}

func stop(q, at domain.Micros) domain.T212Intent {
	return domain.T212Intent{RequestID: "p", Ticker: "BARCl_EQ", Quantity: q, Side: domain.T212Sell, Type: domain.T212StopOrder, StopPrice: at}
}

func TestT212SimOutcomes(t *testing.T) {
	ctx := ports.WithAccount(context.Background(), t212Practice)
	tests := []struct {
		name     string
		script   T212Outcome
		closed   bool
		err      error
		active   int
		history  int
		held     domain.Micros
		cash     domain.Cents
		recorded bool
	}{
		// 10 × 200 p = 2 000 p.
		{"fill", T212Fill, false, nil, 0, 1, 10 * share, 98_000, true},
		{"pending", T212Pending, false, nil, 1, 0, 0, 100_000, true},
		{"reject", T212Reject, false, nil, 0, 1, 0, 100_000, true},
		{"partial", T212PartialFill, false, nil, 1, 0, 5 * share, 99_000, true},
		{"refused", T212Refused, false, domain.ErrNotAccepted, 0, 0, 0, 100_000, false},
		{"lost, applied", T212LostApplied, false, domain.ErrOutcomeUnknown, 0, 1, 10 * share, 98_000, true},
		{"lost, not applied", T212LostNotApplied, false, domain.ErrOutcomeUnknown, 0, 0, 0, 100_000, false},
		{"429", T212RateLimited, false, domain.ErrOutcomeUnknown, 0, 0, 0, 100_000, false},
		{"market closed: queued", T212Fill, true, nil, 1, 0, 0, 100_000, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newT212Sim()
			s.Script, s.MarketClosed = []T212Outcome{tc.script}, tc.closed
			o, err := s.Place(ctx, buy(10*share))
			if tc.err == nil && err != nil || tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("Place = %+v, %v; want %v", o, err, tc.err)
			}
			open, _ := s.OpenOrders(ctx)
			page, _ := s.History(ctx, "", "")
			if len(open) != tc.active || len(page.Orders) != tc.history || s.Held["BARCl_EQ"].Quantity != tc.held || s.Cash != tc.cash {
				t.Fatalf("active %d history %d held %s cash %d", len(open), len(page.Orders), s.Held["BARCl_EQ"].Quantity, s.Cash)
			}
			if (len(open)+len(page.Orders) > 0) != tc.recorded {
				t.Fatal("recorded at Trading 212")
			}
			if s.Places != 1 || len(s.Intents) != 1 || s.Outcomes[tc.script] != 1 || len(s.Violations) != 0 {
				t.Fatalf("records: %d %v %v", s.Places, s.Outcomes, s.Violations)
			}
			if tc.err == nil && (o.ID == 0 || o.InitiatedFrom != "API" || o.ISIN != "GB0031348658") {
				t.Fatalf("answer %+v", o)
			}
		})
	}
}

func TestT212SimStopsAndSells(t *testing.T) {
	ctx := ports.WithAccount(context.Background(), t212Practice)
	s := newT212Sim()
	if _, err := s.Place(ctx, buy(10*share)); err != nil {
		t.Fatal(err)
	}
	// A sell above the held quantity is refused without an order (and is a violation).
	if _, err := s.Place(ctx, sell(11*share)); !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatalf("sell > held: %v", err)
	}
	if len(s.Violations) != 1 {
		t.Fatalf("violations %v", s.Violations)
	}
	s.Violations = nil
	st, err := s.Place(ctx, stop(10*share, 190_000_000))
	if err != nil || st.State != domain.OrderPending || st.Final {
		t.Fatalf("stop = %+v %v", st, err)
	}
	if got, err := s.Order(ctx, st.ID); err != nil || got.StopPrice != 190_000_000 {
		t.Fatalf("Order(stop) = %+v %v", got, err)
	}
	if n := s.TriggerStop("BARCl_EQ", 191_000_000); n != 0 {
		t.Fatal("a stop fired above its price")
	}
	if n := s.TriggerStop("BARCl_EQ", 185_000_000); n != 1 {
		t.Fatal("the stop did not fire")
	}
	if _, ok := s.Held["BARCl_EQ"]; ok {
		t.Fatal("still held after the stop")
	}
	if _, err := s.Order(ctx, st.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a filled stop is still active: %v", err)
	}
	page, _ := s.History(ctx, "BARCl_EQ", "")
	if len(page.Orders) != 2 || page.Orders[0].ID != st.ID || page.Orders[0].FillPrice != 185_000_000 || page.Orders[0].State != domain.OrderFilled {
		t.Fatalf("history %+v", page.Orders)
	}
	// 10 × 185 p = 1 850 p back.
	if s.Cash != 98_000+1_850 {
		t.Fatalf("cash %d", s.Cash)
	}
	// A sell with nothing held is refused.
	if _, err := s.Place(ctx, sell(share)); !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatal("sold what is not held")
	}
	if len(s.Violations) != 1 {
		t.Fatalf("violations %v", s.Violations)
	}
}

func TestT212SimCancel(t *testing.T) {
	ctx := ports.WithAccount(context.Background(), t212Practice)
	s := newT212Sim()
	_, _ = s.Place(ctx, buy(4*share))
	st, _ := s.Place(ctx, stop(4*share, 150_000_000))
	if err := s.Cancel(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Order(ctx, st.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("cancelled stop still active")
	}
	if err := s.Cancel(ctx, st.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second cancel: %v", err)
	}
	// Cancel failing mid-fill: accepted, but the stop fills.
	st2, _ := s.Place(ctx, stop(4*share, 150_000_000))
	s.CancelFillsFirst = true
	if err := s.Cancel(ctx, st2.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Held["BARCl_EQ"]; ok {
		t.Fatal("the stop did not fill while it was being cancelled")
	}
	if len(s.CancelIDs) != 3 {
		t.Fatalf("cancels %v", s.CancelIDs)
	}
	s.CancelErr = domain.ErrOutcomeUnknown
	if err := s.Cancel(ctx, 1); !errors.Is(err, domain.ErrOutcomeUnknown) {
		t.Fatal("CancelErr")
	}
	// Pending buy, then filled by hand; queued orders fill at the open.
	s2 := newT212Sim()
	s2.Script = []T212Outcome{T212Pending}
	o, _ := s2.Place(ctx, buy(share))
	if !s2.Fill(o.ID) || s2.Fill(o.ID) {
		t.Fatal("Fill")
	}
	s2.MarketClosed = true
	q, _ := s2.Place(ctx, buy(share))
	s2.OpenMarket()
	if _, err := s2.Order(ctx, q.ID); !errors.Is(err, domain.ErrNotFound) || s2.Held["BARCl_EQ"].Quantity != 2*share {
		t.Fatal("the queued order did not fill at the open")
	}
	// A buy short of cash is rejected.
	s3 := newT212Sim()
	s3.Cash = 100
	if o, _ := s3.Place(ctx, buy(share)); o.State != domain.OrderRejected {
		t.Fatalf("buy without cash = %+v", o)
	}
}

// TestT212SimRefusesRealAndForeign (D41 g): a real Trading 212 account, an eToro account or
// no account gets nothing, whatever permit ctx carries; nothing is recorded as sent.
func TestT212SimRefusesRealAndForeign(t *testing.T) {
	// No permit can be built for a Trading 212 account; any permit in ctx must not help.
	permit, err := domain.PermitExit(domain.Account{ID: "t212r", Mode: domain.ModeReal}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{
		ports.WithPermit(ports.WithAccount(context.Background(), t212Real), permit),
		ports.WithAccount(context.Background(), domain.Account{ID: "e", Mode: domain.ModeDemo}),
		context.Background(),
	} {
		s := newT212Sim()
		if _, err := s.Place(ctx, buy(share)); err == nil {
			t.Fatal("place allowed")
		}
		if err := s.Cancel(ctx, 1); err == nil {
			t.Fatal("cancel allowed")
		}
		if _, err := s.Account(ctx); err == nil {
			t.Fatal("account read allowed")
		}
		if s.Places != 0 || len(s.CancelIDs) != 0 || s.Reads != 0 || len(s.Violations) != 3 {
			t.Fatalf("records %d %v %d %v", s.Places, s.CancelIDs, s.Reads, s.Violations)
		}
	}
	s := newT212Sim()
	if _, err := s.Place(ports.WithAccount(context.Background(), t212Real), buy(share)); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("real = %v", err)
	}
}

func TestT212SimRefusesBadIntents(t *testing.T) {
	ctx := ports.WithAccount(context.Background(), t212Practice)
	for name, in := range map[string]domain.T212Intent{
		"fraction":        buy(1_500_000),
		"no ticker":       {Quantity: share, Side: domain.T212Buy, Type: domain.T212Market},
		"buy stop":        {Ticker: "BARCl_EQ", Quantity: share, Side: domain.T212Buy, Type: domain.T212StopOrder, StopPrice: 1},
		"stop no price":   stop(share, 0),
		"limit":           {Ticker: "BARCl_EQ", Quantity: share, Side: domain.T212Buy, Type: "limit"},
		"unknown side":    {Ticker: "BARCl_EQ", Quantity: share, Side: "short", Type: domain.T212Market},
		"sell, none held": sell(share),
	} {
		s := newT212Sim()
		if _, err := s.Place(ctx, in); !errors.Is(err, domain.ErrNotAccepted) {
			t.Errorf("%s: %v", name, err)
		}
		if len(s.Violations) != 1 {
			t.Errorf("%s: violations %v", name, s.Violations)
		}
	}
	// A fraction with the switch on goes through.
	s := newT212Sim()
	in := buy(1_500_000)
	in.Fractional = true
	if _, err := s.Place(ctx, in); err != nil || s.Held["BARCl_EQ"].Quantity != 1_500_000 {
		t.Fatalf("fraction with the switch on: %v", err)
	}
	s.PlaceErr = errors.New("boom")
	if _, err := s.Place(ctx, buy(share)); err == nil {
		t.Fatal("PlaceErr")
	}
}

func TestT212SimReadsAndHistoryPages(t *testing.T) {
	ctx := ports.WithAccount(context.Background(), t212Practice)
	s := newT212Sim()
	s.PageSize = 2
	s.Held = map[string]domain.T212Position{"VODl_EQ": {Ticker: "VODl_EQ", Quantity: 5 * share, QuantityAvailable: 5 * share, Cost: 300}}
	s.Prices["VODl_EQ"] = 70_000_000
	s.Catalogue = append(s.Catalogue, domain.T212Instrument{Ticker: "VODl_EQ", Currency: "GBX"})
	for range 3 {
		_, _ = s.Place(ctx, buy(share))
	}
	s.Script = []T212Outcome{T212Pending}
	_, _ = s.Place(ctx, buy(share))
	a, err := s.Account(ctx)
	// cash 100 000 − 3 × 200 = 99 400; reserved 200; positions BARC 3 × 200 = 600, VOD 5 × 70 = 350.
	if err != nil || a.AvailableToTrade != 99_400 || a.ReservedForOrders != 200 || a.Equity != 99_400+200+600+350 ||
		len(a.Positions) != 2 || a.Positions[1].PnL != 50 || a.Currency != "GBP" || !a.AsOf.Equal(simT0) {
		t.Fatalf("Account = %+v %v", a, err)
	}
	var ids []int64
	cursor := ""
	for {
		p, err := s.History(ctx, "", cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range p.Orders {
			ids = append(ids, o.ID)
		}
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	if len(ids) != 3 || ids[0] < ids[2] {
		t.Fatalf("history ids %v (newest first)", ids)
	}
	if _, err := s.History(ctx, "", "x"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("bad cursor")
	}
	if ins, err := s.Instruments(ctx); err != nil || len(ins) != 2 {
		t.Fatal("Instruments")
	}
	s.ReadErr = domain.ErrUnavailable
	if _, err := s.OpenOrders(ctx); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("ReadErr")
	}
}

func TestRequireT212OrderRow(t *testing.T) {
	ctx := context.Background()
	st := NewStore()
	_ = st.Users().Create(ctx, domain.User{ID: "u", Username: "u", Role: domain.RoleAdmin})
	_ = st.Accounts().Create(ctx, domain.Account{ID: "t212", Name: "T", OwnerID: "u", Mode: domain.ModeDemo, Platform: domain.PlatformTrading212})
	o, err := domain.NewOrder(domain.Order{
		RequestID: "r", AccountID: "t212", Mode: domain.ModeDemo, Kind: domain.OrderOpen, Platform: domain.PlatformTrading212,
		Direction: domain.Long, Instrument: 1, Ticker: "BARCl_EQ", Quantity: 10 * share, Amount: 2_100, Entry: 200_000_000, StopLoss: 190_000_000,
	}, simT0)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Orders().Create(ctx, o)
	hook := RequireT212OrderRow(st.Orders())
	if err := hook(ctx, buy(10*share)); err == nil || !strings.Contains(err.Error(), "not a sent") {
		t.Fatalf("created, not sent: %v", err)
	}
	sent, _ := o.Transition(domain.OrderSent, simT0, "")
	_ = st.Orders().Update(ctx, o, sent)
	if err := hook(ctx, buy(10*share)); err != nil {
		t.Fatal(err)
	}
	if err := hook(ctx, buy(9*share)); err == nil {
		t.Fatal("another quantity passed")
	}
	if err := hook(ctx, sell(10*share)); err == nil {
		t.Fatal("another kind passed")
	}
	if err := hook(ctx, domain.T212Intent{RequestID: "nope"}); err == nil {
		t.Fatal("no row passed")
	}
	// Wired into the simulator: a violation, the order still processed.
	s := newT212Sim()
	s.OnPlace = hook
	if _, err := s.Place(ports.WithAccount(ctx, t212Practice), buy(share)); err != nil || len(s.Violations) != 1 {
		t.Fatalf("hook in Place: %v %v", err, s.Violations)
	}
}

// TestFakeT212Repos checks the in-memory catalogue and matches follow the port contracts that
// the shared SQL suite (storetest) checks for SQLite and MariaDB.
func TestFakeT212Repos(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	_ = s.Users().Create(ctx, domain.User{ID: "u", Username: "u", Role: domain.RoleAdmin})
	_ = s.Accounts().Create(ctx, domain.Account{ID: "a", Name: "A", OwnerID: "u", Mode: domain.ModeDemo, Platform: domain.PlatformTrading212})
	r := s.T212Instruments()
	if _, err := r.FetchedAt(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("empty catalogue has a time")
	}
	cat := []domain.T212Instrument{{Ticker: "BARCl_EQ", ShortName: "barc"}, {Ticker: "BARC_US_EQ", ShortName: "BARC"}, {Ticker: "VODl_EQ", ShortName: "VOD"}}
	if err := r.ReplaceAll(ctx, cat, simT0); err != nil {
		t.Fatal(err)
	}
	if at, err := r.FetchedAt(ctx); err != nil || !at.Equal(simT0) {
		t.Fatalf("FetchedAt = %v %v", at, err)
	}
	if l, _ := r.ByShortName(ctx, "Barc"); len(l) != 2 || l[0].Ticker != "BARC_US_EQ" || l[1].ShortName != "BARC" {
		t.Fatalf("ByShortName = %+v", l)
	}
	if g, err := r.Get(ctx, "VODl_EQ"); err != nil || !g.FetchedAt.Equal(simT0) {
		t.Fatalf("Get = %+v %v", g, err)
	}
	if _, err := r.Get(ctx, "vodl_eq"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("Get is not exact")
	}
	// A failing replace leaves the old catalogue.
	if err := r.ReplaceAll(ctx, []domain.T212Instrument{{Ticker: "X"}, {Ticker: "X"}}, simT0.Add(time.Hour)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate = %v", err)
	}
	if err := r.ReplaceAll(ctx, []domain.T212Instrument{{Ticker: strings.Repeat("x", 65)}}, simT0.Add(time.Hour)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("long ticker = %v", err)
	}
	if at, _ := r.FetchedAt(ctx); !at.Equal(simT0) {
		t.Fatal("a failed replace changed the catalogue")
	}
	s.T212Err = errors.New("down")
	if _, err := r.Get(ctx, "VODl_EQ"); err == nil {
		t.Fatal("T212Err")
	}
	s.T212Err = nil

	m := s.InstrumentMatches()
	mt := domain.InstrumentMatch{AccountID: "a", Instrument: 7, Ticker: "BARCl_EQ", Method: domain.MatchMethodAuto, UpdatedAt: simT0}
	if err := m.Put(ctx, mt); err != nil {
		t.Fatal(err)
	}
	mt.ConfirmedBy, mt.ConfirmedAt = "u", simT0
	if err := m.Put(ctx, mt); err != nil {
		t.Fatal(err)
	}
	if g, err := m.Get(ctx, "a", 7); err != nil || !g.Confirmed() {
		t.Fatalf("Get = %+v %v", g, err)
	}
	if err := m.Put(ctx, domain.InstrumentMatch{AccountID: "nope", Instrument: 1}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}
	_ = m.Put(ctx, domain.InstrumentMatch{AccountID: "a", Instrument: 3})
	if l, _ := m.List(ctx, "a"); len(l) != 2 || l[0].Instrument != 3 {
		t.Fatalf("List = %+v", l)
	}
	if err := m.Delete(ctx, "a", 3); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "a", 3); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("second delete")
	}
	if _, err := m.Get(ctx, "a", 3); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("deleted match found")
	}
	_ = s.Accounts().Delete(ctx, "a")
	if l, _ := m.List(ctx, "a"); len(l) != 0 {
		t.Fatal("matches survived their account")
	}
}
