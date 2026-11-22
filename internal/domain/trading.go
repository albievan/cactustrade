package domain

import (
	"strconv"
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

// InstrumentID is eToro's instrument identifier.
type InstrumentID int64

// Market is a trading venue the calendar knows.
type Market string

// Markets in scope: long-only shares on London and New York.
const (
	MarketLSE  Market = "LSE"
	MarketNYSE Market = "NYSE" // also NASDAQ: same hours and holidays
)

// Instrument is a tradable share.
type Instrument struct {
	ID       InstrumentID
	Symbol   string
	Name     string
	Market   Market
	Currency string // quote currency as eToro reports it
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
	RealAsset      bool // settlementTypeID == 1; anything else is closed at once
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

// OrderIntent is what the system asks the broker to open. There is deliberately no
// leverage, direction or settlement field: the eToro adapter sends leverage 1, "buy" and
// settlementType "real" as constants (CLAUDE.md §2.3).
type OrderIntent struct {
	RequestID      string // persisted before sending; reused on retry
	Instrument     InstrumentID
	Amount         Cents
	StopLossRate   Micros
	TakeProfitRate Micros
}

// OrderState is the lifecycle of an order on eToro.
type OrderState string

// Order states.
const (
	OrderPending  OrderState = "pending"
	OrderFilled   OrderState = "filled"
	OrderPartial  OrderState = "partially_filled"
	OrderRejected OrderState = "rejected"
	OrderUnknown  OrderState = "unknown"
)

// OrderStatus is the broker's answer to an order lookup.
type OrderStatus struct {
	RequestID   string
	OrderID     int64
	State       OrderState
	PositionIDs []int64
	Reason      string
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
	Kind           VerdictKind
	SizeMultiplier float64 // clamped to [0, 1]
	Reason         string
}

// RiskDecision is the deterministic risk engine's final say.
type RiskDecision struct {
	Allowed bool
	Size    Cents
	Reason  string
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
