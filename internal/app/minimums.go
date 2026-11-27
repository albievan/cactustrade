package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Trade minimums (PLAN R38): the smallest long and short the platform accepts, checked
// against the account's limits. A check only proposes; the limits change when the owner
// approves, through SetRiskLimits and all its rules. Nothing here, or anywhere in
// cactusTrade, moves money into or out of an account.

func minimumsKey(accountID string) string { return "risk.minimums." + accountID }

// MinimumsView is the current account's latest minimums review (nil before the first check)
// and whether the caller may approve it now (SetRiskLimits' rules).
type MinimumsView struct {
	Review     *domain.MinimumsReview
	CanDecide  bool
	DecideHint string // why the caller may not approve now ("" = may)
}

type minimumsJSON struct {
	ID          string     `json:"id"`
	At          time.Time  `json:"at"`
	Platform    string     `json:"platform"`
	Long        int64      `json:"long"`
	Short       int64      `json:"short"`
	LongLowest  int64      `json:"longLowest"`
	ShortLowest int64      `json:"shortLowest"`
	Instruments int        `json:"instruments"`
	LongKnown   int        `json:"longKnown"`
	ShortKnown  int        `json:"shortKnown"`
	Equity      int64      `json:"equity"`
	Current     limitsJSON `json:"current"`
	Proposed    limitsJSON `json:"proposed"`
	LongRoom    int64      `json:"longRoom"`
	ShortRoom   int64      `json:"shortRoom"`
	Changes     []string   `json:"changes"`
	Problems    []string   `json:"problems"`
	Status      string     `json:"status"`
	DecidedBy   string     `json:"decidedBy,omitempty"`
	DecidedAt   time.Time  `json:"decidedAt,omitzero"`
}

func toMinimumsJSON(r domain.MinimumsReview) minimumsJSON {
	m := r.Minimums
	return minimumsJSON{
		ID: r.ID, At: r.At, Platform: string(m.Platform), Long: int64(m.Long), Short: int64(m.Short),
		LongLowest: int64(m.LongLowest), ShortLowest: int64(m.ShortLowest), Instruments: m.Instruments,
		LongKnown: m.LongKnown, ShortKnown: m.ShortKnown, Equity: int64(r.Equity),
		Current: toLimitsJSON(r.Current), Proposed: toLimitsJSON(r.Fit.Proposed),
		LongRoom: int64(r.Fit.LongRoom), ShortRoom: int64(r.Fit.ShortRoom),
		Changes: r.Fit.Changes, Problems: r.Fit.Problems, Status: string(r.Status), DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt,
	}
}

func (j minimumsJSON) domain(accountID string) domain.MinimumsReview {
	r := domain.MinimumsReview{
		ID: j.ID, AccountID: accountID, At: j.At, Equity: domain.Cents(j.Equity), Current: j.Current.domain(),
		Status: domain.MinimumsStatus(j.Status), DecidedBy: j.DecidedBy, DecidedAt: j.DecidedAt,
		Minimums: domain.TradeMinimums{
			Platform: domain.Platform(j.Platform), Long: domain.Cents(j.Long), Short: domain.Cents(j.Short),
			LongLowest: domain.Cents(j.LongLowest), ShortLowest: domain.Cents(j.ShortLowest), Instruments: j.Instruments,
			LongKnown: j.LongKnown, ShortKnown: j.ShortKnown,
		},
	}
	r.Fit = domain.MinimumFit{
		Proposed: j.Proposed.domain(), Changes: j.Changes, Problems: j.Problems,
		LongRoom: domain.Cents(j.LongRoom), ShortRoom: domain.Cents(j.ShortRoom),
	}
	r.Fit.Fits = r.Status == domain.MinimumsFit
	return r
}

func (s *Service) storedMinimums(ctx context.Context, accountID string) (*domain.MinimumsReview, error) {
	v, err := s.d.Store.Settings().Get(ctx, minimumsKey(accountID))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j minimumsJSON
	if err := json.Unmarshal([]byte(v), &j); err != nil {
		return nil, fmt.Errorf("the stored minimums review is unreadable: %w", err)
	}
	r := j.domain(accountID)
	return &r, nil
}

func (s *Service) storeMinimums(ctx context.Context, r domain.MinimumsReview) error {
	b, err := json.Marshal(toMinimumsJSON(r))
	if err != nil {
		return err
	}
	return s.d.Store.Settings().Set(ctx, minimumsKey(r.AccountID), string(b), s.d.Clock.Now().UTC())
}

// TradeMinimums returns the current account's latest minimums review (view).
func (s *Service) TradeMinimums(ctx context.Context, p Principal) (MinimumsView, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return MinimumsView{}, err
	}
	r, err := s.storedMinimums(ctx, v.ID)
	if err != nil {
		return MinimumsView{}, fmt.Errorf("app.TradeMinimums: %w", err)
	}
	return s.minimumsView(ctx, p, v, r), nil
}

func (s *Service) minimumsView(ctx context.Context, p Principal, v domain.AccountView, r *domain.MinimumsReview) MinimumsView {
	out := MinimumsView{Review: r}
	if r == nil || r.Status != domain.MinimumsPending {
		return out
	}
	out.DecideHint = s.limitsEditBlocked(ctx, p, v)
	out.CanDecide = out.DecideHint == ""
	return out
}

// CheckTradeMinimums checks the current account's limits against its platform's minimums now
// and stores the review, replacing an undecided one (owner). It changes no limit.
func (s *Service) CheckTradeMinimums(ctx context.Context, p Principal) (MinimumsView, error) {
	v, err := s.account(ctx, p, "", domain.AccessOwner)
	if err != nil {
		return MinimumsView{}, err
	}
	r, err := s.checkMinimums(ctx, v, p.Username)
	if err != nil {
		return MinimumsView{}, err
	}
	s.audit(ctx, p, "risk.minimums_checked", v.ID, fmt.Sprintf("status=%s long=%s short=%s", r.Status, r.Minimums.Long, r.Minimums.Short))
	return s.minimumsView(ctx, p, v, &r), nil
}

// checkMinimums works out account v's review: the minimums from the eligibility eToro gave
// for the account's watched shares (every watched share of any account when its list is
// empty), and the fit of its limits at its equity now. A pending review raises an event
// asking the owner to approve it.
func (s *Service) checkMinimums(ctx context.Context, v domain.AccountView, actor string) (domain.MinimumsReview, error) {
	if v.OnPlatform() != domain.PlatformEtoro {
		return domain.MinimumsReview{}, domain.Invalid("account", "trade minimums are checked for eToro accounts, the only platform cactusTrade trades on")
	}
	if err := s.needBroker(); err != nil {
		return domain.MinimumsReview{}, err
	}
	els, err := s.minimumsEligibility(ctx, v.ID)
	if err != nil {
		return domain.MinimumsReview{}, fmt.Errorf("app.CheckTradeMinimums: %w", err)
	}
	m := domain.MinimumsFrom(domain.PlatformEtoro, els)
	l := s.riskLimitsFor(ctx, v.Account)
	var equity domain.Cents
	var eqErr error
	if pf, err := s.d.Broker.Portfolio(brokerCtx(ctx, v)); err == nil {
		equity = pf.Equity()
	} else {
		eqErr = err
	}
	fit := domain.FitMinimums(l, m, equity, accountMode(v.Account))
	if eqErr != nil {
		fit.Problems = append(fit.Problems, "eToro's portfolio could not be read: "+eqErr.Error())
	}
	if m.LongKnown == 0 && m.ShortKnown == 0 {
		fit.Problems = append(fit.Problems, "no share has been checked on eToro yet: add shares to the watchlist, then check again")
		fit.Proposed, fit.Changes, fit.Fits = l, nil, false
	}
	r := domain.MinimumsReview{
		ID: s.d.Random.ID(), AccountID: v.ID, At: s.d.Clock.Now().UTC(), Minimums: m, Equity: equity, Current: l, Fit: fit,
	}
	r.Status = domain.StatusOf(fit)
	if err := s.storeMinimums(ctx, r); err != nil {
		return domain.MinimumsReview{}, fmt.Errorf("app.CheckTradeMinimums: %w", err)
	}
	if r.Status == domain.MinimumsPending {
		s.emitFor(ctx, v.ID, actor, "risk.minimums_pending", domain.SeverityWarning,
			"Approve the trade minimums of "+v.Name,
			fmt.Sprintf("eToro's smallest long is $%s and smallest short $%s; the limits need changes before such a trade fits: %s. Nothing changes until you approve it on the Risk limits page.",
				m.Long, m.Short, strings.Join(fit.Changes, "; ")))
	}
	return r, nil
}

// minimumsEligibility is the eligibility of the account's watched shares, or of every watched
// share when its list is empty.
func (s *Service) minimumsEligibility(ctx context.Context, accountID string) ([]domain.Eligibility, error) {
	items, err := s.d.Store.Watchlist().List(ctx, accountID)
	if err != nil {
		return nil, err
	}
	els := make([]domain.Eligibility, 0, len(items))
	for _, it := range items {
		els = append(els, it.Eligibility)
	}
	if len(els) > 0 {
		return els, nil
	}
	ids, err := s.d.Store.Watchlist().Watched(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, el, err := s.d.Store.Instruments().Get(ctx, id); err == nil {
			els = append(els, el)
		}
	}
	return els, nil
}

// DecideTradeMinimums approves or declines the current account's pending review id (owner).
// Approving changes the limits to the proposed ones through SetRiskLimits (owner, web
// session, agent stopped, budget at most the equity, audited); it is refused when the
// limits changed since the check. Declining changes nothing.
func (s *Service) DecideTradeMinimums(ctx context.Context, p Principal, id string, approve bool) (MinimumsView, error) {
	v, err := s.account(ctx, p, "", domain.AccessOwner)
	if err != nil {
		return MinimumsView{}, err
	}
	r, err := s.storedMinimums(ctx, v.ID)
	switch {
	case err != nil:
		return MinimumsView{}, fmt.Errorf("app.DecideTradeMinimums: %w", err)
	case r == nil || r.ID != id:
		return MinimumsView{}, fmt.Errorf("no such minimums review (check again): %w", domain.ErrNotFound)
	case r.Status != domain.MinimumsPending:
		return MinimumsView{}, fmt.Errorf("the minimums review is already %s: %w", r.Status, domain.ErrConflict)
	}
	if approve {
		if cur := s.riskLimitsFor(ctx, v.Account); cur != r.Current {
			return MinimumsView{}, fmt.Errorf("the risk limits changed since the minimums were checked: check again: %w", domain.ErrConflict)
		}
		if _, err := s.SetRiskLimits(ctx, p, r.Fit.Proposed); err != nil {
			return MinimumsView{}, err
		}
		r.Status = domain.MinimumsApproved
	} else {
		r.Status = domain.MinimumsDeclined
	}
	r.DecidedBy, r.DecidedAt = p.Username, s.d.Clock.Now().UTC()
	if err := s.storeMinimums(ctx, *r); err != nil {
		return MinimumsView{}, fmt.Errorf("app.DecideTradeMinimums: %w", err)
	}
	s.audit(ctx, p, "risk.minimums_"+string(r.Status), v.ID, "review="+r.ID+" changes="+strings.Join(r.Fit.Changes, "; "))
	return s.minimumsView(ctx, p, v, r), nil
}
