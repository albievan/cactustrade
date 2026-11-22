// Package fakes holds in-memory implementations of the ports for use-case tests.
package fakes

import (
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
	PingErr  error
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		users: map[string]domain.User{}, sessions: map[string]domain.Session{},
		keys: map[string]domain.APIKey{}, secrets: map[string]ports.SecretRecord{},
		settings: map[string]string{},
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
