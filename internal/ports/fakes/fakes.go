// Package fakes holds in-memory implementations of the ports for use-case tests.
package fakes

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Clock is a settable clock.
type Clock struct {
	mu sync.Mutex
	T  time.Time
}

// Now returns the current fake time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.T }

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.T = c.T.Add(d); c.mu.Unlock() }

// Random returns predictable, unique tokens and ids.
type Random struct {
	mu sync.Mutex
	n  int
}

// Token returns tok-N.
func (r *Random) Token() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	return fmt.Sprintf("tok%04d", r.n)
}

// ID returns id-N.
func (r *Random) ID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	return fmt.Sprintf("id%04d", r.n)
}

// Hasher "hashes" by prefixing; good enough to test the flow, never used outside tests.
type Hasher struct{ Fail bool }

// Hash prefixes the password.
func (h Hasher) Hash(p string) (string, error) {
	if h.Fail {
		return "", errors.New("hash failed")
	}
	return "h:" + p, nil
}

// Verify compares with the prefix.
func (h Hasher) Verify(hash, p string) (bool, error) { return hash == "h:"+p, nil }

// SecretStore keeps secrets in memory.
type SecretStore struct {
	mu   sync.Mutex
	vals map[domain.CredentialName]string
	at   map[domain.CredentialName]time.Time
	Now  func() time.Time
	Err  error
}

// Put stores a value.
func (s *SecretStore) Put(_ context.Context, n domain.CredentialName, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	if s.vals == nil {
		s.vals, s.at = map[domain.CredentialName]string{}, map[domain.CredentialName]time.Time{}
	}
	s.vals[n] = v
	if s.Now != nil {
		s.at[n] = s.Now()
	}
	return nil
}

// Get returns a value.
func (s *SecretStore) Get(_ context.Context, n domain.CredentialName) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vals[n]
	if !ok {
		return "", domain.ErrNotFound
	}
	return v, nil
}

// Delete removes a value.
func (s *SecretStore) Delete(_ context.Context, n domain.CredentialName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vals[n]; !ok {
		return domain.ErrNotFound
	}
	delete(s.vals, n)
	delete(s.at, n)
	return nil
}

// Status lists set credentials.
func (s *SecretStore) Status(context.Context) (map[domain.CredentialName]ports.SecretMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	out := map[domain.CredentialName]ports.SecretMeta{}
	for k := range s.vals {
		out[k] = ports.SecretMeta{Source: ports.SourceUI, UpdatedAt: s.at[k]}
	}
	return out, nil
}

// Store is an in-memory ports.Store.
type Store struct {
	mu       sync.Mutex
	users    map[string]domain.User
	sessions map[string]domain.Session
	keys     map[string]domain.APIKey
	secrets  map[string]ports.SecretRecord
	audit    []domain.AuditEntry
	settings map[string]string
	instr    map[domain.InstrumentID]instrRec
	watch    map[string]map[domain.InstrumentID]domain.WatchItem // account → instrument
	events   []domain.Event
	accounts map[string]domain.Account
	themes   map[string]domain.Theme
	reviews  map[string]domain.Review                  // account/instrument
	shares   map[string]map[string]domain.AccountShare // account → user
	outbox   []domain.OutboxMail
	resets   map[string]domain.PasswordReset
	equity   []domain.EquitySnapshot
	PingErr  error
}

type instrRec struct {
	in domain.Instrument
	el domain.Eligibility
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		users: map[string]domain.User{}, sessions: map[string]domain.Session{},
		keys: map[string]domain.APIKey{}, secrets: map[string]ports.SecretRecord{},
		settings: map[string]string{}, instr: map[domain.InstrumentID]instrRec{},
		watch:    map[string]map[domain.InstrumentID]domain.WatchItem{},
		accounts: map[string]domain.Account{}, shares: map[string]map[string]domain.AccountShare{},
		themes: map[string]domain.Theme{}, reviews: map[string]domain.Review{},
		resets: map[string]domain.PasswordReset{},
	}
}

var _ ports.Store = (*Store)(nil)

// Users returns the user repository.
func (s *Store) Users() ports.Users { return users{s} }

// Sessions returns the session repository.
func (s *Store) Sessions() ports.Sessions { return sessions{s} }

// APIKeys returns the API key repository.
func (s *Store) APIKeys() ports.APIKeys { return apiKeys{s} }

// Secrets returns the secret repository.
func (s *Store) Secrets() ports.Secrets { return secrets{s} }

// Audit returns the audit repository.
func (s *Store) Audit() ports.Audit { return audit{s} }

// Settings returns the settings repository.
func (s *Store) Settings() ports.Settings { return settings{s} }

// Instruments returns the instrument cache.
func (s *Store) Instruments() ports.Instruments { return instrRepo{s} }

// Watchlist returns the watchlist.
func (s *Store) Watchlist() ports.Watchlist { return watchRepo{s} }

// Events returns the event feed.
func (s *Store) Events() ports.Events { return eventRepo{s} }

// Themes returns the themes repository.
func (s *Store) Themes() ports.Themes { return themeRepo{s} }

// Reviews returns the reviews repository.
func (s *Store) Reviews() ports.Reviews { return reviewRepo{s} }

// Outbox returns the outbox repository.
func (s *Store) Outbox() ports.Outbox { return outboxRepo{s} }

// PasswordResets returns the password-reset repository.
func (s *Store) PasswordResets() ports.PasswordResets { return resetRepo{s} }

// EquitySnapshots returns the equity history.
func (s *Store) EquitySnapshots() ports.EquitySnapshots { return equityRepo{s} }

// Accounts returns the accounts repository.
func (s *Store) Accounts() ports.Accounts { return accountRepo{s} }

// Ping returns PingErr.
func (s *Store) Ping(context.Context) error { return s.PingErr }

// Driver returns "fake".
func (s *Store) Driver() string { return "fake" }

// Close does nothing.
func (s *Store) Close() error { return nil }

// AuditEntries returns a copy of the audit log, oldest first.
func (s *Store) AuditEntries() []domain.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.audit)
}

type users struct{ s *Store }

func (r users) CreateFirst(ctx context.Context, u domain.User) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if len(r.s.users) > 0 {
		return domain.ErrSetupDone
	}
	r.s.users[u.ID] = u
	return nil
}

func (r users) Create(_ context.Context, u domain.User) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, x := range r.s.users {
		if x.Username == u.Username {
			return domain.ErrConflict
		}
	}
	r.s.users[u.ID] = u
	return nil
}

func (r users) Get(_ context.Context, id string) (domain.User, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	u, ok := r.s.users[id]
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

func (r users) GetByUsername(_ context.Context, name string) (domain.User, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, u := range r.s.users {
		if u.Username == name {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

func (r users) List(context.Context) ([]domain.User, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := make([]domain.User, 0, len(r.s.users))
	for _, u := range r.s.users {
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b domain.User) int { return strings.Compare(a.Username, b.Username) })
	return out, nil
}

func (r users) Update(_ context.Context, u domain.User) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.users[u.ID]; !ok {
		return domain.ErrNotFound
	}
	r.s.users[u.ID] = u
	return nil
}

func (r users) Delete(_ context.Context, id string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.users[id]; !ok {
		return domain.ErrNotFound
	}
	for _, a := range r.s.accounts {
		if a.OwnerID == id {
			return errors.New("fake: the user owns an account (foreign key)")
		}
	}
	delete(r.s.users, id)
	return nil
}

func (r users) Count(context.Context) (int, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return len(r.s.users), nil
}

func (r users) CountActiveAdmins(context.Context) (int, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	n := 0
	for _, u := range r.s.users {
		if u.Role == domain.RoleAdmin && !u.Disabled {
			n++
		}
	}
	return n, nil
}

type sessions struct{ s *Store }

func (r sessions) Create(_ context.Context, x domain.Session) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.sessions[x.TokenHash] = x
	return nil
}

func (r sessions) Get(_ context.Context, h string) (domain.Session, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	x, ok := r.s.sessions[h]
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	return x, nil
}

func (r sessions) Touch(_ context.Context, h string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	x, ok := r.s.sessions[h]
	if !ok {
		return domain.ErrNotFound
	}
	x.LastSeen = at
	r.s.sessions[h] = x
	return nil
}

func (r sessions) Delete(_ context.Context, h string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	delete(r.s.sessions, h)
	return nil
}

func (r sessions) DeleteForUser(_ context.Context, uid string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for h, x := range r.s.sessions {
		if x.UserID == uid {
			delete(r.s.sessions, h)
		}
	}
	return nil
}

func (r sessions) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var n int64
	for h, x := range r.s.sessions {
		if !now.Before(x.ExpiresAt) {
			delete(r.s.sessions, h)
			n++
		}
	}
	return n, nil
}

type apiKeys struct{ s *Store }

func (r apiKeys) Create(_ context.Context, k domain.APIKey) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.keys[k.ID] = k
	return nil
}

func (r apiKeys) Get(_ context.Context, id string) (domain.APIKey, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	k, ok := r.s.keys[id]
	if !ok {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return k, nil
}

func (r apiKeys) List(_ context.Context, owner string) ([]domain.APIKey, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.APIKey
	for _, k := range r.s.keys {
		if owner == "" || k.OwnerID == owner {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b domain.APIKey) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (r apiKeys) Touch(_ context.Context, id string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	k, ok := r.s.keys[id]
	if !ok {
		return domain.ErrNotFound
	}
	k.LastUsedAt = &at
	r.s.keys[id] = k
	return nil
}

func (r apiKeys) Delete(_ context.Context, id string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.keys[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.keys, id)
	return nil
}

func (r apiKeys) DeleteForOwner(_ context.Context, owner string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for id, k := range r.s.keys {
		if k.OwnerID == owner {
			delete(r.s.keys, id)
		}
	}
	return nil
}

type secrets struct{ s *Store }

func (r secrets) Put(_ context.Context, x ports.SecretRecord) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.secrets[x.Name] = x
	return nil
}

func (r secrets) Get(_ context.Context, n string) (ports.SecretRecord, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	x, ok := r.s.secrets[n]
	if !ok {
		return ports.SecretRecord{}, domain.ErrNotFound
	}
	return x, nil
}

func (r secrets) List(context.Context) ([]ports.SecretRecord, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []ports.SecretRecord
	for _, x := range r.s.secrets {
		out = append(out, ports.SecretRecord{Name: x.Name, UpdatedAt: x.UpdatedAt})
	}
	return out, nil
}

func (r secrets) Delete(_ context.Context, n string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.secrets[n]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.secrets, n)
	return nil
}

type audit struct{ s *Store }

func (r audit) Append(_ context.Context, e domain.AuditEntry) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	e.ID = int64(len(r.s.audit) + 1)
	r.s.audit = append(r.s.audit, e)
	return nil
}

func (r audit) List(_ context.Context, limit int) ([]domain.AuditEntry, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := slices.Clone(r.s.audit)
	slices.Reverse(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type settings struct{ s *Store }

func (r settings) Get(_ context.Context, k string) (string, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	v, ok := r.s.settings[k]
	if !ok {
		return "", domain.ErrNotFound
	}
	return v, nil
}

func (r settings) Set(_ context.Context, k, v string, _ time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.settings[k] = v
	return nil
}

type instrRepo struct{ s *Store }

func (r instrRepo) Upsert(_ context.Context, in domain.Instrument, el domain.Eligibility, _ time.Time) error {
	if el.Instrument != in.ID {
		return domain.ErrInvalid
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.instr[in.ID] = instrRec{in, el}
	return nil
}

func (r instrRepo) Get(_ context.Context, id domain.InstrumentID) (domain.Instrument, domain.Eligibility, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	x, ok := r.s.instr[id]
	if !ok {
		return domain.Instrument{}, domain.Eligibility{}, domain.ErrNotFound
	}
	return x.in, x.el, nil
}

type watchRepo struct{ s *Store }

func (r watchRepo) Add(_ context.Context, acct string, id domain.InstrumentID, by string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	x, ok := r.s.instr[id]
	if _, acctOK := r.s.accounts[acct]; !ok || !acctOK {
		return domain.ErrNotFound
	}
	if r.s.watch[acct] == nil {
		r.s.watch[acct] = map[domain.InstrumentID]domain.WatchItem{}
	}
	if _, dup := r.s.watch[acct][id]; dup {
		return domain.ErrConflict
	}
	r.s.watch[acct][id] = domain.WatchItem{Instrument: x.in, AddedBy: by, AddedAt: at}
	return nil
}

func (r watchRepo) Remove(_ context.Context, acct string, id domain.InstrumentID) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.watch[acct][id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.watch[acct], id)
	return nil
}

func (r watchRepo) List(_ context.Context, acct string) ([]domain.WatchItem, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := make([]domain.WatchItem, 0, len(r.s.watch[acct]))
	for id, w := range r.s.watch[acct] {
		x := r.s.instr[id]
		w.Instrument, w.Eligibility = x.in, x.el
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b domain.WatchItem) int { return strings.Compare(a.Instrument.Symbol, b.Instrument.Symbol) })
	return out, nil
}

func (r watchRepo) Watched(context.Context) ([]domain.InstrumentID, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	seen := map[domain.InstrumentID]bool{}
	for _, m := range r.s.watch {
		for id := range m {
			seen[id] = true
		}
	}
	out := make([]domain.InstrumentID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

type accountRepo struct{ s *Store }

func (r accountRepo) withOwner(a domain.Account) domain.Account {
	a.OwnerName = r.s.users[a.OwnerID].Username
	return a
}

func (r accountRepo) Create(_ context.Context, a domain.Account) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.accounts[a.ID]; ok {
		return domain.ErrConflict
	}
	a.Platform = a.OnPlatform()
	r.s.accounts[a.ID] = a
	return nil
}

func (r accountRepo) Get(_ context.Context, id string) (domain.Account, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	a, ok := r.s.accounts[id]
	if !ok {
		return domain.Account{}, domain.ErrNotFound
	}
	return r.withOwner(a), nil
}

func (r accountRepo) List(context.Context) ([]domain.Account, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.Account
	for _, a := range r.s.accounts {
		out = append(out, r.withOwner(a))
	}
	slices.SortFunc(out, func(a, b domain.Account) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r accountRepo) ForUser(_ context.Context, userID string) ([]domain.AccountView, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.AccountView
	for id, a := range r.s.accounts {
		v := domain.AccountView{Account: r.withOwner(a)}
		if a.OwnerID == userID {
			v.Access = domain.AccessOwner
		} else if sh, ok := r.s.shares[id][userID]; ok {
			v.Access = sh.Access
		} else {
			continue
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b domain.AccountView) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r accountRepo) Update(_ context.Context, a domain.Account) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	old, ok := r.s.accounts[a.ID]
	if !ok {
		return domain.ErrNotFound
	}
	old.Name, old.Mode, old.UpdatedAt = a.Name, a.Mode, a.UpdatedAt
	r.s.accounts[a.ID] = old
	return nil
}

func (r accountRepo) Delete(_ context.Context, id string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.accounts[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.accounts, id)
	delete(r.s.shares, id)
	delete(r.s.watch, id)
	r.s.equity = slices.DeleteFunc(r.s.equity, func(e domain.EquitySnapshot) bool { return e.AccountID == id })
	return nil
}

type equityRepo struct{ s *Store }

func (r equityRepo) Add(_ context.Context, e domain.EquitySnapshot) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.accounts[e.AccountID]; !ok {
		return domain.ErrNotFound
	}
	for _, x := range r.s.equity {
		if x.AccountID == e.AccountID && x.At.Equal(e.At) {
			return domain.ErrConflict
		}
	}
	r.s.equity = append(r.s.equity, e)
	return nil
}

func (r equityRepo) List(_ context.Context, ids []string, since time.Time) ([]domain.EquitySnapshot, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.EquitySnapshot
	for _, e := range r.s.equity {
		if slices.Contains(ids, e.AccountID) && !e.At.Before(since) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b domain.EquitySnapshot) int {
		return cmp.Or(strings.Compare(a.AccountID, b.AccountID), a.At.Compare(b.At))
	})
	return out, nil
}

func (r equityRepo) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	n := len(r.s.equity)
	r.s.equity = slices.DeleteFunc(r.s.equity, func(e domain.EquitySnapshot) bool { return e.At.Before(before) })
	return int64(n - len(r.s.equity)), nil
}

func (r accountRepo) Shares(_ context.Context, id string) ([]domain.AccountShare, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.AccountShare
	for _, sh := range r.s.shares[id] {
		sh.Username = r.s.users[sh.UserID].Username
		out = append(out, sh)
	}
	slices.SortFunc(out, func(a, b domain.AccountShare) int { return strings.Compare(a.Username, b.Username) })
	return out, nil
}

func (r accountRepo) SetShare(_ context.Context, sh domain.AccountShare) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.shares[sh.AccountID] == nil {
		r.s.shares[sh.AccountID] = map[string]domain.AccountShare{}
	}
	r.s.shares[sh.AccountID][sh.UserID] = sh
	return nil
}

func (r accountRepo) RemoveShare(_ context.Context, acct, user string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.shares[acct][user]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.shares[acct], user)
	return nil
}

func (r accountRepo) CountOwnedBy(_ context.Context, user string) (int, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	n := 0
	for _, a := range r.s.accounts {
		if a.OwnerID == user {
			n++
		}
	}
	return n, nil
}

type eventRepo struct{ s *Store }

func (r eventRepo) Append(_ context.Context, e domain.Event) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.events = append(r.s.events, e)
	return nil
}

func visible(e domain.Event, ids []string) bool {
	return e.AccountID == "" || slices.Contains(ids, e.AccountID)
}

func (r eventRepo) List(_ context.Context, ids []string, limit int) ([]domain.Event, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.Event
	for _, e := range r.s.events {
		if visible(e, ids) {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b domain.Event) int { return cmp.Or(b.At.Compare(a.At), strings.Compare(b.ID, a.ID)) }) // as SQL: at DESC, id DESC
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r eventRepo) CountSince(_ context.Context, ids []string, since time.Time) (int, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	n := 0
	for _, e := range r.s.events {
		if e.At.After(since) && visible(e, ids) {
			n++
		}
	}
	return n, nil
}

func (r eventRepo) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	keep := r.s.events[:0]
	var n int64
	for _, e := range r.s.events {
		if e.At.Before(before) {
			n++
			continue
		}
		keep = append(keep, e)
	}
	r.s.events = keep
	return n, nil
}

type themeRepo struct{ s *Store }

func (r themeRepo) Create(_ context.Context, t domain.Theme) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.themes[t.ID]; ok {
		return domain.ErrConflict
	}
	r.s.themes[t.ID] = t
	return nil
}

func (r themeRepo) Get(_ context.Context, id string) (domain.Theme, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	t, ok := r.s.themes[id]
	if !ok {
		return domain.Theme{}, domain.ErrNotFound
	}
	return t, nil
}

func (r themeRepo) List(_ context.Context, acct string) ([]domain.Theme, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.Theme
	for _, t := range r.s.themes {
		if t.AccountID == acct {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b domain.Theme) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (r themeRepo) Update(_ context.Context, t domain.Theme) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.themes[t.ID]; !ok {
		return domain.ErrNotFound
	}
	r.s.themes[t.ID] = t
	return nil
}

func (r themeRepo) Delete(_ context.Context, id string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.themes[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.themes, id)
	return nil
}

type reviewRepo struct{ s *Store }

func (r reviewRepo) Upsert(_ context.Context, v domain.Review) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.reviews[fmt.Sprintf("%s/%d", v.AccountID, v.Instrument)] = v
	return nil
}

func (r reviewRepo) List(_ context.Context, acct string, limit int) ([]domain.Review, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.Review
	for _, v := range r.s.reviews {
		if v.AccountID == acct {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b domain.Review) int { return cmp.Or(b.At.Compare(a.At), int(a.Instrument-b.Instrument)) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r reviewRepo) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var n int64
	for k, v := range r.s.reviews {
		if v.At.Before(before) {
			delete(r.s.reviews, k)
			n++
		}
	}
	return n, nil
}
