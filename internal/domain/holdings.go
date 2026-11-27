package domain

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// Holdings is an account's cash and open positions as its platform reports them. It is
// read-only: nothing here is ever used to size or place an order. Every Cents field holds
// minor units of Currency (CurrencyDecimals: hundredths, or whole yen for JPY), which is the
// account's own currency: USD for eToro, often GBP, EUR, AUD or another for the view-only
// platforms. Amounts in different currencies are never added up.
type Holdings struct {
	Platform      Platform
	Currency      string // ISO 4217 code of the account currency
	Cash          Cents  // free cash
	Invested      Cents  // cost of the open positions (and, on eToro, cash in pending orders)
	Equity        Cents  // cash + current value of every position
	UnrealisedPnL Cents
	RealisedPnL   Cents // when the platform reports it (Trading 212), else 0
	Positions     []Holding
	AsOf          time.Time
}

// Holding is one open position. Prices are in the instrument's currency (Currency); Value,
// Cost and PnL are in the account currency (Holdings.Currency).
type Holding struct {
	Symbol       string
	Name         string
	Currency     string // the instrument's price currency ("" when unknown)
	Quantity     Micros
	AvgPrice     Micros
	CurrentPrice Micros // 0 when unknown
	Value        Cents
	Cost         Cents
	PnL          Cents
	OpenedAt     time.Time
	Long         bool
	CFD          bool
	// Unknown: the platform did not report this position's value and P&L in the account
	// currency (or not in a field cactusTrade could verify), so Value, Cost and PnL are 0 and
	// shown as unknown — never a guessed number. Such a position is left out of Invested and
	// UnrealisedPnL.
	Unknown bool
}

// EquitySnapshot is an account's cash and equity at one moment, recorded every 15 minutes
// for the overview chart. Cash and Equity are minor units of Currency.
type EquitySnapshot struct {
	AccountID string
	At        time.Time
	Currency  string
	Cash      Cents
	Equity    Cents
}

// Equity history bounds: snapshots are kept EquityRetentionDays; a chart asks for 1 to that
// many days, DefaultEquityHistoryDays when it does not say.
const (
	EquityRetentionDays      = 400
	DefaultEquityHistoryDays = 30
)

// EquityHistoryDays validates a chart range in days (0 = the default).
func EquityHistoryDays(n int) (int, error) {
	switch {
	case n == 0:
		return DefaultEquityHistoryDays, nil
	case n < 0 || n > EquityRetentionDays:
		return 0, Invalid("days", "1-"+strconv.Itoa(EquityRetentionDays))
	}
	return n, nil
}

// Snapshot is the holdings' equity as a snapshot of account id at at.
func (h Holdings) Snapshot(accountID string, at time.Time) EquitySnapshot {
	return EquitySnapshot{AccountID: accountID, At: at, Currency: h.Currency, Cash: h.Cash, Equity: h.Equity}
}

// HoldingsFromPortfolio turns an eToro portfolio (USD) into holdings. ins names the
// instruments (a missing one shows as "#<id>"); quotes, when given, supply the current price
// (the bid for a long, the ask for a short).
func HoldingsFromPortfolio(pf Portfolio, ins map[InstrumentID]Instrument, quotes map[InstrumentID]Quote) Holdings {
	h := Holdings{
		Platform: PlatformEtoro, Currency: "USD", Cash: pf.AvailableCash, Invested: pf.PendingOpen,
		Equity: pf.Equity(), AsOf: pf.AsOf,
	}
	for _, p := range pf.Positions {
		in, ok := ins[p.Instrument]
		if !ok {
			in = Instrument{Symbol: "#" + strconv.FormatInt(int64(p.Instrument), 10)}
		}
		x := Holding{
			Symbol: in.Symbol, Name: in.Name, Currency: in.Currency, Quantity: p.Units, AvgPrice: p.OpenRate,
			Value: p.Amount + p.UnrealisedPnL, Cost: p.Amount, PnL: p.UnrealisedPnL, OpenedAt: p.OpenedAt,
			Long: p.Long, CFD: p.CFD,
		}
		if q, ok := quotes[p.Instrument]; ok {
			x.CurrentPrice = q.Bid
			if !p.Long {
				x.CurrentPrice = q.Ask
			}
		}
		h.Invested += p.Amount
		h.UnrealisedPnL += p.UnrealisedPnL
		h.Positions = append(h.Positions, x)
	}
	return h
}

// CurrencyTotal adds up the accounts that share one currency.
type CurrencyTotal struct {
	Currency      string
	Accounts      int
	Cash          Cents
	Equity        Cents
	UnrealisedPnL Cents
}

// TotalsByCurrency sums holdings per currency, sorted by currency code. Different currencies
// are never added together (there is no FX conversion).
func TotalsByCurrency(hs []Holdings) []CurrencyTotal {
	by := map[string]*CurrencyTotal{}
	for _, h := range hs {
		cur := strings.ToUpper(h.Currency)
		t, ok := by[cur]
		if !ok {
			t = &CurrencyTotal{Currency: cur}
			by[cur] = t
		}
		t.Accounts++
		t.Cash += h.Cash
		t.Equity += h.Equity
		t.UnrealisedPnL += h.UnrealisedPnL
	}
	out := make([]CurrencyTotal, 0, len(by))
	for _, t := range by {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b CurrencyTotal) int { return strings.Compare(a.Currency, b.Currency) })
	return out
}

// HoldingsSummary is the account tile's breakdown (R44): how many long and short positions
// are open, what each side is worth now and its open P&L, in the account currency. A position
// whose value the platform did not report (Unknown) is counted but adds nothing to the sums;
// UnknownValues says how many there were.
type HoldingsSummary struct {
	Longs, Shorts         int
	LongValue, ShortValue Cents
	LongPnL, ShortPnL     Cents
	UnknownValues         int
}

// Summary counts and sums the open positions by side.
func (h Holdings) Summary() HoldingsSummary {
	var s HoldingsSummary
	for _, p := range h.Positions {
		if p.Long {
			s.Longs++
		} else {
			s.Shorts++
		}
		if p.Unknown {
			s.UnknownValues++
			continue
		}
		if p.Long {
			s.LongValue += p.Value
			s.LongPnL += p.PnL
		} else {
			s.ShortValue += p.Value
			s.ShortPnL += p.PnL
		}
	}
	return s
}
