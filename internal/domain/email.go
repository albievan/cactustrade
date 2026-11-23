package domain

import (
	"net/mail"
	"strings"
	"time"
)

// TLS modes of an outgoing mail server.
const (
	SMTPStartTLS = "starttls" // plain connection upgraded with STARTTLS (port 587)
	SMTPTLS      = "tls"      // TLS from the first byte (port 465)
	SMTPNone     = "none"     // no encryption: only a test server on this machine or network
)

// SMTPSettings is an outgoing mail server. The password is a separate write-only secret.
type SMTPSettings struct {
	Enabled  bool
	Host     string
	Port     int
	Username string
	From     string // "cactusTrade <trader@example.com>"
	TLS      string // SMTPStartTLS | SMTPTLS | SMTPNone
}

// SMTPPreset is a provider the e-mail page can fill in.
type SMTPPreset struct {
	ID    string
	Label string
	Host  string
	Port  int
	TLS   string
}

// SMTPPresets are offered on the e-mail page; Gmail is the default (with an app password).
var SMTPPresets = []SMTPPreset{
	{ID: "gmail", Label: "Gmail", Host: "smtp.gmail.com", Port: 587, TLS: SMTPStartTLS},
	{ID: "microsoft", Label: "Outlook / Microsoft 365", Host: "smtp.office365.com", Port: 587, TLS: SMTPStartTLS},
	{ID: "custom", Label: "Custom", Port: 587, TLS: SMTPStartTLS},
}

// DefaultSMTP is what the page shows before anything is saved: Gmail, switched off.
func DefaultSMTP() SMTPSettings {
	return SMTPSettings{Host: "smtp.gmail.com", Port: 587, TLS: SMTPStartTLS}
}

// IsLocalHost reports whether host is this machine (where a password may travel unencrypted,
// as Go's SMTP client allows).
func IsLocalHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Normalize trims the settings and fills the defaults (TLS STARTTLS, From = user name).
func (s SMTPSettings) Normalize() SMTPSettings {
	s.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Host)), ".")
	s.Username = strings.TrimSpace(s.Username)
	s.From = strings.TrimSpace(s.From)
	if s.TLS == "" {
		s.TLS = SMTPStartTLS
	}
	if s.From == "" && strings.Contains(s.Username, "@") {
		s.From = s.Username
	}
	return s
}

// Validate checks normalised settings. Switched-off settings may be incomplete.
func (s SMTPSettings) Validate() error {
	switch {
	case s.TLS != SMTPStartTLS && s.TLS != SMTPTLS && s.TLS != SMTPNone:
		return Invalid("tls", "must be starttls, tls or none")
	case s.Port < 0 || s.Port > 65535:
		return Invalid("port", "must be a TCP port")
	case len(s.Host) > 255 || len(s.Username) > 254 || len(s.From) > 320:
		return Invalid("host", "value too long")
	case strings.ContainsAny(s.Username+s.From, "\r\n"):
		return Invalid("from", "no line breaks")
	}
	if !s.Enabled {
		return nil
	}
	switch {
	case s.Host == "" || strings.ContainsAny(s.Host, " /:@"):
		return Invalid("host", "is required: a host name such as smtp.gmail.com")
	case s.Port == 0:
		return Invalid("port", "is required (587 with STARTTLS, 465 with TLS)")
	case s.TLS == SMTPNone && s.Username != "" && !IsLocalHost(s.Host):
		return Invalid("tls", "a password is only sent encrypted: choose STARTTLS or TLS, or leave the user name empty")
	}
	if a, err := mail.ParseAddress(s.From); err != nil || a.Address == "" {
		return Invalid("from", "must be an e-mail address, optionally with a name: cactusTrade <you@example.com>")
	}
	return nil
}

// EmailSenderWarnings says when a provider is known to send as another address than From:
// Gmail sends as the signed-in account unless From is a verified "Send mail as" alias;
// Microsoft 365 may refuse unless the mailbox may send as From. Warnings only; never nil.
func EmailSenderWarnings(s SMTPSettings) []string {
	out := []string{}
	if s.Username == "" || s.From == "" {
		return out
	}
	a, err := mail.ParseAddress(s.From)
	if err != nil || strings.EqualFold(a.Address, s.Username) {
		return out
	}
	switch s.Host {
	case "smtp.gmail.com", "smtp.googlemail.com":
		out = append(out, "Gmail sends as "+s.Username+" unless "+a.Address+
			" is a verified \"Send mail as\" address of that account (Gmail → Settings → Accounts → Send mail as).")
	case "smtp.office365.com", "smtp-mail.outlook.com":
		out = append(out, "Microsoft 365 may refuse to send as "+a.Address+
			" unless it is the mailbox's own address or "+s.Username+" has Send As permission for it.")
	}
	return out
}

// ValidateEmail checks one e-mail address ("" is allowed: no address).
func ValidateEmail(field, addr string) error {
	if addr == "" {
		return nil
	}
	a, err := mail.ParseAddress(addr)
	if err != nil || a.Address != addr || len(addr) > 254 {
		return Invalid(field, "must be one plain e-mail address, such as you@example.com")
	}
	return nil
}

// Mail is one outgoing message (plain text).
type Mail struct {
	ID      string // also the Message-ID's left part
	To      string
	Subject string
	Body    string
}

// OutboxMail is a message waiting in (or delivered from) the outbox.
type OutboxMail struct {
	Mail
	CreatedAt time.Time
	NextAt    time.Time // the next attempt
	Attempts  int
	SentAt    time.Time // zero until the server accepted it
	LastError string
}

// MaxMailAttempts: a message is given up after this many failed attempts (about a day with
// the backoff of MailRetryDelay).
const MaxMailAttempts = 10

// MailRetryDelay is the wait after the n-th failed attempt (1 min, 2, 4 … capped at 4 h).
func MailRetryDelay(attempts int) time.Duration {
	d := time.Minute
	for i := 1; i < attempts && d < 4*time.Hour; i++ {
		d *= 2
	}
	return min(d, 4*time.Hour)
}

// NotifyCategory is a kind of event a user can ask to be e-mailed about.
type NotifyCategory string

// Notification categories.
const (
	NotifyTrades    NotifyCategory = "trades"    // orders, fills, closes (buy, sell, short)
	NotifyWatchlist NotifyCategory = "watchlist" // shares added to or removed from a watchlist
	NotifyAlerts    NotifyCategory = "alerts"    // warnings and errors: kill switch, limits, failures
)

// NotifyCategories lists the categories in display order.
var NotifyCategories = []NotifyCategory{NotifyTrades, NotifyWatchlist, NotifyAlerts}

// NotifyPrefs are a user's e-mail notification choices.
type NotifyPrefs map[NotifyCategory]bool

// DefaultNotifyPrefs: trades and alerts, not watchlist changes.
func DefaultNotifyPrefs() NotifyPrefs {
	return NotifyPrefs{NotifyTrades: true, NotifyAlerts: true}
}

// CategoryOf puts an event in a notification category ("" = never e-mailed).
func CategoryOf(e Event) NotifyCategory {
	switch {
	case e.Severity == SeverityWarning || e.Severity == SeverityDanger:
		return NotifyAlerts
	case strings.HasPrefix(e.Kind, "order.") || strings.HasPrefix(e.Kind, "position.") || strings.HasPrefix(e.Kind, "trade."):
		return NotifyTrades
	case strings.HasPrefix(e.Kind, "watchlist.") || strings.HasPrefix(e.Kind, "scanner.added"):
		return NotifyWatchlist
	}
	return ""
}

// PasswordReset is a password-reset link sent by e-mail. Only the token's SHA-256 is kept.
type PasswordReset struct {
	TokenHash string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time // zero until used (single use)
}

// Password-reset policy: a link lives 30 minutes; at most 3 links per user per hour.
const (
	PasswordResetTTL       = 30 * time.Minute
	PasswordResetPerHour   = 3
	PasswordResetMinLength = 32
)
