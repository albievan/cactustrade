package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/strategy"
)

// Scanner settings.
const (
	scanMax             = 40              // stocks reviewed per account per scan
	scanInterval        = 5 * time.Minute // candle interval the scanner reads
	scanBars            = 400             // ~5 NYSE sessions of 5-minute bars
	watchlistCap        = 30              // the scanner never grows a watchlist past this
	eligibilityMaxAge   = 24 * time.Hour
	reviewRetention     = 14 * 24 * time.Hour
	scannerNominalCents = domain.Cents(1_000_000) // sizing only shapes the checks; nothing is traded here
)

func scannerKey(accountID string) string { return "scanner.enabled." + accountID }

// ScannerEnabled reports whether the scanner runs for an account (on unless switched off).
func (s *Service) ScannerEnabled(ctx context.Context, accountID string) bool {
	v, err := s.d.Store.Settings().Get(ctx, scannerKey(accountID))
	return err != nil || v != "off"
}

// SetScanner switches the scanner on or off for the current account (trade access).
func (s *Service) SetScanner(ctx context.Context, p Principal, on bool) error {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return err
	}
	val := "on"
	if !on {
		val = "off"
	}
	if err := s.d.Store.Settings().Set(ctx, scannerKey(v.ID), val, s.d.Clock.Now().UTC()); err != nil {
		return fmt.Errorf("app.SetScanner: %w", err)
	}
	s.audit(ctx, p, "scanner."+val, v.ID, "")
	return nil
}

// universeItem is a stock to review and why it is in the universe.
type universeItem struct {
	in    domain.Instrument
	theme string
	llm   string // the theme's reviewer profile ("" = the default)
}

// universe is the stocks the scanner reviews for an account: the watchlist, then the enabled
// themes' picked shares, search words and industry, at most scanMax, supported shares only.
func (s *Service) universe(ctx context.Context, v domain.AccountView) ([]universeItem, error) {
	bctx := brokerCtx(ctx, v)
	var out []universeItem
	seen := map[domain.InstrumentID]bool{}
	add := func(in domain.Instrument, theme, llm string) {
		if len(out) >= scanMax || seen[in.ID] || in.ID <= 0 || in.Market == "" || !domain.IsShare(in.Type) {
			return
		}
		seen[in.ID] = true
		out = append(out, universeItem{in: in, theme: theme, llm: llm})
	}
	wl, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return nil, err
	}
	for _, w := range wl {
		add(w.Instrument, "watchlist", "")
	}
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range themes {
		if !t.Enabled {
			continue
		}
		for _, id := range t.Instruments {
			if in, _, err := s.d.Store.Instruments().Get(ctx, id); err == nil {
				add(in, t.Name, t.LLMProfileID)
			}
		}
		for _, kw := range t.Keywords {
			res, err := s.d.Market.Search(bctx, kw)
			if err != nil {
				s.d.Log.WarnContext(ctx, "scanner search failed", "account", v.ID, "theme", t.Name, "err", err)
				continue
			}
			for _, in := range res {
				add(in, t.Name, t.LLMProfileID)
			}
		}
		if t.IndustryID > 0 && s.d.Research != nil {
			res, err := s.d.Research.Screen(bctx, t.IndustryID, 30)
			if err != nil {
				s.d.Log.WarnContext(ctx, "scanner screen failed", "account", v.ID, "theme", t.Name, "err", err)
				continue
			}
			for _, in := range res {
				add(in, t.Name, t.LLMProfileID)
			}
		}
	}
	return out, nil
}

// refreshEligibility asks eToro about every stock whose cached answer is missing or older
// than a day, and caches the answers.
func (s *Service) refreshEligibility(ctx context.Context, v domain.AccountView, items []universeItem) map[domain.InstrumentID]domain.Eligibility {
	now := s.d.Clock.Now().UTC()
	out := map[domain.InstrumentID]domain.Eligibility{}
	var stale []domain.InstrumentID
	for _, it := range items {
		_, el, err := s.d.Store.Instruments().Get(ctx, it.in.ID)
		if err == nil && !el.CheckedAt.IsZero() && now.Sub(el.CheckedAt) < eligibilityMaxAge {
			out[it.in.ID] = el
			continue
		}
		stale = append(stale, it.in.ID)
	}
	if len(stale) == 0 {
		return out
	}
	els, err := s.d.Broker.Eligibility(brokerCtx(ctx, v), stale)
	if err != nil {
		s.d.Log.WarnContext(ctx, "scanner eligibility failed", "account", v.ID, "err", err)
		return out // unknown eligibility = not eligible (fail closed)
	}
	for _, el := range els {
		if el.CheckedAt.IsZero() {
			el.CheckedAt = now
		}
		out[el.Instrument] = el
		i := slices.IndexFunc(items, func(it universeItem) bool { return it.in.ID == el.Instrument })
		if i >= 0 {
			if err := s.d.Store.Instruments().Upsert(ctx, items[i].in, el, now); err != nil {
				s.d.Log.WarnContext(ctx, "caching eligibility failed", "err", err)
			}
		}
	}
	return out
}

// ScanResult summarises one scan.
type ScanResult struct {
	Reviewed int
	Added    []string // symbols added to the watchlist
}

// scan reviews an account's universe (no principal: called by the runner and by ScanNow).
func (s *Service) scan(ctx context.Context, v domain.AccountView) (ScanResult, error) {
	if err := s.needBroker(); err != nil {
		return ScanResult{}, err
	}
	items, err := s.universe(ctx, v)
	if err != nil {
		return ScanResult{}, fmt.Errorf("app.scan: %w", err)
	}
	elig := s.refreshEligibility(ctx, v, items)
	limits := s.riskLimits(ctx, v.ID)
	sizing := scannerNominalCents
	if limits.TradingBudget > 0 {
		sizing = limits.TradingBudget
	}
	wl, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return ScanResult{}, fmt.Errorf("app.scan: %w", err)
	}
	onList := map[domain.InstrumentID]bool{}
	for _, w := range wl {
		onList[w.Instrument.ID] = true
	}
	var res ScanResult
	bctx := brokerCtx(ctx, v)
	for _, it := range items {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if !s.marketOpen(it.in.Market) {
			continue // reviewed when its own market trades, not on stale overnight bars
		}
		r := domain.Review{AccountID: v.ID, Instrument: it.in.ID, Symbol: it.in.Symbol, Theme: it.theme, At: s.d.Clock.Now().UTC()}
		bars, err := s.d.Market.Candles(bctx, it.in.ID, scanInterval, scanBars)
		if err != nil {
			r.Outcome, r.Summary = domain.ReviewError, "no price history: "+err.Error()
			s.saveReview(ctx, r)
			res.Reviewed++
			continue
		}
		d := s.evaluate(it.in.ID, bars, sizing, limits.MaxShort > 0)
		r.Probs = d.Probs
		for _, c := range d.Checks {
			r.Checks = append(r.Checks, domain.ReviewCheck{Rule: c.Rule, Pass: c.Pass, Detail: c.Detail})
		}
		if s.d.Calendar != nil {
			r.Checks = append(r.Checks, domain.ReviewCheck{Rule: string(it.in.Market) + " is in session", Pass: true})
		}
		if d.Candidate == nil {
			r.Outcome, r.Summary = domain.ReviewNoSignal, d.Reason
		} else {
			c := d.Candidate
			r.Direction = c.Direction
			el := elig[it.in.ID]
			ok, why := el.Allows(c.Direction)
			r.Checks = append(r.Checks, domain.ReviewCheck{Rule: "eToro allows a " + string(c.Direction) + " at leverage 1", Pass: ok, Detail: why})
			switch {
			case !ok:
				r.Outcome, r.Summary = domain.ReviewSkipped, c.Playbook+" passed, but eToro does not allow it: "+cmpOr(why, "not checked")
			case onList[it.in.ID]:
				r.Outcome, r.Summary = domain.ReviewCandidate, c.Playbook+": "+c.Reason
			case len(onList) >= watchlistCap:
				r.Outcome, r.Summary = domain.ReviewSkipped, c.Playbook+" passed, but the watchlist is full ("+strconv.Itoa(watchlistCap)+")"
			default:
				if rc, ok := s.llmCheck(ctx, it.llm, *c, bars); ok {
					r.Checks = append(r.Checks, rc)
					if !rc.Pass {
						r.Outcome, r.Summary = domain.ReviewSkipped, c.Playbook+" passed, but the LLM reviewer said no: "+rc.Detail
						break
					}
				}
				if err := s.d.Store.Watchlist().Add(ctx, v.ID, it.in.ID, "scanner", r.At); err != nil && !errors.Is(err, domain.ErrConflict) {
					r.Outcome, r.Summary = domain.ReviewError, "adding to the watchlist failed: "+err.Error()
					break
				}
				onList[it.in.ID] = true
				r.Outcome, r.Summary = domain.ReviewAdded, c.Playbook+": "+c.Reason
				res.Added = append(res.Added, it.in.Symbol)
				s.emitFor(ctx, v.ID, "scanner", "scanner.added", domain.SeverityInfo,
					it.in.Symbol+" added to the watchlist of "+v.Name+" by the scanner",
					string(c.Direction)+" · "+c.Playbook+" · theme "+it.theme+": "+c.Reason)
			}
		}
		s.saveReview(ctx, r)
		res.Reviewed++
	}
	return res, nil
}

// reviewerFor picks the reviewer for a theme: its own profile when set and present, else the
// default (active) one. ok is false when reviewing is switched off.
func (s *Service) reviewerFor(ctx context.Context, profileID string) (domain.LLMProfile, string, bool, error) {
	st, ps, err := s.loadLLM(ctx)
	if err != nil {
		return domain.LLMProfile{}, "", true, err
	}
	if !st.Enabled {
		return domain.LLMProfile{}, "", false, nil
	}
	i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == profileID && profileID != "" })
	if i < 0 {
		i = slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == st.Active })
	}
	if i < 0 {
		return domain.LLMProfile{}, "", true, errors.New("no reviewer profile is chosen")
	}
	key, err := s.llmKey(ctx, ps[i].ID)
	return ps[i], key, true, err
}

// llmCheck asks the theme's (or the default) reviewer about a candidate before the scanner
// adds it to the watchlist. ok is false when reviewing is off. Any failure is a "no"
// (fail closed); the reviewer can only agree or refuse, never add anything.
func (s *Service) llmCheck(ctx context.Context, profileID string, c domain.Candidate, bars []domain.Candle) (domain.ReviewCheck, bool) {
	if s.d.LLM == nil {
		return domain.ReviewCheck{}, false
	}
	pr, key, on, err := s.reviewerFor(ctx, profileID)
	if !on {
		return domain.ReviewCheck{}, false
	}
	rule := "LLM reviewer agrees"
	if err != nil {
		return domain.ReviewCheck{Rule: rule, Detail: "unusable: " + err.Error()}, true
	}
	rule += " (" + pr.Name + ")"
	if err := pr.Validate(); err != nil {
		return domain.ReviewCheck{Rule: rule, Detail: "setup: " + err.Error()}, true
	}
	recent := bars
	if len(recent) > 30 {
		recent = recent[len(recent)-30:]
	}
	timeout := time.Duration(pr.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// the call's trace is logged from step 5.5 on (purpose "scanner")
	v, _, err := s.d.LLM.Review(rctx, pr.LLMConfig, key, ports.ReviewInput{Candidate: c, RecentCandles: recent})
	if err != nil {
		return domain.ReviewCheck{Rule: rule, Detail: "no usable answer: " + err.Error()}, true
	}
	v = domain.ClampVerdict(v)
	return domain.ReviewCheck{Rule: rule, Pass: v.Kind != domain.VerdictReject, Detail: string(v.Kind) + ": " + v.Reason}, true
}

// evaluate runs the strategy on one stock's bars (tests replace it with evalOverride).
func (s *Service) evaluate(id domain.InstrumentID, bars []domain.Candle, sizing domain.Cents, shorts bool) strategy.Decision {
	if s.evalOverride != nil {
		return s.evalOverride(id, bars)
	}
	eng := strategy.NewEngine(strategy.Config{Shorts: shorts, RefitEvery: scanBars})
	return eng.Evaluate(id, bars, sizing)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Service) saveReview(ctx context.Context, r domain.Review) {
	if err := s.d.Store.Reviews().Upsert(ctx, r); err != nil {
		s.d.Log.ErrorContext(ctx, "saving a review failed", "account", r.AccountID, "instrument", r.Instrument, "err", err)
	}
}

// ScanNow runs the scanner for the current account now (trade access).
func (s *Service) ScanNow(ctx context.Context, p Principal) (ScanResult, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return ScanResult{}, err
	}
	res, err := s.scan(ctx, v)
	if err != nil {
		return res, err
	}
	s.audit(ctx, p, "scanner.run", v.ID, fmt.Sprintf("reviewed=%d added=%d", res.Reviewed, len(res.Added)))
	return res, nil
}

// marketOpen reports whether market m is in its session now. Without a calendar (tests,
// tools) every market counts as open; an unknown year or market is closed (fail closed).
func (s *Service) marketOpen(m domain.Market) bool {
	if s.d.Calendar == nil {
		return true
	}
	now := s.d.Clock.Now()
	open, closeAt, trading, err := s.d.Calendar.Session(m, now)
	return err == nil && trading && !now.Before(open) && now.Before(closeAt)
}

// ScanAll runs the scanner for every account that has it on (the background runner).
func (s *Service) ScanAll(ctx context.Context) {
	accts, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "scanner: listing accounts failed", "err", err)
		return
	}
	for _, a := range accts {
		if ctx.Err() != nil || !a.Tradable() || !s.ScannerEnabled(ctx, a.ID) {
			continue // view-only platforms (Trading 212) are never scanned
		}
		res, err := s.scan(ctx, domain.AccountView{Account: a, Access: domain.AccessOwner})
		if err != nil {
			s.d.Log.WarnContext(ctx, "scanner run failed", "account", a.ID, "err", err)
			continue
		}
		s.d.Log.InfoContext(ctx, "scanner run", "account", a.ID, "reviewed", res.Reviewed, "added", len(res.Added))
	}
	if n, err := s.d.Store.Reviews().DeleteBefore(ctx, s.d.Clock.Now().Add(-reviewRetention)); err == nil && n > 0 {
		s.d.Log.InfoContext(ctx, "purged old reviews", "count", n)
	}
}

// Reviews returns the current account's latest reviews (view).
func (s *Service) Reviews(ctx context.Context, p Principal, limit int) ([]domain.Review, bool, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rs, err := s.d.Store.Reviews().List(ctx, v.ID, limit)
	if err != nil {
		return nil, false, fmt.Errorf("app.Reviews: %w", err)
	}
	return rs, s.ScannerEnabled(ctx, v.ID), nil
}
