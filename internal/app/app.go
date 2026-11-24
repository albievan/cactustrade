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
	"sync/atomic"
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
	// Holdings are the read-only sources for platforms cactusTrade shows but does not trade
	// (Trading 212, Saxo, Tiger). eToro holdings come from Broker. A missing platform =
	// "unavailable". Connectors are the browser sign-ins of those that have one (Saxo).
	Holdings   map[domain.Platform]ports.Holdings
	Connectors map[domain.Platform]ports.OAuthConnector
	// LLM is the LLM reviewer: the agent's review of a trade, the scanner, the admin's test
	// and the model list. Every review is logged with its trace (PLAN 5.5). nil = not
	// available (with reviewing on, the agent then rejects every trade).
	LLM      ports.Reviewer
	Research ports.Research // industries and the screen for the scanner (nil = not available)
	// Researcher is web research for the watchlist (PLAN R29): an LLM whose only tool is its
	// provider's built-in web search. It only returns data; nil = not available.
	Researcher ports.Researcher
	// Reload re-reads settings that live outside the database (credentials from the
	// environment / secret files). Restart asks the process to restart gracefully. Both are
	// supplied by cmd/cactustrade; nil = not supported.
	Reload  func(ctx context.Context) error
	Restart func()
	// Mailer sends e-mail (nil = no e-mail). MailFallback is the deployment's mail server
	// (CACTUSTRADE_SMTP_* and the smtp_password secret), used while none is switched on in
	// the app; ok=false when there is none.
	Mailer       ports.Mailer
	MailFallback func() (fallback FallbackMail, ok bool)
	// Bus carries live events to the dashboard stream (PLAN 6.6b): every stored event, every
	// stored order transition, agent state writes and the supervisor's portfolio reads. It is
	// a view only: nothing decides on it, and Publish must never block (a full subscriber
	// drops). nil = no bus.
	Bus ports.EventBus
	// Sleep waits for d or until ctx ends (the executor's polls and back-offs, the
	// supervisor's loop). nil = a real timer; tests pass one that advances their fake clock.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Config holds use-case settings.
type Config struct {
	Sessions domain.SessionPolicy
	Version  string
	Build    string
	// PublicURL is the address users open the app at (https://host:port), for links in
	// e-mails; "" switches password reset by e-mail off.
	PublicURL string
	// LeaseHolder names this process for the single-writer lease (D22); "" = a random id
	// chosen on first use. Two processes must never share one.
	LeaseHolder string
}

// Service implements the use cases.
type Service struct {
	d         Deps
	cfg       Config
	startedAt time.Time
	dummyHash string // verified against when a username is unknown (equal timing)
	oauth     *oauthStates
	// evalOverride replaces the strategy in scanner tests.
	evalOverride func(domain.InstrumentID, []domain.Candle) strategy.Decision
	// evalConfigOverride replaces the strategy in tests that look at the account's strategy
	// configuration (R28); it wins over evalOverride.
	evalConfigOverride func(domain.InstrumentID, []domain.Candle, strategy.Config) strategy.Decision
	// rt is the in-memory execution state: reconciliation flags, supervisor bookkeeping.
	rt *runtimeState
	// loop is whether this process's agent loop runs (PLAN 6.13; loopStarting until
	// SetAgentLoop is called): no agent may be started while it does not.
	loop atomic.Int32
	// health is the bookkeeping behind TradingHealth (PLAN 7.5): supervisor ticks and passes,
	// LLM call counts.
	health *healthLog
	// gates latches the disarms asked for in this process (PLAN 7.13).
	gates *gateLatch
	// research serialises web research's stored state and marks running runs (PLAN R29).
	research *researchLocks
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
	if d.Sleep == nil {
		d.Sleep = sleepCtx
	}
	return &Service{d: d, cfg: cfg, startedAt: d.Clock.Now(), dummyHash: dummy, rt: newRuntimeState(), oauth: newOAuthStates(), health: newHealthLog(), gates: newGateLatch(), research: newResearchLocks()}, nil
}

// sleepCtx waits for d with a real timer, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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
