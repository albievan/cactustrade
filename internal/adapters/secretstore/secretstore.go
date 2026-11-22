// Package secretstore implements ports.SecretStore: credentials are sealed with AES-256-GCM
// under the master key (the credential name is the associated data, so a ciphertext cannot
// be moved to another name) and stored through ports.Secrets. An environment overlay lets
// selected credentials (ETORO_DEMO_PUB / ETORO_DEMO_PK) come from the environment instead.
package secretstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Cipher is AES-256-GCM with a random 96-bit nonce per message (nonce || ciphertext).
type Cipher struct{ aead cipher.AEAD }

var _ ports.Cipher = (*Cipher)(nil)

// NewCipher builds a cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("secretstore: master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretstore: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretstore: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Seal encrypts plaintext bound to aad.
func (c *Cipher) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretstore: nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open decrypts and authenticates.
func (c *Cipher) Open(ct, aad []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(ct) < n+c.aead.Overhead() {
		return nil, errors.New("secretstore: ciphertext too short")
	}
	pt, err := c.aead.Open(nil, ct[:n], ct[n:], aad)
	if err != nil {
		return nil, errors.New("secretstore: cannot decrypt (wrong master key or tampered data)")
	}
	return pt, nil
}

// ParseMasterKey decodes 64 hex characters.
func ParseMasterKey(s string) ([]byte, error) {
	k, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(k) != 32 {
		return nil, errors.New("secretstore: the master key must be 64 hex characters (32 bytes)")
	}
	return k, nil
}

// LoadMasterKey returns the master key from, in order: the literal value (env
// CACTUSTRADE_MASTER_KEY), the file (CACTUSTRADE_MASTER_KEY_FILE), or — only when
// generatePath is set (standalone) — that file, created with a new random key and mode 0600
// if missing. It never logs the key. created reports a newly generated key.
func LoadMasterKey(value, file, generatePath string) (key []byte, created bool, err error) {
	switch {
	case value != "":
		k, err := ParseMasterKey(value)
		return k, false, err
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, false, fmt.Errorf("secretstore: read master key file: %w", err)
		}
		k, err := ParseMasterKey(string(b))
		return k, false, err
	case generatePath != "":
		if b, err := os.ReadFile(generatePath); err == nil {
			k, err := ParseMasterKey(string(b))
			return k, false, err
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("secretstore: read master key: %w", err)
		}
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return nil, false, fmt.Errorf("secretstore: generate master key: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(generatePath), 0o700); err != nil {
			return nil, false, fmt.Errorf("secretstore: %w", err)
		}
		f, err := os.OpenFile(generatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, false, fmt.Errorf("secretstore: write master key: %w", err)
		}
		if _, err := f.WriteString(hex.EncodeToString(k) + "\n"); err != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("secretstore: write master key: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, false, fmt.Errorf("secretstore: write master key: %w", err)
		}
		return k, true, nil
	}
	return nil, false, fmt.Errorf("secretstore: no master key: set CACTUSTRADE_MASTER_KEY or CACTUSTRADE_MASTER_KEY_FILE: %w", domain.ErrUnavailable)
}

// DB keeps credentials encrypted in the database.
type DB struct {
	repo   ports.Secrets
	cipher ports.Cipher
	clock  ports.Clock
}

var _ ports.SecretStore = (*DB)(nil)

// NewDB builds the database-backed store.
func NewDB(repo ports.Secrets, c ports.Cipher, clock ports.Clock) *DB {
	return &DB{repo: repo, cipher: c, clock: clock}
}

// Put seals and stores a value.
func (s *DB) Put(ctx context.Context, n domain.CredentialName, v string) error {
	ct, err := s.cipher.Seal([]byte(v), []byte(n))
	if err != nil {
		return err
	}
	return s.repo.Put(ctx, ports.SecretRecord{Name: string(n), Ciphertext: ct, UpdatedAt: s.clock.Now().UTC()})
}

// Get loads and opens a value.
func (s *DB) Get(ctx context.Context, n domain.CredentialName) (string, error) {
	r, err := s.repo.Get(ctx, string(n))
	if err != nil {
		return "", err
	}
	pt, err := s.cipher.Open(r.Ciphertext, []byte(n))
	if err != nil {
		return "", fmt.Errorf("%s: %w", n, err)
	}
	return string(pt), nil
}

// Delete removes a value.
func (s *DB) Delete(ctx context.Context, n domain.CredentialName) error {
	return s.repo.Delete(ctx, string(n))
}

// Status lists stored credentials.
func (s *DB) Status(ctx context.Context) (map[domain.CredentialName]ports.SecretMeta, error) {
	rs, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.CredentialName]ports.SecretMeta, len(rs))
	for _, r := range rs {
		out[domain.CredentialName(r.Name)] = ports.SecretMeta{Source: ports.SourceUI, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

// Env overlays credentials taken from the environment on a base store. An env-provided
// credential wins over a stored one and cannot be changed or deleted through the store.
type Env struct {
	base ports.SecretStore
	vals map[domain.CredentialName]string
}

var _ ports.SecretStore = (*Env)(nil)

// ErrFromEnvironment is returned when the UI tries to change an env-provided credential.
var ErrFromEnvironment = fmt.Errorf("this credential is set by an environment variable; change it there: %w", domain.ErrConflict)

// WithEnv wraps base with values from the environment.
func WithEnv(base ports.SecretStore, vals map[domain.CredentialName]string) *Env {
	return &Env{base: base, vals: maps.Clone(vals)}
}

// Put stores in the base store unless the environment provides the credential.
func (e *Env) Put(ctx context.Context, n domain.CredentialName, v string) error {
	if _, ok := e.vals[n]; ok {
		return ErrFromEnvironment
	}
	return e.base.Put(ctx, n, v)
}

// Get prefers the environment.
func (e *Env) Get(ctx context.Context, n domain.CredentialName) (string, error) {
	if v, ok := e.vals[n]; ok {
		return v, nil
	}
	return e.base.Get(ctx, n)
}

// Delete removes from the base store unless the environment provides the credential.
func (e *Env) Delete(ctx context.Context, n domain.CredentialName) error {
	if _, ok := e.vals[n]; ok {
		return ErrFromEnvironment
	}
	return e.base.Delete(ctx, n)
}

// Status merges both sources (the environment wins).
func (e *Env) Status(ctx context.Context) (map[domain.CredentialName]ports.SecretMeta, error) {
	st, err := e.base.Status(ctx)
	if err != nil {
		return nil, err
	}
	for n := range e.vals {
		st[n] = ports.SecretMeta{Source: ports.SourceEnv}
	}
	return st, nil
}

// FromEnvironment reads the credentials of domain.CredentialEnv through getenv: NAME, or
// the file named by NAME_FILE. Values are normalised; an unreadable file or invalid value is
// an error (fail closed rather than silently use nothing).
func FromEnvironment(getenv func(string) string, readFile func(string) ([]byte, error)) (map[domain.CredentialName]string, error) {
	out := map[domain.CredentialName]string{}
	for n, name := range domain.CredentialEnv {
		raw := getenv(name)
		if raw == "" {
			if f := getenv(name + "_FILE"); f != "" {
				b, err := readFile(f)
				if err != nil {
					return nil, fmt.Errorf("secretstore: %s_FILE: %w", name, err)
				}
				raw = string(b)
			}
		}
		if raw == "" {
			continue
		}
		v, err := domain.NormalizeCredential(raw)
		if err != nil {
			return nil, fmt.Errorf("secretstore: %s: %w", name, err)
		}
		out[n] = v
	}
	return out, nil
}
