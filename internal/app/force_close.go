package app

import (
	"context"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
)

// Force close (R24, owner 2026-11-24): an admin makes cactusTrade close everything it opened
// on an account at once. Unlike the kill switch it latches nothing and needs no reset: the
// pending orders of ours are cancelled, every position cactusTrade opened is closed (OD1:
// never the owner's own), and the agent is stopped so it does not enter again. It needs the
// agent.force_close permission (admin), a web session, trade access and the caller's
// password, checked by the same hasher as a sign-in (bounded, D29; the HTTP adapter counts
// it with the sign-in limiter, D28). Real-money accounts are force-closed too, whatever their
// gate's state (PLAN 7.21: exits always work).

// ForceClosePlan is what a force close of one account would do now (for the confirmation).
type ForceClosePlan struct {
	AccountID string
	Name      string
	// Positions counts cactusTrade's open positions, Orders its open orders not final yet
	// (pending opens are cancelled; closes in flight are followed up).
	Positions, Orders int
	// Summary says it for people, e.g. "closes 3 positions and cancels 1 order on Demo".
	Summary string
}

// ForceCloseReport says what a force close of one account did.
type ForceCloseReport struct {
	AccountID string
	Name      string
	// Cancelled counts cancels sent for pending open orders of ours; Closing the closes sent
	// or in flight for positions cactusTrade opened.
	Cancelled, Closing int
	// State is the agent's state afterwards: stopped, or killed when the kill switch was
	// already on (a force close never turns it off).
	State  domain.AgentState
	Errors []string
}

// forceCloser checks the rights every force close needs: the permission (admin), a web
// session (never an API key), and the caller is a user.
func (s *Service) forceCloser(p Principal) error {
	if err := s.authorize(p, domain.PermForceClose); err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("a force close needs a web session, not an API key: %w", domain.ErrForbidden)
	}
	return nil
}

// forceClosable resolves an account the caller may force-close: forceCloser's rights, trade
// access, and an eToro account, demo or real-money (PLAN 7.21).
func (s *Service) forceClosable(ctx context.Context, p Principal, accountID string) (domain.AccountView, error) {
	if err := s.forceCloser(p); err != nil {
		return domain.AccountView{}, err
	}
	v, err := s.account(ctx, p, accountID, domain.AccessTrade)
	if err != nil {
		return domain.AccountView{}, err
	}
	if !v.Tradable() {
		return domain.AccountView{}, fmt.Errorf("%s: %w", v.Name, domain.ErrViewOnly)
	}
	return v, nil
}

// forceClosableAll lists the accounts a force close of all accounts covers: every eToro
// account, demo or real-money, the caller can trade.
func (s *Service) forceClosableAll(ctx context.Context, p Principal) ([]domain.AccountView, error) {
	if err := s.forceCloser(p); err != nil {
		return nil, err
	}
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AccountView, 0, len(vs))
	for _, v := range vs {
		if v.Tradable() && v.Access.AtLeast(domain.AccessTrade) {
			out = append(out, v)
		}
	}
	return out, nil
}

// checkPassword verifies the caller's own password (argon2id through the bounded hasher,
// D29). A wrong one is audited as the action refused (e.g. agent.force_close_refused) and
// refused with domain.ErrWrongPassword.
func (s *Service) checkPassword(ctx context.Context, p Principal, password, target, refused string) error {
	u, err := s.d.Store.Users().Get(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("app.checkPassword: %w", err)
	}
	ok, err := s.d.Hasher.Verify(u.PasswordHash, password)
	if err != nil {
		return fmt.Errorf("app.checkPassword: %w", err)
	}
	if !ok || u.Disabled {
		s.audit(ctx, p, refused, target, "wrong password")
		return domain.ErrWrongPassword
	}
	return nil
}

// ForceClosePreview says what a force close of the account would do now, with the same
// rights as ForceClose but no password (the confirmation dialog shows it). accountID "" is
// the caller's current account. It only reads the database.
func (s *Service) ForceClosePreview(ctx context.Context, p Principal, accountID string) (ForceClosePlan, error) {
	v, err := s.forceClosable(ctx, p, accountID)
	if err != nil {
		return ForceClosePlan{}, err
	}
	return s.forceClosePlan(ctx, v)
}

// ForceCloseAllPreview is ForceClosePreview for every eToro account the caller can trade.
func (s *Service) ForceCloseAllPreview(ctx context.Context, p Principal) ([]ForceClosePlan, error) {
	vs, err := s.forceClosableAll(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]ForceClosePlan, 0, len(vs))
	for _, v := range vs {
		pl, err := s.forceClosePlan(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, nil
}

func (s *Service) forceClosePlan(ctx context.Context, v domain.AccountView) (ForceClosePlan, error) {
	ps, err := s.d.Store.AgentPositions().ListOpen(ctx, v.ID)
	if err != nil {
		return ForceClosePlan{}, fmt.Errorf("app.ForceClosePreview: %w", err)
	}
	os, err := s.d.Store.Orders().ListOpen(ctx, v.ID)
	if err != nil {
		return ForceClosePlan{}, fmt.Errorf("app.ForceClosePreview: %w", err)
	}
	pl := ForceClosePlan{AccountID: v.ID, Name: v.Name, Positions: len(ps)}
	for _, o := range os {
		if o.Kind == domain.OrderOpen {
			pl.Orders++
		}
	}
	pl.Summary = forceCloseSummary(v.Name, pl.Positions, pl.Orders)
	return pl, nil
}

// forceCloseSummary is a plan for people.
func forceCloseSummary(name string, positions, orders int) string {
	switch {
	case positions > 0 && orders > 0:
		return "closes " + count(positions, "position") + " and cancels " + count(orders, "order") + " on " + name
	case positions > 0:
		return "closes " + count(positions, "position") + " on " + name
	case orders > 0:
		return "cancels " + count(orders, "order") + " on " + name
	default:
		return "nothing of cactusTrade's is open on " + name
	}
}

// ForceClose closes everything cactusTrade opened on an account now and stops its agent
// (R24): admin (agent.force_close), web session, trade access, an eToro account, and the
// caller's password (domain.ErrWrongPassword when wrong; nothing is read from eToro or sent
// before it is checked). accountID "" is the caller's current account. The cancels and
// closes are sent once each, paced by the eToro client's execution limiter, and are not
// cancelled when the caller goes away. No kill latch, no reset needed afterwards. Audited
// (agent.force_close), with a danger event (and so an alert e-mail, R12).
func (s *Service) ForceClose(ctx context.Context, p Principal, accountID, password string) (ForceCloseReport, error) {
	v, err := s.forceClosable(ctx, p, accountID)
	if err != nil {
		return ForceCloseReport{}, err
	}
	if err := s.checkPassword(ctx, p, password, v.ID, "agent.force_close_refused"); err != nil {
		return ForceCloseReport{}, err
	}
	rep, err := s.forceClose(ctx, p, v)
	if err != nil {
		return rep, fmt.Errorf("app.ForceClose: %w", err)
	}
	return rep, nil
}

// ForceCloseAll force-closes every eToro account (demo and real-money) the caller can trade,
// checking the password once. One report per account; the error is the first account's
// whose agent could not be stopped.
func (s *Service) ForceCloseAll(ctx context.Context, p Principal, password string) ([]ForceCloseReport, error) {
	vs, err := s.forceClosableAll(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := s.checkPassword(ctx, p, password, "all accounts", "agent.force_close_refused"); err != nil {
		return nil, err
	}
	out := make([]ForceCloseReport, 0, len(vs))
	var first error
	for _, v := range vs {
		rep, err := s.forceClose(ctx, p, v)
		if err != nil && first == nil {
			first = fmt.Errorf("app.ForceCloseAll: %s: %w", v.Name, err)
		}
		out = append(out, rep)
	}
	return out, first
}

// forceClose stops the agent and flattens, under the account's locks in Kill's order
// (control, then entry): the running tick is cancelled and waited for, so nothing is opened
// once the closes start. A killed agent stays killed (its latch and reset are untouched).
// When the stop cannot be stored the account is flattened all the same (exits must work)
// and the error is returned.
func (s *Service) forceClose(ctx context.Context, p Principal, v domain.AccountView) (ForceCloseReport, error) {
	// The closes go on when the caller disconnects (an HTTP request ending), as the
	// supervisor's would.
	ctx = context.WithoutCancel(ctx)
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	rep := ForceCloseReport{AccountID: v.ID, Name: v.Name, State: s.agentState(ctx, v.ID)}
	var serr error
	// An unreadable kill switch is not taken as "killed" here: the agent is stopped all the
	// same (a stored killed agent state is never overwritten: isKilled reads it first).
	if killed, kerr := s.isKilled(ctx, v.ID); killed && kerr == nil {
		rep.State = domain.AgentKilled
	} else if rep.State != domain.AgentStopped {
		if serr = s.setAgentState(ctx, v.ID, domain.AgentStopped); serr != nil {
			rep.Errors = append(rep.Errors, "stopping the agent: "+serr.Error())
		} else {
			rep.State = domain.AgentStopped
		}
	}
	s.rt.cancelEntries(v.ID)
	locks.entry.Lock()
	defer locks.entry.Unlock()
	kr := KillReport{AccountID: v.ID}
	s.flatten(ctx, v.Account, domain.ExitForceClosed, "force close by "+p.Username, &kr)
	rep.Cancelled, rep.Closing = kr.Cancelled, kr.Closing
	rep.Errors = append(rep.Errors, kr.Errors...)
	detail := fmt.Sprintf("%s, %s sent; the agent is %s", count(rep.Closing, "close"), count(rep.Cancelled, "cancel"), rep.State)
	if len(rep.Errors) > 0 {
		detail += fmt.Sprintf("; %d failed", len(rep.Errors))
	}
	s.audit(ctx, p, "agent.force_close", v.ID, detail)
	s.emitFor(ctx, v.ID, p.Username, "agent.force_closed", domain.SeverityDanger, "Force close on "+v.Name,
		"An admin closed everything cactusTrade opened: "+detail+". Positions opened outside cactusTrade are left alone. "+
			"No kill switch: the agent can be started again once the limits are right.")
	return rep, serr
}

// count is "1 close" or "3 closes".
func count(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}
