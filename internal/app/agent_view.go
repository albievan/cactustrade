package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// What the agent panel and the orders page read (PLAN 4.13), plus the manual reconcile and
// the lease release at shutdown (4.14). Nothing here sends anything to eToro.

// agentViewTimeout bounds the one portfolio read of a view (viewPortfolio).
const agentViewTimeout = 10 * time.Second

// AgentControls says which agent actions the caller may take on the account, as the use
// cases themselves decide (OD11): the web UI shows only these. Gated is set for a real-money
// account on which no entry may be made now (S10, PLAN 7.22: not armed, the deployment
// opt-in off, not reconciled, limits over the real ceilings): the agent cannot be started
// there, while pause, stop, the kill switch and force close still work (exits always do).
type AgentControls struct {
	Start, Pause, Stop, Kill, Reset bool
	Gated                           bool
	// ForceClose follows ForceClose's rules (R24): admin, web session, trade access, an eToro
	// account (demo or real-money, PLAN 7.21). ForceCloseBlocked is kept for the API and is
	// always "" now.
	ForceClose        bool
	ForceCloseBlocked string
	// gatedWhy says why a real-money account is Gated ("" when it is not).
	gatedWhy string
}

// startBlocked says why Start is false for a caller who could otherwise start the agent: the
// agent loop is not running (loopWhy), or the real-money account is gated ("" = neither).
func (c AgentControls) startBlocked(loopWhy string, couldStart bool) string {
	switch {
	case !couldStart:
		return ""
	case c.gatedWhy != "":
		return c.gatedWhy
	}
	return loopWhy
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
	Account domain.AccountView
	State   domain.AgentState
	// StateSince is when the agent entered State (zero when unknown).
	StateSince     time.Time
	Reconciliation Reconciliation
	Limits         domain.RiskLimits
	// Risk is the stored risk state (zero Version: none yet). Today is the current risk day;
	// the daily floor applies only when Risk.State.Day == Today.
	Risk  domain.AccountRiskState
	Today domain.RiskDate
	// Equity is eToro's figure, read within the last viewCacheTTL (the view cache, PLAN
	// 6.6a; EquityKnown false with EquityError when it could not be read). The floors and
	// distances are set only when it is known.
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
// which controls the caller may use. It reads the portfolio through the view cache: at most
// one eToro read per account every viewCacheTTL, however many dashboards ask (read-only).
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
		Account: v, State: s.agentState(ctx, v.ID), StateSince: s.agentSince(ctx, v.ID), Reconciliation: s.Reconciled(v.ID),
		Limits: s.riskLimits(ctx, v.ID), Today: domain.RiskDay(now), ResetPhrase: ResetPhrase(v.Name),
		Controls: s.controlsFor(ctx, p, v),
	}
	if why := s.agentLoopBlocked(); why != "" && out.Controls.Start {
		out.Controls.Start = false
	}
	out.StartBlocked = out.Controls.startBlocked(s.agentLoopBlocked(), s.couldStart(p, v))
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

// AgentCard is one account's agent in the list on the Risk limits page (R23): no eToro
// read, so a page with many accounts stays cheap.
type AgentCard struct {
	Account domain.AccountView
	// Current: the caller's current account (the one the limits form edits).
	Current        bool
	State          domain.AgentState
	StateSince     time.Time // as AgentView.StateSince
	Killed         bool
	Reconciliation Reconciliation
	// LimitsEditable follows SetRiskLimits' rules for this caller now; LimitsBlocked says
	// why not ("" when editable).
	LimitsEditable bool
	LimitsBlocked  string
	Controls       AgentControls
	// StartBlocked: as AgentView.StartBlocked.
	StartBlocked           string
	OpenPositions, Pending int
}

// AgentCards lists the agent of every eToro account the caller can view (R23), with the
// controls the caller may use there (the same rules as AgentStatusView) and whether the
// caller may change its limits now. It reads only the database and this process's state.
func (s *Service) AgentCards(ctx context.Context, p Principal) ([]AgentCard, error) {
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return nil, err
	}
	cur := s.currentAccountID(ctx, p, vs)
	why := s.agentLoopBlocked()
	out := make([]AgentCard, 0, len(vs))
	for _, v := range vs {
		if !v.Tradable() {
			continue
		}
		c := AgentCard{
			Account: v, Current: v.ID == cur, State: s.agentState(ctx, v.ID), StateSince: s.agentSince(ctx, v.ID), Reconciliation: s.Reconciled(v.ID),
			LimitsBlocked: s.limitsEditBlocked(ctx, p, v), Controls: s.controlsFor(ctx, p, v),
		}
		c.LimitsEditable = c.LimitsBlocked == ""
		if killed, err := s.isKilled(ctx, v.ID); killed && err == nil {
			c.Killed = true
		}
		if why != "" && c.Controls.Start {
			c.Controls.Start = false
		}
		c.StartBlocked = c.Controls.startBlocked(why, s.couldStart(p, v))
		if ps, err := s.d.Store.AgentPositions().ListOpen(ctx, v.ID); err == nil {
			c.OpenPositions = len(ps)
		}
		if os, err := s.d.Store.Orders().ListOpen(ctx, v.ID); err == nil {
			c.Pending = len(os)
		}
		out = append(out, c)
	}
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
		return s.agentControl(p, v)
	case domain.MissingKillReset:
		return s.killer(p, v) && p.Via == ViaSession && p.Role == domain.RoleAdmin
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

// agentControl: the caller may pause, stop and reconcile the account's agent (agent.control
// and trade access). killer: the caller may use the kill switch (agent.kill and trade access).
func (s *Service) agentControl(p Principal, v domain.AccountView) bool {
	return v.Access.AtLeast(domain.AccessTrade) && p.Can(domain.PermAgentControl)
}

func (s *Service) killer(p Principal, v domain.AccountView) bool {
	return v.Access.AtLeast(domain.AccessTrade) && p.Can(domain.PermKillSwitch)
}

// couldStart: the caller has the rights to start the account's agent (agent.control, trade
// access, a web session), whatever else blocks it.
func (s *Service) couldStart(p Principal, v domain.AccountView) bool {
	return s.agentControl(p, v) && p.Via == ViaSession
}

// controlsFor applies the same rules as the control use cases, without running them. A
// real-money account is Gated while realEntryBlocked says why (S10, PLAN 7.22).
func (s *Service) controlsFor(ctx context.Context, p Principal, v domain.AccountView) AgentControls {
	trade := v.Access.AtLeast(domain.AccessTrade)
	session := p.Via == ViaSession
	var c AgentControls
	switch v.Mode {
	case domain.ModeDemo:
	case domain.ModeReal:
		c.gatedWhy = s.realEntryBlocked(ctx, v.Account)
	default:
		c.gatedWhy = "the account has no trading mode"
	}
	c.Gated = c.gatedWhy != ""
	ctl := s.agentControl(p, v)
	c.Pause, c.Stop = ctl, ctl
	c.Start = s.couldStart(p, v) && !c.Gated
	c.Kill = s.killer(p, v)
	c.Reset = c.Kill && session && p.Role == domain.RoleAdmin
	c.ForceClose = trade && session && p.Can(domain.PermForceClose) && v.Tradable()
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
	pf, _, err := s.viewPortfolio(ctx, v)
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
	// Market is the exchange of the order's instrument ("" when it is not cached).
	Market domain.Market
}

// subject names the order's instrument for people: its symbol, else its id.
func (v OrderView) subject() string {
	if v.Symbol != "" {
		return v.Symbol
	}
	return "instrument " + strconv.FormatInt(int64(v.Instrument), 10)
}

// KindLabel words what the order is, e.g. "Buy GOOG", "Short AAPL", "Cancel request for GOOG
// (buy order 396359836)" or "Close request for GOOG (position 3649965576)". The web shows it
// as it is (no wording in React).
func (v OrderView) KindLabel() string {
	switch v.Kind {
	case domain.OrderCancel:
		target := "order"
		if v.Direction.Valid() {
			target = map[domain.Direction]string{domain.Long: "buy order", domain.Short: "short order"}[v.Direction]
		}
		return fmt.Sprintf("Cancel request for %s (%s %d)", v.subject(), target, v.TargetOrderID)
	case domain.OrderClose:
		return fmt.Sprintf("Close request for %s (position %d)", v.subject(), v.PositionID)
	}
	return verb(v.Direction) + " " + v.subject()
}

// StateLabel words the order's state for people. An open is "filled" or "partially filled";
// a cancel or close that eToro took is stored as filled but means something else: a cancel
// is "accepted" (whether the target was cancelled follows from its own lookup), a close is
// "done" ("partly done" when eToro closed part of it). A refused cancel or close reads
// "refused". Every other state is its name.
func (v OrderView) StateLabel() string {
	if v.Kind == domain.OrderCancel || v.Kind == domain.OrderClose {
		switch v.State {
		case domain.OrderFilled:
			if v.Kind == domain.OrderCancel {
				return "accepted by eToro"
			}
			return "done"
		case domain.OrderPartial:
			return "partly done"
		case domain.OrderRejected:
			return "refused"
		}
	}
	return strings.ReplaceAll(string(v.State), "_", " ")
}

// ReasonLabel words why the order is in its state for people. A cancel's stored reason (e.g.
// "cancel accepted: pending for over 1m0s", or before it was answered just the cause) is said
// in full: what was asked of eToro, why cactusTrade asked, and where the outcome shows. Other
// reasons are shown as stored.
func (v OrderView) ReasonLabel() string {
	if v.Kind != domain.OrderCancel {
		return v.Reason
	}
	cause := strings.TrimPrefix(v.Reason, "cancel accepted: ")
	if rest, ok := strings.CutPrefix(cause, "pending for over "); ok {
		if d, err := time.ParseDuration(rest); err == nil {
			rest = humanDuration(d)
		}
		cause = "the order was still not filled after " + rest + " (cactusTrade cancels an entry order eToro has not filled by then, so its cash is not held up)"
	}
	target := "the order"
	if v.TargetOrderID > 0 {
		target = "order " + strconv.FormatInt(v.TargetOrderID, 10)
	}
	switch v.State {
	case domain.OrderFilled:
		why := ""
		if cause != "" {
			why = " Why it was asked: " + cause + "."
		}
		return "eToro accepted the request to cancel " + target + "." + why +
			" This does not mean it was cancelled: if it had already filled, it stays filled (see that order's own line)."
	case domain.OrderRejected:
		if cause == "" {
			return "eToro refused the request to cancel " + target + "."
		}
		return "eToro refused the request to cancel " + target + ": " + cause
	}
	if cause == "" {
		return ""
	}
	return "Asked to cancel " + target + " because " + cause + "."
}

// humanDuration words a duration as "1 minute", "90 seconds" or "2 minutes".
func humanDuration(d time.Duration) string {
	switch {
	case d%time.Minute == 0 && d >= time.Minute:
		n := int(d / time.Minute)
		if n == 1 {
			return "1 minute"
		}
		return strconv.Itoa(n) + " minutes"
	case d%time.Second == 0:
		return strconv.Itoa(int(d/time.Second)) + " seconds"
	}
	return d.String()
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
	v, err := s.agentAccount(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return nil, err
	}
	orders, err := s.d.Store.Orders().List(ctx, v.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("app.Orders: %w", err)
	}
	out := make([]OrderView, 0, len(orders))
	tags := s.instrumentTags(ctx)
	for _, o := range orders {
		sym, m := "", domain.Market("")
		if o.Instrument > 0 {
			in := tags(o.Instrument)
			sym, m = in.Symbol, in.Market
			if sym == "" {
				sym = "instrument " + strconv.FormatInt(int64(o.Instrument), 10)
			}
		}
		out = append(out, OrderView{Order: o, Symbol: sym, Market: m})
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
