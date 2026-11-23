package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
)

// EnsureDefaultAdmin creates the built-in administrator admin/admin when no user exists
// (first start). The account must change its password at first sign-in before it can do
// anything else. It reports whether the account was created; a concurrent creator (another
// replica) is not an error.
func (s *Service) EnsureDefaultAdmin(ctx context.Context) (bool, error) {
	n, err := s.d.Store.Users().Count(ctx)
	if err != nil {
		return false, fmt.Errorf("app.EnsureDefaultAdmin: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	hash, err := s.d.Hasher.Hash(domain.DefaultAdminPassword)
	if err != nil {
		return false, fmt.Errorf("app.EnsureDefaultAdmin: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	u := domain.User{
		ID: s.d.Random.ID(), Username: domain.DefaultAdminUsername, Role: domain.RoleAdmin,
		PasswordHash: hash, MustChangePassword: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.d.Store.Users().CreateFirst(ctx, u); err != nil {
		if errors.Is(err, domain.ErrSetupDone) {
			return false, nil
		}
		return false, fmt.Errorf("app.EnsureDefaultAdmin: %w", err)
	}
	s.audit(ctx, Principal{}, "setup.default_admin_created", u.ID, "")
	return true, nil
}

func (s *Service) newUser(username, password string, role domain.Role) (domain.User, error) {
	username = domain.NormalizeUsername(username)
	if err := domain.ValidateUsername(username); err != nil {
		return domain.User{}, err
	}
	if err := domain.ValidatePassword(password, username); err != nil {
		return domain.User{}, err
	}
	if !role.Valid() {
		return domain.User{}, domain.Invalid("role", "must be admin, operator or viewer")
	}
	hash, err := s.d.Hasher.Hash(password)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	return domain.User{
		ID: s.d.Random.ID(), Username: username, Role: role, PasswordHash: hash,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// LoginResult is a new session.
type LoginResult struct {
	Token     string // goes into the cookie; never stored
	CSRFToken string
	User      domain.User
	Session   domain.Session
}

// Login checks a username and password and opens a session. Every failure is the same
// ErrUnauthenticated (no user enumeration), and an unknown user costs the same hash time.
func (s *Service) Login(ctx context.Context, username, password, remoteIP string) (LoginResult, error) {
	username = domain.NormalizeUsername(username)
	u, err := s.d.Store.Users().GetByUsername(ctx, username)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return LoginResult{}, fmt.Errorf("app.Login: %w", err)
	}
	hash := u.PasswordHash
	if err != nil {
		hash = s.dummyHash
	}
	ok, verr := s.d.Hasher.Verify(hash, password)
	if verr != nil {
		return LoginResult{}, fmt.Errorf("app.Login: %w", verr)
	}
	if err != nil || !ok || u.Disabled {
		s.audit(ctx, Principal{RemoteIP: remoteIP}, "auth.login_failed", username, "")
		return LoginResult{}, domain.ErrUnauthenticated
	}
	now := s.d.Clock.Now().UTC()
	tok := s.d.Random.Token()
	sess := domain.Session{
		TokenHash: hashToken(tok), UserID: u.ID, CSRFToken: s.d.Random.Token(),
		CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(s.cfg.Sessions.Absolute),
	}
	if err := s.d.Store.Sessions().Create(ctx, sess); err != nil {
		return LoginResult{}, fmt.Errorf("app.Login: %w", err)
	}
	s.audit(ctx, Principal{UserID: u.ID, Username: u.Username, RemoteIP: remoteIP}, "auth.login", u.ID, "")
	return LoginResult{Token: tok, CSRFToken: sess.CSRFToken, User: u, Session: sess}, nil
}

// Logout ends the session holding token (no error when it is already gone).
func (s *Service) Logout(ctx context.Context, token string) error {
	if err := s.d.Store.Sessions().Delete(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("app.Logout: %w", err)
	}
	return nil
}

// SessionPrincipal resolves a session cookie. It returns the principal and the session's
// CSRF token, and refreshes the idle timer.
func (s *Service) SessionPrincipal(ctx context.Context, token, remoteIP string) (Principal, string, error) {
	if token == "" {
		return Principal{}, "", domain.ErrUnauthenticated
	}
	h := hashToken(token)
	sess, err := s.d.Store.Sessions().Get(ctx, h)
	if errors.Is(err, domain.ErrNotFound) {
		return Principal{}, "", domain.ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, "", fmt.Errorf("app.SessionPrincipal: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	if !sess.Active(now, s.cfg.Sessions) {
		_ = s.d.Store.Sessions().Delete(ctx, h)
		return Principal{}, "", domain.ErrUnauthenticated
	}
	u, err := s.d.Store.Users().Get(ctx, sess.UserID)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && u.Disabled) {
		_ = s.d.Store.Sessions().Delete(ctx, h)
		return Principal{}, "", domain.ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, "", fmt.Errorf("app.SessionPrincipal: %w", err)
	}
	if now.Sub(sess.LastSeen) > s.cfg.Sessions.Idle/10 {
		if err := s.d.Store.Sessions().Touch(ctx, h, now); err != nil {
			s.d.Log.WarnContext(ctx, "session touch failed", "err", err)
		}
	}
	return Principal{
		UserID: u.ID, Username: u.Username, Role: u.Role, Via: ViaSession, RemoteIP: remoteIP,
		MustChangePassword: u.MustChangePassword,
	}, sess.CSRFToken, nil
}

// ChangePassword changes the caller's own password and signs out their other sessions.
func (s *Service) ChangePassword(ctx context.Context, p Principal, current, next string) error {
	if p.UserID == "" || p.Via != ViaSession {
		return domain.ErrForbidden
	}
	u, err := s.d.Store.Users().Get(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("app.ChangePassword: %w", err)
	}
	ok, err := s.d.Hasher.Verify(u.PasswordHash, current)
	if err != nil {
		return fmt.Errorf("app.ChangePassword: %w", err)
	}
	if !ok {
		return domain.Invalid("currentPassword", "is not correct")
	}
	if err := domain.ValidatePassword(next, u.Username); err != nil {
		return err
	}
	if u.PasswordHash, err = s.d.Hasher.Hash(next); err != nil {
		return fmt.Errorf("app.ChangePassword: %w", err)
	}
	u.MustChangePassword = false
	u.UpdatedAt = s.d.Clock.Now().UTC()
	if err := s.d.Store.Users().Update(ctx, u); err != nil {
		return fmt.Errorf("app.ChangePassword: %w", err)
	}
	if err := s.d.Store.Sessions().DeleteForUser(ctx, u.ID); err != nil {
		return fmt.Errorf("app.ChangePassword: %w", err)
	}
	s.audit(ctx, p, "auth.password_changed", u.ID, "")
	return nil
}

// Me returns the caller's user record.
func (s *Service) Me(ctx context.Context, p Principal) (domain.User, error) {
	if p.UserID == "" {
		return domain.User{}, domain.ErrUnauthenticated
	}
	u, err := s.d.Store.Users().Get(ctx, p.UserID)
	if err != nil {
		return domain.User{}, fmt.Errorf("app.Me: %w", err)
	}
	return u, nil
}

// PurgeExpiredSessions removes expired sessions (called periodically).
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	n, err := s.d.Store.Sessions().DeleteExpired(ctx, s.d.Clock.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("app.PurgeExpiredSessions: %w", err)
	}
	return n, nil
}
