package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// AccountHoldings is one account in the overview: its holdings, or why they could not be
// read (Error; NoKeys when its key pair is not set yet).
type AccountHoldings struct {
	Account  domain.AccountView
	Holdings *domain.Holdings
	Error    string
	NoKeys   bool
}

// Overview is every account the caller can view, across platforms, with totals per
// currency (amounts in different currencies are never added).
type Overview struct {
	Accounts []AccountHoldings
	Totals   []domain.CurrencyTotal
	AsOf     time.Time
}

// EquitySeries is one account's equity over time, oldest first.
type EquitySeries struct {
	Account  domain.AccountView
	Currency string
	Points   []domain.EquitySnapshot
}

// equityTimeout bounds one account's read in the overview and the snapshot run, so one slow
// platform never holds up the others for long.
const equityTimeout = 30 * time.Second

// holdings reads an account's holdings: eToro accounts through the Broker (portfolio, names
// from the instrument cache or eToro, prices when withQuotes), other platforms through
// their read-only Holdings source.
func (s *Service) holdings(ctx context.Context, a domain.Account, withQuotes bool) (domain.Holdings, error) {
	bctx := ports.WithAccount(ctx, a)
	if !a.Tradable() {
		src := s.d.Holdings[a.OnPlatform()]
		if src == nil {
			return domain.Holdings{}, fmt.Errorf("the %s client is not configured: %w", platformLabel(a.OnPlatform()), domain.ErrUnavailable)
		}
		h, err := src.Holdings(bctx)
		if err != nil {
			return domain.Holdings{}, fmt.Errorf("app.holdings: %w", err)
		}
		return h, nil
	}
	if err := s.needBroker(); err != nil {
		return domain.Holdings{}, err
	}
	pf, err := s.d.Broker.Portfolio(bctx)
	if err != nil {
		return domain.Holdings{}, fmt.Errorf("app.holdings: %w", err)
	}
	ins := map[domain.InstrumentID]domain.Instrument{}
	var ids, missing []domain.InstrumentID
	for _, p := range pf.Positions {
		if _, seen := ins[p.Instrument]; seen || containsID(ids, p.Instrument) {
			continue
		}
		ids = append(ids, p.Instrument)
		if in, _, err := s.d.Store.Instruments().Get(ctx, p.Instrument); err == nil {
			ins[p.Instrument] = in
		} else {
			missing = append(missing, p.Instrument)
		}
	}
	if len(missing) > 0 { // names are cosmetic: a failure leaves "#id"
		if got, err := s.d.Market.Instruments(bctx, missing); err == nil {
			for _, in := range got {
				ins[in.ID] = in
			}
		}
	}
	var quotes map[domain.InstrumentID]domain.Quote
	if withQuotes && len(ids) > 0 { // prices are cosmetic too: a failure leaves them blank
		if qs, err := s.d.Market.Quotes(bctx, ids); err == nil {
			quotes = make(map[domain.InstrumentID]domain.Quote, len(qs))
			for _, q := range qs {
				quotes[q.Instrument] = q
			}
		}
	}
	return domain.HoldingsFromPortfolio(pf, ins, quotes), nil
}

func containsID(ids []domain.InstrumentID, id domain.InstrumentID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// readableError is the human part of a broker error: no Go function names, no bare
// sentinel words.
func readableError(err error) string {
	var keep []string
	for _, seg := range strings.Split(err.Error(), ": ") {
		switch {
		case seg == domain.ErrUnavailable.Error(), seg == domain.ErrNotFound.Error(), seg == domain.ErrCredentialsMissing.Error():
		case strings.Contains(seg, ".") && !strings.Contains(seg, " "):
		default:
			keep = append(keep, seg)
		}
	}
	if len(keep) == 0 {
		return "the platform did not answer"
	}
	return strings.Join(keep, ": ")
}

// Overview reads every account the caller can view, on every platform, at once. One failing
// account never fails the whole overview: it carries its error instead (viewer).
func (s *Service) Overview(ctx context.Context, p Principal) (Overview, error) {
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{Accounts: make([]AccountHoldings, len(vs)), AsOf: s.d.Clock.Now().UTC()}
	var wg sync.WaitGroup
	for i, v := range vs {
		wg.Go(func() {
			actx, cancel := context.WithTimeout(ctx, equityTimeout)
			defer cancel()
			ah := AccountHoldings{Account: v}
			h, err := s.holdings(actx, v.Account, true)
			switch {
			case err == nil:
				ah.Holdings = &h
			case errors.Is(err, domain.ErrCredentialsMissing):
				ah.NoKeys, ah.Error = true, "no keys yet"
			default:
				s.d.Log.WarnContext(ctx, "overview: reading an account failed", "account", v.ID, "platform", string(v.OnPlatform()), "err", err)
				ah.Error = readableError(err)
			}
			out.Accounts[i] = ah
		})
	}
	wg.Wait()
	var hs []domain.Holdings
	for _, a := range out.Accounts {
		if a.Holdings != nil {
			hs = append(hs, *a.Holdings)
		}
	}
	out.Totals = domain.TotalsByCurrency(hs)
	return out, nil
}

// EquityHistory returns the recorded equity of every account the caller can view over the
// last days (0 = the default 30, at most 400), one series per account that has snapshots
// (viewer).
func (s *Service) EquityHistory(ctx context.Context, p Principal, days int) ([]EquitySeries, error) {
	days, err := domain.EquityHistoryDays(days)
	if err != nil {
		return nil, err
	}
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(vs))
	byID := map[string]domain.AccountView{}
	for _, v := range vs {
		ids = append(ids, v.ID)
		byID[v.ID] = v
	}
	since := s.d.Clock.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	snaps, err := s.d.Store.EquitySnapshots().List(ctx, ids, since)
	if err != nil {
		return nil, fmt.Errorf("app.EquityHistory: %w", err)
	}
	var out []EquitySeries
	for _, sn := range snaps { // sorted by account, then time
		if len(out) == 0 || out[len(out)-1].Account.ID != sn.AccountID {
			out = append(out, EquitySeries{Account: byID[sn.AccountID]})
		}
		cur := &out[len(out)-1]
		cur.Points = append(cur.Points, sn)
		cur.Currency = sn.Currency
	}
	return out, nil
}

// RecordEquity snapshots the equity of every account whose keys are set (system: the
// 15-minute runner) and drops snapshots older than 400 days. A failing account is logged
// and skipped. It returns how many snapshots were stored.
func (s *Service) RecordEquity(ctx context.Context) int {
	accts, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "equity snapshots: listing accounts failed", "err", err)
		return 0
	}
	now := s.d.Clock.Now().UTC().Truncate(time.Minute)
	n := 0
	for _, a := range accts {
		if ctx.Err() != nil {
			break
		}
		actx, cancel := context.WithTimeout(ctx, equityTimeout)
		h, err := s.holdings(actx, a, false)
		cancel()
		switch {
		case errors.Is(err, domain.ErrCredentialsMissing):
			continue // no keys yet: nothing to record
		case err != nil:
			s.d.Log.WarnContext(ctx, "equity snapshot failed", "account", a.ID, "platform", string(a.OnPlatform()), "err", err)
			continue
		}
		switch err := s.d.Store.EquitySnapshots().Add(ctx, h.Snapshot(a.ID, now)); {
		case err == nil:
			n++
		case !errors.Is(err, domain.ErrConflict): // a conflict: this minute is already recorded
			s.d.Log.WarnContext(ctx, "storing an equity snapshot failed", "account", a.ID, "err", err)
		}
	}
	before := now.Add(-domain.EquityRetentionDays * 24 * time.Hour)
	if d, err := s.d.Store.EquitySnapshots().DeleteBefore(ctx, before); err != nil {
		s.d.Log.WarnContext(ctx, "purging old equity snapshots failed", "err", err)
	} else if d > 0 {
		s.d.Log.InfoContext(ctx, "purged old equity snapshots", "count", d)
	}
	return n
}
