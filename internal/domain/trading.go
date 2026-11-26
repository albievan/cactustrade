package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cents is an amount of US dollars in cents. The eToro account currency is USD.
type Cents int64

// String formats c as dollars, e.g. "-12.05".
func (c Cents) String() string {
	sign := ""
	v := int64(c)
	if v < 0 {
		sign, v = "-", -v
	}
	frac := strconv.FormatInt(v%100, 10)
	if len(frac) == 1 {
		frac = "0" + frac
	}
	return sign + strconv.FormatInt(v/100, 10) + "." + frac
}

// Micros is a price or a quantity in millionths (6 decimal places): share prices and
// fractional units need more precision than cents.
type Micros int64

// String formats m as an exact decimal without trailing zeros, e.g. "189.1", "-0.000001".
func (m Micros) String() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign, v = "-", -v
	}
	whole := strconv.FormatInt(v/1_000_000, 10)
	frac := strconv.FormatInt(v%1_000_000, 10)
	frac = strings.TrimRight(strings.Repeat("0", 6-len(frac))+frac, "0")
	if frac == "" {
		return sign + whole
	}
	return sign + whole + "." + frac
}

// ParseMicros reads an exact decimal with at most 6 places ("189.1", "-0.000001", "3") as
// Micros; the inverse of String. Exponents, spaces, thousands separators and values that do
// not fit in an int64 are refused.
func ParseMicros(s string) (Micros, error) {
	neg := strings.HasPrefix(s, "-")
	whole, frac, dotted := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	digits := func(x string) bool {
		return x != "" && strings.Trim(x, "0123456789") == ""
	}
	if !digits(whole) || (dotted && (!digits(frac) || len(frac) > 6)) {
		return 0, Invalid("decimal", "a number with at most 6 decimal places, e.g. 3 or 0.25")
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > (1<<63-1)/1_000_000 {
		return 0, Invalid("decimal", "too large")
	}
	f, _ := strconv.ParseInt((frac + "000000")[:6], 10, 64) // 6 digits: always fits
	v := w*1_000_000 + f
	if v < 0 { // w*1e6 + f overflowed
		return 0, Invalid("decimal", "too large")
	}
	if neg {
		v = -v
	}
	return Micros(v), nil
}

// InstrumentID is eToro's instrument identifier.
type InstrumentID int64

// Market is a trading venue the calendar knows.
type Market string

// Markets in scope: shares on London, New York and Sydney (ASX, owner's request R19).
const (
	MarketLSE  Market = "LSE"
	MarketNYSE Market = "NYSE" // also NASDAQ: same hours and holidays
	MarketASX  Market = "ASX"
)

// Markets lists every supported market, for the calendar checks and the dashboard.
var Markets = []Market{MarketLSE, MarketNYSE, MarketASX}

// Label names the market for people, e.g. "New York (NYSE/NASDAQ)"; "" when unknown.
func (m Market) Label() string {
	switch m {
	case MarketLSE:
		return "London (LSE)"
	case MarketNYSE:
		return "New York (NYSE/NASDAQ)"
	case MarketASX:
		return "Sydney (ASX)"
	}
	return ""
}

// Instrument is a tradable share.
type Instrument struct {
	ID         InstrumentID
	Symbol     string
	Name       string
	Type       string // eToro instrument type, e.g. "Stocks"
	ExchangeID int64
	Market     Market // "" when the instrument is not on a supported market
	Currency   string // quote currency as eToro reports it ("" until known)
}

// MarketForSymbol infers the market from an eToro symbol: "BARC.L" is London, "CBA.ASX" is
// Sydney; a plain ticker ("AAPL") or a US share-class suffix ("BRK.B") is New York (NYSE or
// NASDAQ, same calendar). Any other suffix ("SAP.DE", "MC.PA") is an unsupported market
// (ok=false).
func MarketForSymbol(symbol string) (Market, bool) {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	base, suffix, dotted := strings.Cut(s, ".")
	switch {
	case s == "" || base == "":
		return "", false
	case !dotted:
		return MarketNYSE, true
	case suffix == "L":
		return MarketLSE, true
	case suffix == "ASX":
		return MarketASX, true
	case len(suffix) == 1 && suffix[0] >= 'A' && suffix[0] <= 'Z':
		return MarketNYSE, true
	}
	return "", false
}

// IsShare reports whether an eToro instrument type is a share (cactusTrade trades nothing
// else: no CFDs on indices, no FX, commodities or crypto).
func IsShare(instrumentType string) bool {
	switch strings.ToLower(strings.TrimSpace(instrumentType)) {
	case "stocks", "stock":
		return true
	}
	return false
}

// Industry is one of eToro's stock industries (broad sectors such as technology).
type Industry struct {
	ID   int64
	Name string
}

// Direction is long (own the share) or short (a CFD sold short; owner's decision D19).
type Direction string

// Directions.
const (
	Long  Direction = "long"
	Short Direction = "short"
)

// Valid reports whether d is long or short.
func (d Direction) Valid() bool { return d == Long || d == Short }

// Eligibility is what eToro says about opening an unleveraged position in an instrument:
// a long as the real share, or a short as a CFD.
type Eligibility struct {
	Instrument InstrumentID
	Symbol     string
	// RealLong: open allowed with settlement "real", direction long and leverage 1 offered.
	RealLong          bool
	Reason            string // why not, when RealLong is false
	MinPositionAmount Cents  // for real/long/leverage 1, when eToro states it
	// ShortCFD: open allowed with settlement "cfd", direction short and leverage 1 offered.
	ShortCFD       bool
	ShortReason    string
	MinShortAmount Cents
	CheckedAt      time.Time
}

// Allows reports whether the instrument may be opened in direction d, and why not.
func (e Eligibility) Allows(d Direction) (bool, string) {
	switch d {
	case Long:
		return e.RealLong, e.Reason
	case Short:
		return e.ShortCFD, e.ShortReason
	}
	return false, "unknown direction"
}

// MinAmount is eToro's minimum for direction d (0 = not stated).
func (e Eligibility) MinAmount(d Direction) Cents {
	if d == Short {
		return e.MinShortAmount
	}
	return e.MinPositionAmount
}

// WatchItem is an instrument on the watchlist with its cached eligibility.
type WatchItem struct {
	Instrument  Instrument
	Eligibility Eligibility
	AddedBy     string
	AddedAt     time.Time
}

// CostItem is one line of eToro's cost breakdown for a hypothetical order.
type CostItem struct {
	Type     string // markup, marketSpread, transactionFee, overnightFee, overWeekendFee, sdrt
	Value    Micros
	Currency string
}

// ClosedTrade is a position eToro reports as closed (trade history). Amounts are USD cents;
// rates are in the instrument's own quote currency.
type ClosedTrade struct {
	PositionID int64
	Instrument InstrumentID
	OpenedAt   time.Time
	ClosedAt   time.Time
	OpenRate   Micros
	CloseRate  Micros
	// NetProfit is eToro's netProfit of the trade. Whether it is after Fees is not verified
	// (RU5), so TradeStats takes it as the net figure and adds Fees back for the gross.
	NetProfit Cents
	Long      bool
	Leverage  int
	// Fees are eToro's fees of the trade; FeesKnown is false when eToro did not report them
	// (unknown is kept apart and never counted as 0).
	Fees      Cents
	FeesKnown bool
	// Investment is the amount invested when the position was opened (eToro's
	// initialInvestment, else investment).
	Investment Cents
	Units      Micros
	OrderID    int64 // eToro's order id of the open (0 = not reported)
	StopLoss   Micros
	TakeProfit Micros
}

// Direction of the trade.
func (t ClosedTrade) Direction() Direction {
	if t.Long {
		return Long
	}
	return Short
}

// Quote is a bid/ask snapshot.
type Quote struct {
	Instrument InstrumentID
	Bid, Ask   Micros
	At         time.Time
	Delayed    bool
}

// Candle is one OHLCV bar starting at Start.
type Candle struct {
	Start                  time.Time
	Open, High, Low, Close Micros
	Volume                 int64
}

// Position is an open long position on eToro.
type Position struct {
	ID             int64 // eToro positionId: positions are closed by it
	Instrument     InstrumentID
	OpenedAt       time.Time
	OpenRate       Micros
	Units          Micros
	Amount         Cents // invested
	UnrealisedPnL  Cents
	StopLossRate   Micros
	TakeProfitRate Micros
	RealAsset      bool // settlementTypeID == 1
	CFD            bool // settlementTypeID == 0
	Long           bool // isBuy
	Leverage       int
}

// Direction of the position.
func (p Position) Direction() Direction {
	if p.Long {
		return Long
	}
	return Short
}

// Conforms reports whether the position is one cactusTrade may hold: a real share held long,
// or a CFD held short, at leverage 1. A non-conforming position that cactusTrade opened is
// closed by the supervisor. One it did not open is never closed by cactusTrade (OD1): it
// blocks every entry on the account and raises an alert until the owner deals with it.
func (p Position) Conforms() bool {
	if p.Leverage != 1 {
		return false
	}
	return (p.Long && p.RealAsset && !p.CFD) || (!p.Long && p.CFD && !p.RealAsset)
}

// Portfolio is the account as eToro reports it.
type Portfolio struct {
	AvailableCash Cents // clientPortfolio.credit
	Positions     []Position
	PendingOpen   Cents // cash committed to orders not yet filled
	AsOf          time.Time
}

// Equity is cash plus the current value of every position.
func (p Portfolio) Equity() Cents {
	e := p.AvailableCash + p.PendingOpen
	for _, pos := range p.Positions {
		e += pos.Amount + pos.UnrealisedPnL
	}
	return e
}

// OrderIntent is what the system asks the broker to open. There is deliberately no leverage
// or settlement field: the eToro adapter maps the direction to one of exactly two constant
// shapes, long = "buy" + "real", short = "sellShort" + "cfd", always leverage 1
// (CLAUDE.md §2.3).
type OrderIntent struct {
	RequestID      string // persisted before sending; reused on retry
	Direction      Direction
	Instrument     InstrumentID
	Amount         Cents
	StopLossRate   Micros
	TakeProfitRate Micros
}

// Settlement is how eToro settles a position: the real share or a CFD. Only two values
// conform (a long as the real share, a short as a CFD); anything else eToro reports is kept
// as it came and never conforms.
type Settlement string

// Settlements cactusTrade trades (CLAUDE.md §2.3).
const (
	SettlementReal Settlement = "real"
	SettlementCFD  Settlement = "cfd"
)

// PositionExecution is one position an order produced (open) or closed (close).
type PositionExecution struct {
	PositionID int64
	Units      Micros // executed units (open) or units closed (close)
	Amount     Cents  // invested (open) or the closed amount (close), USD
	// OpenRate is the average execution price of an open; 0 for a close.
	OpenRate Micros
	// StopLossRate is the position's stop after an open (0 = none); 0 for a close.
	StopLossRate Micros
	// CloseRate is the execution price of a close; 0 for an open.
	CloseRate Micros
	// Closed: eToro reports the position this open produced as already closed (its
	// take-profit or stop-loss hit before we looked; seen live on 2026-11-24). It is still
	// ours: recorded as an agent position, then settled as closed.
	Closed bool
}

// OrderStatus is the broker's answer about one order: the reply to an open, close or a
// lookup. Settlement, Leverage and Side describe what eToro actually opened (lookup), so the
// executor can check a fill against the two allowed order shapes.
type OrderStatus struct {
	RequestID string // our x-request-id (eToro's referenceId), when the answer carries it
	OrderID   int64  // eToro's order id
	State     OrderState
	// Final: eToro reports a terminal status (the order can no longer execute), e.g. filled,
	// rejected, cancelled, expired, or cancelled/rejected after a partial fill.
	Final      bool
	Instrument InstrumentID
	Settlement Settlement
	Leverage   int
	Side       Direction // "" when eToro did not say
	Executions []PositionExecution
	Reason     string
}

// Conforms reports whether what eToro executed is the order shape cactusTrade sent for
// direction d: a real share held long or a CFD held short, at leverage 1, with a stop on
// every execution. A non-conforming fill is closed at once and its instrument blocked.
func (s OrderStatus) Conforms(d Direction) (bool, string) {
	want := map[Direction]Settlement{Long: SettlementReal, Short: SettlementCFD}[d]
	switch {
	case want == "":
		return false, "unknown direction"
	case s.Side != d:
		return false, fmt.Sprintf("eToro opened side %q, not %s", s.Side, d)
	case s.Settlement != want:
		return false, fmt.Sprintf("eToro settled as %q, not %s", s.Settlement, want)
	case s.Leverage != 1:
		return false, fmt.Sprintf("eToro applied leverage %d, not 1", s.Leverage)
	}
	for _, e := range s.Executions {
		switch {
		case e.StopLossRate <= 0:
			return false, fmt.Sprintf("position %d has no stop-loss", e.PositionID)
		case e.OpenRate <= 0:
			return false, fmt.Sprintf("position %d has no open rate", e.PositionID)
		case d == Long && e.StopLossRate >= e.OpenRate, d == Short && e.StopLossRate <= e.OpenRate:
			return false, fmt.Sprintf("position %d: stop %s on the wrong side of the open rate %s", e.PositionID, e.StopLossRate, e.OpenRate)
		}
	}
	return true, ""
}

// Regime is a hidden-Markov state of a market.
type Regime string

// Regimes the playbooks map from.
const (
	RegimeTrending Regime = "trending"
	RegimeChoppy   Regime = "choppy"
	RegimeHighVol  Regime = "high_volatility"
)

// Candidate is a trade proposed by the regime model's playbook.
type Candidate struct {
	Instrument     InstrumentID
	Direction      Direction
	Playbook       string
	Reason         string
	Entry          Micros
	StopLossRate   Micros
	TakeProfitRate Micros
	SuggestedSize  Cents
	RegimeProbs    map[Regime]float64
	ExpectedEdge   Micros // per unit, before costs
}

// VerdictKind is the LLM reviewer's decision.
type VerdictKind string

// Verdicts. The reviewer can approve, shrink or reject — never enlarge or originate.
const (
	VerdictApprove VerdictKind = "approve"
	VerdictReduce  VerdictKind = "reduce"
	VerdictReject  VerdictKind = "reject"
)

// Verdict is the LLM reviewer's structured answer.
type Verdict struct {
	Kind VerdictKind
	// SizeBp is the share of the proposed size the reviewer accepts, in basis points
	// (10 000 = all of it). The LLM adapter converts the model's number to it, rounding down;
	// ClampVerdict keeps it within (0, 10 000].
	SizeBp BasisPoints
	Reason string
}

// AgentState is what the trading agent is doing.
type AgentState string

// Agent states.
const (
	AgentStopped AgentState = "stopped"
	AgentRunning AgentState = "running"
	AgentPaused  AgentState = "paused"
	AgentKilled  AgentState = "killed" // drawdown or manual kill: admin reset required
)
