package domain

import (
	"maps"
	"strings"
	"time"
)

// DecisionOutcome is how the agent's handling of one candidate ended.
type DecisionOutcome string

// Decision outcomes. DecisionPending is the state written before the order row: an order is
// about to be sent and the decision is updated with its request id and outcome right after.
const (
	DecisionPending          DecisionOutcome = "pending"
	DecisionRejectedReviewer DecisionOutcome = "rejected_reviewer"
	DecisionRejectedCost     DecisionOutcome = "rejected_cost"
	DecisionRejectedRisk     DecisionOutcome = "rejected_risk"
	DecisionOrdered          DecisionOutcome = "ordered"
	DecisionOrderFailed      DecisionOutcome = "order_failed"
)

// DecisionOutcomes lists every outcome, for filters and validation.
var DecisionOutcomes = []DecisionOutcome{
	DecisionPending, DecisionRejectedReviewer, DecisionRejectedCost, DecisionRejectedRisk, DecisionOrdered, DecisionOrderFailed,
}

// Valid reports whether o is a known outcome.
func (o DecisionOutcome) Valid() bool {
	for _, x := range DecisionOutcomes {
		if o == x {
			return true
		}
	}
	return false
}

// DecisionSignal is what the strategy proposed.
type DecisionSignal struct {
	Instrument    InstrumentID
	Symbol        string
	Direction     Direction
	Playbook      string
	Reason        string
	Entry         Micros
	StopLoss      Micros
	TakeProfit    Micros
	SuggestedSize Cents
	RegimeProbs   map[Regime]float64
}

// SignalFromCandidate copies a candidate into a decision's signal.
func SignalFromCandidate(c Candidate, symbol string) DecisionSignal {
	return DecisionSignal{
		Instrument: c.Instrument, Symbol: symbol, Direction: c.Direction, Playbook: c.Playbook, Reason: c.Reason,
		Entry: c.Entry, StopLoss: c.StopLossRate, TakeProfit: c.TakeProfitRate, SuggestedSize: c.SuggestedSize, RegimeProbs: maps.Clone(c.RegimeProbs),
	}
}

// DecisionVerdict is the LLM reviewer's verdict, clamped. Kind "" = not reviewed (reviewing
// off, or the decision ended before the review).
type DecisionVerdict struct {
	Kind      VerdictKind
	SizeBp    BasisPoints
	Reason    string
	LLMCallID string // "" = no call
}

// DecisionRisk is the risk engine's answer.
type DecisionRisk struct {
	Allowed bool
	Size    Cents // the size the risk engine allowed (0 when refused)
	Reason  string
}

// Decision is one entry in the decision log: a candidate, what the reviewer and the risk
// engine said, and how it ended.
type Decision struct {
	ID             string
	AccountID      string
	At             time.Time
	Signal         DecisionSignal
	Verdict        DecisionVerdict
	Risk           DecisionRisk
	Outcome        DecisionOutcome
	OrderRequestID string // the order's x-request-id ("" = no order)
}

// MaxDecisionReason bounds each stored reason.
const MaxDecisionReason = 500

// Validate checks a decision before it is stored.
func (d Decision) Validate() error {
	switch {
	case d.ID == "" || d.AccountID == "":
		return Invalid("decision", "id and account are required")
	case d.At.IsZero():
		return Invalid("at", "the time is required")
	case !d.Outcome.Valid():
		return Invalid("outcome", "unknown outcome "+string(d.Outcome))
	case !d.Signal.Direction.Valid():
		return Invalid("direction", "long or short")
	case d.Verdict.Kind != "" && d.Verdict.Kind != VerdictApprove && d.Verdict.Kind != VerdictReduce && d.Verdict.Kind != VerdictReject:
		return Invalid("verdict", "approve, reduce, reject or none")
	case d.Verdict.SizeBp < 0 || d.Verdict.SizeBp > FullSize:
		return Invalid("sizeBp", "0 to 10 000")
	case d.Risk.Size < 0 || d.Signal.SuggestedSize < 0:
		return Invalid("size", "must not be negative")
	case d.Outcome == DecisionOrdered && d.OrderRequestID == "":
		return Invalid("orderRequestId", "an ordered decision needs the order's request id")
	case d.Outcome == DecisionOrdered && !d.Risk.Allowed:
		return Invalid("risk", "an ordered decision needs the risk engine's approval")
	}
	return nil
}

// Clip returns d with its free-text fields cut to MaxDecisionReason bytes of valid UTF-8.
func (d Decision) Clip() Decision {
	d.Signal.Reason = Truncate(d.Signal.Reason, MaxDecisionReason)
	d.Verdict.Reason = Truncate(d.Verdict.Reason, MaxDecisionReason)
	d.Risk.Reason = Truncate(d.Risk.Reason, MaxDecisionReason)
	d.Signal.Symbol = strings.TrimSpace(Truncate(d.Signal.Symbol, 32))
	return d
}
