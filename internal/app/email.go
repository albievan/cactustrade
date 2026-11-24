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

// E-mail, the cactusInv way (owner's request 2026-11-23): an outgoing mail server set in the
// app (Gmail by default, with an app password), a fallback server from the deployment, an
// outbox with retries, notifications per user, and password reset by e-mail.

const (
	settingSMTP      = "email.smtp"
	credSMTPPassword = domain.CredentialName("email.smtp_password")
	mailRetention    = 30 * 24 * time.Hour
	mailBatch        = 20
)

func notifyKey(userID string) string { return "user." + userID + ".notify" }

type smtpJSON struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	From     string `json:"from"`
	TLS      string `json:"tls"`
}

// EmailSetup is the e-mail page (never the password).
type EmailSetup struct {
	Settings    domain.SMTPSettings
	PasswordSet bool
	Warnings    []string
	// Fallback describes the deployment's server ("" = none); used while the in-app
	// server is switched off.
	Fallback string
}

func (s *Service) smtpSettings(ctx context.Context) (domain.SMTPSettings, bool, error) {
	v, err := s.d.Store.Settings().Get(ctx, settingSMTP)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.DefaultSMTP(), false, nil
	}
	if err != nil {
		return domain.SMTPSettings{}, false, err
	}
	var j smtpJSON
	if err := json.Unmarshal([]byte(v), &j); err != nil {
		return domain.SMTPSettings{}, false, fmt.Errorf("stored e-mail settings are unreadable: %w", err)
	}
	return domain.SMTPSettings{Enabled: j.Enabled, Host: j.Host, Port: j.Port, Username: j.Username, From: j.From, TLS: j.TLS}, true, nil
}

func (s *Service) smtpPassword(ctx context.Context) (string, error) {
	pw, err := s.d.Secrets.Get(ctx, credSMTPPassword)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	return pw, err
}

func fallbackText(st domain.SMTPSettings) string {
	return fmt.Sprintf("%s:%d (%s)", st.Host, st.Port, st.TLS)
}

// EmailSetup returns the mail server configuration (admin).
func (s *Service) EmailSetup(ctx context.Context, p Principal) (EmailSetup, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return EmailSetup{}, err
	}
	st, _, err := s.smtpSettings(ctx)
	if err != nil {
		return EmailSetup{}, fmt.Errorf("app.EmailSetup: %w", err)
	}
	pw, err := s.smtpPassword(ctx)
	if err != nil {
		return EmailSetup{}, fmt.Errorf("app.EmailSetup: %w", err)
	}
	out := EmailSetup{Settings: st, PasswordSet: pw != "", Warnings: domain.EmailSenderWarnings(st)}
	if s.d.MailFallback != nil {
		if fb, _, ok := s.d.MailFallback(); ok {
			out.Fallback = fallbackText(fb)
		}
	}
	return out, nil
}

// SetEmailSetup saves the mail server (admin, web session; audited). password nil keeps the
// stored one, "" removes it. The stored password is never sent to another server or user:
// changing those needs the password again.
func (s *Service) SetEmailSetup(ctx context.Context, p Principal, st domain.SMTPSettings, password *string) (EmailSetup, error) {
	if err := s.llmAdmin(p); err != nil {
		return EmailSetup{}, err
	}
	st = st.Normalize()
	if err := st.Validate(); err != nil {
		return EmailSetup{}, err
	}
	cur, saved, err := s.smtpSettings(ctx)
	if err != nil {
		return EmailSetup{}, fmt.Errorf("app.SetEmailSetup: %w", err)
	}
	pw, err := s.smtpPassword(ctx)
	if err != nil {
		return EmailSetup{}, fmt.Errorf("app.SetEmailSetup: %w", err)
	}
	if password == nil && pw != "" && saved && (cur.Host != st.Host || cur.Username != st.Username) {
		return EmailSetup{}, domain.Invalid("password", "enter the password again: the stored one is for "+cur.Username+" at "+cur.Host)
	}
	if password != nil {
		pw = *password
		if len(pw) > 1024 || strings.ContainsAny(pw, "\r\n") {
			return EmailSetup{}, domain.Invalid("password", "at most 1024 characters, one line")
		}
	}
	if st.Enabled && st.Username != "" && pw == "" {
		return EmailSetup{}, domain.Invalid("password", "is required with a user name (for Gmail: an app password)")
	}
	if password != nil {
		if pw == "" {
			if err := s.d.Secrets.Delete(ctx, credSMTPPassword); err != nil && !errors.Is(err, domain.ErrNotFound) {
				return EmailSetup{}, fmt.Errorf("app.SetEmailSetup: %w", err)
			}
		} else if err := s.d.Secrets.Put(ctx, credSMTPPassword, pw); err != nil {
			return EmailSetup{}, fmt.Errorf("app.SetEmailSetup: %w", err)
		}
	}
	b, err := json.Marshal(smtpJSON{Enabled: st.Enabled, Host: st.Host, Port: st.Port, Username: st.Username, From: st.From, TLS: st.TLS})
	if err != nil {
		return EmailSetup{}, fmt.Errorf("app.SetEmailSetup: %w", err)
	}
	if err := s.d.Store.Settings().Set(ctx, settingSMTP, string(b), s.d.Clock.Now().UTC()); err != nil {
		return EmailSetup{}, fmt.Errorf("app.SetEmailSetup: %w", err)
	}
	detail := fmt.Sprintf("%s:%d as %s, enabled=%t", st.Host, st.Port, st.Username, st.Enabled)
	if password != nil {
		detail += ", password changed"
	}
	s.audit(ctx, p, "email.server_updated", "email", detail)
	return s.EmailSetup(ctx, p)
}

// mailServer picks the server mail goes through: the in-app one when switched on, else the
// deployment's fallback; ok is false when there is none (mail waits in the outbox).
func (s *Service) mailServer(ctx context.Context) (domain.SMTPSettings, string, bool, error) {
	st, _, err := s.smtpSettings(ctx)
	if err != nil {
		return domain.SMTPSettings{}, "", false, err
	}
	if st.Enabled {
		pw, err := s.smtpPassword(ctx)
		if err != nil {
			return domain.SMTPSettings{}, "", false, err
		}
		return st, pw, true, nil
	}
	if s.d.MailFallback != nil {
		if fb, pw, ok := s.d.MailFallback(); ok {
			return fb, pw, true, nil
		}
	}
	return domain.SMTPSettings{}, "", false, nil
}

// MailError is a failure of the mail server itself (not of the input or the database).
type MailError struct{ Err error }

func (e *MailError) Error() string { return e.Err.Error() }
func (e *MailError) Unwrap() error { return e.Err }

// SendTestEmail sends a test message straight away (not through the outbox) so the admin
// sees at once whether the server works (admin, web session; audited).
func (s *Service) SendTestEmail(ctx context.Context, p Principal, to string) error {
	if err := s.llmAdmin(p); err != nil {
		return err
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return domain.Invalid("to", "enter an e-mail address")
	}
	if err := domain.ValidateEmail("to", to); err != nil {
		return err
	}
	if s.d.Mailer == nil {
		return fmt.Errorf("e-mail is not available: %w", domain.ErrUnavailable)
	}
	server, pw, ok, err := s.mailServer(ctx)
	if err != nil {
		return fmt.Errorf("app.SendTestEmail: %w", err)
	}
	if !ok {
		const msg = "No mail server: fill in the server and tick \"Use this server\""
		return &domain.MissingError{What: domain.MissingEmailServer, Msg: msg, CanFix: true, Err: domain.Invalid("enabled", msg)}
	}
	err = s.d.Mailer.Send(ctx, server, pw, domain.Mail{
		ID: s.d.Random.ID(), To: to, Subject: "cactusTrade test e-mail",
		Body: "This is a test e-mail from cactusTrade. Your mail server settings work.\n\n— cactusTrade (software, not financial advice)",
	})
	if err != nil {
		s.audit(ctx, p, "email.test_failed", "email", "to "+to)
		return &MailError{Err: err}
	}
	s.audit(ctx, p, "email.test_sent", "email", "to "+to)
	return nil
}

// enqueue puts a message in the outbox (logged, never fatal to the caller).
func (s *Service) enqueue(ctx context.Context, to, subject, body string) {
	now := s.d.Clock.Now().UTC()
	m := domain.OutboxMail{Mail: domain.Mail{ID: s.d.Random.ID(), To: to, Subject: subject, Body: body}, CreatedAt: now, NextAt: now}
	if err := s.d.Store.Outbox().Enqueue(ctx, m); err != nil {
		s.d.Log.ErrorContext(ctx, "outbox write failed", "err", err)
	}
}

// DeliverMail sends the outbox's due mail (called every few seconds by the runner). It
// returns how many were sent. Without a server nothing is tried and mail waits.
func (s *Service) DeliverMail(ctx context.Context) (int, error) {
	if s.d.Mailer == nil {
		return 0, nil
	}
	server, pw, ok, err := s.mailServer(ctx)
	if err != nil || !ok {
		return 0, err
	}
	due, err := s.d.Store.Outbox().Due(ctx, s.d.Clock.Now().UTC(), mailBatch)
	if err != nil {
		return 0, fmt.Errorf("app.DeliverMail: %w", err)
	}
	sent := 0
	for _, m := range due {
		if ctx.Err() != nil {
			return sent, ctx.Err()
		}
		if err := s.d.Mailer.Send(ctx, server, pw, m.Mail); err != nil {
			n := m.Attempts + 1
			if ferr := s.d.Store.Outbox().MarkFailed(ctx, m.ID, n, s.d.Clock.Now().UTC().Add(domain.MailRetryDelay(n)), err.Error()); ferr != nil {
				s.d.Log.ErrorContext(ctx, "outbox update failed", "id", m.ID, "err", ferr)
			}
			s.d.Log.WarnContext(ctx, "mail not sent", "id", m.ID, "attempt", n, "err", err)
			continue
		}
		// Accepted: record it. If recording fails the message could be sent again, so stop
		// this round rather than risk sending the rest twice as well.
		if err := s.d.Store.Outbox().MarkSent(ctx, m.ID, s.d.Clock.Now().UTC()); err != nil {
			return sent, fmt.Errorf("app.DeliverMail: %w", err)
		}
		sent++
	}
	return sent, nil
}

// PurgeOldMail deletes outbox entries past the retention period (called periodically).
func (s *Service) PurgeOldMail(ctx context.Context) (int64, error) {
	n, err := s.d.Store.Outbox().DeleteBefore(ctx, s.d.Clock.Now().Add(-mailRetention))
	if err != nil {
		return 0, err
	}
	m, err := s.d.Store.PasswordResets().DeleteBefore(ctx, s.d.Clock.Now().Add(-24*time.Hour))
	return n + m, err
}

// Outbox lists the newest mail (admin), without bodies' secrets (bodies hold none).
func (s *Service) Outbox(ctx context.Context, p Principal, limit int) ([]domain.OutboxMail, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.d.Store.Outbox().List(ctx, limit)
}

// ---- notifications --------------------------------------------------------------------

// NotifyPrefs returns a user's notification choices (defaults when never saved).
func (s *Service) notifyPrefs(ctx context.Context, userID string) domain.NotifyPrefs {
	v, err := s.d.Store.Settings().Get(ctx, notifyKey(userID))
	if err != nil {
		return domain.DefaultNotifyPrefs()
	}
	var m map[string]bool
	if json.Unmarshal([]byte(v), &m) != nil {
		return domain.DefaultNotifyPrefs()
	}
	out := domain.NotifyPrefs{}
	for _, c := range domain.NotifyCategories {
		out[c] = m[string(c)]
	}
	return out
}

// Profile is the caller's own e-mail address and notification choices.
type Profile struct {
	Email  string
	Notify domain.NotifyPrefs
}

// MyProfile returns the caller's e-mail and notification choices.
func (s *Service) MyProfile(ctx context.Context, p Principal) (Profile, error) {
	u, err := s.Me(ctx, p)
	if err != nil {
		return Profile{}, err
	}
	return Profile{Email: u.Email, Notify: s.notifyPrefs(ctx, u.ID)}, nil
}

// SetMyProfile changes the caller's e-mail and notification choices (web session; audited).
// A new e-mail address needs the caller's current password (PLAN 7.1.11); notification
// choices alone need none.
func (s *Service) SetMyProfile(ctx context.Context, p Principal, pr Profile, currentPassword string) (Profile, error) {
	if p.UserID == "" || p.Via != ViaSession {
		return Profile{}, domain.ErrForbidden
	}
	if p.MustChangePassword {
		return Profile{}, domain.ErrPasswordChangeRequired
	}
	pr.Email = strings.TrimSpace(pr.Email)
	if err := domain.ValidateEmail("email", pr.Email); err != nil {
		return Profile{}, err
	}
	u, err := s.d.Store.Users().Get(ctx, p.UserID)
	if err != nil {
		return Profile{}, fmt.Errorf("app.SetMyProfile: %w", err)
	}
	// The address receives password-reset links: a stolen session must not be able to
	// redirect them (PLAN 7.1.11). A password sent along is always checked, so that a
	// right answer means one was verified (the HTTP limiter relies on it).
	if u.Email != pr.Email && currentPassword == "" {
		return Profile{}, domain.Invalid("currentPassword", "is needed to change the e-mail address")
	}
	if currentPassword != "" {
		ok, err := s.d.Hasher.Verify(u.PasswordHash, currentPassword)
		if err != nil {
			return Profile{}, fmt.Errorf("app.SetMyProfile: %w", err)
		}
		if !ok {
			return Profile{}, domain.Invalid("currentPassword", "is not correct")
		}
	}
	if u.Email != pr.Email {
		u.Email, u.UpdatedAt = pr.Email, s.d.Clock.Now().UTC()
		if err := s.d.Store.Users().Update(ctx, u); err != nil {
			return Profile{}, fmt.Errorf("app.SetMyProfile: %w", err)
		}
		s.audit(ctx, p, "user.email_changed", u.ID, "")
	}
	m := map[string]bool{}
	for _, c := range domain.NotifyCategories {
		m[string(c)] = pr.Notify[c]
	}
	b, err := json.Marshal(m)
	if err != nil {
		return Profile{}, fmt.Errorf("app.SetMyProfile: %w", err)
	}
	if err := s.d.Store.Settings().Set(ctx, notifyKey(u.ID), string(b), s.d.Clock.Now().UTC()); err != nil {
		return Profile{}, fmt.Errorf("app.SetMyProfile: %w", err)
	}
	return s.MyProfile(ctx, p)
}

// notify e-mails an event to the users who can see it, have an address and asked for its
// category. System-wide events go to administrators only. Failures are logged.
func (s *Service) notify(ctx context.Context, e domain.Event) {
	cat := domain.CategoryOf(e)
	if cat == "" {
		return
	}
	var users []domain.User
	if e.AccountID == "" {
		all, err := s.d.Store.Users().List(ctx)
		if err != nil {
			s.d.Log.WarnContext(ctx, "notify: listing users failed", "err", err)
			return
		}
		for _, u := range all {
			if u.Role.Allows(domain.PermManageSettings) {
				users = append(users, u)
			}
		}
	} else {
		a, err := s.d.Store.Accounts().Get(ctx, e.AccountID)
		if err != nil {
			s.d.Log.WarnContext(ctx, "notify: account lookup failed", "account", e.AccountID, "err", err)
			return
		}
		ids := []string{a.OwnerID}
		shares, err := s.d.Store.Accounts().Shares(ctx, a.ID)
		if err != nil {
			s.d.Log.WarnContext(ctx, "notify: shares lookup failed", "account", a.ID, "err", err)
			return
		}
		for _, sh := range shares {
			ids = append(ids, sh.UserID)
		}
		for _, id := range ids {
			if u, err := s.d.Store.Users().Get(ctx, id); err == nil {
				users = append(users, u)
			}
		}
	}
	for _, u := range users {
		if u.Disabled || u.Email == "" || !s.notifyPrefs(ctx, u.ID)[cat] {
			continue
		}
		body := e.Title + "\n"
		if e.Detail != "" {
			body += "\n" + e.Detail + "\n"
		}
		body += "\nWhen: " + e.At.Format("Mon 2 Jan 2006 15:04 MST") + "\nBy: " + e.Actor +
			"\n\nYou get this because you asked for " + string(cat) + " e-mails (Your account in cactusTrade).\n" +
			"cactusTrade is software, not financial advice — trading can lose money."
		s.enqueue(ctx, u.Email, "[cactusTrade] "+e.Title, body)
	}
}

// ---- password reset -------------------------------------------------------------------

// PasswordResetAvailable reports whether "Forgot password" can work: a public address for
// the link and a mail server.
func (s *Service) PasswordResetAvailable(ctx context.Context) bool {
	if s.cfg.PublicURL == "" || s.d.Mailer == nil {
		return false
	}
	_, _, ok, err := s.mailServer(ctx)
	return err == nil && ok
}

// RequestPasswordReset e-mails a reset link when the user exists, is enabled and has an
// e-mail address. It answers the same in every case (no user enumeration) and sends at most
// domain.PasswordResetPerHour links per user per hour.
func (s *Service) RequestPasswordReset(ctx context.Context, username, remoteIP string) error {
	if !s.PasswordResetAvailable(ctx) {
		// Asked before signing in: nobody here can fix it (CanFix false); an admin can.
		what, msg := domain.MissingEmailServer, "Password reset by e-mail is not set up: an admin must set up a mail server"
		if s.cfg.PublicURL == "" {
			what, msg = domain.MissingPublicURL, "Password reset by e-mail is not set up: the server's public address (CACTUSTRADE_PUBLIC_URL) is not set"
		}
		return domain.MissingSetup(what, msg, fmt.Errorf("password reset by e-mail is not set up: %w", domain.ErrUnavailable))
	}
	u, err := s.d.Store.Users().GetByUsername(ctx, domain.NormalizeUsername(username))
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("app.RequestPasswordReset: %w", err)
	}
	if u.Disabled || u.Email == "" {
		return nil
	}
	now := s.d.Clock.Now().UTC()
	n, err := s.d.Store.PasswordResets().CountSince(ctx, u.ID, now.Add(-time.Hour))
	if err != nil {
		return fmt.Errorf("app.RequestPasswordReset: %w", err)
	}
	if n >= domain.PasswordResetPerHour {
		return nil
	}
	token := s.d.Random.Token()
	if err := s.d.Store.PasswordResets().Create(ctx, domain.PasswordReset{
		TokenHash: hashToken(token), UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(domain.PasswordResetTTL),
	}); err != nil {
		return fmt.Errorf("app.RequestPasswordReset: %w", err)
	}
	link := strings.TrimRight(s.cfg.PublicURL, "/") + "/reset-password#token=" + token
	s.enqueue(ctx, u.Email, "[cactusTrade] Reset your password",
		"Someone (hopefully you) asked to reset the cactusTrade password of "+u.Username+".\n\n"+
			"Choose a new password here (the link works once, for 30 minutes):\n"+link+"\n\n"+
			"If it was not you, ignore this e-mail: your password stays as it is.")
	s.audit(ctx, Principal{UserID: u.ID, Username: u.Username, RemoteIP: remoteIP}, "auth.password_reset_requested", u.ID, "")
	return nil
}

// ResetPassword sets a new password with a reset link's token; the link is used up, every
// session of the user ends, and the default-password flag clears.
func (s *Service) ResetPassword(ctx context.Context, token, next, remoteIP string) error {
	if len(token) < domain.PasswordResetMinLength || len(token) > 256 {
		return domain.Invalid("token", "the link is not valid (ask for a new one)")
	}
	h := hashToken(token)
	r, err := s.d.Store.PasswordResets().Get(ctx, h)
	now := s.d.Clock.Now().UTC()
	if err != nil || !r.UsedAt.IsZero() || !now.Before(r.ExpiresAt) {
		return domain.Invalid("token", "the link has expired or was already used (ask for a new one)")
	}
	u, err := s.d.Store.Users().Get(ctx, r.UserID)
	if err != nil || u.Disabled {
		return domain.Invalid("token", "the link is not valid (ask for a new one)")
	}
	if err := domain.ValidatePassword(next, u.Username); err != nil {
		return err
	}
	if err := s.d.Store.PasswordResets().Use(ctx, h, now); err != nil {
		return domain.Invalid("token", "the link has expired or was already used (ask for a new one)")
	}
	if u.PasswordHash, err = s.d.Hasher.Hash(next); err != nil {
		return fmt.Errorf("app.ResetPassword: %w", err)
	}
	u.MustChangePassword, u.UpdatedAt = false, now
	if err := s.d.Store.Users().Update(ctx, u); err != nil {
		return fmt.Errorf("app.ResetPassword: %w", err)
	}
	if err := s.d.Store.Sessions().DeleteForUser(ctx, u.ID); err != nil {
		return fmt.Errorf("app.ResetPassword: %w", err)
	}
	if err := s.d.Store.PasswordResets().DeleteForUser(ctx, u.ID); err != nil {
		return fmt.Errorf("app.ResetPassword: %w", err)
	}
	s.audit(ctx, Principal{UserID: u.ID, Username: u.Username, RemoteIP: remoteIP}, "auth.password_reset", u.ID, "")
	return nil
}
