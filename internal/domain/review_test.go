package domain

import (
	"math/big"
	"math/rand/v2"
	"testing"
	"time"
)

// Tests for the findings of the phase-4 money-path review (2026-11-24).

// TestAssessEntryHugeValues (review m1): the loss allowance allowance × entry / stop distance
// must not overflow int64. With huge equity, a high price and a tiny stop distance the
// allowance is enormous and must neither shrink nor refuse the entry.
func TestAssessEntryHugeValues(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	checked := 0
	for range 5_000 {
		in := okInput()
		eq := Cents(1_000_000_000_000 + r.Int64N(1_000_000_000_000_000))
		in.Portfolio = Portfolio{AvailableCash: eq, AsOf: in.Portfolio.AsOf}
		in.State.StartOfDayEquity, in.State.HighWaterMark = eq, eq
		in.Limits.TradingBudget, in.Limits.MaxPosition = eq, 0
		in.Entry = Micros(1_000_000_000 + r.Int64N(1_000_000_000_000_000))
		dist := Micros(1 + r.Int64N(int64(in.Entry)/2))
		if r.IntN(2) == 0 {
			dist = Micros(1 + r.Int64N(1_000))
		}
		in.StopLoss = in.Entry - dist
		in.Requested = Cents(1 + r.Int64N(int64(eq)))
		allowance := big.NewInt(int64(eq - DailyFloor(in.State, in.Limits)))
		byStop := new(big.Int).Mul(allowance, big.NewInt(int64(in.Entry)))
		byStop.Quo(byStop, big.NewInt(int64(dist)))
		want := in.Requested
		if byStop.IsInt64() && byStop.Int64() < int64(want) {
			want = Cents(byStop.Int64())
		}
		if want < in.Eligibility.MinPositionAmount {
			continue
		}
		checked++
		d := AssessEntry(in)
		if !d.Allowed || d.Size != want {
			t.Fatalf("equity %d entry %d dist %d requested %d: decision %+v, want size %d", eq, in.Entry, dist, in.Requested, d, want)
		}
	}
	if checked < 1_000 {
		t.Fatalf("only %d inputs checked", checked)
	}
}

func TestMulDivSaturates(t *testing.T) {
	const maxI = int64(^uint64(0) >> 1)
	for _, tt := range []struct{ a, b, c, want int64 }{
		{10, 20, 3, 66},
		{maxI, maxI, maxI, maxI},
		{maxI, 2, 1, maxI},
		{maxI, 2, 4, maxI / 2},
		{1 << 40, 1 << 40, 1 << 20, 1 << 60},
		{0, 5, 1, 0},
		{5, 5, 0, 0},
		{-1, 5, 1, 0},
	} {
		if got := mulDiv(tt.a, tt.b, tt.c); got != tt.want {
			t.Errorf("mulDiv(%d, %d, %d) = %d, want %d", tt.a, tt.b, tt.c, got, tt.want)
		}
	}
}

// TestOpenSendLimit (review M3): an open is sent once and never resent, not even after a 429,
// until 4.17 verifies how eToro treats a repeated x-request-id (U1) and a 429 (U7).
func TestOpenSendLimit(t *testing.T) {
	if MaxSends(OrderOpen) != 1 {
		t.Fatalf("MaxSends(open) = %d, want 1", MaxSends(OrderOpen))
	}
	o := openOrder(t)
	o, err := o.Transition(OrderSent, t0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Transition(OrderSent, t0.Add(5*time.Second), "429"); err == nil {
		t.Fatal("an open was resent")
	}
}

// TestUnseenFills (review B1): a filled open order keeps its cash reserved until a portfolio
// read from after the fill shows every position it opened, or FillVisibleAfter has passed.
func TestUnseenFills(t *testing.T) {
	fillAt := t0.Add(-10 * time.Second)
	filled := func(id string, d Direction, inst InstrumentID, amount Cents, pos int64, at time.Time) Order {
		return Order{RequestID: id, Kind: OrderOpen, State: OrderFilled, Direction: d, Instrument: inst, Amount: amount, PositionID: pos, UpdatedAt: at}
	}
	pf := func(asOf time.Time, ids ...int64) Portfolio {
		p := Portfolio{AsOf: asOf}
		for _, id := range ids {
			p.Positions = append(p.Positions, Position{ID: id})
		}
		return p
	}
	ap := func(order string, id int64) AgentPosition {
		return AgentPosition{PositionID: id, OpenRequestID: order}
	}
	tests := []struct {
		name   string
		orders []Order
		ours   []AgentPosition
		pf     Portfolio
		want   Reservation
	}{
		{
			"fill not in the portfolio yet",
			[]Order{filled("a", Long, 1, 100, 11, fillAt)},
			[]AgentPosition{ap("a", 11)},
			pf(t0),
			Reservation{Amount: 100, Count: 1, Instruments: []InstrumentID{1}},
		},
		{"fill shown by a read after it", []Order{filled("a", Long, 1, 100, 11, fillAt)}, []AgentPosition{ap("a", 11)}, pf(t0, 11), Reservation{}},
		{
			"fill shown, but by a read from before it",
			[]Order{filled("a", Long, 1, 100, 11, fillAt)},
			[]AgentPosition{ap("a", 11)},
			pf(fillAt.Add(-time.Second), 11),
			Reservation{Amount: 100, Count: 1, Instruments: []InstrumentID{1}},
		},
		{"visibility window passed", []Order{filled("a", Long, 1, 100, 11, t0.Add(-FillVisibleAfter))}, nil, pf(t0), Reservation{}},
		{
			"one of two positions shown",
			[]Order{filled("a", Short, 2, 300, 11, fillAt)},
			[]AgentPosition{ap("a", 11), ap("a", 12)},
			pf(t0, 11),
			Reservation{Amount: 300, Count: 1, Short: 300, Instruments: []InstrumentID{2}},
		},
		{
			"no position known: reserved",
			[]Order{filled("a", Long, 1, 100, 0, fillAt)},
			nil, pf(t0, 11),
			Reservation{Amount: 100, Count: 1, Instruments: []InstrumentID{1}},
		},
		{
			"cancelled after a partial fill",
			[]Order{{RequestID: "c", Kind: OrderOpen, State: OrderCancelled, Direction: Long, Instrument: 3, Amount: 50, PositionID: 21, UpdatedAt: fillAt}},
			nil, pf(t0),
			Reservation{Amount: 50, Count: 1, Instruments: []InstrumentID{3}},
		},
		{"cancelled without a fill, rejected, abandoned, close, not final: nothing", []Order{
			{RequestID: "c", Kind: OrderOpen, State: OrderCancelled, Instrument: 3, Amount: 50, UpdatedAt: fillAt},
			{RequestID: "r", Kind: OrderOpen, State: OrderRejected, Instrument: 4, Amount: 50, PositionID: 9, UpdatedAt: fillAt},
			{RequestID: "x", Kind: OrderOpen, State: OrderAbandoned, Instrument: 5, Amount: 50, UpdatedAt: fillAt},
			{RequestID: "k", Kind: OrderClose, State: OrderFilled, Instrument: 6, PositionID: 7, UpdatedAt: fillAt},
			{RequestID: "p", Kind: OrderOpen, State: OrderPending, Instrument: 7, Amount: 50, UpdatedAt: fillAt},
		}, nil, pf(t0), Reservation{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Reservation{}.WithUnseenFills(tt.orders, tt.ours, tt.pf, t0)
			if got.Amount != tt.want.Amount || got.Count != tt.want.Count || got.Short != tt.want.Short || got.Unknown != 0 ||
				len(got.Instruments) != len(tt.want.Instruments) || (len(got.Instruments) > 0 && got.Instruments[0] != tt.want.Instruments[0]) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	// added to the unfinished orders' reservation, instruments merged and sorted
	base := Reservations([]Order{{Kind: OrderOpen, State: OrderPending, Direction: Long, Instrument: 9, Amount: 70}})
	got := base.WithUnseenFills([]Order{filled("a", Long, 1, 100, 11, fillAt), filled("b", Long, 9, 5, 12, fillAt)}, nil, pf(t0), t0)
	if got.Amount != 175 || got.Count != 3 || len(got.Instruments) != 2 || got.Instruments[0] != 1 || got.Instruments[1] != 9 {
		t.Fatalf("merged = %+v", got)
	}
}

// TestAdoptUnrecorded (review B2): a position nobody recorded is ours when one of our recent
// opens that ended (filled, cancelled or abandoned) explains it: same instrument and direction,
// opened after the send, within 24 h, its stored position id when it has one, and no more
// than the order's amount.
func TestAdoptUnrecorded(t *testing.T) {
	sent := t0.Add(-time.Hour)
	ord := func(id string, st OrderState, d Direction, inst InstrumentID, amount Cents, pos int64) Order {
		return Order{RequestID: id, Kind: OrderOpen, State: st, Direction: d, Instrument: inst, Amount: amount, PositionID: pos, SentAt: sent, Sends: 1}
	}
	pos := func(id int64, inst InstrumentID, long bool, amount Cents, opened time.Time) Position {
		return Position{ID: id, Instrument: inst, Long: long, Amount: amount, OpenedAt: opened}
	}
	after := sent.Add(2 * time.Second)
	tests := []struct {
		name   string
		orders []Order
		pos    []Position
		want   map[int64]string // position → order
	}{
		{"abandoned order eToro holds (U2)", []Order{ord("a", OrderAbandoned, Long, 1, 100, 0)}, []Position{pos(11, 1, true, 100, after)}, map[int64]string{11: "a"}},
		{"filled order whose recording failed", []Order{ord("a", OrderFilled, Short, 1, 100, 11)}, []Position{pos(11, 1, false, 100, after)}, map[int64]string{11: "a"}},
		{"stored position id is another one", []Order{ord("a", OrderFilled, Long, 1, 100, 12)}, []Position{pos(11, 1, true, 100, after)}, nil},
		{
			"cancelled with fills, two positions within the amount",
			[]Order{ord("a", OrderCancelled, Long, 1, 100, 0)},
			[]Position{pos(11, 1, true, 40, after), pos(12, 1, true, 60, after.Add(time.Second))},
			map[int64]string{11: "a", 12: "a"},
		},
		{"more than the order's amount", []Order{ord("a", OrderAbandoned, Long, 1, 100, 0)}, []Position{pos(11, 1, true, 200, after)}, nil},
		{"other instrument", []Order{ord("a", OrderAbandoned, Long, 1, 100, 0)}, []Position{pos(11, 2, true, 100, after)}, nil},
		{"other direction", []Order{ord("a", OrderAbandoned, Long, 1, 100, 0)}, []Position{pos(11, 1, false, 100, after)}, nil},
		{"opened before the send", []Order{ord("a", OrderAbandoned, Long, 1, 100, 0)}, []Position{pos(11, 1, true, 100, sent.Add(-time.Second))}, nil},
		{
			"sent over 24 h ago",
			[]Order{{RequestID: "a", Kind: OrderOpen, State: OrderAbandoned, Direction: Long, Instrument: 1, Amount: 100, SentAt: t0.Add(-25 * time.Hour)}},
			[]Position{pos(11, 1, true, 100, t0.Add(-24*time.Hour))},
			nil,
		},
		{"never sent", []Order{{RequestID: "a", Kind: OrderOpen, State: OrderAbandoned, Direction: Long, Instrument: 1, Amount: 100}}, []Position{pos(11, 1, true, 100, after)}, nil},
		{"rejected, still open or a close: never", []Order{
			ord("r", OrderRejected, Long, 1, 100, 0), ord("p", OrderPending, Long, 1, 100, 0),
			{RequestID: "c", Kind: OrderClose, State: OrderFilled, Direction: Long, Instrument: 1, SentAt: sent},
		}, []Position{pos(11, 1, true, 100, after)}, nil},
		{"the latest order sent before the position", []Order{
			ord("old", OrderAbandoned, Long, 1, 100, 0),
			{RequestID: "new", Kind: OrderOpen, State: OrderAbandoned, Direction: Long, Instrument: 1, Amount: 100, SentAt: after.Add(-time.Second), Sends: 1},
		}, []Position{pos(11, 1, true, 100, after)}, map[int64]string{11: "new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdoptUnrecorded(tt.orders, tt.pos, t0)
			if len(got) != len(tt.want) {
				t.Fatalf("adopted %+v, want %v", got, tt.want)
			}
			for _, a := range got {
				if tt.want[a.Position.ID] != a.Order.RequestID {
					t.Fatalf("adopted %+v, want %v", got, tt.want)
				}
			}
		})
	}
}
