package httpapi

import (
	"errors"
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

func toEmailSetup(e app.EmailSetup) EmailSetup {
	out := EmailSetup{
		Enabled: e.Settings.Enabled, Host: e.Settings.Host, Port: e.Settings.Port, Username: e.Settings.Username,
		From: e.Settings.From, Tls: e.Settings.TLS, PasswordSet: e.PasswordSet, Warnings: e.Warnings,
		Fallback: e.Fallback, Presets: make([]SmtpPreset, 0, len(domain.SMTPPresets)),
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, p := range domain.SMTPPresets {
		out.Presets = append(out.Presets, SmtpPreset{Id: p.ID, Label: p.Label, Host: p.Host, Port: p.Port, Tls: p.TLS})
	}
	return out
}

// GetEmailSetup returns the mail server.
func (s *Server) GetEmailSetup(w http.ResponseWriter, r *http.Request) {
	e, err := s.svc.EmailSetup(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEmailSetup(e))
}

// SetEmailSetup stores the mail server.
func (s *Server) SetEmailSetup(w http.ResponseWriter, r *http.Request) {
	var req EmailSetupInput
	if !decode(w, r, &req) {
		return
	}
	st := domain.SMTPSettings{Enabled: req.Enabled, Host: req.Host, Port: req.Port, Username: req.Username, From: req.From, TLS: string(req.Tls)}
	e, err := s.svc.SetEmailSetup(r.Context(), state(r).principal, st, req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEmailSetup(e))
}

// SendTestEmail sends a test message now.
func (s *Server) SendTestEmail(w http.ResponseWriter, r *http.Request) {
	var req TestEmailRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.svc.SendTestEmail(r.Context(), state(r).principal, req.To); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListOutbox lists the newest mail.
func (s *Server) ListOutbox(w http.ResponseWriter, r *http.Request) {
	ms, err := s.svc.Outbox(r.Context(), state(r).principal, 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]OutboxMail, 0, len(ms))
	for _, m := range ms {
		o := OutboxMail{Id: m.ID, To: m.To, Subject: m.Subject, CreatedAt: m.CreatedAt, Attempts: m.Attempts, LastError: m.LastError}
		if !m.SentAt.IsZero() {
			o.SentAt = ptr(m.SentAt)
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
}

func toProfile(p app.Profile) Profile {
	return Profile{Email: p.Email, Notify: NotifyPrefs{
		Trades: p.Notify[domain.NotifyTrades], Watchlist: p.Notify[domain.NotifyWatchlist], Alerts: p.Notify[domain.NotifyAlerts],
	}}
}

// GetMyProfile returns the caller's e-mail and notification choices.
func (s *Server) GetMyProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.MyProfile(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toProfile(p))
}

// SetMyProfile changes the caller's e-mail and notification choices. A current password sent
// along (needed for a new e-mail address, PLAN 7.1.11) is limited like a sign-in.
func (s *Server) SetMyProfile(w http.ResponseWriter, r *http.Request) {
	var req ProfileInput
	if !decode(w, r, &req) {
		return
	}
	st := state(r)
	pw := deref(req.CurrentPassword)
	if pw != "" && !s.signInAttempt(w, r, st.ip, st.principal.Username) {
		return
	}
	p, err := s.svc.SetMyProfile(r.Context(), st.principal, app.Profile{Email: req.Email, Notify: domain.NotifyPrefs{
		domain.NotifyTrades: req.Notify.Trades, domain.NotifyWatchlist: req.Notify.Watchlist, domain.NotifyAlerts: req.Notify.Alerts,
	}}, pw)
	if pw != "" {
		switch {
		case err == nil:
			s.limiter.Success(st.ip, st.principal.Username)
		case !isWrongPassword(err):
			s.limiter.Refund(st.ip, st.principal.Username)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toProfile(p))
}

// RequestPasswordReset e-mails a reset link. Requests are limited per address and per user
// name in their own buckets: they never count as failed sign-ins (PLAN 7.1.5).
func (s *Server) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req PasswordResetRequest
	if !decode(w, r, &req) {
		return
	}
	ip := state(r).ip
	if ok, wait := s.limiter.ResetRequest(ip, domain.NormalizeUsername(req.Username)); !ok {
		tooMany(w, r, wait)
		return
	}
	if err := s.svc.RequestPasswordReset(r.Context(), req.Username, ip); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ConfirmPasswordReset sets a new password with a link's token. Bad tokens are limited per
// address only (PLAN 7.1.5).
func (s *Server) ConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req PasswordResetConfirm
	if !decode(w, r, &req) {
		return
	}
	ip := state(r).ip
	if ok, wait := s.limiter.Allowed(confirmIP, ip); !ok {
		tooMany(w, r, wait)
		return
	}
	err := s.svc.ResetPassword(r.Context(), req.Token, req.Password, ip)
	var ve *domain.ValidationError
	if errors.As(err, &ve) && ve.Field == "token" {
		s.limiter.Failed(confirmIP, ip)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
