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
	"github.com/albievan/cactustrade/internal/ports"
)

// Trading 212 setup (PLAN R45.5): the instrument catalogue, the eToro price source, the
// watchlist's instrument matches and the eligibility they give. Trading 212 trades practice
// accounts only (D41 g): every use case here refuses any other account.

// T212CatalogueMinInterval is the shortest time between two catalogue fetches (Trading 212
// allows one request per 50 s, T6).
const T212CatalogueMinInterval = 50 * time.Second

// T212CatalogueMaxAge is the oldest catalogue a match or an entry may rely on (24 h, like an
// eToro eligibility check).
const T212CatalogueMaxAge = domain.EligibilityMaxAge

func priceSourceKey(accountID string) string { return "t212.price_source." + accountID }

// needT212 refuses when no Trading 212 adapter is wired.
func (s *Service) needT212() error {
	if s.d.T212 == nil {
		return fmt.Errorf("no Trading 212 adapter: trading there is not available: %w", domain.ErrUnavailable)
	}
	return nil
}

// agentAccount resolves an account like account, but also lets a Trading 212 practice account
// through (T212-c): the use cases that serve both platforms. A real Trading 212 account and
// every other platform stay view-only (ErrViewOnly).
func (s *Service) agentAccount(ctx context.Context, p Principal, id string, need domain.AccountAccess) (domain.AccountView, error) {
	v, err := s.anyAccount(ctx, p, id, need)
	if err != nil {
		return domain.AccountView{}, err
	}
	if !v.Agentable() {
		return domain.AccountView{}, fmt.Errorf("%s: %w", v.Name, domain.ErrViewOnly)
	}
	return v, nil
}

// t212Account resolves a Trading 212 practice account for its setup: owner access, in a web
// session (never with an API key).
func (s *Service) t212Account(ctx context.Context, p Principal, id string, need domain.AccountAccess) (domain.AccountView, error) {
	v, err := s.agentAccount(ctx, p, id, need)
	if err != nil {
		return domain.AccountView{}, err
	}
	if !v.T212Trading() {
		return domain.AccountView{}, domain.Invalid("accountId", v.Name+" is not a Trading 212 practice account")
	}
	if need.AtLeast(domain.AccessOwner) && p.Via != ViaSession {
		return domain.AccountView{}, fmt.Errorf("the Trading 212 setup is done in the web UI, not with an API key: %w", domain.ErrForbidden)
	}
	return v, nil
}

// T212Catalogue is the state of the stored Trading 212 instrument catalogue.
type T212Catalogue struct {
	FetchedAt time.Time // zero: never fetched
	Count     int       // instruments stored by the latest refresh (0 when only read)
	Stale     bool      // older than T212CatalogueMaxAge (or never fetched): no matches, no entries
}

// T212CatalogueStatus reports the catalogue's age for a Trading 212 practice account (view).
func (s *Service) T212CatalogueStatus(ctx context.Context, p Principal, accountID string) (T212Catalogue, error) {
	if _, err := s.t212Account(ctx, p, accountID, domain.AccessView); err != nil {
		return T212Catalogue{}, err
	}
	at, err := s.catalogueAt(ctx)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return T212Catalogue{}, err
	}
	return T212Catalogue{FetchedAt: at, Stale: s.catalogueStale(at)}, nil
}

func (s *Service) catalogueAt(ctx context.Context) (time.Time, error) {
	at, err := s.d.Store.T212Instruments().FetchedAt(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return time.Time{}, err
		}
		return time.Time{}, fmt.Errorf("reading the Trading 212 catalogue: %w", err)
	}
	return at, nil
}

func (s *Service) catalogueStale(at time.Time) bool {
	now := s.d.Clock.Now()
	return at.IsZero() || now.Sub(at) > T212CatalogueMaxAge || at.After(now.Add(time.Second))
}

// freshCatalogue refuses when the catalogue is missing, too old or from the future.
func (s *Service) freshCatalogue(ctx context.Context) error {
	at, err := s.catalogueAt(ctx)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if s.catalogueStale(at) {
		return fmt.Errorf("the Trading 212 instrument list is missing or older than %s: refresh it first: %w", T212CatalogueMaxAge, domain.ErrUnavailable)
	}
	return nil
}

// RefreshT212Instruments fetches Trading 212's instrument catalogue with the keys of the
// practice account accountID and replaces the stored one (owner, web session; at most once
// per T212CatalogueMinInterval). Audited.
func (s *Service) RefreshT212Instruments(ctx context.Context, p Principal, accountID string) (T212Catalogue, error) {
	v, err := s.t212Account(ctx, p, accountID, domain.AccessOwner)
	if err != nil {
		return T212Catalogue{}, err
	}
	return s.refreshT212Catalogue(ctx, v.Account, p)
}

// RefreshT212InstrumentsAuto refreshes the catalogue for the background loop (R45.10) with the
// first Trading 212 practice account that has its keys; nothing to do without one.
func (s *Service) RefreshT212InstrumentsAuto(ctx context.Context) (T212Catalogue, error) {
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		return T212Catalogue{}, fmt.Errorf("app.RefreshT212InstrumentsAuto: %w", err)
	}
	for _, a := range as {
		if a.T212Trading() && s.hasKeys(ctx, a) {
			return s.refreshT212Catalogue(ctx, a, Principal{Username: "system"})
		}
	}
	return T212Catalogue{}, fmt.Errorf("no Trading 212 practice account with keys: %w", domain.ErrNotFound)
}

func (s *Service) refreshT212Catalogue(ctx context.Context, a domain.Account, p Principal) (T212Catalogue, error) {
	if err := s.needT212(); err != nil {
		return T212Catalogue{}, err
	}
	at, err := s.catalogueAt(ctx)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return T212Catalogue{}, err
	}
	now := s.d.Clock.Now().UTC()
	if !at.IsZero() && now.Sub(at) < T212CatalogueMinInterval {
		return T212Catalogue{FetchedAt: at}, fmt.Errorf("the Trading 212 instruments were fetched %s ago (at most once per %s): %w",
			now.Sub(at).Round(time.Second), T212CatalogueMinInterval, domain.ErrBusy)
	}
	ins, err := s.d.T212.Instruments(ports.WithAccount(ctx, a))
	if err != nil {
		return T212Catalogue{}, fmt.Errorf("reading Trading 212's instruments: %w", err)
	}
	// Keep what can be stored: a ticker of 1–64 characters, each once (the first wins).
	seen := map[string]bool{}
	keep := slices.DeleteFunc(slices.Clone(ins), func(in domain.T212Instrument) bool {
		bad := in.Ticker == "" || len(in.Ticker) > 64 || seen[in.Ticker]
		seen[in.Ticker] = true
		return bad
	})
	if len(keep) == 0 {
		return T212Catalogue{}, fmt.Errorf("the Trading 212 catalogue came back empty: %w", domain.ErrUnavailable)
	}
	if err := s.d.Store.T212Instruments().ReplaceAll(ctx, keep, now); err != nil {
		return T212Catalogue{}, fmt.Errorf("app.RefreshT212Instruments: %w", err)
	}
	s.audit(ctx, p, "t212.instruments_refreshed", a.ID, strconv.Itoa(len(keep))+" instruments ("+strconv.Itoa(len(ins)-len(keep))+" skipped)")
	return T212Catalogue{FetchedAt: now, Count: len(keep)}, nil
}

// priceSourceOK checks that src may feed prices and signals to the Trading 212 practice
// account a (T212-l, D44): an eToro demo account of the same owner, with its keys.
func (s *Service) priceSourceOK(ctx context.Context, a, src domain.Account) error {
	switch {
	case !src.Tradable():
		return domain.Invalid("priceSourceAccountId", "the price source must be an eToro account")
	case src.Mode != domain.ModeDemo:
		return domain.Invalid("priceSourceAccountId", "the price source must be an eToro demo account, never a real-money one")
	case src.OwnerID == "" || src.OwnerID != a.OwnerID:
		return domain.Invalid("priceSourceAccountId", "the price source must belong to the owner of "+a.Name)
	case !s.hasKeys(ctx, src):
		return domain.Invalid("priceSourceAccountId", "the price source "+src.Name+" has no eToro keys")
	}
	return nil
}

// SetT212PriceSource chooses the eToro demo account whose market data feeds the Trading 212
// practice account accountID (T212-l; D44: eToro data used at the owner's risk). Owner, web
// session, agent stopped; sourceID "" removes it (then nothing is entered). Audited.
func (s *Service) SetT212PriceSource(ctx context.Context, p Principal, accountID, sourceID string) error {
	v, err := s.t212Account(ctx, p, accountID, domain.AccessOwner)
	if err != nil {
		return err
	}
	if sourceID != "" {
		src, err := s.d.Store.Accounts().Get(ctx, sourceID)
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Invalid("priceSourceAccountId", "no such account")
		}
		if err != nil {
			return fmt.Errorf("app.SetT212PriceSource: %w", err)
		}
		if err := s.priceSourceOK(ctx, v.Account, src); err != nil {
			return err
		}
	}
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	if err := s.limitsChangeable(ctx, v.ID); err != nil {
		return err
	}
	old, _ := s.d.Store.Settings().Get(ctx, priceSourceKey(v.ID))
	if err := s.d.Store.Settings().Set(ctx, priceSourceKey(v.ID), sourceID, s.d.Clock.Now().UTC()); err != nil {
		return fmt.Errorf("app.SetT212PriceSource: %w", err)
	}
	s.audit(ctx, p, "t212.price_source_changed", v.ID, "old="+old+" new="+sourceID)
	return nil
}

// t212PriceSource returns the eToro account set as the price source of a Trading 212
// practice account, checked again (T212-l); ErrUnavailable when none is set or it no longer
// qualifies.
func (s *Service) t212PriceSource(ctx context.Context, a domain.Account) (domain.Account, error) {
	id, err := s.d.Store.Settings().Get(ctx, priceSourceKey(a.ID))
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.Account{}, fmt.Errorf("reading the price source: %w", err)
	}
	if id == "" {
		return domain.Account{}, fmt.Errorf("%s has no eToro price source: choose one first: %w", a.Name, domain.ErrUnavailable)
	}
	src, err := s.d.Store.Accounts().Get(ctx, id)
	if err != nil {
		return domain.Account{}, fmt.Errorf("the price source of %s: %w: %w", a.Name, err, domain.ErrUnavailable)
	}
	if err := s.priceSourceOK(ctx, a, src); err != nil {
		return domain.Account{}, fmt.Errorf("the price source of %s no longer qualifies (%w): %w", a.Name, err, domain.ErrUnavailable)
	}
	return src, nil
}

// t212PriceCtx is ctx carrying the price source of the Trading 212 practice account a, for
// eToro's market data (signals, quotes, search). It is checked again on every use.
func (s *Service) t212PriceCtx(ctx context.Context, a domain.Account) (context.Context, error) {
	src, err := s.t212PriceSource(ctx, a)
	if err != nil {
		return nil, err
	}
	return ports.WithAccount(ctx, src), nil
}

// t212AccountCurrency reads the Trading 212 account's primary currency.
func (s *Service) t212AccountCurrency(ctx context.Context, a domain.Account) (string, error) {
	if err := s.needT212(); err != nil {
		return "", err
	}
	acct, err := s.d.T212.Account(ports.WithAccount(ctx, a))
	if err != nil {
		return "", fmt.Errorf("reading the Trading 212 account: %w", err)
	}
	if strings.TrimSpace(acct.Currency) == "" {
		return "", fmt.Errorf("the Trading 212 account summary has no currency: %w", domain.ErrUnavailable)
	}
	return acct.Currency, nil
}

// matchT212 finds the Trading 212 instrument of eToro share in in the stored catalogue
// (D42), with the reasons for people.
func (s *Service) matchT212(ctx context.Context, in domain.Instrument) (domain.T212Instrument, error) {
	if _, ok := domain.T212SuffixFor(in.Market); !ok {
		return domain.T212Instrument{}, domain.Invalid("instrumentId", "Trading 212 trading is for shares listed in London (LSE) or New York (NYSE/NASDAQ) only")
	}
	if err := s.freshCatalogue(ctx); err != nil {
		return domain.T212Instrument{}, err
	}
	sym := strings.ToUpper(strings.TrimSpace(in.Symbol))
	if in.Market == domain.MarketLSE {
		sym = strings.TrimSuffix(sym, ".L")
	}
	cands, err := s.d.Store.T212Instruments().ByShortName(ctx, sym)
	if err != nil {
		return domain.T212Instrument{}, fmt.Errorf("reading the Trading 212 catalogue: %w", err)
	}
	t, err := domain.MatchInstrument(in, cands)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.T212Instrument{}, domain.Invalid("instrumentId", "no Trading 212 instrument matches "+in.Symbol+" (market, ticker, currency and type must all agree)")
	case errors.Is(err, domain.ErrConflict):
		return domain.T212Instrument{}, domain.Invalid("instrumentId", "more than one Trading 212 instrument matches "+in.Symbol+": it cannot be traded on Trading 212")
	case err != nil:
		return domain.T212Instrument{}, err
	}
	return t, nil
}

// checkAndAddT212 adds an eToro share to a Trading 212 practice account's watchlist (R45.5):
// the eToro instrument through the price source, London or New York only, its Trading 212
// instrument (MatchInstrument on a fresh catalogue), the eligibility that gives on the
// account's currency, then the watchlist row and the match, unconfirmed until the owner
// confirms it. eToro's own eligibility cache is never written for it.
func (s *Service) checkAndAddT212(ctx context.Context, v domain.AccountView, actor string, id domain.InstrumentID) (domain.WatchItem, error) {
	if err := s.needBroker(); err != nil {
		return domain.WatchItem{}, err
	}
	if id <= 0 {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "must be positive")
	}
	pctx, err := s.t212PriceCtx(ctx, v.Account)
	if err != nil {
		return domain.WatchItem{}, err
	}
	ins, err := s.d.Market.Instruments(pctx, []domain.InstrumentID{id})
	if err != nil {
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	i := slices.IndexFunc(ins, func(in domain.Instrument) bool { return in.ID == id })
	if i < 0 {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "eToro does not know this instrument")
	}
	in := ins[i]
	t, err := s.matchT212(ctx, in)
	if err != nil {
		return domain.WatchItem{}, err
	}
	cur, err := s.t212AccountCurrency(ctx, v.Account)
	if err != nil {
		return domain.WatchItem{}, err
	}
	el := domain.T212Eligibility(id, in.Symbol, t, cur)
	if !el.RealLong {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "not tradable on Trading 212 "+el.Reason)
	}
	now := s.d.Clock.Now().UTC()
	// The watchlist needs the instrument's row; an eToro eligibility already cached there is
	// left alone (Trading 212's eligibility is never stored in eToro's cache).
	if _, _, err := s.d.Store.Instruments().Get(ctx, id); errors.Is(err, domain.ErrNotFound) {
		if err := s.d.Store.Instruments().Upsert(ctx, in, domain.Eligibility{Instrument: id, Symbol: in.Symbol, Reason: "not checked with eToro"}, now); err != nil {
			return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
		}
	} else if err != nil {
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	if err := s.d.Store.Watchlist().Add(ctx, v.ID, id, actor, now); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return domain.WatchItem{}, fmt.Errorf("%s is already on the watchlist: %w", in.Symbol, domain.ErrConflict)
		}
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	m := domain.InstrumentMatch{AccountID: v.ID, Instrument: id, Ticker: t.Ticker, ISIN: t.ISIN, Method: domain.MatchMethodAuto, UpdatedAt: now}
	if err := s.d.Store.InstrumentMatches().Put(ctx, m); err != nil {
		_ = s.d.Store.Watchlist().Remove(ctx, v.ID, id) // no watchlist row without its match
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	el.RealLong, el.Reason = false, "(the Trading 212 match "+t.Ticker+" is not confirmed yet)"
	return domain.WatchItem{Instrument: in, Eligibility: el, AddedBy: actor, AddedAt: now}, nil
}

// ConfirmT212Match confirms the Trading 212 instrument of a share on the current account's
// watchlist (D42: the account owner confirms each match once, having seen both names and
// Trading 212's ISIN). Owner, web session. The match is checked again on the catalogue: when
// it no longer gives the same instrument, nothing is confirmed. Audited.
func (s *Service) ConfirmT212Match(ctx context.Context, p Principal, id domain.InstrumentID) (domain.InstrumentMatch, error) {
	v, err := s.t212Account(ctx, p, "", domain.AccessOwner)
	if err != nil {
		return domain.InstrumentMatch{}, err
	}
	m, err := s.d.Store.InstrumentMatches().Get(ctx, v.ID, id)
	if err != nil {
		return domain.InstrumentMatch{}, fmt.Errorf("the Trading 212 match of instrument %d: %w", id, err)
	}
	in, _, err := s.d.Store.Instruments().Get(ctx, id)
	if err != nil {
		return domain.InstrumentMatch{}, fmt.Errorf("app.ConfirmT212Match: %w", err)
	}
	t, err := s.matchT212(ctx, in)
	if err != nil {
		return domain.InstrumentMatch{}, err
	}
	if t.Ticker != m.Ticker || t.ISIN != m.ISIN {
		return domain.InstrumentMatch{}, fmt.Errorf("%s now matches %s (%s), not %s (%s): remove it and add it again: %w",
			in.Symbol, t.Ticker, t.ISIN, m.Ticker, m.ISIN, domain.ErrConflict)
	}
	now := s.d.Clock.Now().UTC()
	m.ConfirmedBy, m.ConfirmedAt, m.UpdatedAt = p.UserID, now, now
	if err := s.d.Store.InstrumentMatches().Put(ctx, m); err != nil {
		return domain.InstrumentMatch{}, fmt.Errorf("app.ConfirmT212Match: %w", err)
	}
	s.audit(ctx, p, "t212.match_confirmed", v.ID+"/"+strconv.FormatInt(int64(id), 10), in.Symbol+" = "+m.Ticker+" ("+m.ISIN+")")
	return m, nil
}

// t212Eligibility is the eligibility of a watchlist share on the Trading 212 practice account
// a (R45.5, used by the agent in R45.7): only a confirmed match whose instrument is still in
// the catalogue, of a type, currency and size the account may trade (domain.T212Eligibility).
// Anything else is "not eligible" with the reason; only a store error is an error.
func (s *Service) t212Eligibility(ctx context.Context, a domain.Account, in domain.Instrument, currency string) (domain.Eligibility, domain.InstrumentMatch, error) {
	no := func(why string) domain.Eligibility {
		return domain.Eligibility{Instrument: in.ID, Symbol: in.Symbol, Reason: "(" + why + ")", ShortReason: "(no shorts on Trading 212: long only)"}
	}
	m, err := s.d.Store.InstrumentMatches().Get(ctx, a.ID, in.ID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return no("no Trading 212 match"), domain.InstrumentMatch{}, nil
	case err != nil:
		return domain.Eligibility{}, domain.InstrumentMatch{}, fmt.Errorf("reading the Trading 212 match: %w", err)
	case !m.Confirmed():
		return no("the Trading 212 match " + m.Ticker + " is not confirmed"), m, nil
	}
	t, err := s.d.Store.T212Instruments().Get(ctx, m.Ticker)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return no(m.Ticker + " is no longer listed by Trading 212"), m, nil
	case err != nil:
		return domain.Eligibility{}, m, fmt.Errorf("reading the Trading 212 catalogue: %w", err)
	case t.ISIN != m.ISIN:
		return no(m.Ticker + " changed its ISIN since it was confirmed"), m, nil
	}
	return domain.T212Eligibility(in.ID, in.Symbol, t, currency), m, nil
}
