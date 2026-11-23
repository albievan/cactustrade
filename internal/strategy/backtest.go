package strategy

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Costs is a recorded cost table for one instrument, as fractions of the traded amount:
// what eToro's costs endpoint reported (spread, markup, fees, tax such as UK stamp duty),
// not a guess. Spread is the full bid/ask spread: half is paid on entry, half on exit.
type Costs struct {
	Spread   float64 // e.g. 0.0009 = 9 bp round trip
	BuyFee   float64 // markup + transaction fee + tax on buying (SDRT 0.005 on LSE shares)
	SellFee  float64 // fees on selling
	Recorded string  // where and when the table came from (shown in the report)
}

// BacktestConfig sets up a walk-forward backtest.
type BacktestConfig struct {
	Strategy    Config
	StartEquity domain.Cents
	Costs       Costs
	// SameSession reports whether two bar starts are in the same trading session (market
	// local date). Default: same UTC date. Positions never cross a session.
	SameSession func(a, b time.Time) bool
	// Warmup bars before the first decision (default FitWindow/2 + FeatureWindow).
	Warmup int
	// decide replaces the engine in tests of the fill rules.
	decide func(bars []domain.Candle, equity domain.Cents) Decision
}

// Trade is one simulated round trip.
type Trade struct {
	Playbook   string
	Entered    time.Time
	Exited     time.Time
	EntryPrice domain.Micros // after half the spread
	ExitPrice  domain.Micros // after half the spread
	Amount     domain.Cents
	PnL        domain.Cents // net of every cost
	Exit       string       // stop, take_profit, session_end, end_of_data
}

// Report is the honest summary of a backtest.
type Report struct {
	Instrument     domain.InstrumentID
	From, To       time.Time
	Bars           int
	Trades         []Trade
	StartEquity    domain.Cents
	EndEquity      domain.Cents
	NetReturn      float64 // (end-start)/start, after costs
	MaxDrawdown    float64 // largest peak-to-trough fall of marked equity, as a fraction
	Sharpe         float64 // per-session returns, annualised with √252 (0 with < 2 sessions)
	WinRate        float64
	BuyAndHold     float64 // the instrument's own return over the same bars, no costs
	CostsPaid      domain.Cents
	Refits         int
	Costs          Costs
	SkippedEntries int // signals not filled (gap through the stop, session over, no cash)
}

type openPos struct {
	pb         string
	units      float64
	entry      float64 // price paid per unit (incl. half spread)
	stop, tp   float64
	amount     domain.Cents
	enteredAt  time.Time
	costsOnBuy float64
}

// Backtest replays bars (one instrument, oldest first) bar by bar. At the close of bar t the
// engine sees bars[0..t] only; an entry signal fills at the open of bar t+1 (next-bar
// execution) if t+1 is in the same session and opens above the stop. Exits fill only at
// prices the bar actually traded: a stop at min(open, stop) when the low reaches it, a
// target at max(open, target) when the high reaches it, the stop first when both are in
// one bar (conservative), and the close of the session's last bar otherwise.
func Backtest(id domain.InstrumentID, bars []domain.Candle, cfg BacktestConfig) Report {
	scfg := cfg.Strategy.withDefaults()
	same := cfg.SameSession
	if same == nil {
		same = func(a, b time.Time) bool {
			ay, am, ad := a.UTC().Date()
			by, bm, bd := b.UTC().Date()
			return ay == by && am == bm && ad == bd
		}
	}
	warm := cfg.Warmup
	if warm <= 0 {
		warm = scfg.FitWindow/2 + scfg.FeatureWindow
	}
	rep := Report{Instrument: id, StartEquity: cfg.StartEquity, EndEquity: cfg.StartEquity, Costs: cfg.Costs}
	if len(bars) <= warm+1 {
		return rep
	}
	rep.From, rep.To, rep.Bars = bars[warm].Start, bars[len(bars)-1].Start, len(bars)-warm
	eng := NewEngine(scfg)
	decide := cfg.decide
	if decide == nil {
		decide = func(bs []domain.Candle, eq domain.Cents) Decision { return eng.Evaluate(id, bs, eq) }
	}
	cash := float64(cfg.StartEquity)
	var pos *openPos
	var pending *domain.Candidate
	peak, sessionStart := cash, cash
	var sessionRets []float64
	px := func(m domain.Micros) float64 { return float64(m) / 1e6 }

	closePos := func(at time.Time, price float64, why string) {
		sell := price * (1 - cfg.Costs.Spread/2)
		gross := pos.units * sell * 100 // cents
		fee := gross * cfg.Costs.SellFee
		cash += gross - fee
		pnl := gross - fee - float64(pos.amount)
		rep.CostsPaid += domain.Cents(math.Round(fee + pos.costsOnBuy + pos.units*(price-sell)*100))
		rep.Trades = append(rep.Trades, Trade{
			Playbook: pos.pb, Entered: pos.enteredAt, Exited: at, EntryPrice: micros(pos.entry), ExitPrice: micros(sell),
			Amount: pos.amount, PnL: domain.Cents(math.Round(pnl)), Exit: why,
		})
		pos = nil
	}

	for t := warm; t < len(bars); t++ {
		b := bars[t]
		lastOfSession := t == len(bars)-1 || !same(b.Start, bars[t+1].Start)
		// 1. fill yesterday-bar's signal at this bar's open
		if pending != nil {
			c := pending
			pending = nil
			open := px(b.Open)
			amount := min(float64(c.SuggestedSize), cash)
			switch {
			case !same(bars[t-1].Start, b.Start), open <= px(c.StopLossRate), amount < 100:
				rep.SkippedEntries++
			default:
				buy := open * (1 + cfg.Costs.Spread/2)
				fee := amount * cfg.Costs.BuyFee
				units := (amount - fee) / (buy * 100)
				cash -= amount
				pos = &openPos{
					pb: c.Playbook, units: units, entry: buy, stop: px(c.StopLossRate), tp: px(c.TakeProfitRate),
					amount: domain.Cents(math.Round(amount)), enteredAt: b.Start, costsOnBuy: fee + units*(buy-open)*100,
				}
			}
		}
		// 2. exits within this bar's traded range
		if pos != nil {
			open, lo, hi := px(b.Open), px(b.Low), px(b.High)
			switch {
			case lo <= pos.stop:
				closePos(b.Start, math.Min(open, pos.stop), "stop")
			case hi >= pos.tp:
				closePos(b.Start, math.Max(open, pos.tp), "take_profit")
			case lastOfSession && t == len(bars)-1:
				closePos(b.Start, px(b.Close), "end_of_data")
			case lastOfSession:
				closePos(b.Start, px(b.Close), "session_end") // never overnight
			}
		}
		// 3. mark to market
		eq := cash
		if pos != nil {
			eq += pos.units * px(b.Close) * (1 - cfg.Costs.Spread/2) * 100
		}
		peak = math.Max(peak, eq)
		if peak > 0 {
			rep.MaxDrawdown = math.Max(rep.MaxDrawdown, (peak-eq)/peak)
		}
		if lastOfSession {
			if sessionStart > 0 {
				sessionRets = append(sessionRets, eq/sessionStart-1)
			}
			sessionStart = eq
		}
		// 4. decide at this close (flat only, and not on the session's last bar)
		if pos == nil && !lastOfSession {
			if d := decide(bars[:t+1], domain.Cents(math.Floor(cash))); d.Candidate != nil {
				pending = d.Candidate
			}
		}
	}
	rep.EndEquity = domain.Cents(math.Round(cash))
	rep.Refits = eng.Refits
	if cfg.StartEquity > 0 {
		rep.NetReturn = float64(rep.EndEquity-cfg.StartEquity) / float64(cfg.StartEquity)
	}
	if first := px(bars[warm].Close); first > 0 {
		rep.BuyAndHold = px(bars[len(bars)-1].Close)/first - 1
	}
	wins := 0
	for _, tr := range rep.Trades {
		if tr.PnL > 0 {
			wins++
		}
	}
	if len(rep.Trades) > 0 {
		rep.WinRate = float64(wins) / float64(len(rep.Trades))
	}
	rep.Sharpe = sharpe(sessionRets)
	return rep
}

func sharpe(rets []float64) float64 {
	if len(rets) < 2 {
		return 0
	}
	mean := 0.0
	for _, r := range rets {
		mean += r
	}
	mean /= float64(len(rets))
	ss := 0.0
	for _, r := range rets {
		ss += (r - mean) * (r - mean)
	}
	sd := math.Sqrt(ss / float64(len(rets)-1))
	if sd == 0 {
		return 0
	}
	return mean / sd * math.Sqrt(252)
}

// String is the plain-language report. It states losses as losses and makes no claim about
// the future.
func (r Report) String() string {
	var b strings.Builder
	pct := func(f float64) string { return fmt.Sprintf("%+.2f%%", f*100) }
	fmt.Fprintf(&b, "Backtest of instrument %d, %d bars, %s to %s (walk-forward, next-bar fills)\n",
		r.Instrument, r.Bars, r.From.UTC().Format("2006-01-02 15:04"), r.To.UTC().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "  equity        $%s → $%s  (%s after costs)\n", r.StartEquity, r.EndEquity, pct(r.NetReturn))
	fmt.Fprintf(&b, "  buy and hold  %s (the instrument itself, no costs)\n", pct(r.BuyAndHold))
	fmt.Fprintf(&b, "  max drawdown  %s\n", pct(-r.MaxDrawdown))
	fmt.Fprintf(&b, "  Sharpe        %.2f (per session, annualised; meaningless on few sessions)\n", r.Sharpe)
	fmt.Fprintf(&b, "  trades        %d, win rate %.0f%%, skipped signals %d, costs paid $%s\n",
		len(r.Trades), r.WinRate*100, r.SkippedEntries, r.CostsPaid)
	fmt.Fprintf(&b, "  costs         spread %.4f, buy %.4f, sell %.4f (%s)\n", r.Costs.Spread, r.Costs.BuyFee, r.Costs.SellFee, r.Costs.Recorded)
	switch {
	case len(r.Trades) == 0:
		b.WriteString("  verdict       no trades: nothing can be concluded\n")
	case r.NetReturn < 0:
		b.WriteString("  verdict       the strategy LOST money on this data after costs\n")
	case r.NetReturn < r.BuyAndHold:
		b.WriteString("  verdict       the strategy did worse than simply holding the instrument\n")
	default:
		b.WriteString("  verdict       positive on this data only; a backtest does not predict future results\n")
	}
	return b.String()
}
