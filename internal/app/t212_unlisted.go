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
)

// What Trading 212 does not list (re-review 2026-11-28, ADR-008 T212-s). Trading 212 may list
// a finished order or a fresh fill late (T4, T5b unverified), so "not listed anywhere" never
// means gone: a stop or a sale of ours becomes final only on an answer of Trading 212's that
// gives its filled quantity, and a position of ours is closed only by a recorded fill of its
// stops and sales (D44: its Sold) or by its owner (SettleT212Position). Until then the order
// stays as it is, nothing more of the position is sold, the account takes no new entries (not
// reconciled), and a danger alert names it once the absence lasts t212UnlistedAlertAfter.

// t212UnlistedAlertAfter is how long an order of ours may be missing from both of Trading
// 212's lists before a danger alert is raised (the bound a lost answer is matched within).
const t212UnlistedAlertAfter = domain.T212MatchAfter

// errT212NotListed: Trading 212 does not list the position although its fill should be
// listed by now: nothing is sold (t212Vanished). errT212NotListedYet: a fresh fill Trading 212
// does not list yet (review B2): nothing is sold, the position gets its stop first
// (t212ProtectUnlisted).
var (
	errT212NotListed    = errors.New("the position is not listed by Trading 212")
	errT212NotListedYet = errors.New("the fresh fill is not listed by Trading 212 yet")
)

// t212OrderKey and t212PositionKey key the runtime's t212Unlisted map.
func t212OrderKey(o domain.Order) string { return "order|" + o.RequestID }

func t212PositionKey(accountID string, positionID int64) string {
	return "position|" + accountID + "|" + strconv.FormatInt(positionID, 10)
}

// t212Missing records that key is missing from Trading 212's lists now and returns since when
// it has been missing (now on the first call).
func (s *Service) t212Missing(key string) time.Time {
	now := s.d.Clock.Now()
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	if s.rt.t212Unlisted == nil {
		s.rt.t212Unlisted = map[string]time.Time{}
	}
	first, seen := s.rt.t212Unlisted[key]
	if !seen {
		s.rt.t212Unlisted[key] = now
		return now
	}
	return first
}

// t212Present forgets that key was missing (Trading 212 lists it again).
func (s *Service) t212Present(key string) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	delete(s.rt.t212Unlisted, key)
}

// t212IsMissing reports whether key is recorded as missing from Trading 212's lists.
func (s *Service) t212IsMissing(key string) bool {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	_, ok := s.rt.t212Unlisted[key]
	return ok
}

// t212AnyMissing reports whether anything is recorded as missing from Trading 212's lists
// (the orders page asks nothing of the store otherwise).
func (s *Service) t212AnyMissing() bool {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	return len(s.rt.t212Unlisted) > 0
}

// t212Hold takes the account's reconciliation away (no new entries) for reason, keeping the
// time of the last reconciliation so the supervisor's next one is not put off. The next
// reconciliation that finds everything listed again restores it.
func (s *Service) t212Hold(accountID, reason string) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	prev := s.rt.reconciled[accountID]
	s.rt.reconciled[accountID] = Reconciliation{AccountID: accountID, At: prev.At, Reason: reason}
}

// t212Find looks one of our orders up at Trading 212 by its id (t212Lookup) and records
// whether it is listed: not listed anywhere holds the account's entries and, after
// t212UnlistedAlertAfter, raises a danger alert once. The order itself is never moved here.
func (s *Service) t212Find(ctx context.Context, a domain.Account, o domain.Order) (domain.BrokerOrder, bool, error) {
	bo, found, err := s.t212Lookup(ctx, a, o.EtoroOrderID, o.Ticker)
	switch {
	case err != nil:
		return bo, false, err
	case found:
		s.t212Present(t212OrderKey(o))
		return bo, true, nil
	}
	since := s.t212Missing(t212OrderKey(o))
	label := s.t212Label(ctx, o.Instrument, o.Ticker)
	s.t212Hold(a.ID, fmt.Sprintf("%s (Trading 212 order %d, request %s) is not listed by Trading 212", t212Verb(o)+" "+label, o.EtoroOrderID, o.RequestID))
	if s.d.Clock.Now().Sub(since) >= t212UnlistedAlertAfter && s.rt.firstWarning("order.unlisted", o.RequestID) {
		s.orderEvent(ctx, a, domain.Long, "order.unlisted", domain.SeverityDanger, t212Verb(o)+" "+label+" is not listed by Trading 212",
			fmt.Sprintf("Trading 212 order %d has been in neither its active orders nor its history for %s. cactusTrade does not take it for gone: "+
				"nothing more of the position is sold and the account takes no new entries until Trading 212 lists it, or the owner settles the position "+
				"(Orders → settle) after checking it in Trading 212. Request %s", o.EtoroOrderID, t212UnlistedAlertAfter, o.RequestID))
	}
	return bo, false, nil
}

// t212PositionUnlisted handles a position of ours Trading 212 does not list although its fill
// should be listed by now, with no recorded stop or sale fill explaining it: it stays open
// (never settled as gone: the fill may be listed late, or a stop may have fired unlisted), the
// account takes no new entries, and a danger alert names it once.
func (s *Service) t212PositionUnlisted(ctx context.Context, a domain.Account, ap domain.AgentPosition) {
	s.t212Missing(t212PositionKey(a.ID, ap.PositionID))
	label := s.t212Label(ctx, ap.Instrument, ap.Ticker)
	s.t212Hold(a.ID, fmt.Sprintf("the position in %s (bought %s, sold %s) is not listed by Trading 212", label, ap.Quantity, ap.Sold))
	if s.rt.firstWarning("position.unlisted", t212PositionKey(a.ID, ap.PositionID)) {
		s.orderEvent(ctx, a, domain.Long, "position.unlisted", domain.SeverityDanger, "Position in "+label+" is not listed by Trading 212",
			fmt.Sprintf("cactusTrade bought %s and recorded %s sold, but Trading 212 does not list the position %s after the fill. It is not taken for closed: "+
				"no stop or sale of ours explains it. The account takes no new entries until Trading 212 lists it, or the owner settles it (Orders → settle) "+
				"after checking it in Trading 212. Trading 212 order %d", ap.Quantity, ap.Sold, fillVisibleAfter, ap.PositionID))
	}
}

// t212OurSells returns our stops and sales of a position that are not final.
func (s *Service) t212OurSells(ctx context.Context, accountID string, positionID int64) ([]domain.Order, error) {
	open, err := s.d.Store.Orders().ListOpen(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(open, func(o domain.Order) bool {
		return o.PositionID != positionID || o.OnPlatform() != domain.PlatformTrading212 || (o.Kind != domain.OrderStop && o.Kind != domain.OrderClose)
	}), nil
}

// t212Vanished handles an agent position Trading 212 no longer lists fillVisibleAfter after
// it opened. Its stops and sale that are not final are read once more: a fill listed
// meanwhile settles it (applyT212Stop, applyT212Sell). Otherwise it stays open: only a
// recorded stop or sale fill, or its owner, closes it (t212PositionUnlisted). It takes the
// account's exit lock.
func (s *Service) t212Vanished(ctx context.Context, a domain.Account, ap domain.AgentPosition) {
	ctx, unlock, err := s.t212ExitLock(ctx, a.ID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "checking a position Trading 212 does not list: no exit lock", "account", a.ID, "position", ap.PositionID, "err", err)
		return
	}
	defer unlock()
	cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID)
	if err != nil || !cur.Open() {
		return
	}
	ap = cur
	sells, err := s.t212OurSells(ctx, a.ID, ap.PositionID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "reading the sells of a position Trading 212 does not list failed", "account", a.ID, "position", ap.PositionID, "err", err)
	}
	for _, o := range sells {
		if o.EtoroOrderID <= 0 {
			continue // being matched (resolveT212): never taken for gone
		}
		if _, err := s.pollT212(ctx, a, o); err != nil {
			s.d.Log.WarnContext(ctx, "reading a sell of a position Trading 212 does not list failed", "account", a.ID, "request", o.RequestID, "err", err)
		}
	}
	if cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); err == nil && !cur.Open() {
		s.t212Present(t212PositionKey(a.ID, ap.PositionID))
		return
	}
	s.t212PositionUnlisted(ctx, a, ap)
}

// SettleT212Position closes, in cactusTrade's records only, a position of ours on a Trading
// 212 practice account that Trading 212 does not list, or whose stop or sale it does not list
// (re-review 2026-11-28): its owner, having checked the account in Trading 212, says it is
// gone. requestID is the position's buy. Nothing is sent to Trading 212; cactusTrade never
// sells anything of the position afterwards (whatever Trading 212 still holds of the ticker is
// the owner's, D44). Refused while Trading 212 still works a stop or sale of ours on it, or an
// order of ours on it waits to be resolved, or Trading 212 lists the position and every order
// of ours on it. Owner of the account, web session. Audited.
func (s *Service) SettleT212Position(ctx context.Context, p Principal, requestID string) (domain.Order, error) {
	if err := s.authorize(p, domain.PermAgentControl); err != nil {
		return domain.Order{}, err
	}
	if p.Via != ViaSession {
		return domain.Order{}, fmt.Errorf("settling a position is done in the web UI, not with an API key: %w", domain.ErrForbidden)
	}
	buy, err := s.d.Store.Orders().Get(ctx, requestID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("order %s: %w", requestID, err)
	}
	v, err := s.t212Account(ctx, p, buy.AccountID, domain.AccessOwner)
	if err != nil {
		return domain.Order{}, err
	}
	if buy.OnPlatform() != domain.PlatformTrading212 || buy.Kind != domain.OrderOpen || buy.PositionID <= 0 {
		return domain.Order{}, fmt.Errorf("order %s did not open a Trading 212 position: %w", requestID, domain.ErrConflict)
	}
	a := v.Account
	ctx = ports.WithAccount(ctx, a)
	if err := s.mayT212(ctx, a); err != nil {
		return domain.Order{}, err
	}
	ctx, unlock, err := s.t212ExitLock(ctx, a.ID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("app.SettleT212Position: %w", err)
	}
	defer unlock()
	ap, err := s.d.Store.AgentPositions().Get(ctx, a.ID, buy.PositionID)
	switch {
	case err != nil:
		return domain.Order{}, fmt.Errorf("app.SettleT212Position: %w", err)
	case !ap.Open():
		return domain.Order{}, fmt.Errorf("the position of order %s is closed already: %w", requestID, domain.ErrConflict)
	}
	acct, err := s.d.T212.Account(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("app.SettleT212Position: reading the positions: %w", err)
	}
	open, err := s.d.T212.OpenOrders(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("app.SettleT212Position: reading the open orders: %w", err)
	}
	sells, err := s.t212OurSells(ctx, a.ID, ap.PositionID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("app.SettleT212Position: %w", err)
	}
	var unlisted []domain.Order
	for _, o := range sells {
		if o.EtoroOrderID <= 0 {
			return domain.Order{}, fmt.Errorf("%s (request %s) has no Trading 212 id yet: resolve it first (Orders → resolve): %w", t212Verb(o), o.RequestID, domain.ErrConflict)
		}
		if slices.ContainsFunc(open, func(b domain.BrokerOrder) bool { return b.ID == o.EtoroOrderID }) {
			return domain.Order{}, fmt.Errorf("order %d of ours still works at Trading 212 on the position: it is not settled while a stop or sale of ours may still sell: %w", o.EtoroOrderID, domain.ErrConflict)
		}
		cur, listed, err := s.pollT212Listed(ctx, a, o)
		if err != nil {
			return domain.Order{}, fmt.Errorf("app.SettleT212Position: %w", err)
		}
		if !listed {
			unlisted = append(unlisted, cur)
		}
	}
	// A fill listed by the reads above may have settled it.
	if ap, err = s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); err != nil {
		return domain.Order{}, fmt.Errorf("app.SettleT212Position: %w", err)
	}
	if !ap.Open() {
		return buy, nil
	}
	pos, held := acct.Position(ap.Ticker)
	gone := (!held || pos.Quantity <= 0) && acct.AsOf.Sub(ap.OpenedAt) >= fillVisibleAfter
	if !gone && len(unlisted) == 0 {
		return domain.Order{}, fmt.Errorf("the position and every order of ours on it are listed by Trading 212: cactusTrade exits it itself: %w", domain.ErrConflict)
	}
	why := "settled by the owner: Trading 212 does not list it"
	for _, o := range unlisted {
		n, err := s.move(ctx, o, domain.OrderUnknown, why, nil)
		if err == nil {
			_, err = s.move(ctx, n, domain.OrderAbandoned, why, nil)
		}
		if err != nil {
			return domain.Order{}, fmt.Errorf("app.SettleT212Position: order %s: %w", o.RequestID, err)
		}
		s.t212Present(t212OrderKey(o))
	}
	s.markClosed(ctx, a, ap, fmt.Sprintf("settled by the account owner (bought %s, recorded sold %s): Trading 212 does not list it; cactusTrade sells nothing more of it", ap.Quantity, ap.Sold))
	s.t212Present(t212PositionKey(a.ID, ap.PositionID))
	s.audit(ctx, p, "t212.position_settled", a.ID+"/"+strconv.FormatInt(ap.PositionID, 10),
		fmt.Sprintf("bought %s, recorded sold %s, %d unlisted order(s) abandoned", ap.Quantity, ap.Sold, len(unlisted)))
	return buy, nil
}

// t212Settleable reports whether the order opened a position of ours that Trading 212 does
// not list, or whose stop or sale it does not list, as last seen in this process: its owner
// may settle it (SettleT212Position checks it again against Trading 212).
func (s *Service) t212Settleable(ctx context.Context, o domain.Order) bool {
	if o.OnPlatform() != domain.PlatformTrading212 || o.Kind != domain.OrderOpen || o.PositionID <= 0 || !s.t212AnyMissing() {
		return false
	}
	if s.t212IsMissing(t212PositionKey(o.AccountID, o.PositionID)) {
		return true
	}
	sells, err := s.t212OurSells(ctx, o.AccountID, o.PositionID)
	return err == nil && slices.ContainsFunc(sells, func(st domain.Order) bool { return s.t212IsMissing(t212OrderKey(st)) })
}

// t212ProtectUnlisted gives a position of ours whose sale is refused because Trading 212 does
// not list it yet (a fresh fill during a kill or a force close: no stop was placed, review g)
// its stop, unless a stop or sale of ours already rests on it. The sale follows once it is
// listed. It takes the account's exit lock.
func (s *Service) t212ProtectUnlisted(ctx context.Context, a domain.Account, ap domain.AgentPosition) error {
	ctx, unlock, err := s.t212ExitLock(ctx, a.ID)
	if err != nil {
		return fmt.Errorf("t212.protect: %w", err)
	}
	defer unlock()
	sells, err := s.t212OurSells(ctx, a.ID, ap.PositionID)
	if err != nil {
		return fmt.Errorf("t212.protect: %w", err)
	}
	if len(sells) > 0 {
		return nil
	}
	if _, err := s.placeT212Stop(ctx, a, ap, s.t212StopOf(ctx, ap)); err != nil {
		return fmt.Errorf("t212.protect: %w", err)
	}
	return nil
}
