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

// Instruments caches eToro instrument metadata with the last eligibility check.
type Instruments interface {
	// Upsert stores the instrument and its eligibility (el.Instrument must equal in.ID).
	Upsert(ctx context.Context, in domain.Instrument, el domain.Eligibility, at time.Time) error
	Get(ctx context.Context, id domain.InstrumentID) (domain.Instrument, domain.Eligibility, error)
}

// Watchlist holds, per account, the instruments the agent may trade.
type Watchlist interface {
	// Add puts a cached instrument on an account's list: ErrConflict when already there,
	// ErrNotFound when the instrument is not cached or the account does not exist.
	Add(ctx context.Context, accountID string, id domain.InstrumentID, addedBy string, at time.Time) error
	Remove(ctx context.Context, accountID string, id domain.InstrumentID) error
	List(ctx context.Context, accountID string) ([]domain.WatchItem, error) // by symbol
	// Watched lists every instrument on any account's list (for the price stream).
	Watched(ctx context.Context) ([]domain.InstrumentID, error)
}

// Accounts stores eToro accounts and their shares.
type Accounts interface {
	Create(ctx context.Context, a domain.Account) error // ErrConflict on a duplicate id
	Get(ctx context.Context, id string) (domain.Account, error)
	List(ctx context.Context) ([]domain.Account, error) // all, by name
	// ForUser lists the accounts a user owns or has been given, with the access.
	ForUser(ctx context.Context, userID string) ([]domain.AccountView, error)
	Update(ctx context.Context, a domain.Account) error // name, mode
	Delete(ctx context.Context, id string) error
	Shares(ctx context.Context, accountID string) ([]domain.AccountShare, error)
	SetShare(ctx context.Context, s domain.AccountShare) error // add or change
	RemoveShare(ctx context.Context, accountID, userID string) error
	CountOwnedBy(ctx context.Context, userID string) (int, error)
}

// Events stores the event feed. List and CountSince return system events ("" account) and
// the events of the given accounts.
type Events interface {
	Append(ctx context.Context, e domain.Event) error
	List(ctx context.Context, accountIDs []string, limit int) ([]domain.Event, error) // newest first
	CountSince(ctx context.Context, accountIDs []string, since time.Time) (int, error)
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// Themes stores accounts' themes (with their picked shares).
type Themes interface {
	Create(ctx context.Context, t domain.Theme) error
	Get(ctx context.Context, id string) (domain.Theme, error)
	List(ctx context.Context, accountID string) ([]domain.Theme, error) // by name
	Update(ctx context.Context, t domain.Theme) error                   // incl. the picked shares
	Delete(ctx context.Context, id string) error
}

// Reviews stores the scanner's latest review per account and stock.
type Reviews interface {
	Upsert(ctx context.Context, r domain.Review) error
	List(ctx context.Context, accountID string, limit int) ([]domain.Review, error) // newest first
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// Outbox stores mail waiting to be sent and mail sent.
type Outbox interface {
	Enqueue(ctx context.Context, m domain.OutboxMail) error
	// Due returns unsent mail whose next attempt is due and that has not been given up
	// (attempts < domain.MaxMailAttempts), oldest first.
	Due(ctx context.Context, now time.Time, limit int) ([]domain.OutboxMail, error)
	MarkSent(ctx context.Context, id string, at time.Time) error
	MarkFailed(ctx context.Context, id string, attempts int, next time.Time, lastErr string) error
	List(ctx context.Context, limit int) ([]domain.OutboxMail, error) // newest first
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// PasswordResets stores password-reset links by token hash.
type PasswordResets interface {
	Create(ctx context.Context, r domain.PasswordReset) error
	Get(ctx context.Context, tokenHash string) (domain.PasswordReset, error)
	// Use marks an unused link used; a used or unknown link is domain.ErrNotFound.
	Use(ctx context.Context, tokenHash string, at time.Time) error
	CountSince(ctx context.Context, userID string, since time.Time) (int, error)
	DeleteForUser(ctx context.Context, userID string) error
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// EquitySnapshots stores each account's equity over time (the overview chart).
type EquitySnapshots interface {
	// Add stores a snapshot; ErrConflict when the account already has one at that time,
	// ErrNotFound when the account does not exist.
	Add(ctx context.Context, s domain.EquitySnapshot) error
	// List returns the snapshots of the given accounts taken at or after since, oldest first
	// (by account, then time). No ids = none.
	List(ctx context.Context, accountIDs []string, since time.Time) ([]domain.EquitySnapshot, error)
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// Store bundles the repositories and the database's health.
type Store interface {
	Users() Users
	Sessions() Sessions
	APIKeys() APIKeys
	Secrets() Secrets
	Audit() Audit
	Settings() Settings
	Instruments() Instruments
	Watchlist() Watchlist
	Events() Events
	Accounts() Accounts
	Themes() Themes
	Reviews() Reviews
	Outbox() Outbox
	PasswordResets() PasswordResets
	EquitySnapshots() EquitySnapshots
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

// MarketData reads prices and instrument metadata (eToro REST/WebSocket).
type MarketData interface {
	Quotes(ctx context.Context, ids []domain.InstrumentID) ([]domain.Quote, error)
	// Candles returns up to count bars of the given interval, oldest first.
	Candles(ctx context.Context, id domain.InstrumentID, interval time.Duration, count int) ([]domain.Candle, error)
	Search(ctx context.Context, query string) ([]domain.Instrument, error)
	Instruments(ctx context.Context, ids []domain.InstrumentID) ([]domain.Instrument, error)
}

// Research finds shares for the scanner: eToro's stock industries and a screen of tradable
// shares, most popular first.
type Research interface {
	Industries(ctx context.Context) ([]domain.Industry, error)
	// Screen lists tradable shares, optionally in one industry (0 = any), up to limit.
	Screen(ctx context.Context, industryID int64, limit int) ([]domain.Instrument, error)
}

// Broker is the only door to the eToro account. It exposes no money-movement operation by
// design, and its open order takes no leverage, direction or settlement parameter.
type Broker interface {
	Portfolio(ctx context.Context) (domain.Portfolio, error)
	// Eligibility reports, per instrument, whether it can be bought as a real share, long,
	// at leverage 1 (checked before the first trade in an instrument).
	Eligibility(ctx context.Context, ids []domain.InstrumentID) ([]domain.Eligibility, error)
	// Costs is eToro's cost breakdown (spread, markup, fees, tax) for opening amount of id in
	// direction d.
	Costs(ctx context.Context, id domain.InstrumentID, d domain.Direction, amount domain.Cents) ([]domain.CostItem, error)
	TradeHistory(ctx context.Context, since time.Time) ([]domain.ClosedTrade, error)
	Open(ctx context.Context, o domain.OrderIntent) (domain.OrderStatus, error)
	Lookup(ctx context.Context, requestID string) (domain.OrderStatus, error)
	Cancel(ctx context.Context, orderID int64) error
	Close(ctx context.Context, positionID int64, instrument domain.InstrumentID) (domain.OrderStatus, error)
}

// Holdings reads an account's cash and open positions, read-only, for the account set with
// WithAccount (without one it refuses). It has no operation that changes anything: it is how
// cactusTrade shows platforms it does not trade (Trading 212).
type Holdings interface {
	Holdings(ctx context.Context) (domain.Holdings, error)
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

// Reviewer is the LLM. It has no tools and no broker access; it only returns a verdict,
// which the caller clamps (domain.ClampVerdict). Any error is a rejection (fail closed).
type Reviewer interface {
	Review(ctx context.Context, cfg domain.LLMConfig, apiKey string, in ReviewInput) (domain.Verdict, error)
	// Models lists the model ids the provider offers with this key.
	Models(ctx context.Context, cfg domain.LLMConfig, apiKey string) ([]string, error)
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

// Mailer delivers one message through the given server. A nil error means the server
// accepted the message (it must then never be sent again).
type Mailer interface {
	Send(ctx context.Context, server domain.SMTPSettings, password string, m domain.Mail) error
}
