package domain

import (
	"errors"
	"testing"
	"time"
)

func TestPlatform(t *testing.T) {
	tests := []struct {
		in      string
		want    Platform
		wantErr bool
	}{
		{"", PlatformEtoro, false},
		{"etoro", PlatformEtoro, false},
		{"trading212", PlatformTrading212, false},
		{"ibkr", "", true},
	}
	for _, tt := range tests {
		got, err := ParsePlatform(tt.in)
		if got != tt.want || (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("ParsePlatform(%q) = %q, %v", tt.in, got, err)
		}
	}
	if !(Account{}).Tradable() || !(Account{Platform: PlatformEtoro}).Tradable() || (Account{Platform: PlatformTrading212}).Tradable() {
		t.Fatal("Tradable")
	}
	if (Account{}).OnPlatform() != PlatformEtoro {
		t.Fatal("OnPlatform default")
	}
	// Trading 212 names never collide with eToro's, even with EnvKeys set by mistake.
	if a, s := AccountKeyNames(Account{ID: "x", Platform: PlatformTrading212, EnvKeys: true}); a != "t212.x.api_key" || s != "t212.x.api_secret" {
		t.Fatalf("t212 names = %s %s", a, s)
	}
	if a, u := AccountKeyNames(Account{ID: "x", Platform: PlatformEtoro}); a != "etoro.x.api_key" || u != "etoro.x.user_key" {
		t.Fatal("eToro names changed")
	}
	if !errors.Is(ErrViewOnly, ErrForbidden) {
		t.Fatal("ErrViewOnly must be a Forbidden")
	}
}

func TestEquityHistoryDays(t *testing.T) {
	tests := []struct {
		in, want int
		ok       bool
	}{
		{0, DefaultEquityHistoryDays, true}, {1, 1, true}, {400, 400, true}, {401, 0, false}, {-1, 0, false},
	}
	for _, tt := range tests {
		got, err := EquityHistoryDays(tt.in)
		if got != tt.want || (err == nil) != tt.ok {
			t.Errorf("EquityHistoryDays(%d) = %d, %v", tt.in, got, err)
		}
	}
}

func TestHoldingsFromPortfolio(t *testing.T) {
	at := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	pf := Portfolio{
		AvailableCash: 50_000, PendingOpen: 1_000, AsOf: at,
		Positions: []Position{
			{ID: 1, Instrument: 1001, Units: 2_500_000, OpenRate: 180_000_000, Amount: 45_000, UnrealisedPnL: 1_250, Long: true, RealAsset: true, Leverage: 1, OpenedAt: at.Add(-time.Hour)},
			{ID: 2, Instrument: 9, Units: 1_000_000, OpenRate: 10_000_000, Amount: 1_000, UnrealisedPnL: -300, CFD: true, Leverage: 1},
		},
	}
	ins := map[InstrumentID]Instrument{1001: {ID: 1001, Symbol: "AAPL", Name: "Apple", Currency: "USD"}}
	quotes := map[InstrumentID]Quote{1001: {Instrument: 1001, Bid: 185_000_000, Ask: 185_100_000}, 9: {Instrument: 9, Bid: 12_000_000, Ask: 13_000_000}}
	h := HoldingsFromPortfolio(pf, ins, quotes)
	if h.Platform != PlatformEtoro || h.Currency != "USD" || h.Cash != 50_000 || h.Invested != 47_000 || h.Equity != pf.Equity() ||
		h.UnrealisedPnL != 950 || !h.AsOf.Equal(at) || len(h.Positions) != 2 {
		t.Fatalf("holdings = %+v", h)
	}
	a, b := h.Positions[0], h.Positions[1]
	if a.Symbol != "AAPL" || a.Name != "Apple" || a.Value != 46_250 || a.Cost != 45_000 || a.PnL != 1_250 || a.CurrentPrice != 185_000_000 || !a.Long || a.CFD {
		t.Fatalf("long = %+v", a)
	}
	if b.Symbol != "#9" || b.CurrentPrice != 13_000_000 || b.Long || !b.CFD || b.Value != 700 {
		t.Fatalf("short = %+v", b)
	}
	if h := HoldingsFromPortfolio(pf, nil, nil); h.Positions[0].CurrentPrice != 0 {
		t.Fatal("price without a quote")
	}
	s := h.Snapshot("acc", at)
	if s.AccountID != "acc" || s.Currency != "USD" || s.Cash != 50_000 || s.Equity != h.Equity || !s.At.Equal(at) {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestTotalsByCurrency(t *testing.T) {
	got := TotalsByCurrency([]Holdings{
		{Currency: "USD", Cash: 100, Equity: 1_000, UnrealisedPnL: 10},
		{Currency: "gbp", Cash: 5, Equity: 50, UnrealisedPnL: -1},
		{Currency: "USD", Cash: 200, Equity: 2_000, UnrealisedPnL: -30},
	})
	if len(got) != 2 || got[0].Currency != "GBP" || got[0].Accounts != 1 || got[0].Equity != 50 ||
		got[1].Currency != "USD" || got[1].Accounts != 2 || got[1].Cash != 300 || got[1].Equity != 3_000 || got[1].UnrealisedPnL != -20 {
		t.Fatalf("totals = %+v", got)
	}
	if len(TotalsByCurrency(nil)) != 0 {
		t.Fatal("empty")
	}
}
