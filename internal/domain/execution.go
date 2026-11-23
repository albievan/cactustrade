package domain

import "time"

// AgentPosition is a position cactusTrade opened, recorded when the fill is seen. Only these
// are ever closed by cactusTrade (OD1); a position eToro reports that is not here is left
// alone.
type AgentPosition struct {
	AccountID     string
	PositionID    int64 // eToro's positionId
	OpenRequestID string
	Instrument    InstrumentID
	Direction     Direction
	OpenedAt      time.Time
	// ClosedAt is zero while the position is open.
	ClosedAt time.Time
	// CloseRequestID is the close order sent for it ("" = none yet).
	CloseRequestID string
}

// Open reports whether the position is still open as far as cactusTrade knows.
func (p AgentPosition) Open() bool { return p.ClosedAt.IsZero() }

// AccountRiskState is an account's persisted risk state. Reserved amounts in State are not
// stored: they come from the account's own orders (Reservations).
type AccountRiskState struct {
	AccountID  string
	State      RiskState
	KillReason string
	KilledAt   time.Time // zero unless killed
	UpdatedAt  time.Time
	// Version is the stored version this state was read at (0 = never stored). A write with
	// a stale version fails with ErrConflict, so a kill is never overwritten by an older read.
	Version int64
}

// InstrumentBlock keeps the agent from entering an instrument on one account (e.g. after a
// fill that did not conform), until an operator lifts it.
type InstrumentBlock struct {
	AccountID  string
	Instrument InstrumentID
	Reason     string
	At         time.Time
}

// AgentLease is the single-writer lock of one account (D22): only the holder may send orders
// until ExpiresAt.
type AgentLease struct {
	AccountID string
	Holder    string
	ExpiresAt time.Time
}

// LeaseTTL is how long a lease lasts without renewal (D22).
const LeaseTTL = 30 * time.Second
