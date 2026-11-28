package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Reset all trading data (PLAN R49). An admin, in a web session, with the password and the
// typed phrase, wipes every account's trading data (OD39) after an encrypted backup (R48).
// The order of operations is fixed: refuse new starts and arming (rt.resetting), reconcile
// every eToro account, check, back up, take every account's control lock and (after
// cancelling its running agent tick) its entry lock, check again, wipe in one transaction
// (bounded by dataResetWipeTimeout), latch the gate disarms in memory, mark every account
// unreconciled, announce, reconcile again. Kills, disarms and exits are never blocked: they
// only wait for the control lock for as long as the wipe's transaction runs.

// msgResetting is why a start, an automatic start or an arming is refused during a reset.
const msgResetting = "a data reset is running"

// dataResetLockWait bounds how long the reset waits, in all, for every account's control
// and entry locks (a kill, a start or a cancelled agent tick holds one briefly); then the
// reset is refused. dataResetWipeTimeout bounds the wipe's transaction: when it runs out the
// transaction rolls back and the reset is refused, so the locks (and with them kills and
// starts) are never held for longer.
const (
	dataResetLockWait    = 5 * time.Second
	dataResetLockStep    = 100 * time.Millisecond
	dataResetWipeTimeout = 2 * time.Minute
)

// RealEvidenceWarning is what a reset does to one real-money account's demo evidence (D37,
// D39, D40): the evidence is read from the decisions, orders and agent positions the reset
// wipes, so its qualifying days fall to 0 and arming needs Required new ones.
type RealEvidenceWarning struct {
	AccountID, Name string
	// DemoAccountID is the demo account whose history is the evidence ("" = none chosen).
	DemoAccountID, DemoName string
	DaysNow, Required       int
}

// DataResetPreview is what the reset dialog shows: the checks, the rows wiped and kept, every
// real account's evidence now → 0, and whether a backup can be taken.
type DataResetPreview struct {
	Checks   []domain.GateCheck
	CanReset bool
	// Wiped and Kept are the rows per table now (the watchlist is in Kept; it is wiped only
	// when asked).
	Wiped, Kept  map[string]int64
	RealAccounts []RealEvidenceWarning
	BackupReady  bool
	BackupWhy    string
	BackupFolder string
	Phrase       string
}

// DataResetResult is what a reset did.
type DataResetResult struct {
	BackupFile      string
	Counts          map[string]int64 // rows deleted per table, real_gates_locked, settings
	WipedWatchlists bool
	At              time.Time
}

// dataResetAdmin checks an admin in a web session.
func (s *Service) dataResetAdmin(p Principal) error {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return err
	}
	if p.Role != domain.RoleAdmin {
		return fmt.Errorf("only an admin can reset all trading data: %w", domain.ErrForbidden)
	}
	if p.Via != ViaSession {
		return fmt.Errorf("resetting all trading data needs a web session, not an API key: %w", domain.ErrForbidden)
	}
	return nil
}

// backupReadiness says whether a backup can be taken now ("" = it can).
func (s *Service) backupReadiness(ctx context.Context) string {
	if s.d.Backups == nil {
		return "backups from the app are not set up in this deployment"
	}
	if rs, _ := s.backupRecipients(ctx); len(rs) == 0 {
		return "add an age public key under Setup → Backups first: backups are always encrypted"
	}
	return ""
}

// dataResetFacts gathers the facts of the reset's checks for the accounts as. reconciledSince
// is when the reconciliation that counts started (zero = the latest one counts). Any store
// read error is returned: the reset is then refused (fail closed).
func (s *Service) dataResetFacts(ctx context.Context, as []domain.Account, reconciledSince time.Time) (domain.DataResetFacts, error) {
	now := s.d.Clock.Now().UTC()
	f := domain.DataResetFacts{Agents: map[string]domain.AgentState{}}
	if why := s.backupReadiness(ctx); why != "" {
		f.BackupWhy = why
	} else {
		f.BackupReady = true
	}
	for _, a := range as {
		if a.Agentable() {
			// An unreadable state is never taken for "stopped" here: the reset is refused.
			st, err := s.readAgentState(ctx, a.ID)
			if err != nil {
				return f, fmt.Errorf("reading the agent state of %s: %w", a.Name, err)
			}
			f.Agents[a.ID] = st
		}
		switch l, err := s.d.Store.AgentLeases().Get(ctx, a.ID); {
		case errors.Is(err, domain.ErrNotFound):
		case err != nil:
			return f, fmt.Errorf("reading the lease of %s: %w", a.Name, err)
		case now.Before(l.ExpiresAt) && l.Holder != s.leaseHolder():
			f.LiveLeases++
		}
		if isRealAccount(a) {
			g, err := s.storedGate(ctx, a.ID)
			if err != nil {
				return f, fmt.Errorf("reading the gate of %s: %w", a.Name, err)
			}
			if g.State == domain.GateArmed {
				if live, why := s.gateLive(ctx, g); !live && why != "" {
					s.expireGate(ctx, a, why, g) // no longer in force: locked now (or by the reset)
				} else {
					f.ArmedGates = append(f.ArmedGates, a.Name)
				}
			}
		}
		af := domain.ResetAccountFacts{AccountID: a.ID, Name: a.Name, MustReconcile: a.Tradable() && s.hasKeys(ctx, a)}
		if af.MustReconcile {
			rc := s.Reconciled(a.ID)
			af.Reconciled, af.Reason = rc.OK && !rc.At.Before(reconciledSince), rc.Reason
			if rc.OK && !af.Reconciled {
				af.Reason = "not reconciled just now"
			}
		}
		ps, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
		if err != nil {
			return f, fmt.Errorf("reading the positions of %s: %w", a.Name, err)
		}
		os, err := s.d.Store.Orders().ListOpen(ctx, a.ID)
		if err != nil {
			return f, fmt.Errorf("reading the orders of %s: %w", a.Name, err)
		}
		recent, err := s.d.Store.Orders().ListSince(ctx, a.ID, now.Add(-domain.DataResetQuiet))
		if err != nil {
			return f, fmt.Errorf("reading the orders of %s: %w", a.Name, err)
		}
		af.OpenPositions, af.OpenOrders, af.RecentOrders = len(ps), len(os), len(recent)
		f.Accounts = append(f.Accounts, af)
	}
	return f, nil
}

// realEvidenceWarnings is every real-money account's demo evidence now (the reset sets it to
// 0). The demo account is the one its gate was last armed with, else the owner's first eToro
// demo account.
func (s *Service) realEvidenceWarnings(ctx context.Context, as []domain.Account) ([]RealEvidenceWarning, error) {
	now := s.d.Clock.Now().UTC()
	var out []RealEvidenceWarning
	for _, a := range as {
		if !isRealAccount(a) {
			continue
		}
		rule := s.evidenceRule(ctx, a.ID)
		w := RealEvidenceWarning{AccountID: a.ID, Name: a.Name, Required: rule.MinDays}
		g, err := s.storedGate(ctx, a.ID)
		if err != nil {
			return nil, fmt.Errorf("reading the gate of %s: %w", a.Name, err)
		}
		var demo *domain.Account
		for i := range as {
			d := &as[i]
			if d.Tradable() && d.Mode == domain.ModeDemo && d.OwnerID == a.OwnerID && d.ID != a.ID {
				if d.ID == g.DemoAccountID || demo == nil {
					demo = d
				}
				if d.ID == g.DemoAccountID {
					break
				}
			}
		}
		if demo != nil {
			ev, err := s.demoEvidence(ctx, demo.ID, now, rule)
			if err != nil {
				return nil, err
			}
			w.DemoAccountID, w.DemoName, w.DaysNow = demo.ID, demo.Name, ev.Count
		}
		out = append(out, w)
	}
	return out, nil
}

// DataResetPreview is the reset dialog (admin, web session): the checks now, the rows wiped
// and kept, every real account's evidence now → 0, and the backup's readiness. It only reads
// (a gate found no longer in force is locked, as the Real trading page does).
func (s *Service) DataResetPreview(ctx context.Context, p Principal) (DataResetPreview, error) {
	if err := s.dataResetAdmin(p); err != nil {
		return DataResetPreview{}, err
	}
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		return DataResetPreview{}, fmt.Errorf("app.DataResetPreview: %w", err)
	}
	f, err := s.dataResetFacts(ctx, as, time.Time{})
	if err != nil {
		return DataResetPreview{}, fmt.Errorf("app.DataResetPreview: %w", err)
	}
	out := DataResetPreview{Checks: domain.DataResetChecks(f), BackupReady: f.BackupReady, BackupWhy: f.BackupWhy, Phrase: domain.DataResetPhrase}
	out.CanReset = domain.CanResetData(out.Checks) == nil
	if s.d.Backups != nil {
		out.BackupFolder = s.d.Backups.Folder()
	}
	counts, err := s.d.Store.DataReset().Counts(ctx)
	if err != nil {
		return DataResetPreview{}, fmt.Errorf("app.DataResetPreview: %w", err)
	}
	out.Wiped, out.Kept = map[string]int64{}, map[string]int64{}
	wiped := domain.DataResetWipedTables()
	for t, n := range counts {
		if slices.Contains(wiped, t) {
			out.Wiped[t] = n
		} else {
			out.Kept[t] = n
		}
	}
	if out.RealAccounts, err = s.realEvidenceWarnings(ctx, as); err != nil {
		return DataResetPreview{}, fmt.Errorf("app.DataResetPreview: %w", err)
	}
	return out, nil
}

// failedNames lists the failing checks' names.
func failedNames(err error) string {
	r, ok := errors.AsType[*domain.DataResetRefusal](err)
	if !ok {
		return err.Error()
	}
	names := make([]string, 0, len(r.Failed))
	for _, c := range r.Failed {
		names = append(names, c.Name)
	}
	return "failed checks: " + strings.Join(names, ", ")
}

// ResetAllData wipes all trading data of every account (PLAN R49, OD39): an admin in a web
// session, the typed phrase domain.DataResetPhrase (a domain.ValidationError when wrong), the
// caller's password (domain.ErrWrongPassword, audited data.reset_refused), then the checks
// after a fresh reconciliation of every eToro account (a *domain.DataResetRefusal naming the
// failing ones), an encrypted backup (without it nothing is wiped), every account's control
// lock, the checks again, and the wipe in one transaction. Afterwards every gate disarm is
// latched, every account is unreconciled until the next reconciliation, and it is audited,
// announced and e-mailed to every admin and every account owner.
func (s *Service) ResetAllData(ctx context.Context, p Principal, confirm, password string, wipeWatchlists bool) (DataResetResult, error) {
	if err := s.dataResetAdmin(p); err != nil {
		return DataResetResult{}, err
	}
	if strings.TrimSpace(confirm) != domain.DataResetPhrase {
		s.audit(ctx, p, "data.reset_refused", "all accounts", "wrong phrase")
		return DataResetResult{}, domain.Invalid("confirm", fmt.Sprintf("type %q to confirm", domain.DataResetPhrase))
	}
	if err := s.checkPassword(ctx, p, password, "all accounts", "data.reset_refused"); err != nil {
		return DataResetResult{}, err
	}
	if !s.rt.resetting.CompareAndSwap(false, true) {
		return DataResetResult{}, fmt.Errorf("%s: %w", msgResetting, domain.ErrConflict)
	}
	defer s.rt.resetting.Store(false)
	// Once authorized, the reset runs to the end even if the caller goes away.
	ctx = context.WithoutCancel(ctx)
	refuse := func(err error) (DataResetResult, error) {
		s.audit(ctx, p, "data.reset_refused", "all accounts", failedNames(err))
		return DataResetResult{}, fmt.Errorf("app.ResetAllData: %w", err)
	}
	started := s.d.Clock.Now().UTC()
	if _, err := s.ReconcileAll(ctx); err != nil {
		return refuse(fmt.Errorf("reconciling: %w: %w", err, domain.ErrUnavailable))
	}
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		return refuse(err)
	}
	check := func() error {
		f, err := s.dataResetFacts(ctx, as, started)
		if err != nil {
			return fmt.Errorf("%w: %w", err, domain.ErrUnavailable)
		}
		return domain.CanResetData(domain.DataResetChecks(f))
	}
	if err := check(); err != nil {
		return refuse(err)
	}
	s.audit(ctx, p, "data.reset_started", "all accounts", fmt.Sprintf("%d accounts, watchlists %s", len(as), yesNo(wipeWatchlists)))
	backup, err := s.runBackup(ctx, p, domain.BackupTriggerReset)
	if err != nil {
		return refuse(fmt.Errorf("the backup before the reset failed, nothing was wiped: %w", err))
	}
	unlock, err := s.lockAllAccounts(ctx, as)
	if err != nil {
		return refuse(err)
	}
	res, err := s.wipeLocked(ctx, as, check, wipeWatchlists)
	unlock()
	if err != nil {
		return refuse(err)
	}
	res.BackupFile = backup.Name
	s.announceReset(ctx, p, as, res)
	if _, err := s.ReconcileAll(ctx); err != nil {
		s.d.Log.WarnContext(ctx, "data reset: reconciling afterwards failed", "err", err)
	}
	return res, nil
}

// wipeLocked checks again and wipes; the caller holds every account's control and entry
// locks.
func (s *Service) wipeLocked(ctx context.Context, as []domain.Account, check func() error, wipeWatchlists bool) (DataResetResult, error) {
	if err := check(); err != nil {
		return DataResetResult{}, err
	}
	at := s.d.Clock.Now().UTC()
	ids := make([]string, 0, len(as))
	for _, a := range as {
		ids = append(ids, a.ID)
	}
	wctx, cancel := context.WithTimeout(ctx, dataResetWipeTimeout)
	defer cancel()
	counts, err := s.d.Store.DataReset().Reset(wctx, ports.ResetSpec{At: at, WipeWatchlists: wipeWatchlists, AccountIDs: ids})
	// A committed wipe counts even if the deadline passed just after; a failed one rolled
	// back (sqlstore.dataReset.Reset) and is refused. A driver may not wrap the context's
	// error, so the deadline is read from the context itself.
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(wctx.Err(), context.DeadlineExceeded)) {
		return DataResetResult{}, fmt.Errorf("the wipe took longer than %s and was rolled back, nothing was wiped: %w", dataResetWipeTimeout, domain.ErrUnavailable)
	}
	if err != nil {
		return DataResetResult{}, fmt.Errorf("wiping: %w: %w", err, domain.ErrUnavailable)
	}
	for _, a := range as {
		if isRealAccount(a) {
			s.gates.latch(a.ID, at) // a gate armed before the reset is never in force again
		}
		s.unreconcile(a.ID, "all trading data was reset: waiting for the next reconciliation")
	}
	return DataResetResult{Counts: counts, WipedWatchlists: wipeWatchlists, At: at}, nil
}

// lockAllAccounts takes every account's control lock and then its entry lock, account by
// account in id order, each by TryLock, retried for at most dataResetLockWait in all (a kill
// or a start holds the control lock only briefly). Before waiting for an entry lock it
// cancels the account's running agent tick, so a tick in flight unwinds instead of placing
// an order the wipe would delete; once the reset holds the entry lock no tick runs on the
// account until it is released. On failure nothing stays locked. The result unlocks them in
// reverse order.
func (s *Service) lockAllAccounts(ctx context.Context, as []domain.Account) (func(), error) {
	ids := make([]string, 0, len(as))
	for _, a := range as {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	var held []*sync.Mutex
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
	}
	left := int(dataResetLockWait / dataResetLockStep)
	take := func(m *sync.Mutex) error {
		for !m.TryLock() {
			if left <= 0 {
				return fmt.Errorf("an account is busy (a kill, a start, a change or an agent tick is running): try again: %w", domain.ErrConflict)
			}
			left--
			if err := s.d.Sleep(ctx, dataResetLockStep); err != nil {
				return err
			}
		}
		held = append(held, m)
		return nil
	}
	for _, id := range ids {
		l := s.rt.agent(id)
		if err := take(&l.ctl); err != nil {
			unlock()
			return nil, err
		}
		s.rt.cancelEntries(id)
		if err := take(&l.entry); err != nil {
			unlock()
			return nil, err
		}
	}
	return unlock, nil
}

// announceReset audits the reset, raises a warning event and e-mails every admin and every
// account owner (OD39).
func (s *Service) announceReset(ctx context.Context, p Principal, as []domain.Account, res DataResetResult) {
	keys := make([]string, 0, len(res.Counts))
	for k := range res.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, res.Counts[k]))
	}
	detail := fmt.Sprintf("backup %s; %s; watchlists %s", res.BackupFile, strings.Join(parts, ", "), yesNo(res.WipedWatchlists))
	s.audit(ctx, p, "data.reset", "all accounts", detail)
	body := "All trading data of every account was reset by " + p.Username + " (backup " + res.BackupFile + " taken first). " +
		"Orders, positions history, trades, decisions, LLM calls, equity history, events and reviews start empty; " +
		"users, accounts, keys, settings, limits, kill switches and the audit log are kept. " +
		"Every account reconciles again before its agent can start; real trading needs new qualifying demo days before it can be armed."
	s.emit(ctx, p.Username, "data.reset", domain.SeverityWarning, "All trading data was reset", body)
	users, err := s.d.Store.Users().List(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "data reset: listing users for the e-mail failed", "err", err)
		return
	}
	owners := map[string]bool{}
	for _, a := range as {
		owners[a.OwnerID] = true
	}
	// The event's own alert e-mail (notify) already reached the admins who take that
	// category: they are not mailed twice (as mailGateChange).
	cat := domain.CategoryOf(domain.Event{Kind: "data.reset", Severity: domain.SeverityWarning})
	for _, u := range users {
		if u.Disabled || u.Email == "" || (u.Role != domain.RoleAdmin && !owners[u.ID]) {
			continue
		}
		if cat != "" && u.Role.Allows(domain.PermManageSettings) && s.notifyPrefs(ctx, u.ID)[cat] {
			continue
		}
		s.enqueue(ctx, u.Email, "[cactusTrade] All trading data was reset", "All trading data was reset\n\n"+body+
			"\n\nWhen: "+res.At.Format("Mon 2 Jan 2006 15:04 MST")+"\nBy: "+p.Username+
			"\n\nYou get this because you are an administrator or an account owner.\n")
	}
}

// yesNo words a flag.
func yesNo(b bool) string {
	if b {
		return "wiped"
	}
	return "kept"
}
