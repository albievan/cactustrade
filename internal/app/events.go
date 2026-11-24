package app

import (
	"context"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// eventRetention is how long the feed keeps events.
const eventRetention = 90 * 24 * time.Hour

func seenKey(userID string) string { return "events.seen." + userID }

// emit appends a system-wide event (seen by everyone).
func (s *Service) emit(ctx context.Context, actor, kind string, sev domain.EventSeverity, title, detail string) {
	s.emitFor(ctx, "", actor, kind, sev, title, detail)
}

// emitFor appends an event about one account (seen by the users who can see that account).
// A failed write is logged, never fatal to the action.
func (s *Service) emitFor(ctx context.Context, accountID, actor, kind string, sev domain.EventSeverity, title, detail string) {
	if actor == "" {
		actor = "system"
	}
	e := domain.Event{ID: s.d.Random.ID(), At: s.d.Clock.Now().UTC(), Kind: kind, Severity: sev, Title: title, Detail: detail, Actor: actor, AccountID: accountID}
	if err := s.d.Store.Events().Append(ctx, e); err != nil {
		s.d.Log.ErrorContext(ctx, "event write failed", "kind", kind, "err", err)
	}
	// After the write (attempted): a live viewer still learns of a kill when the database
	// is down; the feed has it once the write succeeds.
	s.publish(BusEvent, e.At, e)
	s.notify(ctx, e)
}

// Live bus event types (PLAN 6.6b). Each event's Data names its account (BusAccount).
const (
	// BusEvent: an event of the feed (Data domain.Event; AccountID "" = system-wide).
	BusEvent = "event"
	// BusOrder: one of our orders after a stored transition, creation included (Data
	// domain.Order as stored).
	BusOrder = "order.updated"
	// BusAgentState: an account's agent state after it was written (Data AgentStatus).
	BusAgentState = "agent.state"
	// BusPortfolio: the supervisor read an account's portfolio (Data PortfolioRead).
	BusPortfolio = "account.portfolio"
	// BusTick: an agent tick of an account ended (Data TickSummary). Bus only: it is never
	// stored as an event, so the bell and the e-mails stay quiet (R26).
	BusTick = "agent.tick"
	// BusDecision: a decision with its outcome, after its write was attempted (Data
	// DecisionView). Bus only, like BusTick; the decision log holds the stored row (R26).
	BusDecision = "decision"
)

// tickDetailMax bounds each share's detail in a tick summary.
const tickDetailMax = 200

// TickShare is what one agent tick did with one watchlist share.
type TickShare struct {
	Instrument domain.InstrumentID
	Symbol     string
	// Entered: an order was sent. Otherwise Step is the step that stopped the share.
	Entered bool
	Step    TraceStep
	// Detail is the last step's answer (at most tickDetailMax bytes, cut at a rune).
	Detail string
}

// TickSummary is what one agent tick of an account did, for the live activity log (R26).
type TickSummary struct {
	AccountID string
	At        time.Time // when the tick started
	// Checked counts the shares looked at, Ideas the candidates (one decision each), Orders
	// the orders sent.
	Checked, Ideas, Orders int
	// Skipped says why the tick did nothing ("" = it ran).
	Skipped string
	// Reasons counts the shares that led to no order by the step that stopped them.
	Reasons map[TraceStep]int
	Shares  []TickShare
	Killed  bool
	Errors  int
}

// Summary condenses an agent trace into its tick summary.
func (t AgentTrace) Summary() TickSummary {
	out := TickSummary{
		AccountID: t.AccountID, At: t.At, Checked: len(t.Instruments), Orders: t.Entries, Skipped: t.Skipped,
		Reasons: map[TraceStep]int{}, Shares: make([]TickShare, 0, len(t.Instruments)), Killed: t.Killed, Errors: len(t.Errors),
	}
	for _, it := range t.Instruments {
		if it.DecisionID != "" {
			out.Ideas++
		}
		sh := TickShare{Instrument: it.Instrument, Symbol: it.Symbol, Entered: it.Entered, Detail: cutDetail(lastDetail(&it))}
		if !it.Entered && len(it.Checks) > 0 {
			sh.Step = it.Checks[len(it.Checks)-1].Step
			out.Reasons[sh.Step]++
		}
		out.Shares = append(out.Shares, sh)
	}
	return out
}

// cutDetail shortens s to at most tickDetailMax bytes without splitting a rune.
func cutDetail(s string) string {
	if len(s) <= tickDetailMax {
		return s
	}
	cut := tickDetailMax - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// PortfolioRead says that an account's portfolio was read at At (the figures are in the view
// cache; a stream sends a new snapshot).
type PortfolioRead struct {
	AccountID string
	At        time.Time
	Equity    domain.Cents
}

// BusAccount returns the account a bus event is about ("" = system-wide or unknown, which
// a stream shows to no one but the system feed's readers).
func BusAccount(e ports.Event) string {
	switch d := e.Data.(type) {
	case domain.Event:
		return d.AccountID
	case domain.Order:
		return d.AccountID
	case AgentStatus:
		return d.AccountID
	case PortfolioRead:
		return d.AccountID
	case TickSummary:
		return d.AccountID
	case DecisionView:
		return d.AccountID
	default:
		return ""
	}
}

// publish puts one event on the bus (none configured: nothing). The bus never blocks: a
// subscriber that is full misses the event (ports.EventBus).
func (s *Service) publish(typ string, at time.Time, data any) {
	if s.d.Bus == nil {
		return
	}
	s.d.Bus.Publish(ports.Event{Type: typ, At: at, Data: data})
}

// publishOrder announces an order version that was just stored.
func (s *Service) publishOrder(o domain.Order) { s.publish(BusOrder, o.UpdatedAt, o) }

// publishAgentState announces an account's agent state that was just written.
func (s *Service) publishAgentState(accountID string, st domain.AgentState) {
	s.publish(BusAgentState, s.d.Clock.Now().UTC(), AgentStatus{AccountID: accountID, State: st})
}

// publishPortfolio announces the supervisor's portfolio read.
func (s *Service) publishPortfolio(accountID string, pf domain.Portfolio) {
	s.publish(BusPortfolio, s.d.Clock.Now().UTC(), PortfolioRead{AccountID: accountID, At: s.d.Clock.Now().UTC(), Equity: pf.Equity()})
}

// publishTick announces what an agent tick did (bus only).
func (s *Service) publishTick(tr AgentTrace) {
	s.publish(BusTick, s.d.Clock.Now().UTC(), tr.Summary())
}

// publishDecision announces a decision with its outcome after its write was attempted (bus
// only). The order state is the trace's: no read is made for it.
func (s *Service) publishDecision(d domain.Decision, st domain.OrderState) {
	s.publish(BusDecision, s.d.Clock.Now().UTC(), DecisionView{Decision: d, OrderState: st})
}

// EventFeed is the newest events and how many the caller has not seen.
type EventFeed struct {
	Events []domain.Event
	Unread int
}

// Events returns the newest events (viewer) and the caller's unread count.
func (s *Service) Events(ctx context.Context, p Principal, limit int) (EventFeed, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return EventFeed{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return EventFeed{}, err
	}
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.ID
	}
	es, err := s.d.Store.Events().List(ctx, ids, limit)
	if err != nil {
		return EventFeed{}, err
	}
	n, err := s.d.Store.Events().CountSince(ctx, ids, s.seen(ctx, p.UserID))
	if err != nil {
		return EventFeed{}, err
	}
	return EventFeed{Events: es, Unread: n}, nil
}

func (s *Service) seen(ctx context.Context, userID string) time.Time {
	v, err := s.d.Store.Settings().Get(ctx, seenKey(userID))
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.d.Log.WarnContext(ctx, "reading the events mark failed", "err", err)
		}
		return time.Time{}
	}
	us, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

// MarkEventsSeen clears the caller's unread count (viewer).
func (s *Service) MarkEventsSeen(ctx context.Context, p Principal) error {
	if err := s.authorize(p, domain.PermView); err != nil {
		return err
	}
	now := s.d.Clock.Now().UTC()
	return s.d.Store.Settings().Set(ctx, seenKey(p.UserID), strconv.FormatInt(now.UnixMicro(), 10), now)
}

// PurgeOldEvents deletes events past the retention period (called periodically).
func (s *Service) PurgeOldEvents(ctx context.Context) (int64, error) {
	return s.d.Store.Events().DeleteBefore(ctx, s.d.Clock.Now().Add(-eventRetention))
}
