package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// APIKeyPrefix starts every API key: ctk_<id>_<secret>.
const APIKeyPrefix = "ctk_"

// MaxAPIKeyDays bounds an API key's lifetime.
const MaxAPIKeyDays = 365

// CreatedAPIKey is a new key with its secret, shown once.
type CreatedAPIKey struct {
	Key    domain.APIKey
	Secret string // the full bearer value
}

// CreateAPIKey creates a key for the caller with a role no higher than their own.
// expiresInDays 0 = never (up to MaxAPIKeyDays otherwise). Only a signed-in user (not
// another API key) can create keys.
func (s *Service) CreateAPIKey(ctx context.Context, p Principal, name string, role domain.Role, expiresInDays int) (CreatedAPIKey, error) {
	if err := s.authorize(p, domain.PermManageOwnAPIKeys); err != nil {
		return CreatedAPIKey{}, err
	}
	if p.Via != ViaSession {
		return CreatedAPIKey{}, fmt.Errorf("API keys cannot create API keys: %w", domain.ErrForbidden)
	}
	name = strings.TrimSpace(name)
	if err := domain.ValidateKeyName(name); err != nil {
		return CreatedAPIKey{}, err
	}
	if !role.Valid() {
		return CreatedAPIKey{}, domain.Invalid("role", "must be admin, operator or viewer")
	}
	if !role.AtMost(p.Role) {
		return CreatedAPIKey{}, domain.Invalid("role", "cannot exceed your own role")
	}
	if expiresInDays < 0 || expiresInDays > MaxAPIKeyDays {
		return CreatedAPIKey{}, domain.Invalid("expiresInDays", "0-365")
	}
	now := s.d.Clock.Now().UTC()
	secret := s.d.Random.Token()
	k := domain.APIKey{
		ID: s.d.Random.ID(), OwnerID: p.UserID, Name: name, Role: role,
		SecretHash: hashToken(secret), CreatedAt: now,
	}
	if expiresInDays > 0 {
		exp := now.Add(time.Duration(expiresInDays) * 24 * time.Hour)
		k.ExpiresAt = &exp
	}
	if err := s.d.Store.APIKeys().Create(ctx, k); err != nil {
		return CreatedAPIKey{}, fmt.Errorf("app.CreateAPIKey: %w", err)
	}
	s.audit(ctx, p, "apikey.create", k.ID, "name="+name+" role="+string(role))
	return CreatedAPIKey{Key: k, Secret: APIKeyPrefix + k.ID + "_" + secret}, nil
}

// ListAPIKeys lists the caller's keys; an admin sees everyone's.
func (s *Service) ListAPIKeys(ctx context.Context, p Principal) ([]domain.APIKey, error) {
	if err := s.authorize(p, domain.PermManageOwnAPIKeys); err != nil {
		return nil, err
	}
	owner := p.UserID
	if p.Can(domain.PermManageUsers) {
		owner = ""
	}
	ks, err := s.d.Store.APIKeys().List(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("app.ListAPIKeys: %w", err)
	}
	return ks, nil
}

// RevokeAPIKey deletes a key: the caller's own, or anyone's for an admin.
func (s *Service) RevokeAPIKey(ctx context.Context, p Principal, id string) error {
	if err := s.authorize(p, domain.PermManageOwnAPIKeys); err != nil {
		return err
	}
	k, err := s.d.Store.APIKeys().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("app.RevokeAPIKey: %w", err)
	}
	if k.OwnerID != p.UserID && !p.Can(domain.PermManageUsers) {
		return domain.ErrNotFound // do not reveal other users' keys
	}
	if err := s.d.Store.APIKeys().Delete(ctx, id); err != nil {
		return fmt.Errorf("app.RevokeAPIKey: %w", err)
	}
	s.audit(ctx, p, "apikey.revoke", id, "")
	return nil
}

// APIKeyPrincipal resolves a bearer API key. The principal's role is the key's role, capped
// by the owner's current role; a disabled owner or expired key fails.
func (s *Service) APIKeyPrincipal(ctx context.Context, bearer, remoteIP string) (Principal, error) {
	rest, ok := strings.CutPrefix(bearer, APIKeyPrefix)
	if !ok {
		return Principal{}, domain.ErrUnauthenticated
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || id == "" || secret == "" {
		return Principal{}, domain.ErrUnauthenticated
	}
	k, err := s.d.Store.APIKeys().Get(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return Principal{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("app.APIKeyPrincipal: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(hashToken(secret)), []byte(k.SecretHash)) != 1 {
		return Principal{}, domain.ErrUnauthenticated
	}
	now := s.d.Clock.Now().UTC()
	if !k.Usable(now) {
		return Principal{}, domain.ErrUnauthenticated
	}
	u, err := s.d.Store.Users().Get(ctx, k.OwnerID)
	if err != nil || u.Disabled {
		return Principal{}, domain.ErrUnauthenticated
	}
	role := k.Role
	if !role.AtMost(u.Role) {
		role = u.Role
	}
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) > time.Minute {
		if err := s.d.Store.APIKeys().Touch(ctx, k.ID, now); err != nil {
			s.d.Log.WarnContext(ctx, "api key touch failed", "key", k.ID, "err", err)
		}
	}
	return Principal{UserID: u.ID, Username: u.Username, Role: role, Via: ViaAPIKey, APIKeyID: k.ID, RemoteIP: remoteIP}, nil
}
