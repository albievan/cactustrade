// Package app holds cactusTrade's use cases. Every exported method that acts for someone
// takes a Principal and checks its permission first; nothing here knows about HTTP or SQL.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/strategy"
)

// Via says how a principal authenticated.
type Via string

// Authentication methods.
const (
	ViaSession Via = "session"
	ViaAPIKey  Via = "apikey"
)

// Principal is an authenticated caller.
type Principal struct {
	UserID   string
	Username string
	Role     domain.Role // for an API key: the key's role (never above its owner's)
	Via      Via
	APIKeyID string
	RemoteIP string
	// MustChangePassword: the account still has the default password, so every use case
	// except Me, ChangePassword and sign-out is refused (domain.ErrPasswordChangeRequired).
	MustChangePassword bool
}

// Can reports whether the principal has permission p.
func (p Principal) Can(perm domain.Permission) bool { return p.UserID != "" && p.Role.Allows(perm) }

// Deps are the ports the use cases need.
type Deps struct {
	Store   ports.Store
	Secrets ports.SecretStore
	Hasher  ports.PasswordHasher
	Random  ports.Random
	Clock   ports.Clock
	Log     *slog.Logger
	// Broker, Market and Calendar are the eToro adapter and the market calendar (phase 2).
	// When nil, the features that need them answer "unavailable".
	Broker   ports.Broker
	Market   ports.MarketData
	Calendar ports.MarketCalendar
	Reviewer ports.Reviewer // the LLM reviewer (nil = not available)
	Research ports.Research // industries and the screen for the scanner (nil = not available)
	// Reload re-reads settings that live outside the database (credentials from the
	// environment / secret files). Restart asks the process to restart gracefully. Both are
	// supplied by cmd/cactustrade; nil = not supported.
	Reload  func(ctx context.Context) error
	Restart func()
	// Mailer sends e-mail (nil = no e-mail). MailFallback is the deployment's mail server
	// (CACTUSTRADE_SMTP_* and the smtp_password secret), used while none is switched on in
	// the app; ok=false when there is none.
	Mailer       ports.Mailer
	MailFallback func() (server domain.SMTPSettings, password string, ok bool)
}

// Config holds use-case settings.
type Config struct {
	Sessions domain.SessionPolicy
	Version  string
	Build    string
	// PublicURL is the address users open the app at (https://host:port), for links in
	// e-mails; "" switches password reset by e-mail off.
	PublicURL string
}

// Service implements the use cases.
type Service struct {
	d         Deps
	cfg       Config
	startedAt time.Time
	dummyHash string // verified against when a username is unknown (equal timing)
	// evalOverride replaces the strategy in scanner tests.
	evalOverride func(domain.InstrumentID, []domain.Candle) strategy.Decision
}

// New builds the service.
func New(d Deps, cfg Config) (*Service, error) {
	if d.Store == nil || d.Secrets == nil || d.Hasher == nil || d.Random == nil || d.Clock == nil {
		return nil, errors.New("app.New: missing dependency")
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Sessions.Absolute == 0 {
		cfg.Sessions = domain.DefaultSessionPolicy
	}
	dummy, err := d.Hasher.Hash(d.Random.Token())
	if err != nil {
		return nil, fmt.Errorf("app.New: %w", err)
	}
	return &Service{d: d, cfg: cfg, startedAt: d.Clock.Now(), dummyHash: dummy}, nil
}

func (s *Service) authorize(p Principal, perm domain.Permission) error {
	if p.UserID == "" {
		return domain.ErrUnauthenticated
	}
	if p.MustChangePassword {
		return domain.ErrPasswordChangeRequired
	}
	if !p.Can(perm) {
		return domain.ErrForbidden
	}
	return nil
}

// audit records an action. A failed audit write is logged, never silently dropped.
func (s *Service) audit(ctx context.Context, p Principal, action, target, details string) {
	actor := p.Username
	if actor == "" {
		actor = "system"
	}
	e := domain.AuditEntry{
		At: s.d.Clock.Now().UTC(), ActorID: p.UserID, Actor: actor, Action: action,
		Target: target, Details: details, RemoteIP: p.RemoteIP,
	}
	if err := s.d.Store.Audit().Append(ctx, e); err != nil {
		s.d.Log.ErrorContext(ctx, "audit write failed", "action", action, "target", target, "err", err)
	}
}

// hashToken is the stored form of a session token or API key secret.
func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}
