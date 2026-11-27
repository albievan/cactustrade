package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The executor (PLAN 4.7) is the only code that sends orders to eToro. Every order is
// persisted before its request leaves (CLAUDE.md §2.7), every mutation needs the account's
// lease (D22), non-eToro accounts never reach the broker, and a real-money account change
// carries a permit (PLAN 7.21–7.22): an exit permit for closes and cancels of ours, whatever
// the gate; a fresh entry permit for every entry.

// Execution timings. Offsets are measured from the order's latest send.
var (
	// openPolls are the lookups of an open order: 1, 2, 4, 8, 15 and 30 s (step 4.7), then
	// at 60 s (still pending → cancel), a few quick ones to see the cancel, and on to 120 s
	// (a lookup still 404 → abandoned).
	openPolls = []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second,
		60 * time.Second, 62 * time.Second, 66 * time.Second, 75 * time.Second, 90 * time.Second, 105 * time.Second, 120 * time.Second,
	}
	// closePolls are the reads of a close order (CloseOrder).
	closePolls = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
)

const (
	// pendingMax: an open order still pending this long after its send is cancelled.
	pendingMax = 60 * time.Second
	// lookupGiveUp: an order eToro still does not know this long after its send is
	// abandoned (OD2); it is never resent.
	lookupGiveUp = 2 * time.Minute
	// minRetryAfter is the shortest wait after a 429 (before a close or cancel is resent, or
	// an open is looked up).
	minRetryAfter = time.Second
	// lostAnswerWait is how long a lost close answer is given before the portfolio is read
	// again; lostAnswerReads is how often it is read before the outcome stays unknown.
	lostAnswerWait  = 2 * time.Second
	lostAnswerReads = 3
	// stopToleranceBp: the stop eToro set may differ from ours by at most 1 % (rounding to
	// the instrument's precision); more means a unit or price mix-up (U4) and the fill does
	// not conform.
	stopToleranceBp = 100
)

// runtimeState is the in-memory execution state of one process.
type runtimeState struct {
	mu sync.Mutex
	// reconciled is each account's latest reconciliation (4.8).
	reconciled map[string]Reconciliation
	// supervising marks accounts whose supervisor pass is still running (4.9).
	supervising map[string]bool
	// holder is this process's lease holder id (Config.LeaseHolder, else random).
	holderOnce sync.Once
	holder     string
	// agents holds each account's agent locks (4.10, 4.11); entryCancel the context cancel of
	// the account's running agent tick (nil when none).
	agents      map[string]*agentLocks
	entryCancel map[string]context.CancelFunc
	// ticking marks accounts whose agent tick was started by AgentLoop and still runs.
	ticking map[string]bool
	// killed latches every kill in this process (review m3): even when neither kill write
	// reached the database, entries stay refused after it recovers, until ResetKill.
	killed map[string]bool
	// realStarted marks real-money accounts whose agent a person started in THIS process
	// (OD26, review M2 of the lift): a real account ticks and enters only while marked, so a
	// stored "running" that survived a restart (or a failed write of "paused") never trades.
	realStarted map[string]bool
	// views is the portfolio view cache (PLAN 6.6a): read by view use cases only, never by
	// an order path (CLAUDE.md §2.2).
	views *viewCache
	// sweeping is set while a sweep of the real gates runs (one at a time, review minor 4).
	sweeping atomic.Bool
	// warned holds the order warnings already raised in this process (kind + request id): a
	// stuck order raises its warning once, not on every supervisor pass (live findings
	// 2026-11-24).
	warned map[string]bool
	// lastScan is when each account was last scanned in the background (R28: the interval
	// follows the account's autonomy).
	lastScan map[string]time.Time
}

// agentLocks are one account's agent locks. Lock order: ctl before entry, never the other
// way round (an agent tick holds entry only and never waits for ctl).
type agentLocks struct {
	// ctl serialises StartAgent, PauseAgent, StopAgent, SetRiskLimits, Kill, ResetKill, the
	// supervisor's kill and the OD5 resume: limits change only while stopped, with no race.
	ctl sync.Mutex
	// entry is held by the entry pipeline (one agent tick at a time per account); Kill takes
	// it after cancelling the tick, so nothing is opened once Kill closes positions.
	entry sync.Mutex
}

func newRuntimeState() *runtimeState {
	return &runtimeState{
		reconciled: map[string]Reconciliation{}, supervising: map[string]bool{},
		agents: map[string]*agentLocks{}, entryCancel: map[string]context.CancelFunc{}, ticking: map[string]bool{},
		killed: map[string]bool{}, realStarted: map[string]bool{}, views: newViewCache(), warned: map[string]bool{}, lastScan: map[string]time.Time{},
	}
}

// firstWarning reports whether the warning kind for an order is raised for the first time in
// this process (and remembers it).
func (r *runtimeState) firstWarning(kind, requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := kind + "|" + requestID
	if r.warned[k] {
		return false
	}
	r.warned[k] = true
	return true
}

// latchKill records in memory that the account's kill switch was turned on (review m3).
func (r *runtimeState) latchKill(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.killed[accountID] = true
}

// killLatched reports whether a kill was latched in this process and not reset since.
func (r *runtimeState) killLatched(accountID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.killed[accountID]
}

// clearKillLatch forgets the latched kill (ResetKill only).
func (r *runtimeState) clearKillLatch(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.killed, accountID)
}

// agent returns the account's agent locks, created on first use.
func (r *runtimeState) agent(accountID string) *agentLocks {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.agents[accountID]
	if !ok {
		l = &agentLocks{}
		r.agents[accountID] = l
	}
	return l
}

// setEntryCancel records (or, with nil, forgets) the cancel function of the account's
// running agent tick.
func (r *runtimeState) setEntryCancel(accountID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel == nil {
		delete(r.entryCancel, accountID)
		return
	}
	r.entryCancel[accountID] = cancel
}

// cancelEntries cancels the account's running agent tick, if any: its pending waits and
// broker calls end, and no new entry starts.
func (r *runtimeState) cancelEntries(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.entryCancel[accountID]; c != nil {
		c()
	}
}

// entryRequest is an entry the risk engine allowed: its size and the prices it rests on.
type entryRequest struct {
	Direction  domain.Direction
	Instrument domain.InstrumentID
	Amount     domain.Cents
	Entry      domain.Micros
	StopLoss   domain.Micros
	TakeProfit domain.Micros
	// PortfolioAt is when the cash figure the size came from was read; it must still be
	// within MaxPortfolioAge when the order is sent (CLAUDE.md §2.2).
	PortfolioAt time.Time
	// RequestID is the order's x-request-id when the caller chose it first (the agent writes
	// it into the pending decision before the order row, PLAN 5.5); "" = a new one.
	RequestID string
}

// mayMutate refuses an account change for a non-eToro account (before any broker call),
// without an eToro client, or when another process holds the lease. The real-money rules are
// the callers': mayOpen for entries, mayExit for closes and cancels.
func (s *Service) mayMutate(ctx context.Context, a domain.Account) error {
	switch {
	case !a.Tradable():
		return fmt.Errorf("%s: %w", a.Name, domain.ErrViewOnly)
	case a.Mode != domain.ModeDemo && a.Mode != domain.ModeReal:
		s.d.Log.WarnContext(ports.WithAccount(ctx, a), "account change refused: the account has no trading mode")
		return fmt.Errorf("%s: %w", a.Name, domain.ErrRealModeGated)
	case s.d.Broker == nil:
		return fmt.Errorf("the eToro client is not configured: %w", domain.ErrUnavailable)
	}
	return s.holdLease(ctx, a.ID)
}

// mayExit allows closing a position or cancelling an order of cactusTrade's own (ours) on
// account a (S5 for exits, PLAN 7.21): mayMutate, and on a real-money account an exit permit,
// whatever the gate's state — a disarmed or expired gate, the opt-in off, a kill — because
// exits must still work (CLAUDE.md §2.6). Something not ours is refused (OD1). The permit is
// the zero permit on a demo account.
func (s *Service) mayExit(ctx context.Context, a domain.Account, ours bool) (domain.RealPermit, error) {
	if err := s.mayMutate(ctx, a); err != nil {
		return domain.RealPermit{}, err
	}
	if a.Mode != domain.ModeReal {
		return domain.RealPermit{}, nil
	}
	p, err := domain.PermitExit(a, ours)
	if err != nil {
		s.d.Log.WarnContext(ports.WithAccount(ctx, a), "real-money exit refused", "err", err)
		return domain.RealPermit{}, err
	}
	return p, nil
}

// mayOpen allows an entry on account a (S5 for entries): mayMutate, and on a real-money
// account a fresh entry permit (realEntryPermit, PLAN 7.22). The permit is the zero permit on
// a demo account.
func (s *Service) mayOpen(ctx context.Context, a domain.Account) (domain.RealPermit, error) {
	if a.Mode == domain.ModeReal && a.Tradable() {
		p, err := s.realEntryPermit(ctx, a, s.d.Clock.Now()) // before the order's CreatedAt
		if err != nil {
			s.d.Log.WarnContext(ports.WithAccount(ctx, a), "real-money entry refused", "err", err)
			return domain.RealPermit{}, err
		}
		if err := s.mayMutate(ctx, a); err != nil {
			return domain.RealPermit{}, err
		}
		return p, nil
	}
	return domain.RealPermit{}, s.mayMutate(ctx, a)
}

// brokerCtxFor is the context of a broker call that changes account a: the account, and the
// permit for a real-money account (ports.WithPermit).
func brokerCtxFor(ctx context.Context, a domain.Account, p domain.RealPermit) context.Context {
	return ports.WithPermit(ports.WithAccount(ctx, a), p)
}

// holdLease takes or renews the account's single-writer lease for this process (D22).
func (s *Service) holdLease(ctx context.Context, accountID string) error {
	now := s.d.Clock.Now().UTC()
	if err := s.d.Store.AgentLeases().Acquire(ctx, accountID, s.leaseHolder(), now, now.Add(domain.LeaseTTL)); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return fmt.Errorf("another process holds the lease of account %s: %w", accountID, err)
		}
		return fmt.Errorf("taking the lease of account %s: %w", accountID, err)
	}
	return nil
}

// leaseHolder is this process's lease holder id: Config.LeaseHolder, else a random id chosen
// once.
func (s *Service) leaseHolder() string {
	s.rt.holderOnce.Do(func() {
		s.rt.holder = s.cfg.LeaseHolder
		if s.rt.holder == "" {
			s.rt.holder = "proc-" + s.d.Random.ID()
		}
	})
	return s.rt.holder
}

// isKilled reports whether the account's kill switch is on: latched in this process, or
// stored. A read error counts as killed for entries (fail closed) and is returned.
func (s *Service) isKilled(ctx context.Context, accountID string) (bool, error) {
	if s.rt.killLatched(accountID) || s.agentState(ctx, accountID) == domain.AgentKilled {
		return true, nil
	}
	st, err := s.d.Store.RiskStates().Get(ctx, accountID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return false, nil
	case err != nil:
		return true, err
	}
	return st.State.Killed, nil
}

// move transitions o, lets edit set the broker ids on the new version, and stores it with
// a compare-and-set on the old one. On any error o is returned unchanged.
func (s *Service) move(ctx context.Context, o domain.Order, to domain.OrderState, reason string, edit func(*domain.Order)) (domain.Order, error) {
	next := o
	if to != o.State || to == domain.OrderSent {
		var err error
		if next, err = o.Transition(to, s.d.Clock.Now().UTC(), reason); err != nil {
			return o, err
		}
	} else {
		next.UpdatedAt = s.d.Clock.Now().UTC()
		if reason != "" {
			next.Reason = reason
		}
	}
	if edit != nil {
		edit(&next)
	}
	if err := s.d.Store.Orders().Update(ctx, o, next); err != nil {
		return o, fmt.Errorf("storing order %s (%s → %s): %w", o.RequestID, o.State, to, err)
	}
	s.d.Log.InfoContext(ctx, "order", "account_id", next.AccountID, "mode", string(next.Mode), "request", next.RequestID, "kind", string(next.Kind),
		"from", string(o.State), "to", string(next.State), "reason", next.Reason)
	s.publishOrder(next)
	return next, nil
}

// createOrder stores a new order row and announces it on the bus (after the write).
func (s *Service) createOrder(ctx context.Context, o domain.Order) error {
	if err := s.d.Store.Orders().Create(ctx, o); err != nil {
		return err
	}
	s.d.Log.InfoContext(ctx, "order", "account_id", o.AccountID, "mode", string(o.Mode), "request", o.RequestID, "kind", string(o.Kind), "to", string(o.State))
	s.publishOrder(o)
	return nil
}

// waitToResend waits out a 429's Retry-After (at least minRetryAfter) and renews the lease:
// a resend, like any send, needs it.
func (s *Service) waitToResend(ctx context.Context, a domain.Account, retryAfter time.Duration) error {
	if err := s.d.Sleep(ctx, max(retryAfter, minRetryAfter)); err != nil {
		return err
	}
	return s.holdLease(ctx, a.ID)
}

// sleepUntil waits until t (not at all when t has passed).
func (s *Service) sleepUntil(ctx context.Context, t time.Time) error {
	return s.d.Sleep(ctx, t.Sub(s.d.Clock.Now()))
}

// instrumentLabel names an instrument for people: its symbol when cached.
func (s *Service) instrumentLabel(ctx context.Context, id domain.InstrumentID) string {
	if in, _, err := s.d.Store.Instruments().Get(ctx, id); err == nil && in.Symbol != "" {
		return in.Symbol
	}
	return "instrument " + strconv.FormatInt(int64(id), 10)
}

func verb(d domain.Direction) string {
	if d == domain.Short {
		return "Short"
	}
	return "Buy"
}

// warnOnce raises a warning event about an order the first time only (per kind and order, in
// this process); later calls only log at debug level. A stuck order thus raises one alert, not
// one per supervisor pass.
func (s *Service) warnOnce(ctx context.Context, a domain.Account, o domain.Order, kind, title, detail string) {
	if !s.rt.firstWarning(kind, o.RequestID) {
		s.d.Log.DebugContext(ctx, "order warning already raised", "kind", kind, "request", o.RequestID)
		return
	}
	s.orderEvent(ctx, a, o.Direction, kind, domain.SeverityWarning, title, detail+". Request "+o.RequestID)
}

// orderEvent records an order.* / position.* event of an account (trade e-mails, R12).
func (s *Service) orderEvent(ctx context.Context, a domain.Account, d domain.Direction, kind string, sev domain.EventSeverity, title, detail string) {
	s.emitEvent(ctx, domain.Event{AccountID: a.ID, Actor: "agent", Kind: kind, Severity: sev, Title: title + " (" + a.Name + ")", Detail: detail, Direction: d})
}

// placeEntry sends an entry the risk engine allowed and follows it until eToro has filled,
// rejected or cancelled it, or it is abandoned. Nothing is sent unless the account is a demo
// eToro account, or a real-money one with a fresh entry permit (mayOpen), that is reconciled,
// not killed and whose lease this process holds, and the
// order row is stored first (state created, then sent). The returned order is its latest
// stored version; an error means it was not (or not knowingly) placed.
func (s *Service) placeEntry(ctx context.Context, a domain.Account, r entryRequest, l domain.RiskLimits) (domain.Order, error) {
	ctx = ports.WithAccount(ctx, a) // log lines name the account and mode (PLAN 7.2)
	permit, err := s.mayOpen(ctx, a)
	if err != nil {
		return domain.Order{}, fmt.Errorf("executor.placeEntry: %w", err)
	}
	switch killed, err := s.isKilled(ctx, a.ID); {
	case err != nil:
		return domain.Order{}, fmt.Errorf("executor.placeEntry: reading the kill switch: %w: %w", err, domain.ErrUnavailable)
	case killed:
		return domain.Order{}, fmt.Errorf("executor.placeEntry: the kill switch is on: %w", domain.ErrConflict)
	}
	if rc := s.Reconciled(a.ID); !rc.OK {
		return domain.Order{}, fmt.Errorf("executor.placeEntry: account not reconciled (%s): %w", rc.Reason, domain.ErrUnavailable)
	}
	// The request id is the decision's, chosen and stored (pending decision) by the caller
	// before this call; an entry without one is refused, so nothing can be sent unlogged.
	id := r.RequestID
	if id == "" {
		return domain.Order{}, fmt.Errorf("executor.placeEntry: no request id (the decision must be stored first): %w", domain.ErrInvalid)
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: id, AccountID: a.ID, Mode: a.Mode, Kind: domain.OrderOpen,
		Direction: r.Direction, Instrument: r.Instrument, Amount: r.Amount,
		Entry: r.Entry, StopLoss: r.StopLoss, TakeProfit: r.TakeProfit,
	}, s.d.Clock.Now().UTC())
	if err != nil {
		return domain.Order{}, fmt.Errorf("executor.placeEntry: %w", err)
	}
	if err := s.createOrder(ctx, o); err != nil {
		return domain.Order{}, fmt.Errorf("executor.placeEntry: storing the order (nothing sent): %w", err)
	}
	now := s.d.Clock.Now()
	if r.PortfolioAt.IsZero() || now.Sub(r.PortfolioAt) > l.MaxPortfolioAge || r.PortfolioAt.After(now.Add(time.Second)) {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "the cash figure was stale when the order was due: not sent", nil)
		return o, fmt.Errorf("executor.placeEntry: the portfolio is older than %s: %w", l.MaxPortfolioAge, domain.ErrUnavailable)
	}
	intent, err := domain.IntentFor(o, permit)
	if err != nil {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "no valid intent: not sent", nil)
		return o, fmt.Errorf("executor.placeEntry: %w", err)
	}
	if o, err = s.move(ctx, o, domain.OrderSent, "", nil); err != nil {
		return o, fmt.Errorf("executor.placeEntry: marking the order sent (nothing sent): %w", err)
	}
	return s.sendOpen(ctx, a, o, intent, permit)
}

// sendOpen sends a stored open order once and follows it. An open is never resent (review
// M3, stricter than OD2: U1/U7 are unverified): after a 429 it waits out Retry-After and looks
// the order up; found → followed, not found → polled and abandoned after lookupGiveUp. Its
// cash stays reserved until it is final. Once the request has left, its outcome is stored
// even if ctx was cancelled meanwhile (a kill, review m4).
func (s *Service) sendOpen(ctx context.Context, a domain.Account, o domain.Order, in domain.OrderIntent, permit domain.RealPermit) (domain.Order, error) {
	bctx := brokerCtxFor(ctx, a, permit)
	wctx := context.WithoutCancel(ctx)
	label := s.instrumentLabel(ctx, o.Instrument)
	if cerr := ctx.Err(); cerr != nil {
		// Cancelled (a kill, a shutdown) before the request left: eToro never saw it.
		o, _ = s.move(wctx, o, domain.OrderAbandoned, "cancelled before it was sent", nil)
		return o, fmt.Errorf("executor.sendOpen: not sent: %w", cerr)
	}
	st, err := s.d.Broker.Open(bctx, in)
	na, isNA := errors.AsType[*domain.NotAcceptedError](err)
	switch {
	case err == nil:
		o, err = s.move(wctx, o, domain.OrderPending, "accepted by eToro", func(n *domain.Order) { n.EtoroOrderID = st.OrderID })
		if err != nil {
			return o, fmt.Errorf("executor.sendOpen: %w", err)
		}
		s.orderEvent(wctx, a, o.Direction, "order.sent", domain.SeverityInfo, fmt.Sprintf("%s $%s of %s sent", verb(o.Direction), o.Amount, label), "stop "+o.StopLoss.String()+", request "+o.RequestID)
		return s.resolveOpen(ctx, a, o)
	case isNA && na.RateLimited:
		// eToro may have processed it despite the 429 (U7) and may not dedupe the id (U1):
		// never resent. It stays sent (reserved, its instrument in flight) until a lookup
		// resolves it.
		s.d.Log.WarnContext(ctx, "open order rate limited: not resent, following it by lookup", "account", a.ID, "request", o.RequestID, "retry_after", na.RetryAfter)
		if err := s.d.Sleep(ctx, max(na.RetryAfter, minRetryAfter)); err != nil {
			return o, fmt.Errorf("executor.sendOpen: waiting after a 429 (reconciliation resolves it): %w", err)
		}
		lk, lerr := s.d.Broker.Lookup(bctx, o.RequestID, o.EtoroOrderID)
		switch {
		case lerr == nil:
			if o, err = s.applyOpenStatus(wctx, a, o, lk); err != nil && !errors.Is(err, errFillsNotRecorded) {
				return o, err
			}
			if o.State.Final() {
				return o, nil
			}
		case errors.Is(lerr, domain.ErrNotFound):
			// Not processed, or not visible yet (U2): polled, abandoned after lookupGiveUp.
		default:
			o, _ = s.move(wctx, o, domain.OrderUnknown, "rate limited, and the lookup failed: "+lerr.Error(), nil)
		}
		return s.resolveOpen(ctx, a, o)
	case isNA:
		o, _ = s.move(wctx, o, domain.OrderRejected, "eToro refused the order: "+na.Reason, nil)
		s.orderEvent(wctx, a, o.Direction, "order.rejected", domain.SeverityWarning, verb(o.Direction)+" "+label+" refused by eToro", na.Reason)
		return o, fmt.Errorf("executor.sendOpen: %w", err)
	case errors.Is(err, domain.ErrRealModeGated):
		o, _ = s.move(wctx, o, domain.OrderAbandoned, "refused before sending: real mode is gated", nil)
		return o, fmt.Errorf("executor.sendOpen: %w", err)
	default:
		// Outcome unknown (or an error we cannot classify): it may exist at eToro. Only a
		// lookup can tell; it is never resent (OD2).
		o, _ = s.move(wctx, o, domain.OrderUnknown, "the answer was lost: "+err.Error(), nil)
		s.orderEvent(wctx, a, o.Direction, "order.unknown", domain.SeverityWarning, verb(o.Direction)+" "+label+": outcome unknown", "resolving by lookup; entries wait. Request "+o.RequestID)
		return s.resolveOpen(ctx, a, o)
	}
}

// errFillsNotRecorded: eToro reported fills that could not all be stored as agent positions;
// the order is not moved on, so the next lookup records them (review B2).
var errFillsNotRecorded = errors.New("fills not recorded")

// resolveOpen polls the lookup of an open order on the openPolls schedule until it is
// final; offsets already past are skipped (after a 429's wait), but the last one always
// runs. Still pending at pendingMax → cancelled; unknown to eToro at lookupGiveUp →
// abandoned. An order left unresolved is picked up by the supervisor and reconciliation.
func (s *Service) resolveOpen(ctx context.Context, a domain.Account, o domain.Order) (domain.Order, error) {
	bctx := ports.WithAccount(ctx, a)
	cancelSent := false
	for i, off := range openPolls {
		if i < len(openPolls)-1 && o.SentAt.Add(off).Before(s.d.Clock.Now()) {
			continue
		}
		if err := s.sleepUntil(ctx, o.SentAt.Add(off)); err != nil {
			return o, fmt.Errorf("executor.resolveOpen: %w", err)
		}
		st, err := s.d.Broker.Lookup(bctx, o.RequestID, o.EtoroOrderID)
		age := s.d.Clock.Now().Sub(o.SentAt)
		switch {
		case err == nil:
			if o, err = s.applyOpenStatus(ctx, a, o, st); err != nil {
				if errors.Is(err, errFillsNotRecorded) {
					s.d.Log.WarnContext(ctx, "recording the fills failed; trying again on the next lookup", "account", a.ID, "request", o.RequestID, "err", err)
					continue
				}
				// Another writer (the supervisor, reconciliation) moved it first: follow the
				// stored version.
				cur, gerr := s.d.Store.Orders().Get(ctx, o.RequestID)
				if !errors.Is(err, domain.ErrConflict) || gerr != nil {
					return o, err
				}
				o = cur
			}
			if o.State.Final() {
				return o, nil
			}
			// Cancel only what eToro itself reports as still pending (never a fill whose
			// executions are not listed yet).
			stillPending := !st.Final && (st.State == domain.OrderPending || st.State == domain.OrderPartial)
			if !cancelSent && stillPending && age >= pendingMax && o.EtoroOrderID > 0 {
				cancelSent = true
				// At most one cancel per order while one is in flight or was accepted (the
				// supervisor may have sent one); an unreadable history leaves it to the
				// supervisor.
				hist, herr := s.cancelHistory(ctx, a.ID, []domain.Order{o})
				switch {
				case herr != nil:
					s.d.Log.WarnContext(ctx, "reading our cancels failed; the supervisor cancels the order", "account", a.ID, "request", o.RequestID, "err", herr)
				case !s.cancelBlocked(ctx, a, o, hist[o.EtoroOrderID], s.d.Clock.Now()):
					if _, err := s.cancelOrder(ctx, a, o, fmt.Sprintf("still pending after %s", pendingMax)); err != nil {
						s.d.Log.WarnContext(ctx, "cancelling a pending order failed", "account", a.ID, "request", o.RequestID, "err", err)
					}
				}
			}
		case errors.Is(err, domain.ErrNotFound):
			// An order eToro accepted (pending) is never abandoned (review-c): it stays
			// reserved and the supervisor follows it (one warning, live findings 2026-11-24).
			if age >= lookupGiveUp && domain.CanMove(o.State, domain.OrderAbandoned) {
				return s.abandon(ctx, a, o, fmt.Sprintf("eToro did not know the order %s after it was sent", lookupGiveUp))
			}
		default:
			s.d.Log.WarnContext(ctx, "order lookup failed", "account", a.ID, "request", o.RequestID, "err", err)
		}
	}
	return o, nil
}

// abandon marks an order eToro never knew as abandoned (final, never resent).
func (s *Service) abandon(ctx context.Context, a domain.Account, o domain.Order, why string) (domain.Order, error) {
	o, err := s.move(ctx, o, domain.OrderAbandoned, why, nil)
	if err != nil {
		return o, fmt.Errorf("executor.abandon: %w", err)
	}
	s.orderEvent(ctx, a, o.Direction, "order.abandoned", domain.SeverityWarning, fmt.Sprintf("%s order for %s abandoned", o.Kind, s.instrumentLabel(ctx, o.Instrument)), why+". Request "+o.RequestID)
	return o, nil
}

// applyOpenStatus folds eToro's answer about an open order into it: fills are recorded as
// agent positions and checked, and the state follows eToro's. A partial fill that eToro
// reports final (rest cancelled or rejected) becomes cancelled. A fill reported without any
// positionExecutions counts as still pending, and an order whose fills could not all be
// recorded is not moved at all (errFillsNotRecorded): an order is never final while a
// position it opened is not recorded as ours (review B2).
func (s *Service) applyOpenStatus(ctx context.Context, a domain.Account, o domain.Order, st domain.OrderStatus) (domain.Order, error) {
	if (st.State == domain.OrderFilled || st.State == domain.OrderPartial) && len(st.Executions) == 0 {
		st.State, st.Final = domain.OrderPending, false
	}
	if len(st.Executions) > 0 {
		if err := s.recordFills(ctx, a, o, st); err != nil {
			return o, fmt.Errorf("executor.applyOpenStatus: order %s: %w", o.RequestID, err)
		}
	}
	target := st.State
	reason := st.Reason
	switch {
	case st.State == domain.OrderPartial && st.Final:
		target, reason = domain.OrderCancelled, "partly filled; eToro ended the rest"
	case st.State == domain.OrderRejected && o.State == domain.OrderPartial:
		target = domain.OrderCancelled
	case st.State != domain.OrderPending && st.State != domain.OrderFilled && st.State != domain.OrderPartial &&
		st.State != domain.OrderRejected && st.State != domain.OrderCancelled:
		return o, nil // nothing eToro said moves the order
	}
	edit := func(n *domain.Order) {
		if st.OrderID > 0 {
			n.EtoroOrderID = st.OrderID
		}
		if n.PositionID == 0 && len(st.Executions) > 0 {
			n.PositionID = st.Executions[0].PositionID
		}
	}
	if target == o.State {
		if (st.OrderID > 0 && st.OrderID != o.EtoroOrderID) || (o.PositionID == 0 && len(st.Executions) > 0) {
			return s.move(ctx, o, target, "", edit)
		}
		return o, nil
	}
	if !domain.CanMove(o.State, target) {
		s.d.Log.WarnContext(ctx, "eToro reported a state the order cannot move to", "request", o.RequestID, "from", o.State, "to", target)
		return o, nil
	}
	o, err := s.move(ctx, o, target, reason, edit)
	if err != nil {
		return o, fmt.Errorf("executor.applyOpenStatus: %w", err)
	}
	label := s.instrumentLabel(ctx, o.Instrument)
	switch o.State {
	case domain.OrderRejected:
		s.orderEvent(ctx, a, o.Direction, "order.rejected", domain.SeverityWarning, verb(o.Direction)+" "+label+" rejected by eToro", reason)
	case domain.OrderCancelled:
		s.orderEvent(ctx, a, o.Direction, "order.cancelled", domain.SeverityInfo, verb(o.Direction)+" "+label+" cancelled", reason)
	}
	return o, nil
}

// stopClose reports whether eToro's stop is within stopToleranceBp of the stop we sent.
func stopClose(got, want domain.Micros) bool {
	d := int64(got - want)
	if d < 0 {
		d = -d
	}
	return want > 0 && d*10_000 <= int64(want)*stopToleranceBp
}

// recordFills records the positions an open order produced as agent positions (only new
// ones) and checks them: the shape must be the one sent (real/long/x1 or cfd/short/x1), with
// a stop on the losing side, close to the stop we sent. A fill that does not conform is
// closed at once, its instrument blocked and an alert raised. A position eToro reports as
// already closed (Executions[].Closed) is recorded as ours and settled as closed at once; it is
// never sent a close. It returns errFillsNotRecorded
// when any position could not be stored: the caller must not move the order on.
func (s *Service) recordFills(ctx context.Context, a domain.Account, o domain.Order, st domain.OrderStatus) error {
	ok, why := st.Conforms(o.Direction)
	if ok && st.Instrument != 0 && st.Instrument != o.Instrument {
		ok, why = false, fmt.Sprintf("eToro filled instrument %d, not %d", st.Instrument, o.Instrument)
	}
	for _, e := range st.Executions {
		if ok && !stopClose(e.StopLossRate, o.StopLoss) {
			ok, why = false, fmt.Sprintf("position %d: stop %s is not the stop sent (%s)", e.PositionID, e.StopLossRate, o.StopLoss)
		}
	}
	label := s.instrumentLabel(ctx, o.Instrument)
	now := s.d.Clock.Now().UTC()
	var (
		fresh  []domain.AgentPosition // newly recorded and still open: closed if non-conforming
		added  int                    // newly recorded, open or already closed by eToro
		failed error
	)
	for _, e := range st.Executions {
		ap := domain.AgentPosition{
			AccountID: a.ID, PositionID: e.PositionID, OpenRequestID: o.RequestID,
			Instrument: o.Instrument, Direction: o.Direction, OpenedAt: now,
		}
		err := s.d.Store.AgentPositions().Add(ctx, ap)
		switch {
		case errors.Is(err, domain.ErrConflict):
			continue // seen before
		case err != nil:
			// Not recorded as ours yet: the order stays open and the next lookup tries again
			// (a non-conforming one is checked and closed then).
			s.d.Log.ErrorContext(ctx, "recording an agent position failed", "account", a.ID, "position", e.PositionID, "err", err)
			failed = errors.Join(failed, fmt.Errorf("position %d: %w", e.PositionID, err))
			continue
		}
		added++
		s.orderEvent(ctx, a, o.Direction, "order.filled", domain.SeverityInfo,
			fmt.Sprintf("%s $%s of %s filled", verb(o.Direction), e.Amount, label),
			fmt.Sprintf("position %d at %s, stop %s", e.PositionID, e.OpenRate, e.StopLossRate))
		if e.Closed {
			// Ours, and already closed by eToro (its take-profit or stop-loss hit before we
			// looked, seen live 2026-11-24): settled at once, never closed again.
			s.markClosed(ctx, a, ap, fmt.Sprintf("position %d was already closed at eToro (take-profit or stop-loss) when its fill was first seen", e.PositionID))
			continue
		}
		fresh = append(fresh, ap)
	}
	if failed != nil {
		failed = fmt.Errorf("%w: %w", errFillsNotRecorded, failed)
	}
	if ok || added == 0 {
		return failed
	}
	if err := s.d.Store.InstrumentBlocks().Block(ctx, domain.InstrumentBlock{AccountID: a.ID, Instrument: o.Instrument, Reason: "non-conforming fill: " + why, At: now}); err != nil {
		s.d.Log.ErrorContext(ctx, "blocking an instrument failed", "account", a.ID, "instrument", o.Instrument, "err", err)
	}
	title := "Non-conforming fill of " + label + ": closing it and blocking the instrument"
	if len(fresh) == 0 {
		title = "Non-conforming fill of " + label + " (already closed by eToro): blocking the instrument"
	}
	s.orderEvent(ctx, a, o.Direction, "order.nonconforming", domain.SeverityDanger, title, why)
	for _, ap := range fresh {
		if _, err := s.closePosition(ctx, a, ap, "non-conforming fill: "+why, closePolls); err != nil {
			s.d.Log.ErrorContext(ctx, "closing a non-conforming position failed", "account", a.ID, "position", ap.PositionID, "err", err)
		}
	}
	return failed
}

// closePosition closes one of cactusTrade's positions (OD1: never anyone else's): the close
// order is stored first, then sent; polls are the CloseOrder reads to make afterwards (nil
// = none: the supervisor reads it on its next pass). A lost answer is followed by a fresh
// portfolio read: still open → the same request id is sent again (at most MaxSends). Works
// while the kill switch is on.
func (s *Service) closePosition(ctx context.Context, a domain.Account, ap domain.AgentPosition, why string, polls []time.Duration) (domain.Order, error) {
	ctx = ports.WithAccount(ctx, a) // log lines name the account and mode (PLAN 7.2)
	permit, err := s.mayExit(ctx, a, ap.AccountID == a.ID && ap.PositionID > 0)
	if err != nil {
		return domain.Order{}, fmt.Errorf("executor.closePosition: %w", err)
	}
	if ap.CloseRequestID != "" {
		prev, err := s.d.Store.Orders().Get(ctx, ap.CloseRequestID)
		if err == nil && !prev.State.Final() {
			switch {
			case prev.EtoroOrderID > 0:
				// Read it once. Ended without closing the position (partly filled, rejected,
				// cancelled; review M2): a new close follows at once.
				done, err := s.pollClose(ctx, a, ap, prev, []time.Duration{0})
				if err != nil || !done.State.Final() || done.State == domain.OrderFilled {
					return done, err
				}
			case s.d.Clock.Now().Sub(prev.UpdatedAt) < lookupGiveUp:
				return prev, nil // in flight
			default:
				if _, err := s.abandon(ctx, a, prev, "close not confirmed: a new close is sent"); err != nil {
					return prev, fmt.Errorf("executor.closePosition: %w", err)
				}
			}
		}
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: s.d.Random.ID(), AccountID: a.ID, Mode: a.Mode, Kind: domain.OrderClose,
		Direction: ap.Direction, Instrument: ap.Instrument, PositionID: ap.PositionID, Reason: why,
	}, s.d.Clock.Now().UTC())
	if err != nil {
		return domain.Order{}, fmt.Errorf("executor.closePosition: %w", err)
	}
	o.Reason = why
	if err := s.createOrder(ctx, o); err != nil {
		return domain.Order{}, fmt.Errorf("executor.closePosition: storing the close (nothing sent): %w", err)
	}
	ap.CloseRequestID = o.RequestID
	if err := s.d.Store.AgentPositions().Update(ctx, ap); err != nil {
		s.d.Log.WarnContext(ctx, "recording the close on the agent position failed", "account", a.ID, "position", ap.PositionID, "err", err)
	}
	if o, err = s.move(ctx, o, domain.OrderSent, "", nil); err != nil {
		return o, fmt.Errorf("executor.closePosition: marking the close sent (nothing sent): %w", err)
	}
	bctx := brokerCtxFor(ctx, a, permit)
	label := s.instrumentLabel(ctx, ap.Instrument)
	for {
		st, err := s.d.Broker.Close(bctx, ap.PositionID, ap.Instrument, o.RequestID)
		na, isNA := errors.AsType[*domain.NotAcceptedError](err)
		switch {
		case err == nil:
			if o, err = s.move(ctx, o, domain.OrderPending, "", func(n *domain.Order) { n.EtoroOrderID = st.OrderID }); err != nil {
				return o, fmt.Errorf("executor.closePosition: %w", err)
			}
			return s.pollClose(ctx, a, ap, o, polls)
		case isNA && na.RateLimited:
			if o.Sends >= domain.MaxSends(domain.OrderClose) {
				o, _ = s.move(ctx, o, domain.OrderRejected, fmt.Sprintf("rate limited %d times", o.Sends), nil)
				return o, fmt.Errorf("executor.closePosition: %w", err)
			}
			if err := s.waitToResend(ctx, a, na.RetryAfter); err != nil {
				return o, fmt.Errorf("executor.closePosition: %w", err)
			}
			if o, err = s.move(ctx, o, domain.OrderSent, "resent after a 429", nil); err != nil {
				return o, fmt.Errorf("executor.closePosition: %w", err)
			}
		case isNA:
			if errors.Is(err, domain.ErrNotFound) {
				if open, known := s.positionOpen(ctx, a, ap); known && !open {
					o, _ = s.move(ctx, o, domain.OrderRejected, "the position was already closed at eToro", nil)
					s.markClosed(ctx, a, ap, "closed at eToro before our close")
					return o, nil
				}
			}
			o, _ = s.move(ctx, o, domain.OrderRejected, "eToro refused the close: "+na.Reason, nil)
			s.orderEvent(ctx, a, o.Direction, "order.rejected", domain.SeverityWarning, "Close of "+label+" refused by eToro", na.Reason)
			return o, fmt.Errorf("executor.closePosition: %w", err)
		case errors.Is(err, domain.ErrRealModeGated):
			o, _ = s.move(ctx, o, domain.OrderAbandoned, "refused before sending: real mode is gated", nil)
			return o, fmt.Errorf("executor.closePosition: %w", err)
		default:
			open, known := s.positionOpenAfterLoss(ctx, a, ap)
			switch {
			case known && !open:
				if o, err = s.move(ctx, o, domain.OrderFilled, "the answer was lost; the position is gone from the portfolio", nil); err != nil {
					return o, fmt.Errorf("executor.closePosition: %w", err)
				}
				s.markClosed(ctx, a, ap, o.Reason)
				return o, nil
			case known && o.Sends < domain.MaxSends(domain.OrderClose):
				if err := s.holdLease(ctx, a.ID); err != nil {
					return o, fmt.Errorf("executor.closePosition: %w", err)
				}
				if o, err = s.move(ctx, o, domain.OrderSent, "resent: the answer was lost and the position is still open", nil); err != nil {
					return o, fmt.Errorf("executor.closePosition: %w", err)
				}
			default:
				o, _ = s.move(ctx, o, domain.OrderUnknown, "the answer was lost: "+err.Error(), nil)
				s.orderEvent(ctx, a, o.Direction, "order.unknown", domain.SeverityWarning, "Close of "+label+": outcome unknown", "the supervisor tries again. Request "+o.RequestID)
				return o, fmt.Errorf("executor.closePosition: %w", err)
			}
		}
	}
}

// positionOpen reads the portfolio once: whether the agent position is open, and whether that
// is known. A read error is not known, and neither is the absence of a position recorded less
// than fillVisibleAfter before the read: its fill may not show in the portfolio yet (U11), so
// absence does not prove it closed (found by the review-M4 simulator).
func (s *Service) positionOpen(ctx context.Context, a domain.Account, ap domain.AgentPosition) (open, known bool) {
	pf, err := s.d.Broker.Portfolio(ports.WithAccount(ctx, a))
	if err != nil {
		return false, false
	}
	for _, p := range pf.Positions {
		if p.ID == ap.PositionID {
			return true, true
		}
	}
	return false, pf.AsOf.Sub(ap.OpenedAt) >= fillVisibleAfter
}

// positionOpenAfterLoss gives a close whose answer was lost a moment, then reads the
// portfolio (up to lostAnswerReads times while the answer is not known).
func (s *Service) positionOpenAfterLoss(ctx context.Context, a domain.Account, ap domain.AgentPosition) (open, known bool) {
	for range lostAnswerReads {
		if s.d.Sleep(ctx, lostAnswerWait) != nil {
			return false, false
		}
		if open, known = s.positionOpen(ctx, a, ap); known {
			return open, known
		}
	}
	return false, false
}

// pollClose reads a close order at the given offsets from its send until it is final.
func (s *Service) pollClose(ctx context.Context, a domain.Account, ap domain.AgentPosition, o domain.Order, polls []time.Duration) (domain.Order, error) {
	for _, off := range polls {
		if err := s.sleepUntil(ctx, o.SentAt.Add(off)); err != nil {
			return o, fmt.Errorf("executor.pollClose: %w", err)
		}
		st, err := s.d.Broker.CloseOrder(ports.WithAccount(ctx, a), o.EtoroOrderID)
		if err != nil {
			s.d.Log.WarnContext(ctx, "reading a close order failed", "account", a.ID, "request", o.RequestID, "err", err)
			continue
		}
		if o, err = s.applyCloseStatus(ctx, a, ap, o, st); err != nil || o.State.Final() {
			return o, err
		}
	}
	return o, nil
}

// applyCloseStatus folds eToro's answer about a close order into it. A close that eToro ends
// partly filled (status 9/10) closes the position only when a fresh portfolio no longer has
// it; otherwise the close order is cancelled ("the rest is still open"), the position stays
// ours and open, and the supervisor closes it again (review M2).
func (s *Service) applyCloseStatus(ctx context.Context, a domain.Account, ap domain.AgentPosition, o domain.Order, st domain.OrderStatus) (domain.Order, error) {
	target, reason, partLeft := st.State, st.Reason, false
	switch {
	case st.State == domain.OrderFilled:
	case st.State == domain.OrderPartial && st.Final:
		target = domain.OrderFilled
		if open, known := s.positionOpen(ctx, a, ap); open || !known {
			target, reason, partLeft = domain.OrderCancelled, "partly closed by eToro; the rest of the position is still open (or unconfirmed) and is closed again", true
		}
	case st.State == domain.OrderRejected, st.State == domain.OrderCancelled:
	default:
		return o, nil // still pending
	}
	if !domain.CanMove(o.State, target) {
		return o, nil
	}
	o, err := s.move(ctx, o, target, reason, nil)
	if err != nil {
		return o, fmt.Errorf("executor.applyCloseStatus: %w", err)
	}
	label := s.instrumentLabel(ctx, ap.Instrument)
	if partLeft {
		s.orderEvent(ctx, a, o.Direction, "order.partial_close", domain.SeverityWarning, "Close of "+label+" only partly filled", fmt.Sprintf("position %d stays open and is closed again. Request %s", ap.PositionID, o.RequestID))
		return o, nil
	}
	if o.State == domain.OrderFilled {
		detail := fmt.Sprintf("position %d", ap.PositionID)
		for _, e := range st.Executions {
			if e.PositionID == ap.PositionID && e.CloseRate > 0 {
				detail += " at " + e.CloseRate.String()
			}
		}
		s.markClosed(ctx, a, ap, detail+" — "+o.Reason)
		return o, nil
	}
	s.orderEvent(ctx, a, o.Direction, "order.rejected", domain.SeverityWarning, "Close of "+label+" not executed", st.Reason)
	return o, nil
}

// markClosed records an agent position as closed and announces it.
func (s *Service) markClosed(ctx context.Context, a domain.Account, ap domain.AgentPosition, detail string) {
	ap.ClosedAt = s.d.Clock.Now().UTC()
	if err := s.d.Store.AgentPositions().Update(ctx, ap); err != nil {
		s.d.Log.ErrorContext(ctx, "recording a closed position failed", "account", a.ID, "position", ap.PositionID, "err", err)
	}
	s.orderEvent(ctx, a, ap.Direction, "position.closed", domain.SeverityInfo, "Position in "+s.instrumentLabel(ctx, ap.Instrument)+" closed", detail)
}

// cancelOrder asks eToro to cancel a pending open order of ours, with its own stored
// request id. The target's own state follows from its next lookup.
func (s *Service) cancelOrder(ctx context.Context, a domain.Account, target domain.Order, why string) (domain.Order, error) {
	ctx = ports.WithAccount(ctx, a) // log lines name the account and mode (PLAN 7.2)
	permit, err := s.mayExit(ctx, a, target.AccountID == a.ID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("executor.cancelOrder: %w", err)
	}
	if target.Kind != domain.OrderOpen || target.EtoroOrderID <= 0 || target.State.Final() {
		return domain.Order{}, domain.Invalid("target", "only a pending open order with an eToro id can be cancelled")
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: s.d.Random.ID(), AccountID: a.ID, Mode: a.Mode, Kind: domain.OrderCancel,
		Direction: target.Direction, Instrument: target.Instrument, TargetOrderID: target.EtoroOrderID,
	}, s.d.Clock.Now().UTC())
	if err != nil {
		return domain.Order{}, fmt.Errorf("executor.cancelOrder: %w", err)
	}
	o.Reason = why
	if err := s.createOrder(ctx, o); err != nil {
		return domain.Order{}, fmt.Errorf("executor.cancelOrder: storing the cancel (nothing sent): %w", err)
	}
	if o, err = s.move(ctx, o, domain.OrderSent, "", nil); err != nil {
		return o, fmt.Errorf("executor.cancelOrder: marking the cancel sent (nothing sent): %w", err)
	}
	bctx := brokerCtxFor(ctx, a, permit)
	for {
		err := s.d.Broker.Cancel(bctx, target.EtoroOrderID, o.RequestID)
		na, isNA := errors.AsType[*domain.NotAcceptedError](err)
		switch {
		case err == nil:
			o, err = s.move(ctx, o, domain.OrderFilled, "cancel accepted: "+why, nil)
			if err != nil {
				return o, fmt.Errorf("executor.cancelOrder: %w", err)
			}
			return o, nil
		case isNA && na.RateLimited:
			if o.Sends >= domain.MaxSends(domain.OrderCancel) {
				o, _ = s.move(ctx, o, domain.OrderRejected, fmt.Sprintf("rate limited %d times", o.Sends), nil)
				return o, fmt.Errorf("executor.cancelOrder: %w", err)
			}
			if err := s.waitToResend(ctx, a, na.RetryAfter); err != nil {
				return o, fmt.Errorf("executor.cancelOrder: %w", err)
			}
			if o, err = s.move(ctx, o, domain.OrderSent, "resent after a 429", nil); err != nil {
				return o, fmt.Errorf("executor.cancelOrder: %w", err)
			}
		case isNA:
			o, _ = s.move(ctx, o, domain.OrderRejected, "eToro refused the cancel: "+na.Reason, nil)
			return o, fmt.Errorf("executor.cancelOrder: %w", err)
		case errors.Is(err, domain.ErrRealModeGated):
			o, _ = s.move(ctx, o, domain.OrderAbandoned, "refused before sending: real mode is gated", nil)
			return o, fmt.Errorf("executor.cancelOrder: %w", err)
		default:
			o, _ = s.move(ctx, o, domain.OrderUnknown, "the answer was lost: "+err.Error(), nil)
			return o, fmt.Errorf("executor.cancelOrder: %w", err)
		}
	}
}

// setRealStarted marks (or clears) a real account's agent as started by hand in this process.
func (r *runtimeState) setRealStarted(accountID string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		r.realStarted[accountID] = true
		return
	}
	delete(r.realStarted, accountID)
}

// realStartedHere reports whether a person started the real account's agent in this process.
func (r *runtimeState) realStartedHere(accountID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.realStarted[accountID]
}
