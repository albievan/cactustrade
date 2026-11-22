// Package ports declares the interfaces between cactusTrade's use cases and the outside
// world. Use cases depend only on these; adapters implement them; cmd/cactustrade wires them.
package ports

import (
	"context"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// ---------------------------------------------------------------------------------------
// Repository (per aggregate). Implementations: adapters/sqlstore (SQLite and MariaDB).
// Every method returns domain.ErrNotFound / domain.ErrConflict where they apply.
// ---------------------------------------------------------------------------------------

// Users stores users.
type Users interface {
	// CreateFirst inserts u only if no user exists, atomically; else domain.ErrSetupDone.
	CreateFirst(ctx context.Context, u domain.User) error
	Create(ctx context.Context, u domain.User) error
	Get(ctx context.Context, id string) (domain.User, error)
	GetByUsername(ctx context.Context, username string) (domain.User, error)
	List(ctx context.Context) ([]domain.User, error)
	Update(ctx context.Context, u domain.User) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
	// CountActiveAdmins counts enabled admins (the last one cannot be removed or demoted).
	CountActiveAdmins(ctx context.Context) (int, error)
}

// Sessions stores browser sessions by token hash.
type Sessions interface {
	Create(ctx context.Context, s domain.Session) error
	Get(ctx context.Context, tokenHash string) (domain.Session, error)
	Touch(ctx context.Context, tokenHash string, at time.Time) error
	Delete(ctx context.Context, tokenHash string) error
	DeleteForUser(ctx context.Context, userID string) error
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// APIKeys stores API keys.
type APIKeys interface {
	Create(ctx context.Context, k domain.APIKey) error
	Get(ctx context.Context, id string) (domain.APIKey, error)
	List(ctx context.Context, ownerID string) ([]domain.APIKey, error) // "" = all
	Touch(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteForOwner(ctx context.Context, ownerID string) error
}

// SecretRecord is an encrypted secret as stored.
type SecretRecord struct {
	Name       string
	Ciphertext []byte
	UpdatedAt  time.Time
}

// Secrets stores encrypted secrets (ciphertext only; never plaintext).
type Secrets interface {
	Put(ctx context.Context, r SecretRecord) error
	Get(ctx context.Context, name string) (SecretRecord, error)
	List(ctx context.Context) ([]SecretRecord, error) // Ciphertext omitted
	Delete(ctx context.Context, name string) error
}

// Audit appends and reads the audit log.
type Audit interface {
	Append(ctx context.Context, e domain.AuditEntry) error
	List(ctx context.Context, limit int) ([]domain.AuditEntry, error) // newest first
}

// Settings is a small key/value store for server-side settings (mode, limits…).
type Settings interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, at time.Time) error
}

// Store bundles the repositories and the database's health.
type Store interface {
	Users() Users
	Sessions() Sessions
	APIKeys() APIKeys
	Secrets() Secrets
	Audit() Audit
	Settings() Settings
	Ping(ctx context.Context) error
	Driver() string
	Close() error
}

// ---------------------------------------------------------------------------------------
// Security primitives.
// ---------------------------------------------------------------------------------------

// SecretStore keeps the owner's credentials (eToro keys, LLM key) encrypted at rest. There is
// deliberately no way to list values: secrets are write-only for the UI and API.
type SecretStore interface {
	Put(ctx context.Context, name domain.CredentialName, value string) error
	// Get is for adapters that need the value (eToro client, LLM client) — never the API.
	Get(ctx context.Context, name domain.CredentialName) (string, error)
	Delete(ctx context.Context, name domain.CredentialName) error
	// Status says which credentials are set, where from, and when they last changed.
	Status(ctx context.Context) (map[domain.CredentialName]SecretMeta, error)
}

// Secret sources.
const (
	SourceUI  = "ui"  // entered in the web UI, encrypted in the database
	SourceEnv = "env" // from an environment variable or a *_FILE secret file; read-only in the UI
)

// SecretMeta describes a stored credential without its value.
type SecretMeta struct {
	Source    string
	UpdatedAt time.Time // zero for SourceEnv
}

// Cipher is authenticated encryption with associated data (AES-256-GCM).
type Cipher interface {
	Seal(plaintext, aad []byte) ([]byte, error)
	Open(ciphertext, aad []byte) ([]byte, error)
}

// PasswordHasher hashes and verifies passwords (argon2id).
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(hash, password string) (bool, error)
}

// Random produces unguessable tokens and identifiers.
type Random interface {
	Token() string // ≥ 256 bits, URL-safe
	ID() string    // a UUID
}

// Clock tells the time; use cases never call time.Now.
type Clock interface {
	Now() time.Time
}

// MarketCalendar knows trading sessions, DST, half days and holidays. An unknown date is
// reported as closed (fail closed). Implemented in phase 2.
type MarketCalendar interface {
	// Session returns the open and close of market m on the trading day containing at, and
	// whether the market trades that day.
	Session(m domain.Market, at time.Time) (open, close time.Time, trading bool, err error)
}

// ---------------------------------------------------------------------------------------
// Trading. Implemented from phase 2 on.
// ---------------------------------------------------------------------------------------

// MarketData reads prices (eToro REST/WebSocket).
type MarketData interface {
	Quotes(ctx context.Context, ids []domain.InstrumentID) ([]domain.Quote, error)
	Candles(ctx context.Context, id domain.InstrumentID, interval time.Duration, count int) ([]domain.Candle, error)
	Search(ctx context.Context, query string) ([]domain.Instrument, error)
}

// Broker is the only door to the eToro account. It exposes no money-movement operation by
// design, and its open order takes no leverage, direction or settlement parameter.
type Broker interface {
	Portfolio(ctx context.Context) (domain.Portfolio, error)
	// EligibleRealLong reports whether the instrument can be bought as a real share at
	// leverage 1.
	EligibleRealLong(ctx context.Context, id domain.InstrumentID) (bool, error)
	Open(ctx context.Context, o domain.OrderIntent) (domain.OrderStatus, error)
	Lookup(ctx context.Context, requestID string) (domain.OrderStatus, error)
	Cancel(ctx context.Context, orderID int64) error
	Close(ctx context.Context, positionID int64, instrument domain.InstrumentID) (domain.OrderStatus, error)
}

// SignalModel turns candles into candidates (regime model + playbooks).
type SignalModel interface {
	Candidates(ctx context.Context, m domain.Market, at time.Time) ([]domain.Candidate, error)
}

// ReviewInput is everything the LLM reviewer sees. It holds no credentials.
type ReviewInput struct {
	Candidate     domain.Candidate
	RecentCandles []domain.Candle
	Positions     []domain.Position
	TodayPnL      domain.Cents
}

// Reviewer is the LLM. It has no tools and no broker access; it only returns a verdict.
type Reviewer interface {
	Review(ctx context.Context, in ReviewInput) (domain.Verdict, error)
}

// RiskEngine has the final, deterministic say.
type RiskEngine interface {
	Evaluate(ctx context.Context, c domain.Candidate, v domain.Verdict, p domain.Portfolio, now time.Time) domain.RiskDecision
}

// Event is something that happened, pushed to the dashboard and the decision log.
type Event struct {
	Type string
	At   time.Time
	Data any
}

// EventBus fans events out to subscribers (dashboard SSE, loggers).
type EventBus interface {
	Publish(e Event)
	// Subscribe returns a channel of events and a cancel function that closes it.
	Subscribe(buffer int) (<-chan Event, func())
}
