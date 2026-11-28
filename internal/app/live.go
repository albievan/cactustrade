package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/strategy"
)

// The live view of an account (PLAN 6.6b): what the dashboard stream sends. It is built from
// the portfolio view cache and the database only; it never calls eToro (WarmLiveView does,
// through the cache) and no decision is ever taken on it (CLAUDE.md §2.2, §2.6).

const (
	// liveStaleAfter: a snapshot whose portfolio is older than this is marked stale.
	liveStaleAfter = 30 * time.Second
	// liveRecentOrders is how many of the latest orders a snapshot carries.
	liveRecentOrders = 20
	// liveReviews is how many of the latest reviews are searched for each share's regime.
	liveReviews = 500
)

// LivePosition is an open position as the dashboard shows it.
type LivePosition struct {
	domain.Position
	Symbol string
	Market domain.Market
	// Ours: cactusTrade opened it (OD1; only these are ever closed by cactusTrade), with the
	// request id of its open order.
	Ours          bool
	OpenRequestID string
	// FlattenAt is when the supervisor closes it today (ours only; zero = its market does
	// not trade today or is unknown).
	FlattenAt time.Time
}

// LiveRegime is the latest regime read for a watchlist share (from its latest review).
type LiveRegime struct {
	Instrument domain.InstrumentID
	Symbol     string
	Market     domain.Market // the share's exchange (for the live order messages)
	// Known: there is a review with regime probabilities; Regime is the most likely one.
	Known  bool
	Regime domain.Regime
	Probs  map[domain.Regime]float64
	At     time.Time
	// Mood is the most likely regime for people (R28: "Trending", "moving steadily one way").
	Mood domain.RegimeText
	// Stance and MayDo say which playbooks may act at the account's current thresholds
	// (its effective aggressiveness and whether shorts are allowed).
	Stance strategy.Stance
	MayDo  string
}

// LiveSnapshot is an account's live view.
type LiveSnapshot struct {
	Account domain.AccountView
	At      time.Time // when it was built
	// PortfolioKnown: a portfolio is in the view cache, read at PortfolioAt. Stale: there is
	// none, or it is older than liveStaleAfter. Figures and Positions need a portfolio.
	PortfolioKnown bool
	PortfolioAt    time.Time
	Stale          bool
	Figures        domain.LiveFigures
	Positions      []LivePosition
	// OpenOrders are our orders that are not final (oldest first); RecentOrders the latest
	// liveRecentOrders (newest first).
	OpenOrders   []OrderView
	RecentOrders []OrderView
	Regimes      []LiveRegime
	// RegimeLegend explains the market moods (R28).
	RegimeLegend []domain.RegimeText
	State        domain.AgentState
	// Reconciliation is the latest in this process; Killed the kill switch (stored or
	// latched in memory) with its reason; DayStopped the daily loss latch of today's risk day.
	Reconciliation Reconciliation
	Killed         bool
	KillReason     string
	DayStopped     bool
	Lease          LeaseStatus
	// Currency of the money figures: USD on eToro, the Trading 212 account's own ("" = not
	// known yet).
	Currency string
}

// LiveSnapshot returns the live view of an account the caller can view (accountID "" = the
// current account): the limit distances, positions, orders, regimes, agent state,
// reconciliation, kill switch, latch, lease and how fresh the portfolio is. It uses only the
// view cache and stored data: it never calls eToro.
func (s *Service) LiveSnapshot(ctx context.Context, p Principal, accountID string) (LiveSnapshot, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return LiveSnapshot{}, err
	}
	v, err := s.agentAccount(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return LiveSnapshot{}, err
	}
	now := s.d.Clock.Now()
	out := LiveSnapshot{Account: v, At: now.UTC(), State: s.agentState(ctx, v.ID), Reconciliation: s.Reconciled(v.ID), Currency: s.viewCurrency(v.Account)}
	rs, err := s.d.Store.RiskStates().Get(ctx, v.ID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		rs = domain.AccountRiskState{AccountID: v.ID}
	case err != nil:
		return LiveSnapshot{}, fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	killed, err := s.isKilled(ctx, v.ID)
	if err != nil {
		return LiveSnapshot{}, fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	out.Killed, out.KillReason = killed, rs.KillReason
	out.DayStopped = rs.State.DayStopped && rs.State.Day == domain.RiskDay(now)
	if l, err := s.d.Store.AgentLeases().Get(ctx, v.ID); err == nil && now.Before(l.ExpiresAt) {
		out.Lease = LeaseStatus{Mine: l.Holder == s.leaseHolder(), Other: l.Holder != s.leaseHolder(), ExpiresAt: l.ExpiresAt}
	}
	if err := s.liveOrders(ctx, v.ID, &out); err != nil {
		return LiveSnapshot{}, err
	}
	out.RegimeLegend = slices.Clone(domain.RegimeLegend)
	if out.Regimes, err = s.liveRegimes(ctx, v.Account); err != nil {
		return LiveSnapshot{}, err
	}
	pf, at, ok := s.rt.views.peek(v.ID)
	out.PortfolioKnown, out.PortfolioAt = ok, at.UTC()
	out.Stale = !ok || now.Sub(at) > liveStaleAfter
	if !ok {
		return out, nil
	}
	l := s.riskLimits(ctx, v.ID)
	res, err := s.reservations(ctx, v.ID, pf, now)
	if err != nil {
		return LiveSnapshot{}, fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	sessions := s.marketSessions(ctx, now)
	out.Figures = domain.LimitDistances(pf, rs, l, res, now, sessions)
	if out.Positions, err = s.livePositions(ctx, v.ID, pf, sessions, l); err != nil {
		return LiveSnapshot{}, err
	}
	return out, nil
}

// WarmLiveView makes sure the view cache holds a recent portfolio of the account (view
// access): at most one eToro read per account every viewCacheTTL, shared with every other
// view. The live stream calls it on connect, for accounts the supervisor does not read.
func (s *Service) WarmLiveView(ctx context.Context, p Principal, accountID string) error {
	if err := s.authorize(p, domain.PermView); err != nil {
		return err
	}
	v, err := s.agentAccount(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return err
	}
	switch {
	case v.T212Trading() && s.d.T212 == nil:
		return fmt.Errorf("the Trading 212 client is not configured: %w", domain.ErrUnavailable)
	case v.T212Trading():
		if !s.hasKeys(ctx, v.Account) {
			return fmt.Errorf("the account's Trading 212 keys are not set: %w", domain.ErrUnavailable)
		}
	case s.d.Broker == nil:
		return fmt.Errorf("the eToro client is not configured: %w", domain.ErrUnavailable)
	case !s.hasKeys(ctx, v.Account):
		return fmt.Errorf("the account's eToro keys are not set: %w", domain.ErrUnavailable)
	}
	if _, _, err := s.viewPortfolio(ctx, v); err != nil {
		return fmt.Errorf("app.WarmLiveView: %w", err)
	}
	return nil
}

// LiveFilter returns the ids of the accounts the caller may view (viewer): a live stream
// passes on a bus event only when its account is one of them (system-wide events, account
// "", are passed by the caller's choice). An API key sees what its owner sees, within its
// role.
func (s *Service) LiveFilter(ctx context.Context, p Principal) (map[string]bool, error) {
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(vs))
	for _, v := range vs {
		if v.Access.AtLeast(domain.AccessView) {
			out[v.ID] = true
		}
	}
	return out, nil
}

// SubscribeLive subscribes to the live bus (PLAN 6.9) with a buffer of the given size: the
// stream filters what it receives with LiveFilter and BusAccount. ok is false when no bus is
// configured (the stream then sends snapshots only). cancel ends the subscription and closes
// the channel. It is a view only: nothing is ever decided on what it carries.
func (s *Service) SubscribeLive(buffer int) (events <-chan ports.Event, cancel func(), ok bool) {
	if s.d.Bus == nil {
		return nil, func() {}, false
	}
	events, cancel = s.d.Bus.Subscribe(buffer)
	return events, cancel, true
}

func (s *Service) liveOrders(ctx context.Context, accountID string, out *LiveSnapshot) error {
	open, err := s.d.Store.Orders().ListOpen(ctx, accountID)
	if err != nil {
		return fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	recent, err := s.d.Store.Orders().List(ctx, accountID, liveRecentOrders)
	if err != nil {
		return fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	tags := s.instrumentTags(ctx)
	view := func(os []domain.Order) []OrderView {
		vs := make([]OrderView, 0, len(os))
		for _, o := range os {
			var in domain.Instrument
			if o.Instrument > 0 {
				in = tags(o.Instrument)
			}
			vs = append(vs, OrderView{Order: o, Symbol: in.Symbol, Market: in.Market})
		}
		return vs
	}
	out.OpenOrders, out.RecentOrders = view(open), view(recent)
	return nil
}

// liveRegimes gives the latest regime of every watchlist share, from its latest review, with
// what the agent may do in it at the account's current thresholds.
func (s *Service) liveRegimes(ctx context.Context, a domain.Account) ([]LiveRegime, error) {
	accountID := a.ID
	wl, err := s.d.Store.Watchlist().List(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	if len(wl) == 0 {
		return []LiveRegime{}, nil
	}
	rv, err := s.d.Store.Reviews().List(ctx, accountID, liveReviews)
	if err != nil {
		return nil, fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	cfg := s.strategyConfig(ctx, a, s.riskLimits(ctx, accountID))
	out := make([]LiveRegime, 0, len(wl))
	for _, w := range wl {
		r := LiveRegime{Instrument: w.Instrument.ID, Symbol: w.Instrument.Symbol, Market: w.Instrument.Market}
		for _, x := range rv { // newest first: the first with probabilities is the latest
			if x.Instrument == w.Instrument.ID && len(x.Probs) > 0 {
				r.Known, r.Regime, r.Probs, r.At = true, mostLikely(x.Probs), x.Probs, x.At
				r.Mood, _ = domain.RegimeTextOf(r.Regime)
				break
			}
		}
		r.Stance, r.MayDo = strategy.MayDo(r.Probs, cfg)
		out = append(out, r)
	}
	return out, nil
}

// mostLikely is the regime with the highest probability (ties: the first by name).
func mostLikely(probs map[domain.Regime]float64) domain.Regime {
	keys := make([]domain.Regime, 0, len(probs))
	for k := range probs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	best := keys[0]
	for _, k := range keys[1:] {
		if probs[k] > probs[best] {
			best = k
		}
	}
	return best
}

// marketSessions is today's session of every supported market at now (a market whose
// calendar answer fails, or without a calendar, is unknown).
func (s *Service) marketSessions(ctx context.Context, now time.Time) map[domain.Market]domain.MarketSession {
	out := make(map[domain.Market]domain.MarketSession, len(domain.Markets))
	for _, m := range domain.Markets {
		if s.d.Calendar == nil {
			out[m] = domain.MarketSession{}
			continue
		}
		open, closeAt, trading, err := s.d.Calendar.Session(m, now)
		if err != nil {
			s.d.Log.WarnContext(ctx, "market calendar", "market", string(m), "err", err)
			out[m] = domain.MarketSession{}
			continue
		}
		out[m] = domain.MarketSession{Known: true, Trading: trading, Open: open, Close: closeAt}
	}
	return out
}

// livePositions marks the portfolio's positions as ours or not and adds each of ours its
// flatten time today.
func (s *Service) livePositions(ctx context.Context, accountID string, pf domain.Portfolio, sessions map[domain.Market]domain.MarketSession, l domain.RiskLimits) ([]LivePosition, error) {
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("app.LiveSnapshot: %w", err)
	}
	byID := make(map[int64]domain.AgentPosition, len(ours))
	for _, ap := range ours {
		byID[ap.PositionID] = ap
	}
	out := make([]LivePosition, 0, len(pf.Positions))
	for _, p := range pf.Positions {
		lp := LivePosition{Position: p}
		if in, _, err := s.d.Store.Instruments().Get(ctx, p.Instrument); err == nil {
			lp.Symbol, lp.Market = in.Symbol, in.Market
		}
		if ap, ok := byID[p.ID]; ok {
			lp.Ours, lp.OpenRequestID = true, ap.OpenRequestID
			if ses := sessions[lp.Market]; ses.Known && ses.Trading {
				lp.FlattenAt = domain.FlattenAt(ses.Close, l).UTC()
			}
		}
		out = append(out, lp)
	}
	slices.SortFunc(out, func(a, b LivePosition) int {
		return cmp.Or(a.OpenedAt.Compare(b.OpenedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
