package domain

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func validDecision() Decision {
	return Decision{
		ID: "d1", AccountID: "a1", At: time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC),
		Signal:  DecisionSignal{Instrument: 1001, Symbol: "AAPL", Direction: Long, Playbook: "trend_breakout", Entry: 1, SuggestedSize: 100},
		Verdict: DecisionVerdict{Kind: VerdictReduce, SizeBp: 5_000, LLMCallID: "c1"},
		Risk:    DecisionRisk{Allowed: true, Size: 50},
		Outcome: DecisionOrdered, OrderRequestID: "r1",
	}
}

func TestDecisionValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Decision)
		ok     bool
	}{
		{"ordered", func(*Decision) {}, true},
		{"pending", func(d *Decision) { d.Outcome, d.OrderRequestID = DecisionPending, "" }, true},
		{"not reviewed", func(d *Decision) { d.Verdict = DecisionVerdict{} }, true},
		{"short rejected by the reviewer", func(d *Decision) {
			d.Signal.Direction, d.Outcome, d.OrderRequestID = Short, DecisionRejectedReviewer, ""
			d.Verdict, d.Risk = DecisionVerdict{Kind: VerdictReject}, DecisionRisk{}
		}, true},
		{"order failed without request id", func(d *Decision) { d.Outcome, d.OrderRequestID = DecisionOrderFailed, "" }, true},
		{"no id", func(d *Decision) { d.ID = "" }, false},
		{"no account", func(d *Decision) { d.AccountID = "" }, false},
		{"no time", func(d *Decision) { d.At = time.Time{} }, false},
		{"unknown outcome", func(d *Decision) { d.Outcome = "won" }, false},
		{"empty outcome", func(d *Decision) { d.Outcome = "" }, false},
		{"bad direction", func(d *Decision) { d.Signal.Direction = "sideways" }, false},
		{"bad verdict", func(d *Decision) { d.Verdict.Kind = "increase" }, false},
		{"verdict above full size", func(d *Decision) { d.Verdict.SizeBp = FullSize + 1 }, false},
		{"negative bp", func(d *Decision) { d.Verdict.SizeBp = -1 }, false},
		{"negative risk size", func(d *Decision) { d.Risk.Size = -1 }, false},
		{"negative suggested size", func(d *Decision) { d.Signal.SuggestedSize = -1 }, false},
		{"ordered without request id", func(d *Decision) { d.OrderRequestID = "" }, false},
		{"ordered without risk approval", func(d *Decision) { d.Risk.Allowed = false }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDecision()
			tt.change(&d)
			err := d.Validate()
			if tt.ok != (err == nil) || (err != nil && !errors.Is(err, ErrInvalid)) {
				t.Fatalf("Validate = %v, want ok=%v", err, tt.ok)
			}
		})
	}
	for _, o := range DecisionOutcomes {
		if !o.Valid() {
			t.Errorf("%s invalid", o)
		}
	}
}

func TestDecisionSignalAndClip(t *testing.T) {
	c := Candidate{
		Instrument: 7, Direction: Short, Playbook: "p", Reason: "r", Entry: 10, StopLossRate: 11, TakeProfitRate: 8,
		SuggestedSize: 99, RegimeProbs: map[Regime]float64{RegimeTrending: 0.25},
	}
	s := SignalFromCandidate(c, "TSLA")
	if s.Instrument != 7 || s.Symbol != "TSLA" || s.Direction != Short || s.StopLoss != 11 || s.TakeProfit != 8 || s.SuggestedSize != 99 || s.RegimeProbs[RegimeTrending] != 0.25 {
		t.Fatalf("signal = %+v", s)
	}
	c.RegimeProbs[RegimeTrending] = 0.9 // the decision keeps its own copy
	if s.RegimeProbs[RegimeTrending] != 0.25 {
		t.Fatal("probabilities shared with the candidate")
	}
	if SignalFromCandidate(Candidate{}, "").RegimeProbs != nil {
		t.Fatal("nil probabilities became a map")
	}
	d := validDecision()
	d.Signal.Reason = strings.Repeat("é", 400)
	d.Verdict.Reason = strings.Repeat("x", 600)
	d.Risk.Reason = "fine"
	d.Signal.Symbol = " " + strings.Repeat("S", 40)
	d = d.Clip()
	if len(d.Signal.Reason) > MaxDecisionReason || !utf8.ValidString(d.Signal.Reason) || len(d.Verdict.Reason) > MaxDecisionReason || d.Risk.Reason != "fine" || len(d.Signal.Symbol) > 32 {
		t.Fatalf("clip = %d %d %q %q", len(d.Signal.Reason), len(d.Verdict.Reason), d.Risk.Reason, d.Signal.Symbol)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"abcdefghijk", 10, "abcdefg…"},
		{"ééééé", 6, "éé"[:2] + "…"}, // "é" is 2 bytes, "…" is 3: one é fits
		{"€€€", 7, "€…"},
		{"abc", 2, ""},
		{"", 0, ""},
		{"a\xffb", 10, "a�b"},
	}
	for _, tt := range tests {
		got := Truncate(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
	// property: valid UTF-8, never longer than n, a prefix of the (valid) input when not cut
	r := rand.New(rand.NewPCG(1, 2))
	alphabet := []string{"a", "é", "€", "𝄞", "\xff", "\n", "…"}
	for range 20_000 {
		var b strings.Builder
		for range r.IntN(60) {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		in, n := b.String(), r.IntN(80)
		got := Truncate(in, n)
		valid := strings.ToValidUTF8(in, "�")
		switch {
		case len(got) > n || !utf8.ValidString(got):
			t.Fatalf("Truncate(%q, %d) = %q", in, n, got)
		case len(valid) <= n && got != valid:
			t.Fatalf("Truncate(%q, %d) cut a fitting string: %q", in, n, got)
		case len(valid) > n && got != "" && !strings.HasPrefix(valid, strings.TrimSuffix(got, "…")):
			t.Fatalf("Truncate(%q, %d) = %q is not a prefix", in, n, got)
		}
	}
	if len(Truncate(strings.Repeat("x", MaxLLMText+5), MaxLLMText)) != MaxLLMText {
		t.Fatal("MaxLLMText cut")
	}
}

func TestLLMCost(t *testing.T) {
	p := LLMProfile{InputPrice: 3_000_000, OutputPrice: 15_000_000} // $3 / $15 per million
	tests := []struct {
		name    string
		p       LLMProfile
		in, out int64
		want    Micros
	}{
		{"zero tokens", p, 0, 0, 0},
		{"one million each", p, 1_000_000, 1_000_000, 18_000_000},
		{"typical", p, 1_234, 567, 12_207}, // 3 702 + 8 505 = 12 207 micros
		{"rounded up", p, 1, 0, 3},         // 3 micros exactly
		{"sub-micro rounds up", LLMProfile{InputPrice: 1, OutputPrice: 1}, 1, 0, 1},
		{"negative counts are zero", p, -5, -9, 0},
		{"unknown input price", LLMProfile{OutputPrice: 1}, 10, 10, CostUnknown},
		{"unknown output price", LLMProfile{InputPrice: 1}, 10, 10, CostUnknown},
		{"negative price", LLMProfile{InputPrice: -1, OutputPrice: 1}, 10, 10, CostUnknown},
		{"capped tokens", p, math.MaxInt64, 0, Micros(MaxLLMTokens * 3)},
		{"capped result", LLMProfile{InputPrice: math.MaxInt64, OutputPrice: math.MaxInt64}, math.MaxInt64, math.MaxInt64, math.MaxInt64},
	}
	for _, tt := range tests {
		if got := tt.p.Cost(tt.in, tt.out); got != tt.want {
			t.Errorf("%s: Cost(%d, %d) = %d, want %d", tt.name, tt.in, tt.out, got, tt.want)
		}
	}
}

// TestLLMCostMonotonicNoOverflow: for random prices (including the extremes) and token
// counts, the cost is exact (ceil of the big-integer value, capped), never negative for known
// prices, and never falls when either count grows.
func TestLLMCostMonotonicNoOverflow(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 11))
	pick := func() int64 {
		switch r.IntN(4) {
		case 0:
			return r.Int64()
		case 1:
			return r.Int64N(MaxLLMTokens * 2)
		case 2:
			return r.Int64N(100_000) - 10
		}
		return []int64{0, 1, math.MaxInt64, MaxLLMTokens, MaxLLMTokens + 1, -1, math.MinInt64}[r.IntN(7)]
	}
	price := func() Micros {
		if r.IntN(3) == 0 {
			return []Micros{1, MaxLLMPrice, math.MaxInt64}[r.IntN(3)]
		}
		return Micros(r.Int64N(int64(MaxLLMPrice)) + 1)
	}
	million, maxI := big.NewInt(1_000_000), big.NewInt(math.MaxInt64)
	clamp := func(n int64) int64 { return min(max(n, 0), MaxLLMTokens) }
	for range 100_000 {
		p := LLMProfile{InputPrice: price(), OutputPrice: price()}
		in, out := pick(), pick()
		got := p.Cost(in, out)
		want := new(big.Int).Mul(big.NewInt(clamp(in)), big.NewInt(int64(p.InputPrice)))
		want.Add(want, new(big.Int).Mul(big.NewInt(clamp(out)), big.NewInt(int64(p.OutputPrice))))
		want.Add(want, big.NewInt(999_999)).Div(want, million)
		if want.Cmp(maxI) > 0 {
			want = maxI
		}
		if got < 0 || big.NewInt(int64(got)).Cmp(want) != 0 {
			t.Fatalf("Cost(%d, %d) at %d/%d = %d, want %s", in, out, p.InputPrice, p.OutputPrice, got, want)
		}
		dIn, dOut := r.Int64N(1_000_000), r.Int64N(1_000_000)
		in2, out2 := in, out
		if in2 <= math.MaxInt64-dIn {
			in2 += dIn
		}
		if out2 <= math.MaxInt64-dOut {
			out2 += dOut
		}
		if more := p.Cost(in2, out2); more < got {
			t.Fatalf("Cost fell: (%d, %d) = %d, (%d, %d) = %d", in, out, got, in2, out2, more)
		}
	}
}

func TestLLMProfilePrices(t *testing.T) {
	base := LLMProfile{Name: "n", LLMConfig: LLMConfig{Provider: "anthropic"}}
	for _, c := range []struct {
		in, out Micros
		ok      bool
	}{
		{0, 0, true},
		{3_000_000, 15_000_000, true},
		{MaxLLMPrice, MaxLLMPrice, true},
		{-1, 0, false},
		{0, -1, false},
		{MaxLLMPrice + 1, 0, false},
		{0, MaxLLMPrice + 1, false},
	} {
		p := base
		p.InputPrice, p.OutputPrice = c.in, c.out
		if err := p.Validate(); c.ok != (err == nil) {
			t.Errorf("prices %d/%d: %v", c.in, c.out, err)
		}
	}
	for _, u := range []LLMPurpose{LLMPurposeAgent, LLMPurposeScanner, LLMPurposeTest} {
		if !u.Valid() {
			t.Errorf("%s invalid", u)
		}
	}
	if LLMPurpose("x").Valid() {
		t.Fatal("unknown purpose valid")
	}
}
