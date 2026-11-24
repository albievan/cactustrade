package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Trading statements (R30): daily, weekly and monthly PDFs of what happened on a user's
// accounts, e-mailed on a schedule through the outbox, previewed and sent on demand. A
// statement is a view of stored records (trades, equity snapshots, orders, decisions,
// events, LLM calls) plus, for view-only platforms, their holdings read when it is made. It
// calls no eToro endpoint, and nothing here is read by an order path.

const (
	// statementActivityCap bounds the decisions and events one account's statement reads;
	// beyond it the counts are lower bounds (said on the statement).
	statementActivityCap = 20_000
	// statementPositionLookback is how far before a period cactusTrade's positions are read
	// to find those still open at its end (positions are flattened daily; a longer-lived one
	// is still found while it is open).
	statementPositionLookback = 31 * 24 * time.Hour
	// statementSendTimeout bounds one schedule's statements in the loop.
	statementSendTimeout = 5 * time.Minute
)

// StatementScheduleInput is what the owner edits on a schedule.
type StatementScheduleInput struct {
	Period                                             domain.StatementPeriod
	AccountIDs                                         []string
	Recipients                                         []string
	IncludeCombined, IncludeAccounts, IncludeExchanges bool
	Enabled                                            bool
}

// StatementAccountRef names an account on the statements page.
type StatementAccountRef struct {
	ID       string
	Name     string
	Platform domain.Platform
	Mode     domain.TradingMode
}

// StatementScheduleView is a schedule as the caller sees it.
type StatementScheduleView struct {
	domain.StatementSchedule
	OwnerName string
	// Accounts are the schedule's accounts that still exist and may be on it; with no
	// AccountIDs they are every account the owner may put on a schedule now.
	Accounts []StatementAccountRef
	// NextSendAt is when the next scheduled statement goes out (zero when switched off).
	NextSendAt time.Time
}

// StatementsPage is the statements section of the System page.
type StatementsPage struct {
	Schedules []StatementScheduleView
	// Accounts are the accounts the caller may put on a schedule.
	Accounts []StatementAccountRef
	// MailServer: a mail server is set up (else statements wait in the outbox);
	// CanFixMail: the caller may set one up.
	MailServer, CanFixMail bool
}

// StatementFile is a rendered statement.
type StatementFile struct {
	Name string
	Data []byte
}

// StatementSendResult says what one send put in the outbox.
type StatementSendResult struct {
	Window     domain.StatementWindow
	Recipients int // e-mails enqueued now (an e-mail already queued for a recipient is not counted)
	Files      []string
}

// StatementPreviewRequest chooses the statement to preview: the period of kind Period that
// holds the risk day From ("" = the last complete one), the scope (AccountID for
// ScopeAccount, Exchange for ScopeExchange) and, with ScheduleID, only that schedule's
// accounts.
type StatementPreviewRequest struct {
	Period     domain.StatementPeriod
	From       string
	Scope      domain.StatementScope
	AccountID  string
	Exchange   string
	ScheduleID string
}

// statementCaller checks that p may use statements: view permission, from the web UI (the
// recipients are addresses the user typed, so no API key manages them).
func (s *Service) statementCaller(p Principal) error {
	if err := s.authorize(p, domain.PermView); err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("trading statements are managed from the web UI: %w", domain.ErrForbidden)
	}
	return nil
}

// statementAccounts are the accounts a user may put on a schedule: the accounts they own,
// and for an administrator every account they can see.
func (s *Service) statementAccounts(ctx context.Context, userID string, role domain.Role) ([]domain.AccountView, error) {
	vs, err := s.d.Store.Accounts().ForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("app.statementAccounts: %w", err)
	}
	admin := role.Allows(domain.PermManageSettings)
	return slices.DeleteFunc(vs, func(v domain.AccountView) bool { return v.Access != domain.AccessOwner && !admin }), nil
}

func refs(vs []domain.AccountView) []StatementAccountRef {
	out := make([]StatementAccountRef, 0, len(vs))
	for _, v := range vs {
		out = append(out, StatementAccountRef{ID: v.ID, Name: v.Name, Platform: v.OnPlatform(), Mode: v.Mode})
	}
	return out
}

// scheduleAccounts resolves a schedule's accounts against what its owner may put on it now
// (an account deleted, unshared or no longer owned drops out).
func scheduleAccounts(allowed []domain.AccountView, ids []string) []domain.AccountView {
	if len(ids) == 0 {
		return allowed
	}
	return slices.DeleteFunc(slices.Clone(allowed), func(v domain.AccountView) bool { return !slices.Contains(ids, v.ID) })
}

// owned loads a schedule the caller may manage: its owner, or an administrator. Anything
// else is not found.
func (s *Service) owned(ctx context.Context, p Principal, id string) (domain.StatementSchedule, domain.User, error) {
	x, err := s.d.Store.StatementSchedules().Get(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.StatementSchedule{}, domain.User{}, fmt.Errorf("statement schedule: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.StatementSchedule{}, domain.User{}, fmt.Errorf("app.statementSchedule: %w", err)
	}
	if x.OwnerID != p.UserID && !p.Can(domain.PermManageSettings) {
		return domain.StatementSchedule{}, domain.User{}, fmt.Errorf("statement schedule: %w", domain.ErrNotFound)
	}
	owner, err := s.d.Store.Users().Get(ctx, x.OwnerID)
	if err != nil {
		return domain.StatementSchedule{}, domain.User{}, fmt.Errorf("app.statementSchedule: %w", err)
	}
	return x, owner, nil
}

// StatementSchedules lists the caller's schedules (an administrator sees every user's) with
// the accounts the caller may put on one and whether mail can go out (web session).
func (s *Service) StatementSchedules(ctx context.Context, p Principal) (StatementsPage, error) {
	if err := s.statementCaller(p); err != nil {
		return StatementsPage{}, err
	}
	mine, err := s.statementAccounts(ctx, p.UserID, p.Role)
	if err != nil {
		return StatementsPage{}, err
	}
	owner := p.UserID
	if p.Can(domain.PermManageSettings) {
		owner = ""
	}
	xs, err := s.d.Store.StatementSchedules().List(ctx, owner)
	if err != nil {
		return StatementsPage{}, fmt.Errorf("app.StatementSchedules: %w", err)
	}
	out := StatementsPage{Accounts: refs(mine), Schedules: make([]StatementScheduleView, 0, len(xs))}
	for _, x := range xs {
		u, err := s.d.Store.Users().Get(ctx, x.OwnerID)
		if err != nil {
			continue // the owner went away: its schedules go with it
		}
		v, err := s.scheduleView(ctx, x, u)
		if err != nil {
			return StatementsPage{}, err
		}
		out.Schedules = append(out.Schedules, v)
	}
	_, _, ok, _ := s.mailServer(ctx)
	out.MailServer = ok && s.d.Mailer != nil
	out.CanFixMail = s.canFix(p, domain.AccountView{}, domain.MissingEmailServer)
	return out, nil
}

func (s *Service) scheduleView(ctx context.Context, x domain.StatementSchedule, owner domain.User) (StatementScheduleView, error) {
	allowed, err := s.statementAccounts(ctx, owner.ID, owner.Role)
	if err != nil {
		return StatementScheduleView{}, err
	}
	v := StatementScheduleView{StatementSchedule: x, OwnerName: owner.Username, Accounts: refs(scheduleAccounts(allowed, x.AccountIDs))}
	if x.Enabled {
		v.NextSendAt, _ = domain.NextSendAt(x.Period, s.d.Clock.Now().UTC())
	}
	return v, nil
}

// checkScheduleInput normalises and validates in for owner: every account must be one the
// owner may put on a schedule.
func (s *Service) checkScheduleInput(ctx context.Context, owner domain.User, in StatementScheduleInput) (domain.StatementSchedule, error) {
	x := domain.StatementSchedule{
		Period: in.Period, AccountIDs: in.AccountIDs, Recipients: in.Recipients, IncludeCombined: in.IncludeCombined,
		IncludeAccounts: in.IncludeAccounts, IncludeExchanges: in.IncludeExchanges, Enabled: in.Enabled,
	}.Normalize()
	if err := x.Validate(); err != nil {
		return domain.StatementSchedule{}, err
	}
	allowed, err := s.statementAccounts(ctx, owner.ID, owner.Role)
	if err != nil {
		return domain.StatementSchedule{}, err
	}
	for _, id := range x.AccountIDs {
		if !slices.ContainsFunc(allowed, func(v domain.AccountView) bool { return v.ID == id }) {
			return domain.StatementSchedule{}, domain.Invalid("accountIds", "only accounts you own (an administrator: accounts they can see)")
		}
	}
	if len(allowed) == 0 {
		return domain.StatementSchedule{}, &domain.MissingError{
			What: domain.MissingAccount, Msg: "You own no account to make statements of: add one under Platform accounts",
			CanFix: owner.Role.Allows(domain.PermAgentControl), Err: domain.Invalid("accountIds", "no account"),
		}
	}
	return x, nil
}

func describeSchedule(x domain.StatementSchedule) string {
	accts := "all accounts"
	if len(x.AccountIDs) > 0 {
		accts = strings.Join(x.AccountIDs, ",")
	}
	return fmt.Sprintf("%s to %s; accounts %s; combined=%t per-account=%t per-exchange=%t enabled=%t", x.Period,
		strings.Join(x.Recipients, ","), accts, x.IncludeCombined, x.IncludeAccounts, x.IncludeExchanges, x.Enabled)
}

// CreateStatementSchedule adds a schedule owned by the caller (web session; audited). The
// first scheduled send is for the next period that closes: the last complete one is marked
// handled (use "Send now" for it).
func (s *Service) CreateStatementSchedule(ctx context.Context, p Principal, in StatementScheduleInput) (StatementScheduleView, error) {
	if err := s.statementCaller(p); err != nil {
		return StatementScheduleView{}, err
	}
	owner, err := s.d.Store.Users().Get(ctx, p.UserID)
	if err != nil {
		return StatementScheduleView{}, fmt.Errorf("app.CreateStatementSchedule: %w", err)
	}
	x, err := s.checkScheduleInput(ctx, owner, in)
	if err != nil {
		return StatementScheduleView{}, err
	}
	now := s.d.Clock.Now().UTC()
	w, err := domain.LastCompleteWindow(x.Period, now)
	if err != nil {
		return StatementScheduleView{}, err
	}
	x.ID, x.OwnerID, x.LastPeriod, x.CreatedAt, x.UpdatedAt = s.d.Random.ID(), owner.ID, w.Key, now, now
	if err := s.d.Store.StatementSchedules().Create(ctx, x); err != nil {
		return StatementScheduleView{}, fmt.Errorf("app.CreateStatementSchedule: %w", err)
	}
	s.audit(ctx, p, "statement.schedule_created", x.ID, describeSchedule(x))
	return s.scheduleView(ctx, x, owner)
}

// UpdateStatementSchedule changes a schedule (its owner or an administrator, web session;
// audited). A new period starts after the last complete period of that kind.
func (s *Service) UpdateStatementSchedule(ctx context.Context, p Principal, id string, in StatementScheduleInput) (StatementScheduleView, error) {
	if err := s.statementCaller(p); err != nil {
		return StatementScheduleView{}, err
	}
	cur, owner, err := s.owned(ctx, p, id)
	if err != nil {
		return StatementScheduleView{}, err
	}
	x, err := s.checkScheduleInput(ctx, owner, in)
	if err != nil {
		return StatementScheduleView{}, err
	}
	now := s.d.Clock.Now().UTC()
	x.ID, x.OwnerID, x.LastPeriod, x.UpdatedAt = cur.ID, cur.OwnerID, cur.LastPeriod, now
	x.CreatedAt, x.LastSentAt, x.LastError, x.ManualSentAt = cur.CreatedAt, cur.LastSentAt, cur.LastError, cur.ManualSentAt
	if x.Period != cur.Period || (x.Enabled && !cur.Enabled) {
		// A new kind of period, or switched back on: start with the next period that
		// closes, never a backlog.
		w, err := domain.LastCompleteWindow(x.Period, now)
		if err != nil {
			return StatementScheduleView{}, err
		}
		x.LastPeriod = w.Key
	}
	if err := s.d.Store.StatementSchedules().Update(ctx, x); err != nil {
		return StatementScheduleView{}, fmt.Errorf("app.UpdateStatementSchedule: %w", err)
	}
	s.audit(ctx, p, "statement.schedule_updated", x.ID, describeSchedule(x))
	return s.scheduleView(ctx, x, owner)
}

// DeleteStatementSchedule removes a schedule (its owner or an administrator, web session;
// audited). Mail already in the outbox is still delivered.
func (s *Service) DeleteStatementSchedule(ctx context.Context, p Principal, id string) error {
	if err := s.statementCaller(p); err != nil {
		return err
	}
	x, _, err := s.owned(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.d.Store.StatementSchedules().Delete(ctx, x.ID); err != nil {
		return fmt.Errorf("app.DeleteStatementSchedule: %w", err)
	}
	s.audit(ctx, p, "statement.schedule_deleted", x.ID, describeSchedule(x))
	return nil
}

func (s *Service) needStatements() error {
	if s.d.Statements == nil {
		return fmt.Errorf("trading statements are not available: %w", domain.ErrUnavailable)
	}
	return nil
}

// SendStatementNow e-mails the statements of the schedule's last complete period at once,
// whatever was sent before (its owner or an administrator, web session; audited; at most one
// a minute per schedule). It needs a mail server.
func (s *Service) SendStatementNow(ctx context.Context, p Principal, id string) (StatementSendResult, error) {
	if err := s.statementCaller(p); err != nil {
		return StatementSendResult{}, err
	}
	if err := s.needStatements(); err != nil {
		return StatementSendResult{}, err
	}
	x, owner, err := s.owned(ctx, p, id)
	if err != nil {
		return StatementSendResult{}, err
	}
	if _, _, ok, err := s.mailServer(ctx); err != nil {
		return StatementSendResult{}, fmt.Errorf("app.SendStatementNow: %w", err)
	} else if !ok || s.d.Mailer == nil {
		const msg = "No mail server: an administrator must set one up (E-mail) before statements can be sent"
		return StatementSendResult{}, &domain.MissingError{
			What: domain.MissingEmailServer, Msg: msg, CanFix: s.canFix(p, domain.AccountView{}, domain.MissingEmailServer),
			Err: fmt.Errorf("%s: %w", msg, domain.ErrUnavailable),
		}
	}
	now := s.d.Clock.Now().UTC()
	if !x.ManualSentAt.IsZero() && now.Sub(x.ManualSentAt) < domain.ManualStatementInterval {
		return StatementSendResult{}, fmt.Errorf("this schedule's statements were sent less than a minute ago: %w", domain.ErrBusy)
	}
	switch err := s.d.Store.StatementSchedules().MarkManual(ctx, x.ID, x.ManualSentAt, now); {
	case errors.Is(err, domain.ErrConflict):
		return StatementSendResult{}, fmt.Errorf("this schedule's statements are being sent: %w", domain.ErrBusy)
	case err != nil:
		return StatementSendResult{}, fmt.Errorf("app.SendStatementNow: %w", err)
	}
	w, err := domain.LastCompleteWindow(x.Period, now)
	if err != nil {
		return StatementSendResult{}, err
	}
	res, err := s.sendStatements(ctx, x, owner, w, func(string) string { return s.d.Random.ID() })
	if err != nil {
		return StatementSendResult{}, err
	}
	s.audit(ctx, p, "statement.sent_now", x.ID, fmt.Sprintf("%s %s to %s: %s", x.Period, w.Key, strings.Join(x.Recipients, ","),
		strings.Join(res.Files, ",")))
	return res, nil
}

// PreviewStatement renders one statement for download without e-mailing it (web session):
// the combined statement of every account the caller may put on a schedule, one of those
// accounts, or one exchange across them.
func (s *Service) PreviewStatement(ctx context.Context, p Principal, req StatementPreviewRequest) (StatementFile, error) {
	if err := s.statementCaller(p); err != nil {
		return StatementFile{}, err
	}
	if err := s.needStatements(); err != nil {
		return StatementFile{}, err
	}
	if !req.Period.Valid() {
		return StatementFile{}, domain.Invalid("period", "daily, weekly or monthly")
	}
	now := s.d.Clock.Now().UTC()
	w, err := domain.LastCompleteWindow(req.Period, now)
	if req.From != "" {
		day, perr := domain.ParseRiskDate(req.From)
		if perr != nil {
			return StatementFile{}, perr
		}
		w, err = domain.WindowFor(req.Period, day)
	}
	if err != nil {
		return StatementFile{}, err
	}
	if w.From.After(now) {
		return StatementFile{}, domain.Invalid("from", "the period has not started yet")
	}
	if w.To.Before(now.AddDate(0, 0, -domain.EquityRetentionDays)) {
		return StatementFile{}, domain.Invalid("from", "at most 400 days back")
	}
	accts, err := s.previewAccounts(ctx, p, req.ScheduleID)
	if err != nil {
		return StatementFile{}, err
	}
	name := ""
	switch req.Scope {
	case domain.ScopeCombined:
	case domain.ScopeAccount:
		i := slices.IndexFunc(accts, func(v domain.AccountView) bool { return v.ID == req.AccountID })
		if i < 0 {
			return StatementFile{}, fmt.Errorf("account: %w", domain.ErrNotFound)
		}
		accts, name = accts[i:i+1], accts[i].Name
	case domain.ScopeExchange:
		if !validExchange(req.Exchange) {
			return StatementFile{}, domain.Invalid("exchange", "LSE, NYSE, ASX or Other")
		}
		name = req.Exchange
	default:
		return StatementFile{}, domain.Invalid("scope", "combined, account or exchange")
	}
	inputs, err := s.statementInputs(ctx, accts, w)
	if err != nil {
		return StatementFile{}, err
	}
	st := domain.BuildStatement(domain.StatementInput{Window: w, Scope: req.Scope, Exchange: req.Exchange, GeneratedAt: now, Accounts: inputs})
	data, err := s.d.Statements.Render(ctx, st)
	if err != nil {
		return StatementFile{}, fmt.Errorf("app.PreviewStatement: %w", err)
	}
	return StatementFile{Name: domain.StatementFileName(w, req.Scope, name), Data: data}, nil
}

// previewAccounts are the accounts a preview may cover: those the caller may put on a
// schedule, or a schedule's accounts (its owner or an administrator).
func (s *Service) previewAccounts(ctx context.Context, p Principal, scheduleID string) ([]domain.AccountView, error) {
	if scheduleID == "" {
		return s.statementAccounts(ctx, p.UserID, p.Role)
	}
	x, owner, err := s.owned(ctx, p, scheduleID)
	if err != nil {
		return nil, err
	}
	allowed, err := s.statementAccounts(ctx, owner.ID, owner.Role)
	if err != nil {
		return nil, err
	}
	return scheduleAccounts(allowed, x.AccountIDs), nil
}

func validExchange(e string) bool {
	return e == domain.OtherExchange || slices.Contains(domain.Markets, domain.Market(e))
}

// SendDueStatements sends every enabled schedule's statements whose period has closed and
// was not handled yet (system: the statements loop, every few minutes). Each period is sent
// once: the e-mails get ids derived from the schedule, the period and the recipient (a
// second enqueue is refused), and the period is then recorded with a compare-and-set, so a
// restart or a second process never sends it again. Only the newest period is sent after a
// long stop (no backlog). It returns how many e-mails were enqueued.
func (s *Service) SendDueStatements(ctx context.Context) (int, error) {
	if s.d.Statements == nil {
		return 0, nil
	}
	xs, err := s.d.Store.StatementSchedules().List(ctx, "")
	if err != nil {
		return 0, fmt.Errorf("app.SendDueStatements: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	sent := 0
	for _, x := range xs {
		if err := ctx.Err(); err != nil {
			return sent, fmt.Errorf("app.SendDueStatements: %w", err)
		}
		w, err := domain.LastCompleteWindow(x.Period, now)
		if !x.Enabled || err != nil || x.LastPeriod >= w.Key {
			continue
		}
		owner, err := s.d.Store.Users().Get(ctx, x.OwnerID)
		if err != nil || owner.Disabled {
			continue // a disabled owner's statements wait until the account is enabled again
		}
		n, err := s.sendScheduled(ctx, x, owner, w)
		sent += n
		if err != nil {
			s.d.Log.WarnContext(ctx, "trading statement not sent", "schedule", x.ID, "period", w.Key, "err", err)
		}
	}
	return sent, nil
}

// sendScheduled sends one schedule's statements for w and records the period. A failure
// that sending again cannot fix (nothing to send, too large) is recorded with the period; any
// other failure leaves the period open for the next round.
func (s *Service) sendScheduled(ctx context.Context, x domain.StatementSchedule, owner domain.User, w domain.StatementWindow) (int, error) {
	sctx, cancel := context.WithTimeout(ctx, statementSendTimeout)
	defer cancel()
	res, err := s.sendStatements(sctx, x, owner, w, func(to string) string { return statementMailID(x.ID, w.Key, to) })
	lastErr := ""
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrTooLarge):
		lastErr = readableError(err)
	default:
		return 0, err
	}
	switch merr := s.d.Store.StatementSchedules().MarkPeriod(ctx, x.ID, x.LastPeriod, w.Key, s.d.Clock.Now().UTC(), lastErr); {
	case merr == nil, errors.Is(merr, domain.ErrConflict), errors.Is(merr, domain.ErrNotFound):
		// Conflict: another process recorded it (its mails had the same ids). Not found: the
		// schedule was deleted meanwhile.
	default:
		return res.Recipients, fmt.Errorf("app.sendScheduled: %w", merr)
	}
	if err != nil {
		return 0, err
	}
	s.audit(ctx, Principal{}, "statement.sent", x.ID, fmt.Sprintf("%s %s to %s: %s", x.Period, w.Key, strings.Join(x.Recipients, ","),
		strings.Join(res.Files, ",")))
	return res.Recipients, nil
}

// statementMailID is the outbox id of a scheduled statement e-mail: the same schedule, period
// and recipient always give the same id, so the outbox refuses a second copy.
func statementMailID(scheduleID, key, to string) string {
	sum := sha256.Sum256([]byte("statement\x00" + scheduleID + "\x00" + key + "\x00" + to))
	return hex.EncodeToString(sum[:])
}

// sendStatements builds the schedule's statements for w and enqueues one e-mail per
// recipient with every PDF attached; mailID names each e-mail.
func (s *Service) sendStatements(ctx context.Context, x domain.StatementSchedule, owner domain.User, w domain.StatementWindow,
	mailID func(to string) string,
) (StatementSendResult, error) {
	res := StatementSendResult{Window: w}
	allowed, err := s.statementAccounts(ctx, owner.ID, owner.Role)
	if err != nil {
		return res, err
	}
	accts := scheduleAccounts(allowed, x.AccountIDs)
	if len(accts) == 0 {
		return res, domain.Invalid("accountIds", "none of the schedule's accounts can be on a statement any more")
	}
	inputs, err := s.statementInputs(ctx, accts, w)
	if err != nil {
		return res, err
	}
	now := s.d.Clock.Now().UTC()
	build := func(scope domain.StatementScope, exchange string, in []domain.StatementAccountInput) domain.Statement {
		return domain.BuildStatement(domain.StatementInput{Window: w, Scope: scope, Exchange: exchange, GeneratedAt: now, Accounts: in})
	}
	combined := build(domain.ScopeCombined, "", inputs)
	var files []domain.Attachment
	var lines []string
	add := func(st domain.Statement, name string) error {
		data, err := s.d.Statements.Render(ctx, st)
		if err != nil {
			return fmt.Errorf("app.sendStatements: %w", err)
		}
		fn := domain.StatementFileName(w, st.Scope, name)
		for n := 2; slices.ContainsFunc(files, func(a domain.Attachment) bool { return a.Filename == fn }); n++ {
			fn = strings.TrimSuffix(domain.StatementFileName(w, st.Scope, name), ".pdf") + fmt.Sprintf("-%d.pdf", n)
		}
		files = append(files, domain.Attachment{Filename: fn, ContentType: "application/pdf", Data: data})
		lines = append(lines, "- "+fn+" ("+st.Title+")")
		res.Files = append(res.Files, fn)
		return nil
	}
	if x.IncludeCombined {
		if err := add(combined, ""); err != nil {
			return res, err
		}
	}
	if x.IncludeAccounts {
		for _, in := range inputs {
			if err := add(build(domain.ScopeAccount, "", []domain.StatementAccountInput{in}), in.Account.Name); err != nil {
				return res, err
			}
		}
	}
	if x.IncludeExchanges {
		for _, e := range statementExchanges(inputs) {
			if err := add(build(domain.ScopeExchange, e, inputs), e); err != nil {
				return res, err
			}
		}
	}
	if len(files) == 0 {
		return res, domain.Invalid("include", "no statement to send: no exchange had trades or positions in the period")
	}
	if err := domain.ValidateAttachments(files); err != nil {
		return res, fmt.Errorf("the statements are too large for one e-mail (more than 10 MiB): %w", err)
	}
	subject := "[cactusTrade] " + w.Period.Label() + " trading statement: " + w.Title()
	body := w.Period.Label() + " trading statement, " + w.Title() + ".\n\n" + combined.Summary + "\n\nAttached:\n" + strings.Join(lines, "\n") +
		"\n\nYou get this because " + owner.Username + " set up a " + string(w.Period) + " trading statement in cactusTrade " +
		"(System → Trading statements) and entered this address.\n" +
		"cactusTrade is software, not financial advice — trading can lose money."
	for _, to := range x.Recipients {
		m := domain.OutboxMail{Mail: domain.Mail{ID: mailID(to), To: to, Subject: subject, Body: body, Attachments: files}, CreatedAt: now, NextAt: now}
		switch err := s.d.Store.Outbox().Enqueue(ctx, m); {
		case err == nil:
			res.Recipients++
		case errors.Is(err, domain.ErrConflict): // already queued for this period: never twice
		default:
			return res, fmt.Errorf("app.sendStatements: %w", err)
		}
	}
	return res, nil
}

// statementExchanges are the exchanges with trades or open positions in the inputs, in the
// order of domain.Markets, then Other.
func statementExchanges(inputs []domain.StatementAccountInput) []string {
	seen := map[string]bool{}
	mark := func(m domain.Market) {
		if m == "" {
			seen[domain.OtherExchange] = true
		} else {
			seen[string(m)] = true
		}
	}
	for _, in := range inputs {
		for _, t := range in.Trades {
			mark(t.Market)
		}
		for _, p := range in.OpenPositions {
			mark(p.Market)
		}
	}
	var out []string
	for _, m := range domain.Markets {
		if seen[string(m)] {
			out = append(out, string(m))
		}
	}
	if seen[domain.OtherExchange] {
		out = append(out, domain.OtherExchange)
	}
	return out
}

// statementInputs reads what a statement shows about each account for w, one account after
// the other.
func (s *Service) statementInputs(ctx context.Context, accts []domain.AccountView, w domain.StatementWindow) ([]domain.StatementAccountInput, error) {
	share := s.shareInfo(ctx)
	out := make([]domain.StatementAccountInput, 0, len(accts))
	for _, v := range accts {
		in, err := s.statementInput(ctx, v.Account, w, share)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

// shareInfo names instruments from the instrument cache with their exchange (the cached
// market, else the symbol's suffix), remembering each answer for the call.
func (s *Service) shareInfo(ctx context.Context) func(domain.InstrumentID) (string, domain.Market) {
	type info struct {
		sym string
		m   domain.Market
	}
	seen := map[domain.InstrumentID]info{}
	return func(id domain.InstrumentID) (string, domain.Market) {
		if x, ok := seen[id]; ok {
			return x.sym, x.m
		}
		var x info
		if in, _, err := s.d.Store.Instruments().Get(ctx, id); err == nil {
			x.sym, x.m = in.Symbol, in.Market
			if x.m == "" {
				x.m, _ = domain.MarketForSymbol(in.Symbol)
			}
		}
		seen[id] = x
		return x.sym, x.m
	}
}

func (s *Service) statementInput(ctx context.Context, a domain.Account, w domain.StatementWindow,
	share func(domain.InstrumentID) (string, domain.Market),
) (domain.StatementAccountInput, error) {
	in := domain.StatementAccountInput{Account: a}
	snaps, err := s.snapshots(ctx, a.ID, w.From, w.To)
	if err != nil {
		return in, err
	}
	in.Snapshots = snaps
	if !a.Tradable() {
		actx, cancel := context.WithTimeout(ctx, equityTimeout)
		h, err := s.holdings(actx, a, false)
		cancel()
		switch {
		case err == nil:
			in.Holdings = &h
		case errors.Is(err, domain.ErrCredentialsMissing):
			in.HoldingsError = "no keys yet"
		default:
			in.HoldingsError = readableError(err)
		}
		return in, nil
	}
	if err := s.statementTrades(ctx, &in, w, share); err != nil {
		return in, err
	}
	if err := s.statementPositions(ctx, &in, w, share); err != nil {
		return in, err
	}
	if err := s.statementActivity(ctx, &in, w); err != nil {
		return in, err
	}
	return in, nil
}

func (s *Service) statementTrades(ctx context.Context, in *domain.StatementAccountInput, w domain.StatementWindow,
	share func(domain.InstrumentID) (string, domain.Market),
) error {
	ts, err := s.closedTradeRows(ctx, in.Account.ID, w.From, w.To, nil)
	if err != nil {
		return err
	}
	for _, t := range ts {
		sym, m := share(t.Instrument)
		in.Trades = append(in.Trades, domain.StatementTrade{Trade: t, Symbol: sym, Market: m})
	}
	return nil
}

// statementPositions finds cactusTrade's positions open at the end of the period: opened
// before it and closed at or after it (or not yet).
func (s *Service) statementPositions(ctx context.Context, in *domain.StatementAccountInput, w domain.StatementWindow,
	share func(domain.InstrumentID) (string, domain.Market),
) error {
	ps, err := s.d.Store.AgentPositions().ListSince(ctx, in.Account.ID, w.From.Add(-statementPositionLookback))
	if err != nil {
		return fmt.Errorf("app.statement: %w", err)
	}
	open, err := s.d.Store.AgentPositions().ListOpen(ctx, in.Account.ID)
	if err != nil {
		return fmt.Errorf("app.statement: %w", err)
	}
	seen := map[int64]bool{}
	for _, p := range append(ps, open...) {
		if seen[p.PositionID] || !p.OpenedAt.Before(w.To) || (!p.ClosedAt.IsZero() && p.ClosedAt.Before(w.To)) {
			continue
		}
		seen[p.PositionID] = true
		sym, m := share(p.Instrument)
		in.OpenPositions = append(in.OpenPositions, domain.StatementPosition{
			PositionID: p.PositionID, Symbol: sym, Market: m, Direction: p.Direction, OpenedAt: p.OpenedAt,
		})
	}
	return nil
}

// statementActivity counts decisions, orders, risk events and LLM calls in the period.
func (s *Service) statementActivity(ctx context.Context, in *domain.StatementAccountInput, w domain.StatementWindow) error {
	id := in.Account.ID
	ds, err := s.d.Store.Decisions().List(ctx, id, w.To, "", statementActivityCap+1)
	if err != nil {
		return fmt.Errorf("app.statement: %w", err)
	}
	if len(ds) > statementActivityCap {
		ds = ds[:statementActivityCap]
		in.ActivityCapped = !ds[len(ds)-1].At.Before(w.From)
	}
	in.Decisions = map[domain.DecisionOutcome]int{}
	for _, d := range ds {
		if !d.At.Before(w.From) {
			in.Decisions[d.Outcome]++
		}
	}
	os, err := s.d.Store.Orders().ListSince(ctx, id, w.From)
	if err != nil {
		return fmt.Errorf("app.statement: %w", err)
	}
	in.Orders = map[domain.OrderState]int{}
	for _, o := range os {
		if o.CreatedAt.Before(w.To) {
			in.Orders[o.State]++
		}
	}
	es, err := s.d.Store.Events().List(ctx, []string{id}, statementActivityCap)
	if err != nil {
		return fmt.Errorf("app.statement: %w", err)
	}
	if len(es) == statementActivityCap && !es[len(es)-1].At.Before(w.From) {
		in.ActivityCapped = true
	}
	for _, e := range es {
		if e.AccountID != id || !w.Contains(e.At) {
			continue
		}
		switch e.Kind {
		case domain.EventKillSwitch:
			in.KillSwitches++
		case domain.EventDailyStop:
			in.DailyStops++
		}
	}
	u, err := s.d.Store.LLMCalls().UsageFor(ctx, []string{id}, w.From, w.To)
	if err != nil {
		return fmt.Errorf("app.statement: %w", err)
	}
	in.LLM = u
	return nil
}
