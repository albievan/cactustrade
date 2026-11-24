package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// What the platform does by itself at each autonomy level (PLAN R28, domain.AutonomyPolicy):
// how often the scanner runs, the themes of the shares it adds, taking stale automatic shares
// off the watchlist, and full autonomy's start at the market open. None of it places an
// order: entries still go through the agent tick and the risk engine.

// ScanTick is how often the scanner loop wakes; each account is scanned when its autonomy's
// interval (domain.AutonomyPolicy.ScanEvery) has passed since its last scan.
const ScanTick = domain.ScanEveryOften

// scanSlack lets a scan due "almost now" run on this tick rather than the next.
const scanSlack = time.Minute

// staleLookBack bounds how far back trading days are counted for a stale share.
const staleLookBack = 60 * 24 * time.Hour

func watchSignalsKey(accountID string) string { return "watchlist.signals." + accountID }
func autoStartKey(accountID string) string    { return "agent.autostart." + accountID }

// watchSignals is when the scanner last found a signal for each watchlist share of an
// account (missing or unreadable: none known).
func (s *Service) watchSignals(ctx context.Context, accountID string) map[domain.InstrumentID]time.Time {
	out := map[domain.InstrumentID]time.Time{}
	v, err := s.d.Store.Settings().Get(ctx, watchSignalsKey(accountID))
	if err != nil {
		return out
	}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		s.d.Log.WarnContext(ctx, "the watchlist's signal times are unreadable; starting afresh", "account", accountID, "err", err)
		return map[domain.InstrumentID]time.Time{}
	}
	return out
}

func (s *Service) storeWatchSignals(ctx context.Context, accountID string, m map[domain.InstrumentID]time.Time) {
	b, err := json.Marshal(m)
	if err == nil {
		err = s.d.Store.Settings().Set(ctx, watchSignalsKey(accountID), string(b), s.d.Clock.Now().UTC())
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "storing the watchlist's signal times failed", "account", accountID, "err", err)
	}
}

// scanDue reports whether account a is due for a background scan at now, by its autonomy's
// interval, and if so remembers now as its last scan.
func (s *Service) scanDue(ctx context.Context, a domain.Account, now time.Time) bool {
	every := s.policyOf(ctx, a).ScanEvery
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	if last, ok := s.rt.lastScan[a.ID]; ok && now.Sub(last) < every-scanSlack {
		return false
	}
	s.rt.lastScan[a.ID] = now
	return true
}

// autoAssignThemes gives each share the scanner just added the closest theme of the account
// (R27's deterministic scoring; high and full autonomy). A share that already has a theme, or
// matches none, is left as it is. Failures are logged: nothing here is needed for trading.
func (s *Service) autoAssignThemes(ctx context.Context, v domain.AccountView, ids []domain.InstrumentID) {
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil || len(themes) == 0 {
		return
	}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "auto-assign: reading the watchlist failed", "account", v.ID, "err", err)
		return
	}
	themeOf := shareThemes(themes)
	var open []domain.WatchItem
	for _, it := range items {
		if _, has := themeOf[it.Instrument.ID]; !has && slices.Contains(ids, it.Instrument.ID) {
			open = append(open, it)
		}
	}
	if len(open) == 0 {
		return
	}
	industryOf, names, _ := s.shareIndustries(ctx, v, themes, open)
	changed := map[string]bool{}
	var done []string
	for _, it := range open {
		f := domain.ShareFacts{Symbol: it.Instrument.Symbol, Name: it.Instrument.Name, IndustryID: industryOf[it.Instrument.ID]}
		f.IndustryName = names[f.IndustryID]
		t, _, ok := domain.BestTheme(f, themes)
		if !ok {
			continue
		}
		for id := range moveShare(themes, it.Instrument.ID, t.ID) {
			changed[id] = true
		}
		done = append(done, it.Instrument.Symbol+" → "+t.Name)
	}
	if len(done) == 0 {
		return
	}
	if _, err := s.storeThemes(ctx, themes, changed); err != nil {
		s.d.Log.WarnContext(ctx, "auto-assign: storing the themes failed", "account", v.ID, "err", err)
		return
	}
	s.audit(ctx, Principal{Username: "autonomy"}, "theme.auto_assign", v.ID, strings.Join(done, ", "))
	s.emitFor(ctx, v.ID, "autonomy", "watchlist.themes_assigned", domain.SeverityInfo,
		"Themes assigned on "+v.Name, strings.Join(done, ", "))
}

// pruneStale takes off the watchlist every share the scanner or the research added that has
// shown no signal for domain.StaleWatchTradingDays trading days of its market (high and full
// autonomy). Never a share a person added, never one with a position or an order of ours,
// and nothing without a market calendar (fail closed: a share stays). It returns the
// symbols removed.
func (s *Service) pruneStale(ctx context.Context, v domain.AccountView, signals map[domain.InstrumentID]time.Time) []string {
	if s.d.Calendar == nil {
		return nil
	}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return nil
	}
	busy, ok := s.busyInstruments(ctx, v.ID)
	if !ok {
		return nil
	}
	now := s.d.Clock.Now()
	var removed []string
	for _, it := range items {
		id := it.Instrument.ID
		if !domain.IsAutoAdded(it.AddedBy) || busy[id] {
			continue
		}
		since := it.AddedAt
		if t := signals[id]; t.After(since) {
			since = t
		}
		n := s.tradingDaysBetween(it.Instrument.Market, since, now)
		if n < domain.StaleWatchTradingDays {
			continue
		}
		if err := s.d.Store.Watchlist().Remove(ctx, v.ID, id); err != nil {
			s.d.Log.WarnContext(ctx, "removing a stale share failed", "account", v.ID, "instrument", id, "err", err)
			continue
		}
		delete(signals, id)
		removed = append(removed, it.Instrument.Symbol)
		why := fmt.Sprintf("no signal for %d trading days (added by the %s)", n, it.AddedBy)
		s.audit(ctx, Principal{Username: "autonomy"}, "watchlist.auto_remove", v.ID+"/"+strconv.FormatInt(int64(id), 10), it.Instrument.Symbol+": "+why)
		s.emitFor(ctx, v.ID, "autonomy", "watchlist.auto_removed", domain.SeverityInfo,
			it.Instrument.Symbol+" taken off the watchlist of "+v.Name, why)
	}
	if len(removed) > 0 {
		s.storeWatchSignals(ctx, v.ID, signals)
	}
	return removed
}

// busyInstruments are the instruments with a position or an unfinished order of ours. ok is
// false when they cannot be read.
func (s *Service) busyInstruments(ctx context.Context, accountID string) (map[domain.InstrumentID]bool, bool) {
	out := map[domain.InstrumentID]bool{}
	ps, err := s.d.Store.AgentPositions().ListOpen(ctx, accountID)
	if err != nil {
		return nil, false
	}
	for _, p := range ps {
		out[p.Instrument] = true
	}
	orders, err := s.d.Store.Orders().ListOpen(ctx, accountID)
	if err != nil {
		return nil, false
	}
	for _, o := range orders {
		out[o.Instrument] = true
	}
	return out, true
}

// tradingDaysBetween counts the complete sessions of market m that opened after since and
// closed by now (at most staleLookBack back). A calendar error counts nothing (fail closed:
// the share stays).
func (s *Service) tradingDaysBetween(m domain.Market, since, now time.Time) int {
	from := since
	if lb := now.Add(-staleLookBack); from.Before(lb) {
		from = lb
	}
	seen := map[int64]bool{}
	// Half-day steps: every calendar day is visited at least once, across DST changes too.
	for t := from; !t.After(now); t = t.Add(12 * time.Hour) {
		open, closeAt, trading, err := s.d.Calendar.Session(m, t)
		if err != nil {
			return 0
		}
		if trading && open.After(since) && !closeAt.After(now) {
			seen[open.Unix()] = true
		}
	}
	return len(seen)
}

// autoStartAll starts, at the first market open of a trading day, the stopped agent of every
// eToro demo account with full autonomy (R28). Each account starts at most once per risk
// day, so an agent the owner stops stays stopped until the next trading day. It never starts
// a killed, paused or real-money agent (OD26), and starts only what StartAgent would start.
func (s *Service) autoStartAll(ctx context.Context, as []domain.Account) {
	if s.d.Calendar == nil || !s.anyMarketOpen() {
		return
	}
	for _, a := range as {
		if !a.Tradable() || a.Mode != domain.ModeDemo || !s.policyOf(ctx, a).AutoStart {
			continue
		}
		s.autoStart(ports.WithAccount(ctx, a), a)
	}
}

// anyMarketOpen reports whether any supported market is in session now.
func (s *Service) anyMarketOpen() bool {
	return slices.ContainsFunc(domain.Markets, s.marketOpen)
}

// autoStart starts account a's agent if it is stopped and has not been started by autonomy
// on today's risk day. It reports whether it started it.
func (s *Service) autoStart(ctx context.Context, a domain.Account) bool {
	day := string(domain.RiskDay(s.d.Clock.Now()))
	if v, err := s.d.Store.Settings().Get(ctx, autoStartKey(a.ID)); err == nil && v == day {
		return false
	}
	if s.agentState(ctx, a.ID) != domain.AgentStopped {
		return false
	}
	locks := s.rt.agent(a.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	// Again under the control lock: a kill, a start or a change of limits either happened
	// before (seen here) or waits for this start.
	// An unreadable state is never "stopped" here (fail closed): only a stored stop or a
	// state never stored.
	switch raw, err := s.d.Store.Settings().Get(ctx, agentStateKey(a.ID)); {
	case errors.Is(err, domain.ErrNotFound):
	case err != nil || domain.AgentState(raw) != domain.AgentStopped:
		return false
	}
	if s.agentLoopBlocked() != "" {
		return false
	}
	if err := s.startable(ctx, a); err != nil {
		s.d.Log.InfoContext(ctx, "full autonomy: the agent cannot start itself yet", "account", a.ID, "reason", err.Error())
		return false
	}
	if err := s.d.Store.Settings().Set(ctx, autoStartKey(a.ID), day, s.d.Clock.Now().UTC()); err != nil {
		s.d.Log.WarnContext(ctx, "full autonomy: storing the start day failed; not starting", "account", a.ID, "err", err)
		return false
	}
	if err := s.setAgentState(ctx, a.ID, domain.AgentRunning); err != nil {
		s.d.Log.WarnContext(ctx, "full autonomy: starting the agent failed", "account", a.ID, "err", err)
		return false
	}
	l := s.riskLimits(ctx, a.ID)
	s.audit(ctx, Principal{Username: "autonomy"}, "agent.auto_start", a.ID, "full autonomy, trading budget $"+l.TradingBudget.String())
	s.emitFor(ctx, a.ID, "autonomy", "agent.auto_started", domain.SeverityInfo, "Agent started itself on "+a.Name,
		"full autonomy: first market open of the trading day; trading budget $"+l.TradingBudget.String())
	return true
}

// CanAddToWatchlist reports whether p may add shares to the current account's watchlist
// (trade access): the review board offers to accept the scanner's proposals then (R28).
func (s *Service) CanAddToWatchlist(ctx context.Context, p Principal) bool {
	_, err := s.account(ctx, p, "", domain.AccessTrade)
	return err == nil
}
