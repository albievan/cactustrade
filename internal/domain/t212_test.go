package domain

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func TestT212AccountPredicates(t *testing.T) {
	tests := []struct {
		name                           string
		a                              Account
		tradable, t212Trading, agentOK bool
	}{
		{"eToro demo", Account{Mode: ModeDemo}, true, false, true},
		{"eToro real", Account{Platform: PlatformEtoro, Mode: ModeReal}, true, false, true},
		{"Trading 212 practice", Account{Platform: PlatformTrading212, Mode: ModeDemo}, false, true, true},
		{"Trading 212 real", Account{Platform: PlatformTrading212, Mode: ModeReal}, false, false, false},
		{"Trading 212 no mode", Account{Platform: PlatformTrading212}, false, false, false},
		{"Saxo demo", Account{Platform: PlatformSaxo, Mode: ModeDemo}, false, false, false},
		{"Tiger demo", Account{Platform: PlatformTiger, Mode: ModeDemo}, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if g := tc.a.Tradable(); g != tc.tradable {
				t.Errorf("Tradable = %v", g)
			}
			if g := tc.a.T212Trading(); g != tc.t212Trading {
				t.Errorf("T212Trading = %v", g)
			}
			if g := tc.a.Agentable(); g != tc.agentOK {
				t.Errorf("Agentable = %v", g)
			}
		})
	}
}

// t212Open is a valid Trading 212 buy of 3 shares.
func t212Open() Order {
	return Order{
		RequestID: "r-buy", AccountID: "t212", Mode: ModeDemo, Kind: OrderOpen, Platform: PlatformTrading212,
		Direction: Long, Instrument: 1001, Ticker: "AAPL_US_EQ", Quantity: 3 * wholeShare, Amount: 60_000,
		Entry: 189_000_000, StopLoss: 185_000_000, TakeProfit: 199_000_000,
	}
}

func t212Stop() Order {
	return Order{
		RequestID: "r-stop", AccountID: "t212", Mode: ModeDemo, Kind: OrderStop, Platform: PlatformTrading212,
		Direction: Long, Instrument: 1001, Ticker: "AAPL_US_EQ", Quantity: 3 * wholeShare, PositionID: 77, StopLoss: 185_000_000,
	}
}

func t212Sell() Order {
	return Order{
		RequestID: "r-sell", AccountID: "t212", Mode: ModeDemo, Kind: OrderClose, Platform: PlatformTrading212,
		Direction: Long, Instrument: 1001, Ticker: "AAPL_US_EQ", Quantity: 3 * wholeShare, PositionID: 77,
	}
}

func TestT212OrderValidation(t *testing.T) {
	tests := []struct {
		name   string
		o      Order
		change func(*Order)
		want   error
	}{
		{"buy", t212Open(), func(*Order) {}, nil},
		{"stop", t212Stop(), func(*Order) {}, nil},
		{"sell", t212Sell(), func(*Order) {}, nil},
		{"cancel", Order{RequestID: "c", AccountID: "t212", Mode: ModeDemo, Kind: OrderCancel, Platform: PlatformTrading212, TargetOrderID: 9}, func(*Order) {}, nil},
		{"cancel without target", Order{RequestID: "c", AccountID: "t212", Mode: ModeDemo, Kind: OrderCancel, Platform: PlatformTrading212}, func(*Order) {}, ErrInvalid},
		{"real account", t212Open(), func(o *Order) { o.Mode = ModeReal }, ErrRealModeGated},
		{"short buy", t212Open(), func(o *Order) { o.Direction = Short; o.StopLoss = 193_000_000; o.TakeProfit = 0 }, ErrInvalid},
		{"short cancel", Order{RequestID: "c", AccountID: "t212", Mode: ModeDemo, Kind: OrderCancel, Platform: PlatformTrading212, TargetOrderID: 9, Direction: Short}, func(*Order) {}, ErrInvalid},
		{"no direction", t212Sell(), func(o *Order) { o.Direction = "" }, ErrInvalid},
		{"buy without ticker", t212Open(), func(o *Order) { o.Ticker = "" }, ErrInvalid},
		{"buy without quantity", t212Open(), func(o *Order) { o.Quantity = 0 }, ErrInvalid},
		{"negative quantity", t212Sell(), func(o *Order) { o.Quantity = -1 }, ErrInvalid},
		{"buy with stop above entry", t212Open(), func(o *Order) { o.StopLoss = 190_000_000 }, ErrInvalid},
		{"buy without amount", t212Open(), func(o *Order) { o.Amount = 0 }, ErrInvalid},
		{"buy without instrument", t212Open(), func(o *Order) { o.Instrument = 0 }, ErrInvalid},
		{"stop without position", t212Stop(), func(o *Order) { o.PositionID = 0 }, ErrInvalid},
		{"stop without price", t212Stop(), func(o *Order) { o.StopLoss = 0 }, ErrInvalid},
		{"stop without quantity", t212Stop(), func(o *Order) { o.Quantity = 0 }, ErrInvalid},
		{"sell without quantity", t212Sell(), func(o *Order) { o.Quantity = 0 }, ErrInvalid},
		{"sell without position", t212Sell(), func(o *Order) { o.PositionID = 0 }, ErrInvalid},
		{"Saxo order", t212Open(), func(o *Order) { o.Platform = PlatformSaxo }, ErrInvalid},
		{"eToro stop", t212Stop(), func(o *Order) { o.Platform = PlatformEtoro }, ErrInvalid},
		{"eToro open with a ticker", t212Open(), func(o *Order) { o.Platform = "" }, ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			tc.change(&o)
			_, err := NewOrder(o, t0)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("NewOrder = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMaxSendsFor(t *testing.T) {
	tests := []struct {
		p    Platform
		k    OrderKind
		want int
	}{
		{PlatformEtoro, OrderOpen, 1},
		{PlatformEtoro, OrderClose, 5},
		{PlatformEtoro, OrderCancel, 3},
		{"", OrderClose, 5},
		{PlatformTrading212, OrderOpen, 1},
		{PlatformTrading212, OrderStop, 1},
		{PlatformTrading212, OrderClose, 1},
		{PlatformTrading212, OrderCancel, 3},
	}
	for _, tc := range tests {
		if g := MaxSendsFor(tc.p, tc.k); g != tc.want {
			t.Errorf("MaxSendsFor(%q, %s) = %d, want %d", tc.p, tc.k, g, tc.want)
		}
	}
	// A Trading 212 buy, stop or sell is sent once: a second send is illegal.
	for _, o := range []Order{t212Open(), t212Stop(), t212Sell()} {
		o, err := NewOrder(o, t0)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := o.Transition(OrderSent, t0, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sent.Transition(OrderSent, t0, ""); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("%s sent twice: %v", o.Kind, err)
		}
	}
	// An eToro close still may be resent (unchanged).
	c, _ := NewOrder(Order{RequestID: "e", AccountID: "a", Mode: ModeDemo, Kind: OrderClose, PositionID: 1, Instrument: 1}, t0)
	for i := range 5 {
		var err error
		if c, err = c.Transition(OrderSent, t0, ""); err != nil {
			t.Fatalf("eToro close send %d: %v", i+1, err)
		}
	}
}

func TestT212IntentFor(t *testing.T) {
	created := func(o Order) Order {
		o, err := NewOrder(o, t0)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	frac := t212Open()
	frac.Quantity = 2_500_000
	tests := []struct {
		name  string
		o     Order
		held  Micros
		frac  bool
		want  T212Intent
		error error
	}{
		{"buy", created(t212Open()), 0, false, T212Intent{RequestID: "r-buy", Ticker: "AAPL_US_EQ", Quantity: 3 * wholeShare, Side: T212Buy, Type: T212Market}, nil},
		{"stop", created(t212Stop()), 3 * wholeShare, false, T212Intent{RequestID: "r-stop", Ticker: "AAPL_US_EQ", Quantity: 3 * wholeShare, Side: T212Sell, Type: T212StopOrder, StopPrice: 185_000_000}, nil},
		{"sell", created(t212Sell()), 5 * wholeShare, false, T212Intent{RequestID: "r-sell", Ticker: "AAPL_US_EQ", Quantity: 3 * wholeShare, Side: T212Sell, Type: T212Market}, nil},
		{"sell more than held", created(t212Sell()), 2 * wholeShare, false, T212Intent{}, ErrInvalid},
		{"stop more than held", created(t212Stop()), 0, false, T212Intent{}, ErrInvalid},
		{"sell with nothing held", created(t212Sell()), -1, false, T212Intent{}, ErrInvalid},
		{"fraction while locked", frac, 0, false, T212Intent{}, ErrInvalid},
		{"fraction with the switch on but unverified", frac, 0, true, T212Intent{}, ErrInvalid},
		{"cancel", created(Order{RequestID: "c", AccountID: "t212", Mode: ModeDemo, Kind: OrderCancel, Platform: PlatformTrading212, TargetOrderID: 9}), 0, false, T212Intent{}, ErrInvalid},
		{"eToro order", created(Order{RequestID: "e", AccountID: "a", Mode: ModeDemo, Kind: OrderOpen, Direction: Long, Instrument: 1, Amount: 100, Entry: 10, StopLoss: 9}), 0, false, T212Intent{}, ErrInvalid},
		{"real", func() Order { o := t212Open(); o.Mode = ModeReal; return o }(), 0, false, T212Intent{}, ErrRealModeGated},
		{"short", func() Order { o := t212Open(); o.Direction = Short; return o }(), 0, false, T212Intent{}, ErrInvalid},
		{"final", func() Order { o := created(t212Open()); o.State = OrderFilled; return o }(), 0, false, T212Intent{}, ErrIllegalTransition},
		{"invalid", func() Order { o := t212Open(); o.Ticker = ""; return o }(), 0, false, T212Intent{}, ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.o.State == "" {
				tc.o.State = OrderCreated
			}
			got, err := T212IntentFor(tc.o, tc.held, tc.frac)
			if tc.error != nil {
				if !errors.Is(err, tc.error) || got != (T212Intent{}) {
					t.Fatalf("T212IntentFor = %+v, %v; want %v", got, err, tc.error)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("T212IntentFor = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestT212IntentNeverShort (R45.2, CLAUDE.md §2.3): whatever is held, asked for, the mode or
// the switch, an intent is never a sell of more than held, never short, never for a real
// account and never a fraction while fractions are locked.
func TestT212IntentNeverShort(t *testing.T) {
	r := rand.New(rand.NewPCG(45, 2))
	// Weighted towards valid orders, so that enough intents are built to test.
	kinds := []OrderKind{OrderOpen, OrderStop, OrderClose, OrderClose, OrderStop, OrderCancel}
	modes := []TradingMode{ModeDemo, ModeDemo, ModeDemo, ModeReal, ""}
	dirs := []Direction{Long, Long, Long, Short, ""}
	built := 0
	for range 20_000 {
		o := t212Open()
		o.Kind = kinds[r.IntN(len(kinds))]
		o.Mode = modes[r.IntN(len(modes))]
		o.Direction = dirs[r.IntN(len(dirs))]
		o.Quantity = Micros(r.Int64N(10_000_000)) - 1_000_000
		if r.IntN(2) == 0 {
			o.Quantity = o.Quantity / wholeShare * wholeShare
		}
		o.PositionID = r.Int64N(4)
		o.TargetOrderID = r.Int64N(3)
		o.State = []OrderState{OrderCreated, OrderSent, OrderSent, OrderFilled}[r.IntN(4)]
		held := Micros(r.Int64N(10_000_000)) - 1_000_000
		frac := r.IntN(2) == 0
		in, err := T212IntentFor(o, held, frac)
		if err != nil {
			if in != (T212Intent{}) {
				t.Fatalf("an error came with an intent: %+v", in)
			}
			continue
		}
		built++
		switch {
		case o.Mode != ModeDemo:
			t.Fatalf("an intent for mode %q", o.Mode)
		case o.Direction != Long:
			t.Fatalf("an intent for direction %q", o.Direction)
		case in.Quantity <= 0:
			t.Fatalf("an intent of quantity %s", in.Quantity)
		case in.Side == T212Sell && in.Quantity > held:
			t.Fatalf("a sell of %s with %s held", in.Quantity, held)
		case in.Side == T212Buy && (in.Type != T212Market || o.Kind != OrderOpen):
			t.Fatalf("a buy that is not a market open: %+v", in)
		case in.Side != T212Buy && in.Side != T212Sell:
			t.Fatalf("side %q", in.Side)
		case !T212FractionsVerified && (in.Fractional || in.Quantity%wholeShare != 0):
			t.Fatalf("a fraction while locked: %+v", in)
		case in.Type == T212StopOrder && in.StopPrice <= 0:
			t.Fatalf("a stop without a price: %+v", in)
		}
	}
	if built < 500 {
		t.Fatalf("only %d intents built: the generator is too narrow", built)
	}
}

// TestT212RealAlwaysRefused (D41 g): a real Trading 212 account is refused by every domain
// door: the account predicates, order validation and the intent.
func TestT212RealAlwaysRefused(t *testing.T) {
	a := Account{ID: "t", Platform: PlatformTrading212, Mode: ModeReal}
	if a.T212Trading() || a.Agentable() || a.Tradable() {
		t.Fatal("a real Trading 212 account is tradable")
	}
	for _, o := range []Order{t212Open(), t212Stop(), t212Sell()} {
		o.Mode = ModeReal
		if _, err := NewOrder(o, t0); !errors.Is(err, ErrRealModeGated) {
			t.Fatalf("NewOrder %s real = %v", o.Kind, err)
		}
		o.State = OrderCreated
		if _, err := T212IntentFor(o, 100*wholeShare, true); !errors.Is(err, ErrRealModeGated) {
			t.Fatalf("T212IntentFor %s real = %v", o.Kind, err)
		}
	}
	// The eToro door refuses a Trading 212 order too.
	o, _ := NewOrder(t212Open(), t0)
	if _, err := IntentFor(o, RealPermit{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("IntentFor(Trading 212 order) = %v", err)
	}
}

func TestT212PriceScale(t *testing.T) {
	tests := []struct {
		instr, acct string
		want        int64
		ok          bool
	}{
		{"USD", "USD", 100, true},
		{"usd", " USD ", 100, true},
		{"GBX", "GBP", 1, true},
		{"GBp", "GBP", 1, true},
		{"GBP", "GBP", 100, true},
		{"USD", "GBP", 0, false},
		{"GBX", "USD", 0, false},
		{"EUR", "EUR", 0, false},
		{"GBX", "EUR", 0, false},
		{"", "USD", 0, false},
	}
	for _, tc := range tests {
		g, err := T212PriceScale(tc.instr, tc.acct)
		if g != tc.want || (err == nil) != tc.ok {
			t.Errorf("T212PriceScale(%q, %q) = %d, %v", tc.instr, tc.acct, g, err)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("error %v is not ErrInvalid", err)
		}
	}
	if g, err := T212MinorPerShare(189_100_000, "USD", "USD"); err != nil || g != 18_910_000_000 {
		t.Fatalf("USD per share = %d %v", g, err)
	}
	if g, err := T212MinorPerShare(250_500_000, "GBX", "GBP"); err != nil || g != 250_500_000 {
		t.Fatalf("GBX per share = %d %v", g, err)
	}
	if _, err := T212MinorPerShare(0, "USD", "USD"); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero price accepted")
	}
	if _, err := T212MinorPerShare(1, "USD", "GBP"); !errors.Is(err, ErrInvalid) {
		t.Fatal("USD on GBP accepted")
	}
}

func TestT212QuantityAndReserve(t *testing.T) {
	tests := []struct {
		name     string
		size     Cents
		perShare Micros
		want     Micros
		reserve  Cents
	}{
		// $1 000 at $189.10 (+1 % = 190.991): 5.23 shares → 5; 5 × 189.10 × 1.01 = 954.9550 → 95 496 cents.
		{"USD", 100_000, 18_910_000_000, 5 * wholeShare, 95_496},
		// £500 at 250.5 GBX (+1 % = 253.005 p): 197.6 shares → 197; 197 × 250.5 × 1.01 = 49 841.985 → 49 842 pence.
		{"GBX", 50_000, 250_500_000, 197 * wholeShare, 49_842},
		{"too small", 100, 18_910_000_000, 0, 0},
		{"no size", 0, 18_910_000_000, 0, 0},
		{"no price", 100_000, 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := T212Quantity(tc.size, tc.perShare, false)
			if q != tc.want {
				t.Fatalf("T212Quantity = %s, want %s", q, tc.want)
			}
			if r := T212Reserve(q, tc.perShare); r != tc.reserve {
				t.Fatalf("T212Reserve = %d, want %d", r, tc.reserve)
			}
		})
	}
	// The fractional switch changes nothing while fractions are unverified.
	if T212Quantity(100_000, 18_910_000_000, true) != 5*wholeShare {
		t.Fatal("a fraction while locked")
	}
	if T212QuantityStep(true) != wholeShare || T212QuantityStep(false) != wholeShare {
		t.Fatal("step while locked")
	}
	// Beyond any real order: nothing.
	if T212Quantity(1<<62, 1, false) != 0 {
		t.Fatal("a saturated quantity")
	}
}

// TestT212QuantityWithinCash (§2.2, §2.4): for any size and price the reservation of the
// quantity bought never exceeds the size, and the quantity × price never exceeds the
// reservation.
func TestT212QuantityWithinCash(t *testing.T) {
	r := rand.New(rand.NewPCG(45, 7))
	nonzero := 0
	for range 50_000 {
		size := Cents(r.Int64N(100_000_000))
		perShare := Micros(1 + r.Int64N(1_000_000_000_000))
		q := T212Quantity(size, perShare, r.IntN(2) == 0)
		if q < 0 || q%wholeShare != 0 {
			t.Fatalf("quantity %s", q)
		}
		if q == 0 {
			continue
		}
		nonzero++
		res := T212Reserve(q, perShare)
		if res > size {
			t.Fatalf("reservation %d > size %d (q %s, per share %d)", res, size, q, perShare)
		}
		// q × perShare / 10^12 in minor units, exactly.
		cost := new(big.Int).Mul(big.NewInt(int64(q)), big.NewInt(int64(perShare)))
		if cost.Cmp(new(big.Int).Mul(big.NewInt(int64(res)), big.NewInt(1_000_000_000_000))) > 0 {
			t.Fatalf("quantity × price > reservation %d (q %s, per share %d)", res, q, perShare)
		}
	}
	if nonzero < 1000 {
		t.Fatalf("only %d non-zero quantities: the generator is too narrow", nonzero)
	}
}

func TestT212CostCheck(t *testing.T) {
	us := T212CostInput{Market: MarketNYSE, Quantity: 10 * wholeShare, Bid: 99_900_000, Ask: 100_000_000, TakeProfit: 102_000_000, InstrumentCurrency: "USD", AccountCurrency: "USD"}
	lse := T212CostInput{Market: MarketLSE, Quantity: 100 * wholeShare, Bid: 249_000_000, Ask: 250_000_000, TakeProfit: 256_000_000, InstrumentCurrency: "GBX", AccountCurrency: "GBP"}
	tests := []struct {
		name   string
		in     T212CostInput
		change func(*T212CostInput)
		ok     bool
		cost   Cents
		gain   Cents
	}{
		// gain 10 × $2 = 2000 c; spread 2 × 10 × $0.10 = 200 c.
		{"US allowed", us, func(*T212CostInput) {}, true, 200, 2000},
		// spread 2 × 10 × $1 = 2000 c ≥ gain 2000 c.
		{"US spread eats the gain", us, func(in *T212CostInput) { in.Bid = 99_000_000 }, false, 2000, 2000},
		// value 25 000 p; stamp 125 p; spread 2 × 100 × 1 p = 200 p; gain 100 × 6 p = 600 p.
		{"LSE allowed", lse, func(*T212CostInput) {}, true, 325, 600},
		// gain 100 × 3 p = 300 p < 325 p.
		{"LSE stamp duty eats the gain", lse, func(in *T212CostInput) { in.TakeProfit = 253_000_000 }, false, 325, 300},
		// 5 000 shares × 250 p = £12 500 > £10 000: stamp 6 250 + levy 150 + spread 10 000; gain 30 000.
		{"LSE over £10 000 pays the PTM levy", lse, func(in *T212CostInput) { in.Quantity = 5000 * wholeShare }, true, 16_400, 30_000},
		{"no take-profit", us, func(in *T212CostInput) { in.TakeProfit = 0 }, false, 0, 0},
		{"take-profit below the ask", us, func(in *T212CostInput) { in.TakeProfit = 99_000_000 }, false, 0, 0},
		{"crossed quote", us, func(in *T212CostInput) { in.Bid = 101_000_000 }, false, 0, 0},
		{"no bid", us, func(in *T212CostInput) { in.Bid = 0 }, false, 0, 0},
		{"no quantity", us, func(in *T212CostInput) { in.Quantity = 0 }, false, 0, 0},
		{"USD on a GBP account", us, func(in *T212CostInput) { in.AccountCurrency = "GBP" }, false, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			tc.change(&in)
			g := T212CostCheck(in)
			if g.Allowed != tc.ok || g.Cost != tc.cost || g.Gain != tc.gain || (!g.Allowed && g.Reason == "") {
				t.Fatalf("T212CostCheck = %+v, want ok %v cost %d gain %d", g, tc.ok, tc.cost, tc.gain)
			}
		})
	}
}

func TestT212FillConforms(t *testing.T) {
	tests := []struct {
		name                       string
		sent, filled, price, entry Micros
		ok                         bool
	}{
		{"exact", 3, 3, 100_000_000, 100_000_000, true},
		{"partial", 3, 2, 100_000_000, 100_000_000, true},
		{"2 % above", 3, 3, 102_000_000, 100_000_000, true},
		{"2 % below", 3, 3, 98_000_000, 100_000_000, true},
		{"just over 2 %", 3, 3, 102_000_001, 100_000_000, false},
		{"far below (wrong instrument)", 3, 3, 50_000_000, 100_000_000, false},
		{"nothing filled", 3, 0, 100_000_000, 100_000_000, false},
		{"more than sent", 3, 4, 100_000_000, 100_000_000, false},
		{"no price", 3, 3, 0, 100_000_000, false},
		{"no entry", 3, 3, 100_000_000, 0, false},
	}
	for _, tc := range tests {
		ok, why := T212FillConforms(tc.sent, tc.filled, tc.price, tc.entry)
		if ok != tc.ok || (!ok && why == "") {
			t.Errorf("%s: T212FillConforms = %v %q", tc.name, ok, why)
		}
	}
}

func TestT212OrderState(t *testing.T) {
	tests := []struct {
		status string
		want   OrderState
		final  bool
	}{
		{"FILLED", OrderFilled, true},
		{"PARTIALLY_FILLED", OrderPartial, false},
		{"CANCELLED", OrderCancelled, true},
		{"REJECTED", OrderRejected, true},
		{"NEW", OrderPending, false},
		{"LOCAL", OrderPending, false},
		{"UNCONFIRMED", OrderPending, false},
		{"CONFIRMED", OrderPending, false},
		{"CANCELLING", OrderPending, false},
		{"REPLACED", OrderUnknown, false},
		{"REPLACING", OrderUnknown, false},
		{"", OrderUnknown, false},
		{"WHATEVER", OrderUnknown, false},
		{" filled ", OrderFilled, true},
	}
	for _, tc := range tests {
		if g, f := T212OrderState(tc.status); g != tc.want || f != tc.final {
			t.Errorf("T212OrderState(%q) = %s %v", tc.status, g, f)
		}
	}
}

func sentT212(o Order, at time.Time) Order {
	o, _ = NewOrder(o, at)
	o, _ = o.Transition(OrderSent, at, "")
	return o
}

func TestMatchT212Order(t *testing.T) {
	buy := sentT212(t212Open(), t0)
	ours := BrokerOrder{ID: 501, Ticker: "AAPL_US_EQ", Side: T212Buy, Type: T212Market, Quantity: 3 * wholeShare, InitiatedFrom: "API", CreatedAt: t0.Add(time.Second)}
	with := func(f func(*BrokerOrder)) BrokerOrder { b := ours; f(&b); return b }
	tests := []struct {
		name    string
		o       Order
		cands   []BrokerOrder
		claimed []int64
		want    T212MatchResult
		id      int64
	}{
		{"one", buy, []BrokerOrder{ours}, nil, T212MatchOne, 501},
		{"listed in both lists", buy, []BrokerOrder{ours, ours}, nil, T212MatchOne, 501},
		{"none", buy, nil, nil, T212MatchNone, 0},
		{"claimed", buy, []BrokerOrder{ours}, []int64{501}, T212MatchNone, 0},
		{"another ticker", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.Ticker = "MSFT_US_EQ" })}, nil, T212MatchNone, 0},
		{"another side", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.Side = T212Sell })}, nil, T212MatchNone, 0},
		{"another type", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.Type = "limit" })}, nil, T212MatchNone, 0},
		{"another quantity", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.Quantity = 4 * wholeShare })}, nil, T212MatchNone, 0},
		{"from the app", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.InitiatedFrom = "IOS" })}, nil, T212MatchNone, 0},
		{"too early", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.CreatedAt = t0.Add(-6 * time.Second) })}, nil, T212MatchNone, 0},
		{"just in time before", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.CreatedAt = t0.Add(-5 * time.Second) })}, nil, T212MatchOne, 501},
		{"too late", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.CreatedAt = t0.Add(2*time.Minute + time.Second) })}, nil, T212MatchNone, 0},
		{"two candidates", buy, []BrokerOrder{ours, with(func(b *BrokerOrder) { b.ID = 502 })}, nil, T212MatchAmbiguous, 0},
		{"two, one claimed", buy, []BrokerOrder{ours, with(func(b *BrokerOrder) { b.ID = 502 })}, []int64{502}, T212MatchOne, 501},
		{"no id", buy, []BrokerOrder{with(func(b *BrokerOrder) { b.ID = 0 })}, nil, T212MatchNone, 0},
		{"never sent", func() Order { o, _ := NewOrder(t212Open(), t0); return o }(), []BrokerOrder{ours}, nil, T212MatchNone, 0},
		{"stop", sentT212(t212Stop(), t0), []BrokerOrder{with(func(b *BrokerOrder) { b.Side, b.Type = T212Sell, T212StopOrder })}, nil, T212MatchOne, 501},
		{"sell", sentT212(t212Sell(), t0), []BrokerOrder{with(func(b *BrokerOrder) { b.Side = T212Sell })}, nil, T212MatchOne, 501},
		{"cancel never matches", sentT212(Order{RequestID: "c", AccountID: "t212", Mode: ModeDemo, Kind: OrderCancel, Platform: PlatformTrading212, TargetOrderID: 9}, t0), []BrokerOrder{ours}, nil, T212MatchNone, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, res := MatchT212Order(tc.o, tc.cands, tc.claimed)
			if res != tc.want || g.ID != tc.id {
				t.Fatalf("MatchT212Order = %d %s, want %d %s", g.ID, res, tc.id, tc.want)
			}
		})
	}
}

// TestMatchT212OrderProperties (T212-d): an adopted order is never claimed already, of
// another ticker, side or quantity, or outside the window; two or more candidates are
// ambiguous.
func TestMatchT212OrderProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(45, 3))
	tickers := []string{"AAPL_US_EQ", "MSFT_US_EQ"}
	sides := []T212Side{T212Buy, T212Sell}
	types := []T212OrderType{T212Market, T212StopOrder, "limit"}
	from := []string{"API", "WEB"}
	ones, ambiguous := 0, 0
	for range 5_000 {
		o := sentT212(t212Open(), t0)
		var cands []BrokerOrder
		var claimed []int64
		for i := range r.IntN(6) {
			// Each field is ours, or with a chance of 1 in 6 something else.
			c := BrokerOrder{
				ID: int64(1 + r.IntN(8)), Ticker: o.Ticker, Side: T212Buy, Type: T212Market, Quantity: o.Quantity,
				InitiatedFrom: "API", CreatedAt: t0.Add(time.Duration(r.IntN(60)-3) * time.Second),
			}
			if r.IntN(6) == 0 {
				c.Ticker = tickers[r.IntN(2)]
			}
			if r.IntN(6) == 0 {
				c.Side = sides[r.IntN(2)]
			}
			if r.IntN(6) == 0 {
				c.Type = types[r.IntN(3)]
			}
			if r.IntN(6) == 0 {
				c.Quantity = Micros(1+r.IntN(4)) * wholeShare
			}
			if r.IntN(6) == 0 {
				c.InitiatedFrom = from[r.IntN(2)]
			}
			if r.IntN(6) == 0 {
				c.CreatedAt = t0.Add(time.Duration(r.IntN(300)-60) * time.Second)
			}
			cands = append(cands, c)
			if r.IntN(5) == 0 {
				claimed = append(claimed, int64(1+i))
			}
		}
		g, res := MatchT212Order(o, cands, claimed)
		fits := map[int64]bool{}
		seen := map[int64]bool{}
		for _, c := range cands {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			inWindow := !c.CreatedAt.Before(t0.Add(-T212MatchBefore)) && !c.CreatedAt.After(t0.Add(T212MatchAfter))
			free := true
			for _, id := range claimed {
				free = free && id != c.ID
			}
			if free && inWindow && c.Ticker == o.Ticker && c.Side == T212Buy && c.Type == T212Market && c.Quantity == o.Quantity && c.InitiatedFrom == "API" {
				fits[c.ID] = true
			}
		}
		switch res {
		case T212MatchOne:
			ones++
			if !fits[g.ID] || len(fits) != 1 {
				t.Fatalf("adopted %+v; fitting %v", g, fits)
			}
		case T212MatchAmbiguous:
			ambiguous++
			if len(fits) < 2 {
				t.Fatalf("ambiguous with %d fitting", len(fits))
			}
		case T212MatchNone:
			if len(fits) != 0 {
				t.Fatalf("none with fitting %v", fits)
			}
		}
	}
	if ones < 50 || ambiguous < 5 {
		t.Fatalf("the generator is too narrow: %d adopted, %d ambiguous", ones, ambiguous)
	}
}

func TestMatchInstrument(t *testing.T) {
	aapl := Instrument{ID: 1001, Symbol: "AAPL", Market: MarketNYSE, Type: "Stocks", Currency: "USD"}
	barc := Instrument{ID: 2002, Symbol: "BARC.L", Market: MarketLSE, Type: "Stocks", Currency: "GBX"}
	vusa := Instrument{ID: 3003, Symbol: "VUSA.L", Market: MarketLSE, Type: "ETF", Currency: "GBX"}
	cat := []T212Instrument{
		{Ticker: "AAPL_US_EQ", ShortName: "AAPL", Currency: "USD", Type: "STOCK", ISIN: "US0378331005"},
		{Ticker: "BARCl_EQ", ShortName: "BARC", Currency: "GBX", Type: "STOCK", ISIN: "GB0031348658"},
		{Ticker: "VUSAl_EQ", ShortName: "VUSA", Currency: "GBX", Type: "ETF"},
		{Ticker: "AAPL_EQ", ShortName: "AAPL", Currency: "EUR", Type: "STOCK"},
	}
	tests := []struct {
		name   string
		in     Instrument
		cands  []T212Instrument
		ticker string
		err    error
	}{
		{"New York", aapl, cat, "AAPL_US_EQ", nil},
		{"London", barc, cat, "BARCl_EQ", nil},
		{"London ETF", vusa, cat, "VUSAl_EQ", nil},
		{"GBp spelling", func() Instrument { i := barc; i.Currency = "GBp"; return i }(), cat, "BARCl_EQ", nil},
		{"lower-case symbol", func() Instrument { i := aapl; i.Symbol = "aapl"; return i }(), cat, "AAPL_US_EQ", nil},
		{"wrong suffix", aapl, []T212Instrument{{Ticker: "AAPLl_EQ", ShortName: "AAPL", Currency: "USD", Type: "STOCK"}}, "", ErrNotFound},
		{"short name differs", aapl, []T212Instrument{{Ticker: "AAPL_US_EQ", ShortName: "APPL", Currency: "USD", Type: "STOCK"}}, "", ErrNotFound},
		{"currency differs on Trading 212", barc, []T212Instrument{{Ticker: "BARCl_EQ", ShortName: "BARC", Currency: "GBP", Type: "STOCK"}}, "", ErrNotFound},
		{"type differs", vusa, []T212Instrument{{Ticker: "VUSAl_EQ", ShortName: "VUSA", Currency: "GBX", Type: "STOCK"}}, "", ErrNotFound},
		{"two candidates", aapl, append(cat, T212Instrument{Ticker: "AAPL2_US_EQ", ShortName: "AAPL", Currency: "USD", Type: "STOCK"}), "", ErrConflict},
		{"eToro prices it in USD in London", func() Instrument { i := barc; i.Currency = "USD"; return i }(), cat, "", ErrInvalid},
		{"Sydney", Instrument{Symbol: "CBA.ASX", Market: MarketASX, Type: "Stocks", Currency: "AUD"}, cat, "", ErrInvalid},
		{"not a share", Instrument{Symbol: "BTC", Market: MarketNYSE, Type: "Crypto", Currency: "USD"}, cat, "", ErrInvalid},
		{"empty catalogue", aapl, nil, "", ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, err := MatchInstrument(tc.in, tc.cands)
			if tc.err != nil {
				if !errors.Is(err, tc.err) || g.Ticker != "" {
					t.Fatalf("MatchInstrument = %+v, %v; want %v", g, err, tc.err)
				}
				return
			}
			if err != nil || g.Ticker != tc.ticker {
				t.Fatalf("MatchInstrument = %+v, %v; want %s", g, err, tc.ticker)
			}
		})
	}
}

func TestT212Eligibility(t *testing.T) {
	ok := T212Instrument{Ticker: "BARCl_EQ", ShortName: "BARC", Currency: "GBX", Type: "STOCK", MaxOpenQuantity: 1000 * wholeShare, FetchedAt: t0}
	tests := []struct {
		name   string
		t      T212Instrument
		acct   string
		long   bool
		reason string
	}{
		{"GBX on GBP", ok, "GBP", true, ""},
		{"ETF", func() T212Instrument { i := ok; i.Type = "ETF"; return i }(), "GBP", true, ""},
		{"GBX on USD", ok, "USD", false, "cannot be traded"},
		{"EUR account", ok, "EUR", false, "cannot be traded"},
		{"no position allowed", func() T212Instrument { i := ok; i.MaxOpenQuantity = 0; return i }(), "GBP", false, "no position"},
		{"warrant", func() T212Instrument { i := ok; i.Type = "WARRANT"; return i }(), "GBP", false, "not a share"},
		{"no instrument", T212Instrument{}, "GBP", false, "no Trading 212 instrument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := T212Eligibility(2002, "BARC.L", tc.t, tc.acct)
			if e.RealLong != tc.long || !strings.Contains(e.Reason, tc.reason) || e.Instrument != 2002 || e.CheckedAt != tc.t.FetchedAt {
				t.Fatalf("T212Eligibility = %+v", e)
			}
			if allowed, _ := e.Allows(Short); allowed || e.ShortCFD {
				t.Fatal("a short is allowed on Trading 212")
			}
			if e.MinAmount(Long) != 0 {
				t.Fatal("a minimum from nowhere")
			}
		})
	}
	m := InstrumentMatch{AccountID: "a", Instrument: 1, Ticker: "X"}
	if m.Confirmed() {
		t.Fatal("unconfirmed match confirmed")
	}
	m.ConfirmedBy, m.ConfirmedAt = "u", t0
	if !m.Confirmed() {
		t.Fatal("confirmed match not confirmed")
	}
	if s, ok := T212SuffixFor(MarketASX); ok || s != "" {
		t.Fatal("ASX suffix")
	}
}

func TestValidateForPlatform(t *testing.T) {
	etoro := DefaultRiskLimits
	etoro.TradingBudget = 100_000
	t212 := etoro
	t212.MaxShort, t212.MinOrderValue = 0, 5_000
	tests := []struct {
		name   string
		l      RiskLimits
		change func(*RiskLimits)
		p      Platform
		field  string
	}{
		{"eToro defaults", etoro, func(*RiskLimits) {}, PlatformEtoro, ""},
		{"eToro blank platform", etoro, func(*RiskLimits) {}, "", ""},
		{"eToro with a minimum order", etoro, func(l *RiskLimits) { l.MinOrderValue = 1 }, PlatformEtoro, "minOrderValueCents"},
		{"eToro with the fractional switch", etoro, func(l *RiskLimits) { l.T212Fractional = true }, PlatformEtoro, "t212Fractional"},
		{"Trading 212", t212, func(*RiskLimits) {}, PlatformTrading212, ""},
		{"Trading 212 minimum equals the budget", t212, func(l *RiskLimits) { l.MinOrderValue = l.TradingBudget }, PlatformTrading212, ""},
		{"Trading 212 shorts", t212, func(l *RiskLimits) { l.MaxShort = 100 }, PlatformTrading212, "maxShortBp"},
		{"Trading 212 without a minimum", t212, func(l *RiskLimits) { l.MinOrderValue = 0 }, PlatformTrading212, "minOrderValueCents"},
		{"Trading 212 minimum over the budget", t212, func(l *RiskLimits) { l.MinOrderValue = l.TradingBudget + 1 }, PlatformTrading212, "minOrderValueCents"},
		{"Trading 212 without a budget or a minimum", t212, func(l *RiskLimits) { l.TradingBudget, l.MinOrderValue = 0, 0 }, PlatformTrading212, ""},
		{"Trading 212 without a budget, with a minimum", t212, func(l *RiskLimits) { l.TradingBudget = 0 }, PlatformTrading212, ""},
		{"Trading 212 fractional while unverified", t212, func(l *RiskLimits) { l.T212Fractional = true }, PlatformTrading212, "t212Fractional"},
		{"Saxo", etoro, func(*RiskLimits) {}, PlatformSaxo, "platform"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.l
			tc.change(&l)
			err := l.ValidateForPlatform(tc.p)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("ValidateForPlatform = %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("ValidateForPlatform = %v, want field %s", err, tc.field)
			}
		})
	}
	neg := etoro
	neg.MinOrderValue = -1
	if err := neg.Validate(); err == nil {
		t.Fatal("a negative minimum order value validated")
	}
}

func TestAssessEntryMinOrderValue(t *testing.T) {
	in := okInput() // requests 100 000
	in.Limits.MinOrderValue = 100_000
	if d := AssessEntry(in); !d.Allowed || d.Size != 100_000 {
		t.Fatalf("at the minimum: %+v", d)
	}
	in.Limits.MinOrderValue = 100_001
	if d := AssessEntry(in); d.Allowed || !strings.Contains(d.Reason, "minimum order value") {
		t.Fatalf("below the minimum: %+v", d)
	}
	// The cap shrinks the size below the minimum: refused, never enlarged.
	in.Limits.MinOrderValue = 50_000
	in.Limits.MaxPosition = 400 // 4 % of 1 000 000 = 40 000
	if d := AssessEntry(in); d.Allowed {
		t.Fatalf("a shrunk size below the minimum was allowed: %+v", d)
	}
}

func TestT212AccountPosition(t *testing.T) {
	a := T212Account{Positions: []T212Position{{Ticker: "AAPL_US_EQ", Quantity: wholeShare}}}
	if p, ok := a.Position("AAPL_US_EQ"); !ok || p.Quantity != wholeShare {
		t.Fatal("position not found")
	}
	if _, ok := a.Position("MSFT_US_EQ"); ok {
		t.Fatal("a position from nowhere")
	}
}

func TestT212StopPrice(t *testing.T) {
	for _, c := range []struct{ in, want Micros }{
		{0, 0},
		{-5, 0},
		{1, 10_000},
		{10_000, 10_000},
		{10_001, 20_000},
		{197_123_456, 197_130_000},
		{197_130_000, 197_130_000},
	} {
		if got := T212StopPrice(c.in); got != c.want {
			t.Errorf("T212StopPrice(%d) = %d, want %d", c.in, got, c.want)
		}
		if c.in > 0 && (T212StopPrice(c.in) < c.in || T212StopPrice(c.in)-c.in >= T212StopTick) {
			t.Errorf("T212StopPrice(%d) not rounded up by less than a tick", c.in)
		}
	}
}

func TestT212OrderValue(t *testing.T) {
	for _, c := range []struct {
		q, per Micros
		want   Cents
	}{
		{0, 1, 0},
		{1, 0, 0},
		{-1, 5, 0},
		{3_000_000, 200_500_000, 601},       // 3 × 200.5 GBX = 601.5 pence, rounded down
		{2_000_000, 18_910_000_000, 37_820}, // 2 × $189.10
		{1_000_000, 999_999, 0},             // under one minor unit
		{math.MaxInt64, math.MaxInt64, math.MaxInt64}, // saturates, never wraps
	} {
		if got := T212OrderValue(c.q, c.per); got != c.want {
			t.Errorf("T212OrderValue(%d, %d) = %d, want %d", c.q, c.per, got, c.want)
		}
	}
	// The value is never above the reservation.
	for q := Micros(1_000_000); q <= 50_000_000; q += 7_000_000 {
		if v, r := T212OrderValue(q, 123_456_789), T212Reserve(q, 123_456_789); v > r {
			t.Fatalf("value %s above the reservation %s", v, r)
		}
	}
}

func TestT212AccountPortfolio(t *testing.T) {
	at := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	acct := T212Account{
		Currency: "GBP", AvailableToTrade: 100_000, ReservedForOrders: 2_000, AsOf: at,
		Positions: []T212Position{
			{Ticker: "BARCl_EQ", Quantity: 3_000_000, AvgPrice: 200_000_000, Cost: 600, PnL: 15},
			{Ticker: "VODl_EQ", Quantity: 10_000_000, Cost: 700, PnL: -20},
			{Ticker: "OWNl_EQ", Quantity: 1_000_000, Cost: 50, PnL: 0},
		},
	}
	ours := []AgentPosition{
		{PositionID: 9001, Ticker: "BARCl_EQ", Instrument: 1, Direction: Long},
		{PositionID: 9002, Ticker: "VODl_EQ", Instrument: 4, Direction: Long, ClosedAt: at}, // closed: not ours any more
	}
	pf := acct.Portfolio(ours, map[string]InstrumentID{"VODl_EQ": 4})
	if pf.AvailableCash != 100_000 || pf.PendingOpen != 2_000 || !pf.AsOf.Equal(at) || len(pf.Positions) != 3 {
		t.Fatalf("portfolio = %+v", pf)
	}
	if p := pf.Positions[0]; p.ID != 9001 || p.Instrument != 1 || !p.Conforms() || p.Amount != 600 || p.UnrealisedPnL != 15 {
		t.Fatalf("ours = %+v", p)
	}
	if p := pf.Positions[1]; p.ID != 0 || p.Instrument != 4 || !p.Conforms() {
		t.Fatalf("the owner's matched share = %+v", p)
	}
	if p := pf.Positions[2]; p.ID != 0 || p.Instrument != 0 {
		t.Fatalf("the owner's other share = %+v", p)
	}
	if eq := pf.Equity(); eq != 100_000+2_000+600+15+700-20+50 {
		t.Fatalf("equity = %s", eq)
	}
}
