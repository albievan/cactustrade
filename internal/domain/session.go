package domain

import "time"

// Session is a signed-in browser. Only the SHA-256 of its token is stored.
type Session struct {
	TokenHash string
	UserID    string
	CSRFToken string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

// SessionPolicy bounds a session's life.
type SessionPolicy struct {
	Absolute time.Duration // from sign-in, whatever the activity
	Idle     time.Duration // since the last request
}

// DefaultSessionPolicy: 12 hours absolute, 1 hour idle.
var DefaultSessionPolicy = SessionPolicy{Absolute: 12 * time.Hour, Idle: time.Hour}

// Active reports whether the session may still be used at now.
func (s Session) Active(now time.Time, p SessionPolicy) bool {
	return now.Before(s.ExpiresAt) && now.Sub(s.LastSeen) < p.Idle
}

// APIKey is a bearer credential for programmatic access. Only the SHA-256 of its secret is
// stored; the secret is shown once at creation.
type APIKey struct {
	ID         string
	OwnerID    string
	Name       string
	Role       Role
	SecretHash string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
}

// Usable reports whether the key may authenticate at now.
func (k APIKey) Usable(now time.Time) bool { return k.ExpiresAt == nil || now.Before(*k.ExpiresAt) }

// ValidateKeyName checks an API key's label.
func ValidateKeyName(s string) error {
	if s == "" || len(s) > 64 {
		return Invalid("name", "1-64 characters")
	}
	return nil
}

// EventSeverity colours an event in the UI.
type EventSeverity string

// Event severities.
const (
	SeverityInfo    EventSeverity = "info"
	SeveritySuccess EventSeverity = "success"
	SeverityWarning EventSeverity = "warning"
	SeverityDanger  EventSeverity = "danger"
)

// Event is one entry of the feed behind the bell (what happened, not who may do what:
// that is the audit log).
type Event struct {
	ID       string
	At       time.Time
	Kind     string // e.g. watchlist.added, scanner.suggested, order.filled, killswitch
	Severity EventSeverity
	Title    string
	Detail   string
	Actor    string // username, or "agent" / "system"
	// AccountID is the eToro account the event is about ("" = the whole system).
	AccountID string
	// Direction is the long or short of the order or position an event is about ("" = none).
	Direction Direction
}

// AuditEntry records who did what. Details never hold secrets.
type AuditEntry struct {
	ID       int64
	At       time.Time
	ActorID  string // user id, "" for the system
	Actor    string // username or "system"
	Action   string // e.g. "user.create", "credential.set"
	Target   string // e.g. a user id or credential name
	Details  string
	RemoteIP string
}
