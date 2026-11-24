package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// What the agent panel and the orders page read (PLAN 4.13), plus the manual reconcile and
// the lease release at shutdown (4.14). Nothing here sends anything to eToro.

// agentViewTimeout bounds the one portfolio read of AgentStatusView.
const agentViewTimeout = 10 * time.Second

// AgentControls says which agent actions the caller may take on the account, as the use
// cases themselves decide (OD11): the web UI shows only these. Gated is set for a real-money
// account: nothing may be started there until the phase-7 gate.
type AgentControls struct {
	Start, Pause, Stop, Kill, Reset bool
	Gated                           bool
}

// FlattenTime is when the supervisor closes the positions of one market today.
type FlattenTime struct {
	Market  domain.Market
	Trading bool      // the market trades today
	Closes  time.Time // zero when not trading
	At      time.Time // Closes − FlattenBeforeClose; zero when not trading
	Note    string    // a calendar problem (the market is then treated as closed)
}

// BlockedInstrument is an instrument the agent may not enter on the account.
type BlockedInstrument struct {
	domain.InstrumentBlock
	Symbol string
}

// LeaseStatus says who may send orders for the account (D22).
type LeaseStatus struct {
	// Mine: this process holds it; Other: another process holds an unexpired one.
	Mine, Other bool
	ExpiresAt   time.Time
}

// AgentView is an account's agent as the dashboard shows it.
type AgentView struct {
	Account        domain.AccountView
	State          domain.AgentState
	Reconciliation Reconciliation
	Limits         domain.RiskLimits
	// Risk is the stored risk state (zero Version: none yet). Today is the current risk day;
	// the daily floor applies only when Risk.State.Day == Today.
	Risk  domain.AccountRiskState
	Today domain.RiskDate
	// Equity is eToro's figure read now (EquityKnown false with EquityError when it could
	// not be read). The floors and distances are set only when it is known.
	Equity          domain.Cents
	EquityKnown     bool
	EquityError     string
	DailyFloor      domain.Cents
	DailyFloorKnown bool
	ToDailyFloor    domain.Cents
	DrawdownFloor   domain.Cents
	ToDrawdownFloor domain.Cents
	Blocked         []BlockedInstrument
	Flatten         []FlattenTime
	Lease           LeaseStatus
	Controls        AgentControls
	// StartBlocked says why Controls.Start is false although the caller could otherwise
	// start the agent: the agent loop does not run in this process (PLAN 6.13). "" = not
	// blocked.
	StartBlocked           string
	ResetPhrase            string
	OpenPositions, Pending int
	// Missing lists what must be set up before the agent can start (R22), in the order
	// StartAgent checks it, plus an empty watchlist and an unusable LLM reviewer. Empty while
	// the agent runs and on a real-money account (nothing there may start before phase 7).
	Missing []MissingItem
}

// MissingItem is one piece of missing setup and whether the caller may fix it.
type MissingItem struct {
	What   domain.Missing
	CanFix bool
}

// AgentStatusView returns the agent of an account the caller can view (accountID "" = the
// current account): its state, reconciliation, kill switch, risk-day figures and distances
// to the daily floor and the drawdown limit, blocked instruments, today's flatten times and
// which controls the caller may use. It reads the portfolio once (read-only).
func (s *Service) AgentStatusView(ctx context.Context, p Principal, accountID string) (AgentView, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return AgentView{}, err
	}
	v, err := s.account(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return AgentView{}, err
	}
	now := s.d.Clock.Now()
	out := AgentView{
		Account: v, State: s.agentState(ctx, v.ID), Reconciliation: s.Reconciled(v.ID),
		Limits: s.riskLimits(ctx, v.ID), Today: domain.RiskDay(now), ResetPhrase: ResetPhrase(v.Name),
		Controls: s.controlsFor(p, v),
	}
	if why := s.agentLoopBlocked(); why != "" && out.Controls.Start {
		out.Controls.Start, out.StartBlocked = false, why
	}
	switch rs, err := s.d.Store.RiskStates().Get(ctx, v.ID); {
	case err == nil:
		out.Risk = rs
	case !errors.Is(err, domain.ErrNotFound):
		return AgentView{}, fmt.Errorf("app.AgentStatusView: %w", err)
	default:
		out.Risk = domain.AccountRiskState{AccountID: v.ID}
	}
	s.viewEquity(ctx, v, &out)
	if err := s.viewBlocks(ctx, v.ID, &out); err != nil {
		return AgentView{}, err
	}
	out.Flatten = s.flattenTimes(ctx, now, out.Limits)
	if l, err := s.d.Store.AgentLeases().Get(ctx, v.ID); err == nil && now.Before(l.ExpiresAt) {
		out.Lease = LeaseStatus{Mine: l.Holder == s.leaseHolder(), Other: l.Holder != s.leaseHolder(), ExpiresAt: l.ExpiresAt}
	}
	if ps, err := s.d.Store.AgentPositions().ListOpen(ctx, v.ID); err == nil {
		out.OpenPositions = len(ps)
	}
	if pending, err := s.d.Store.Orders().ListOpen(ctx, v.ID); err == nil {
		out.Pending = len(pending)
	}
	out.Missing = s.missingFor(ctx, p, v, out)
	return out, nil
}

// missingFor applies startable's rules without running StartAgent (R22), and adds an empty
// watchlist and a switched-on reviewer that cannot review. A store error leaves the item out:
// the list is guidance, StartAgent still checks everything.
func (s *Service) missingFor(ctx context.Context, p Principal, v domain.AccountView, out AgentView) []MissingItem {
	if out.State == domain.AgentRunning || out.Controls.Gated {
		return []MissingItem{}
	}
	var ms []domain.Missing
	if !s.hasKeys(ctx, v.Account) {
		ms = append(ms, domain.MissingAccountKeys)
	}
	if killed, err := s.isKilled(ctx, v.ID); killed && err == nil {
		ms = append(ms, domain.MissingKillReset)
	}
	if out.Limits.TradingBudget <= 0 {
		ms = append(ms, domain.MissingTradingBudget)
	}
	if !out.Reconciliation.OK {
		ms = append(ms, domain.MissingReconcile)
	}
	if items, err := s.d.Store.Watchlist().List(ctx, v.ID); err == nil && len(items) == 0 {
		ms = append(ms, domain.MissingWatchlist)
	}
	if !s.reviewerUsable(ctx) {
		ms = append(ms, domain.MissingLLMProfile)
	}
	items := make([]MissingItem, 0, len(ms))
	for _, m := range ms {
		items = append(items, MissingItem{What: m, CanFix: s.canFix(p, v, m)})
	}
	return items
}

// reviewerUsable reports whether reviewing is off, or on with a valid default profile that
// has a key when its provider needs one. An unreadable setup counts as unusable.
func (s *Service) reviewerUsable(ctx context.Context) bool {
	st, ps, err := s.loadLLM(ctx)
	if err != nil {
		return false
	}
	if !st.Enabled {
		return true
	}
	i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == st.Active })
	if i < 0 || ps[i].Validate() != nil {
		return false
	}
	if pr, _ := domain.LLMProviderByID(ps[i].Provider); pr.KeyRequired {
		k, err := s.llmKey(ctx, ps[i].ID)
		return err == nil && k != ""
	}
	return true
}

// canFix says whether p may fix what on account v, by the rules of the use case that fixes
// it (R22): SetRiskLimits and SetAccountCredentials (owner, web session), ReconcileAccount
// and ResetKill (as controlsFor), AddToWatchlist (trade access), the LLM reviewer and e-mail
// setup (settings, web session), CreateAccount (agent.control). A deployment setting such as
// CACTUSTRADE_PUBLIC_URL is never fixed in the app.
func (s *Service) canFix(p Principal, v domain.AccountView, what domain.Missing) bool {
	session := p.Via == ViaSession
	switch what {
	case domain.MissingTradingBudget, domain.MissingAccountKeys:
		return session && v.Access == domain.AccessOwner
	case domain.MissingReconcile:
		return s.controlsFor(p, v).Pause
	case domain.MissingKillReset:
		return s.controlsFor(p, v).Reset
	case domain.MissingWatchlist:
		return v.Access.AtLeast(domain.AccessTrade)
	case domain.MissingLLMProfile, domain.MissingEmailServer:
		return session && p.Can(domain.PermManageSettings)
	case domain.MissingAccount:
		return p.Can(domain.PermAgentControl)
	default:
		return false
	}
}

// fixable sets CanFix on the *domain.MissingError in err (if any) for p on account v and
// returns err.
func (s *Service) fixable(p Principal, v domain.AccountView, err error) error {
	if me, ok := errors.AsType[*domain.MissingError](err); ok {
		me.CanFix = s.canFix(p, v, me.What)
	}
	return err
}

// controlsFor applies the same rules as the control use cases, without running them.
func (s *Service) controlsFor(p Principal, v domain.AccountView) AgentControls {
	trade := v.Access.AtLeast(domain.AccessTrade)
	session := p.Via == ViaSession
	c := AgentControls{Gated: v.Mode != domain.ModeDemo}
	ctl := trade && p.Can(domain.PermAgentControl)
	c.Pause, c.Stop = ctl, ctl
	c.Start = ctl && session && !c.Gated
	c.Kill = trade && p.Can(domain.PermKillSwitch)
	c.Reset = c.Kill && session && p.Role == domain.RoleAdmin
	return c
}

// viewEquity reads the equity now and works out the floors and distances.
func (s *Service) viewEquity(ctx context.Context, v domain.AccountView, out *AgentView) {
	switch {
	case s.d.Broker == nil:
		out.EquityError = "the eToro client is not configured"
		return
	case !s.hasKeys(ctx, v.Account):
		out.EquityError = "the account's eToro keys are not set"
		return
	}
	cctx, cancel := context.WithTimeout(ctx, agentViewTimeout)
	defer cancel()
	pf, err := s.d.Broker.Portfolio(brokerCtx(cctx, v))
	if err != nil {
		out.EquityError = "reading the portfolio failed: " + err.Error()
		return
	}
	out.Equity, out.EquityKnown = pf.Equity(), true
	st := out.Risk.State
	out.DrawdownFloor = domain.DrawdownFloor(out.Equity, st, out.Limits)
	out.ToDrawdownFloor = out.Equity - out.DrawdownFloor
	if st.Day == out.Today && st.StartOfDayEquity > 0 {
		out.DailyFloor, out.DailyFloorKnown = domain.DailyFloor(st, out.Limits), true
		out.ToDailyFloor = out.Equity - out.DailyFloor
	}
}

func (s *Service) viewBlocks(ctx context.Context, accountID string, out *AgentView) error {
	bs, err := s.d.Store.InstrumentBlocks().List(ctx, accountID)
	if err != nil {
		return fmt.Errorf("app.AgentStatusView: %w", err)
	}
	for _, b := range bs {
		out.Blocked = append(out.Blocked, BlockedInstrument{InstrumentBlock: b, Symbol: s.instrumentLabel(ctx, b.Instrument)})
	}
	return nil
}

// flattenTimes gives today's session close and flatten time of every supported market.
func (s *Service) flattenTimes(ctx context.Context, now time.Time, l domain.RiskLimits) []FlattenTime {
	out := make([]FlattenTime, 0, len(domain.Markets))
	for _, m := range domain.Markets {
		ft := FlattenTime{Market: m}
		if s.d.Calendar == nil {
			ft.Note = "no market calendar: treated as closed"
			out = append(out, ft)
			continue
		}
		_, closeAt, trading, err := s.d.Calendar.Session(m, now)
		if err != nil {
			s.d.Log.WarnContext(ctx, "market calendar", "market", string(m), "err", err)
			ft.Note = err.Error()
		}
		if trading && err == nil {
			ft.Trading, ft.Closes, ft.At = true, closeAt.UTC(), domain.FlattenAt(closeAt, l).UTC()
		}
		out = append(out, ft)
	}
	return out
}

// OrderView is one of our orders with its instrument's symbol.
type OrderView struct {
	domain.Order
	Symbol string
}

// Orders lists an account's orders, newest first (view access; accountID "" = the current
// account). limit 0 is 100; it must be 1–500.
func (s *Service) Orders(ctx context.Context, p Principal, accountID string, limit int) ([]OrderView, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, domain.Invalid("limit", "must be 1–500")
	}
	v, err := s.account(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return nil, err
	}
	orders, err := s.d.Store.Orders().List(ctx, v.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("app.Orders: %w", err)
	}
	out := make([]OrderView, 0, len(orders))
	labels := map[domain.InstrumentID]string{}
	for _, o := range orders {
		sym, ok := labels[o.Instrument]
		if !ok && o.Instrument > 0 {
			sym = s.instrumentLabel(ctx, o.Instrument)
			labels[o.Instrument] = sym
		}
		out = append(out, OrderView{Order: o, Symbol: sym})
	}
	return out, nil
}

// ReconcileAccount reconciles an account with eToro now (agent.control, trade access; a web
// session or an API key). It only reads eToro. A pass that runs but finds a problem is a
// result with OK false, not an error. While the supervisor is working on the account it is
// a conflict (try again in a few seconds).
func (s *Service) ReconcileAccount(ctx context.Context, p Principal, accountID string) (Reconciliation, error) {
	v, err := s.controlled(ctx, p, accountID, domain.PermAgentControl, false, "reconciling")
	if err != nil {
		return Reconciliation{}, err
	}
	if err := s.needBroker(); err != nil {
		return Reconciliation{}, err
	}
	if !s.hasKeys(ctx, v.Account) {
		return Reconciliation{}, s.fixable(p, v, domain.MissingSetup(domain.MissingAccountKeys, msgNoKeys,
			fmt.Errorf("%w: %w", domain.ErrCredentialsMissing, domain.ErrUnavailable)))
	}
	if !s.rt.claim(v.ID) {
		return Reconciliation{}, fmt.Errorf("the supervisor is working on %s; try again in a few seconds: %w", v.Name, domain.ErrConflict)
	}
	defer s.rt.release(v.ID)
	r, err := s.Reconcile(ctx, v.ID)
	if err != nil && r.At.IsZero() {
		return Reconciliation{}, err
	}
	s.audit(ctx, p, "agent.reconcile", v.ID, fmt.Sprintf("ok=%v %s", r.OK, r.Reason))
	return r, nil
}

// ReleaseLeases gives up every account lease this process holds (graceful shutdown, 4.14),
// so another process can take over at once instead of after domain.LeaseTTL. Positions are
// left as they are (OD6).
func (s *Service) ReleaseLeases(ctx context.Context) error {
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		return fmt.Errorf("app.ReleaseLeases: %w", err)
	}
	var errs []error
	for _, a := range as {
		if !a.Tradable() {
			continue
		}
		if err := s.d.Store.AgentLeases().Release(ctx, a.ID, s.leaseHolder()); err != nil && !errors.Is(err, domain.ErrNotFound) {
			errs = append(errs, fmt.Errorf("account %s: %w", a.ID, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("app.ReleaseLeases: %w", errors.Join(errs...))
	}
	return nil
}
