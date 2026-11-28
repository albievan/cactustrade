// Package ports declares the interfaces between cactusTrade's use cases and the outside
// world. Use cases depend only on these; adapters implement them; cmd/cactustrade wires them.
package ports

import (
	"context"
	"io"
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

// Orders stores our orders to eToro. The request id (x-request-id) is the key, so a row
// exists before its request is sent (CLAUDE.md §2.7).
type Orders interface {
	// Create stores a new order: ErrConflict when the request id exists, ErrNotFound when
	// the account does not.
	Create(ctx context.Context, o domain.Order) error
	Get(ctx context.Context, requestID string) (domain.Order, error)
	// Update replaces the mutable fields (state, reason, sends, sent/updated times and the
	// broker ids) with next's, only if the stored order still has prev's state and send
	// count: otherwise ErrConflict, and nothing changes (no transition is lost). Unknown
	// request id: ErrNotFound.
	Update(ctx context.Context, prev, next domain.Order) error
	// ListOpen returns the account's orders that are not final, oldest first.
	ListOpen(ctx context.Context, accountID string) ([]domain.Order, error)
	// List returns the account's orders, newest first, at most limit.
	List(ctx context.Context, accountID string, limit int) ([]domain.Order, error)
	// ListSince returns the account's orders created at or after since, oldest first (the
	// demo evidence of the real-mode gate, PLAN 7.12).
	ListSince(ctx context.Context, accountID string, since time.Time) ([]domain.Order, error)
}

// LLMCalls stores every call to an LLM reviewer (OD12: kept 400 days). AccountID "" is a call
// for no account (the admin's test); it has no foreign key, so a call outlives its account.
type LLMCalls interface {
	// Add stores a call: ErrConflict when the id exists. Prompt and response are cut at
	// domain.MaxLLMText, the error at 500 bytes.
	Add(ctx context.Context, c domain.LLMCall) error
	Get(ctx context.Context, id string) (domain.LLMCall, error)
	// Usage sums the calls made at or after since per profile and purpose, ordered by
	// profile id and purpose.
	Usage(ctx context.Context, since time.Time) ([]domain.LLMUsage, error)
	// UsageFor sums the calls of the given accounts made in [from, to) into one figure
	// (ProfileID and Purpose empty; R30 statements). No ids = nothing.
	UsageFor(ctx context.Context, accountIDs []string, from, to time.Time) (domain.LLMUsage, error)
	// DeleteBefore removes calls made before t and returns how many.
	DeleteBefore(ctx context.Context, t time.Time) (int64, error)
}

// Decisions stores the decision log: one entry per candidate the agent handled (OD12: kept
// 400 days). Entries go with their account.
type Decisions interface {
	// Add stores a validated decision (domain.Decision.Validate): ErrConflict when the id
	// exists, ErrNotFound when the account does not.
	Add(ctx context.Context, d domain.Decision) error
	// Update replaces the verdict, risk answer, outcome and order request id of the
	// account's decision id; ErrNotFound when there is none.
	Update(ctx context.Context, d domain.Decision) error
	// Get returns the account's decision id; ErrNotFound for another account's.
	Get(ctx context.Context, accountID, id string) (domain.Decision, error)
	// List returns the account's decisions made before before (zero = no bound) with the
	// outcome (empty = any), newest first, at most limit.
	List(ctx context.Context, accountID string, before time.Time, outcome domain.DecisionOutcome, limit int) ([]domain.Decision, error)
	// DeleteBefore removes decisions made before t, of every account, and returns how many.
	DeleteBefore(ctx context.Context, t time.Time) (int64, error)
}

// AgentPositions stores the positions cactusTrade opened (OD1: only these are closed by it).
type AgentPositions interface {
	// Add records a position: ErrConflict when the account already has it, ErrNotFound when
	// the account does not exist.
	Add(ctx context.Context, p domain.AgentPosition) error
	Get(ctx context.Context, accountID string, positionID int64) (domain.AgentPosition, error)
	// Update stores ClosedAt and CloseRequestID (the rest never changes).
	Update(ctx context.Context, p domain.AgentPosition) error
	// ListOpen returns the account's positions not closed yet, oldest first.
	ListOpen(ctx context.Context, accountID string) ([]domain.AgentPosition, error)
	// ListSince returns the account's positions opened at or after since, open or closed,
	// oldest first (the demo evidence of the real-mode gate, PLAN 7.12).
	ListSince(ctx context.Context, accountID string, since time.Time) ([]domain.AgentPosition, error)
}

// TradeFilter selects an account's stored trades by their close time: From ≤ closed < To
// (zero = no bound) and, for paging, closed < Before (zero = no bound). Ours nil = every
// trade, true = only cactusTrade's, false = only the others (OD15). Limit caps the rows.
type TradeFilter struct {
	From, To time.Time
	Ours     *bool
	Before   time.Time
	Limit    int
}

// Trades stores the closed trades read from eToro's trade history (PLAN 6.4). They are kept
// for as long as the account exists (OD16): there is no purge; they go with their account.
type Trades interface {
	// Upsert stores a validated trade (domain.Trade.Validate), replacing the stored one with
	// the same account and position id (update, else insert). ErrNotFound when the account
	// does not exist.
	Upsert(ctx context.Context, t domain.Trade) error
	// List returns the account's trades matching f, newest close first (ties: higher
	// position id first), at most f.Limit.
	List(ctx context.Context, accountID string, f TradeFilter) ([]domain.Trade, error)
	// LastClosedAt is the latest close time stored for the account (zero = none).
	LastClosedAt(ctx context.Context, accountID string) (time.Time, error)
}

// RiskStates stores each account's risk state (risk day, start-of-day equity, high-water
// mark, daily latch, kill switch).
type RiskStates interface {
	// Get returns the stored state (ErrNotFound when there is none yet).
	Get(ctx context.Context, accountID string) (domain.AccountRiskState, error)
	// Put stores s if the stored version still equals s.Version (0 = not stored yet) and
	// returns s with the new version. A stale version is ErrConflict: read again, re-apply.
	Put(ctx context.Context, s domain.AccountRiskState) (domain.AccountRiskState, error)
}

// RealGates stores each real-money account's real-mode gate (PLAN 7.12). A gate goes with its
// account.
type RealGates interface {
	// Get returns the stored gate (ErrNotFound when there is none: the gate is locked).
	Get(ctx context.Context, accountID string) (domain.RealGate, error)
	// Put stores a validated gate if the stored version still equals g.Version (0 = not
	// stored yet) and returns g with the new version. A stale version is ErrConflict (read
	// again, re-apply: a disarm is never overwritten by an older arm); an unknown account
	// is ErrNotFound.
	Put(ctx context.Context, g domain.RealGate) (domain.RealGate, error)
}

// InstrumentBlocks stores the instruments the agent may not enter, per account.
type InstrumentBlocks interface {
	// Block adds or refreshes a block (ErrNotFound when the account does not exist).
	Block(ctx context.Context, b domain.InstrumentBlock) error
	Unblock(ctx context.Context, accountID string, id domain.InstrumentID) error  // ErrNotFound
	List(ctx context.Context, accountID string) ([]domain.InstrumentBlock, error) // by instrument
}

// AgentLeases is the single-writer lock per account (D22).
type AgentLeases interface {
	// Acquire takes the account's lease for holder until until, or renews it. It succeeds
	// when there is no lease, holder already holds it, or the lease expired at now;
	// otherwise ErrConflict. Exactly one of several concurrent holders wins.
	Acquire(ctx context.Context, accountID, holder string, now, until time.Time) error
	// Release gives the lease up if holder holds it (otherwise nothing happens).
	Release(ctx context.Context, accountID, holder string) error
	Get(ctx context.Context, accountID string) (domain.AgentLease, error) // ErrNotFound
}

// StatementSchedules stores the trading-statement e-mail schedules (R30). A schedule goes
// with its owner.
type StatementSchedules interface {
	// Create stores a validated schedule: ErrConflict when the id exists, ErrNotFound when
	// the owner does not.
	Create(ctx context.Context, s domain.StatementSchedule) error
	Get(ctx context.Context, id string) (domain.StatementSchedule, error)
	// List returns the owner's schedules ("" = every schedule), oldest first.
	List(ctx context.Context, ownerID string) ([]domain.StatementSchedule, error)
	// Update replaces what the owner edits (period, accounts, recipients, includes, enabled,
	// LastPeriod, UpdatedAt); ErrNotFound when there is none.
	Update(ctx context.Context, s domain.StatementSchedule) error
	Delete(ctx context.Context, id string) error
	// MarkPeriod records that period key was handled at at (with lastErr, "" = sent), only
	// if the stored LastPeriod still equals prev: otherwise ErrConflict and nothing changes
	// (another process handled it first). Unknown id: ErrNotFound.
	MarkPeriod(ctx context.Context, id, prev, key string, at time.Time, lastErr string) error
	// MarkManual records a "Send now" at at, only if the stored ManualSentAt is still prev
	// (else ErrConflict: a concurrent "Send now" won).
	MarkManual(ctx context.Context, id string, prev, at time.Time) error
}

// T212Instruments caches Trading 212's instrument catalogue (GET metadata/instruments, PLAN
// R45). It is one catalogue for every Trading 212 account (the instruments are the same).
type T212Instruments interface {
	// ReplaceAll replaces the whole catalogue with ins, each stamped fetched at at, in one
	// transaction: on any failure (e.g. a ticker twice: ErrConflict) the old catalogue stays
	// as it was. ShortName is stored upper case (matching is case-insensitive, the same in
	// both databases). A ticker must be 1–64 characters (ErrInvalid otherwise).
	ReplaceAll(ctx context.Context, ins []domain.T212Instrument, at time.Time) error
	// Get returns one instrument by its exact ticker; ErrNotFound when it is not listed.
	Get(ctx context.Context, ticker string) (domain.T212Instrument, error)
	// ByShortName returns the instruments whose short name equals shortName ignoring case,
	// by ticker.
	ByShortName(ctx context.Context, shortName string) ([]domain.T212Instrument, error)
	// FetchedAt is when the stored catalogue was fetched; ErrNotFound when it is empty.
	FetchedAt(ctx context.Context) (time.Time, error)
}

// InstrumentMatches stores, per account, which Trading 212 instrument an eToro share of its
// watchlist trades as (D42). A match goes with its account.
type InstrumentMatches interface {
	// Put adds or replaces the account's match for m.Instrument (upsert);
	// ErrNotFound when the account does not exist.
	Put(ctx context.Context, m domain.InstrumentMatch) error
	Get(ctx context.Context, accountID string, id domain.InstrumentID) (domain.InstrumentMatch, error) // ErrNotFound
	// List returns the account's matches by instrument id.
	List(ctx context.Context, accountID string) ([]domain.InstrumentMatch, error)
	Delete(ctx context.Context, accountID string, id domain.InstrumentID) error // ErrNotFound
}

// ResetSpec is what a data reset wipes (PLAN R49): At is the reset's time (data_reset.at and
// every account's history.since.<id>, OD38); WipeWatchlists also empties the watchlists and
// their signals; AccountIDs are the accounts whose trade-history floor is set (all of them,
// OD39).
type ResetSpec struct {
	At             time.Time
	WipeWatchlists bool
	AccountIDs     []string
}

// DataReset wipes all trading data in one transaction (PLAN R49): the orders, agent
// positions, trades, decisions, LLM calls, equity snapshots, events and LLM reviews of every
// account, the derived settings (and, when asked, the watchlists); every armed real-money
// gate is locked (domain.DisarmDataReset). Users, sessions, keys, secrets, accounts, the
// other settings, risk state (kill switch, high-water mark), instrument blocks, instruments,
// themes, schedules, the mail outbox, gate records and the audit log are kept. Any failure
// rolls everything back.
type DataReset interface {
	// Counts returns the rows of every table the reset knows (wiped and kept), by table name.
	Counts(ctx context.Context) (map[string]int64, error)
	// Reset wipes as spec says and returns the rows deleted per wiped table, plus
	// "real_gates_locked" (gates locked) and "settings" (derived settings deleted).
	Reset(ctx context.Context, spec ResetSpec) (map[string]int64, error)
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
	Orders() Orders
	AgentPositions() AgentPositions
	RiskStates() RiskStates
	InstrumentBlocks() InstrumentBlocks
	AgentLeases() AgentLeases
	LLMCalls() LLMCalls
	Decisions() Decisions
	Trades() Trades
	RealGates() RealGates
	StatementSchedules() StatementSchedules
	T212Instruments() T212Instruments
	InstrumentMatches() InstrumentMatches
	// DataReset wipes all trading data (PLAN R49).
	DataReset() DataReset
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

// PlatformWatchlists reads the user's own watchlists on the trading platform (R46, owner's
// decision D38): read-only, there is no method that writes one.
type PlatformWatchlists interface {
	Watchlists(ctx context.Context) ([]domain.PlatformWatchlist, error)
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

	// The operations below change the account. Each sends exactly one request with
	// x-request-id = the given request id (which the caller persisted first, CLAUDE.md §2.7)
	// and never retries by itself. Errors: domain.ErrRealModeGated for a real-money account
	// without a valid permit in ctx (WithPermit: an entry permit to open, an exit or entry
	// permit to close or cancel; refused before any I/O); *domain.NotAcceptedError
	// (domain.ErrNotAccepted) when the broker refused it, RateLimited for a 429;
	// domain.ErrOutcomeUnknown when it was sent but the answer was lost (network error,
	// timeout, 5xx) — resolve with Lookup / CloseOrder.

	// Open sends a market order to open a position; the intent's direction picks one of the
	// two constant order shapes. The answer means accepted, not filled: poll Lookup.
	Open(ctx context.Context, o domain.OrderIntent) (domain.OrderStatus, error)
	// Lookup finds an open order, with what it executed: by eToro's order id when orderID > 0
	// (eToro answered 404 to a lookup by our request id for filled demo orders, 2026-11-24),
	// else by the request id it was sent with. domain.ErrNotFound when the broker does not
	// know it.
	Lookup(ctx context.Context, requestID string, orderID int64) (domain.OrderStatus, error)
	// Cancel asks the broker to cancel a pending open order (eToro's order id).
	Cancel(ctx context.Context, orderID int64, requestID string) error
	// Close sends a market order closing the whole position; poll CloseOrder with the
	// returned order id.
	Close(ctx context.Context, positionID int64, instrument domain.InstrumentID, requestID string) (domain.OrderStatus, error)
	// CloseOrder reads the status of a close order by eToro's order id.
	CloseOrder(ctx context.Context, orderID int64) (domain.OrderStatus, error)
}

// T212Broker is the only door to a Trading 212 practice account (PLAN R45, T212-a), for the
// account set with WithAccount. It is not a Broker: Trading 212 works by ticker and share
// quantity, takes the stop as its own order, has no position id and no request id. It has no
// money-movement operation, and Place takes only a domain.T212Intent (three constant shapes,
// CLAUDE.md §2.3). Every call for an account that is not a Trading 212 practice account
// (domain.Account.T212Trading) is refused with domain.ErrRealModeGated before any key is read
// or any request sent (D41 g), whatever permit ctx carries.
type T212Broker interface {
	// Account reads the account summary and its positions (AsOf = when they were read).
	Account(ctx context.Context) (domain.T212Account, error)
	// Instruments reads Trading 212's whole instrument catalogue.
	Instruments(ctx context.Context) ([]domain.T212Instrument, error)
	// OpenOrders lists the account's active orders.
	OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error)
	// Order reads one active order; domain.ErrNotFound when Trading 212 does not list it
	// (unknown, or no longer active: look in History).
	Order(ctx context.Context, id int64) (domain.BrokerOrder, error)
	// History reads one page of the order history with fills, newest first; ticker "" = all,
	// cursor "" = the first page, else the page's Next.
	History(ctx context.Context, ticker, cursor string) (domain.T212HistoryPage, error)

	// The operations below change the account. Each sends at most one request and never
	// retries (Trading 212 takes no request id: a resend may duplicate the order, T14). The
	// caller persisted the order row first (CLAUDE.md §2.7). Errors: domain.ErrRealModeGated
	// (see above); *domain.NotAcceptedError (domain.ErrNotAccepted) when Trading 212 answered
	// and refused it, or it was not sent at all (e.g. a rate-limit wait too long, a sell above
	// the held quantity); domain.ErrOutcomeUnknown when it was (or may have been) sent and the
	// answer was lost (network error, timeout, 408, 429, 5xx, an unreadable answer) — resolve
	// it with domain.MatchT212Order, never by sending it again.

	// Place sends the intent's order: a market buy, a sell stop or a market sell. The answer
	// means accepted, not filled: poll Order, then History.
	Place(ctx context.Context, in domain.T212Intent) (domain.BrokerOrder, error)
	// Cancel asks Trading 212 to cancel an active order. Accepted is not done: the order may
	// still fill while it is filling (T10); confirm with Order. domain.ErrNotFound when
	// Trading 212 does not know it.
	Cancel(ctx context.Context, id int64) error
}

// Holdings reads an account's cash and open positions, read-only, for the account set with
// WithAccount (without one it refuses). It has no operation that changes anything: it is how
// cactusTrade shows platforms it does not trade (Trading 212, Saxo, Tiger Brokers).
type Holdings interface {
	Holdings(ctx context.Context) (domain.Holdings, error)
}

// OAuthConnector is a view-only platform's browser sign-in (OAuth 2.0 authorization code
// grant; Saxo). Both calls are for the account set with WithAccount.
type OAuthConnector interface {
	// AuthorizeURL is where the owner's browser signs in; the platform sends it back to
	// redirectURI with a code and state. codeChallenge is the PKCE S256 challenge
	// (domain.PKCEChallenge). It needs the account's app key, nothing is sent.
	AuthorizeURL(ctx context.Context, redirectURI, state, codeChallenge string) (string, error)
	// Exchange trades the code for tokens, proving the sign-in with the PKCE codeVerifier,
	// and stores the refresh token in the SecretStore (write-only) before anything uses the
	// access token.
	Exchange(ctx context.Context, code, redirectURI, codeVerifier string) error
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
	// Review asks for a verdict and returns the call's trace, filled even when it fails:
	// Provider, Model (the one that answered, else the requested one), Prompt (the request
	// body as sent), Response (the raw body, kept when it is unusable), the token counts,
	// Latency and Error. The caller sets ID, AccountID, Purpose, ProfileID, CostMicros and
	// At. The trace never holds the API key or a request header.
	Review(ctx context.Context, cfg domain.LLMConfig, apiKey string, in ReviewInput) (domain.Verdict, domain.LLMCall, error)
	// Models lists the model ids the provider offers with this key.
	Models(ctx context.Context, cfg domain.LLMConfig, apiKey string) ([]string, error)
}

// ResearchTheme is one of the account's themes as web research sees it.
type ResearchTheme struct {
	Name     string
	Keywords []string
}

// ResearchRequest is everything web research is told (PLAN R29). It holds no credentials,
// no account figures and nothing about orders or positions.
type ResearchRequest struct {
	Date      time.Time       // today, so the model looks for current information
	Themes    []ResearchTheme // the account's enabled themes
	Exchanges []string        // the exchanges allowed (domain.ResearchExchanges)
	Watchlist []string        // symbols already on the watchlist or proposed (not to repeat)
	Max       int             // at most this many shares
}

// ResearchResult is what web research returned after the strict parse
// (domain.ParseResearchAnswer): the shares that passed, and the ones refused with why.
type ResearchResult struct {
	Picks   []domain.ResearchPick
	Refused []domain.ResearchRefusal
}

// Researcher is an LLM with its provider's built-in web search as its only tool (PLAN R29).
// It has no broker, no other tool and no credential but its own key; it only returns data,
// which the caller checks again and sends through the watchlist's eligibility path. Nothing
// is traded because of it.
type Researcher interface {
	// Research asks for up to req.Max shares and returns the call's trace on every path
	// (like Reviewer.Review; never the key). A provider without a built-in web search is an
	// error before any request. An answer that is not the expected JSON is an error and
	// nothing of it is used.
	Research(ctx context.Context, cfg domain.LLMConfig, apiKey string, req ResearchRequest) (ResearchResult, domain.LLMCall, error)
}

// Event is something that happened, pushed to the dashboard and the decision log.
type Event struct {
	Type string
	At   time.Time
	Data any
}

// EventBus fans events out to subscribers (dashboard SSE, loggers). It is a view only: no
// decision is ever taken on it (CLAUDE.md §2.6).
type EventBus interface {
	// Publish delivers e to every subscriber that has room. It must never block: the
	// executor publishes every stored order transition, so a full or slow subscriber misses
	// the event instead of holding up an order (PLAN 6.6b).
	Publish(e Event)
	// Subscribe returns a channel of events and a cancel function that closes it.
	Subscribe(buffer int) (<-chan Event, func())
}

// StatementRenderer turns a built trading statement into a document (R30: a PDF). It only
// formats what it is given: no I/O, no network, no clock (the statement carries its times).
type StatementRenderer interface {
	Render(ctx context.Context, s domain.Statement) ([]byte, error)
}

// AnalysisRenderer turns a built analysis into a one-page document (R31: a PDF whose page
// is sized to fit). Like StatementRenderer it only formats what it is given.
type AnalysisRenderer interface {
	RenderAnalysis(ctx context.Context, a domain.Analysis) ([]byte, error)
}

// Mailer delivers one message through the given server. A nil error means the server
// accepted the message (it must then never be sent again).
type Mailer interface {
	Send(ctx context.Context, server domain.SMTPSettings, password string, m domain.Mail) error
}

// Backups is the app's backup folder (R48): it writes encrypted backups of the database
// into the folder fixed at deploy time, lists, opens and removes them. Every file is
// age-encrypted to the recipients given (public keys only: the machine that writes a backup
// cannot read it, OD29); without a recipient nothing is written. Names are always bare file
// names that domain.ParseBackupName accepts; anything else is refused (domain.ErrNotFound).
// Implementation: adapters/backup.
type Backups interface {
	// Folder is the absolute path of the backup folder (shown, never changed by the app).
	Folder() string
	// Driver is the database driver backed up ("sqlite" or "mariadb").
	Driver() string
	// ParseRecipients checks age recipients (X25519 or hybrid public keys, age1…); an
	// unusable one is a domain.ValidationError on "recipients".
	ParseRecipients(recipients []string) error
	// Write takes a consistent backup now, encrypted to the recipients, named for at. A
	// failure leaves no partial or plain file behind.
	Write(ctx context.Context, recipients []string, at time.Time) (domain.BackupFile, error)
	// List returns the backup files in the folder, newest first.
	List(ctx context.Context) ([]domain.BackupFile, error)
	// Remove deletes one backup file.
	Remove(ctx context.Context, name string) error
	// Open opens one backup file for reading (the caller closes it).
	Open(ctx context.Context, name string) (io.ReadCloser, domain.BackupFile, error)
}
