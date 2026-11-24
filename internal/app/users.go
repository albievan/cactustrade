package app

import (
	"context"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
)

// ListUsers lists every user (admin).
func (s *Service) ListUsers(ctx context.Context, p Principal) ([]domain.User, error) {
	if err := s.authorize(p, domain.PermManageUsers); err != nil {
		return nil, err
	}
	us, err := s.d.Store.Users().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("app.ListUsers: %w", err)
	}
	return us, nil
}

// userAdmin checks that p may manage the user target ("" = a new user): the manage-users
// permission and a web session (PLAN 7.1.1: an API key never creates, changes or deletes a
// user). As defence in depth, a key is also refused outright when it targets its own owner.
func (s *Service) userAdmin(p Principal, target string) error {
	if err := s.authorize(p, domain.PermManageUsers); err != nil {
		return err
	}
	if p.APIKeyID != "" && target == p.UserID {
		return fmt.Errorf("an API key cannot change its own user: %w", domain.ErrForbidden)
	}
	if p.Via != ViaSession {
		return fmt.Errorf("users are managed in the web UI only: %w", domain.ErrForbidden)
	}
	return nil
}

// CreateUser adds a user (admin, web session).
func (s *Service) CreateUser(ctx context.Context, p Principal, username, password string, role domain.Role) (domain.User, error) {
	if err := s.userAdmin(p, ""); err != nil {
		return domain.User{}, err
	}
	u, err := s.newUser(username, password, role)
	if err != nil {
		return domain.User{}, err
	}
	if err := s.d.Store.Users().Create(ctx, u); err != nil {
		return domain.User{}, fmt.Errorf("app.CreateUser: %w", err)
	}
	s.audit(ctx, p, "user.create", u.ID, "username="+u.Username+" role="+string(u.Role))
	return u, nil
}

// UserUpdate holds the fields an admin may change; nil = unchanged.
type UserUpdate struct {
	Role     *domain.Role
	Disabled *bool
	Password *string
}

// UpdateUser changes a user's role, enabled state or password (admin, web session). The last enabled
// admin can be neither demoted nor disabled. A disabled user or a new password ends the
// user's sessions; disabling or demoting also revokes API keys above the new role.
func (s *Service) UpdateUser(ctx context.Context, p Principal, id string, up UserUpdate) (domain.User, error) {
	if err := s.userAdmin(p, id); err != nil {
		return domain.User{}, err
	}
	u, err := s.d.Store.Users().Get(ctx, id)
	if err != nil {
		return domain.User{}, fmt.Errorf("app.UpdateUser: %w", err)
	}
	wasActiveAdmin := u.Role == domain.RoleAdmin && !u.Disabled
	details := ""
	if up.Role != nil {
		if !up.Role.Valid() {
			return domain.User{}, domain.Invalid("role", "must be admin, operator or viewer")
		}
		details += " role=" + string(*up.Role)
		u.Role = *up.Role
	}
	if up.Disabled != nil {
		details += fmt.Sprintf(" disabled=%t", *up.Disabled)
		u.Disabled = *up.Disabled
	}
	if up.Password != nil {
		if err := domain.ValidatePassword(*up.Password, u.Username); err != nil {
			return domain.User{}, err
		}
		if u.PasswordHash, err = s.d.Hasher.Hash(*up.Password); err != nil {
			return domain.User{}, fmt.Errorf("app.UpdateUser: %w", err)
		}
		details += " password=reset"
	}
	if wasActiveAdmin && (u.Role != domain.RoleAdmin || u.Disabled) {
		if err := s.ensureAnotherAdmin(ctx); err != nil {
			return domain.User{}, err
		}
	}
	u.UpdatedAt = s.d.Clock.Now().UTC()
	if err := s.d.Store.Users().Update(ctx, u); err != nil {
		return domain.User{}, fmt.Errorf("app.UpdateUser: %w", err)
	}
	if u.Disabled || up.Password != nil {
		if err := s.d.Store.Sessions().DeleteForUser(ctx, u.ID); err != nil {
			return domain.User{}, fmt.Errorf("app.UpdateUser: %w", err)
		}
	}
	if err := s.trimKeys(ctx, u); err != nil {
		return domain.User{}, err
	}
	s.audit(ctx, p, "user.update", u.ID, details[min(1, len(details)):])
	return u, nil
}

// trimKeys revokes a user's API keys that exceed their role, or all of them when disabled.
func (s *Service) trimKeys(ctx context.Context, u domain.User) error {
	keys, err := s.d.Store.APIKeys().List(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("app.trimKeys: %w", err)
	}
	for _, k := range keys {
		if u.Disabled || !k.Role.AtMost(u.Role) {
			if err := s.d.Store.APIKeys().Delete(ctx, k.ID); err != nil {
				return fmt.Errorf("app.trimKeys: %w", err)
			}
		}
	}
	return nil
}

func (s *Service) ensureAnotherAdmin(ctx context.Context) error {
	n, err := s.d.Store.Users().CountActiveAdmins(ctx)
	if err != nil {
		return fmt.Errorf("count admins: %w", err)
	}
	if n <= 1 {
		return fmt.Errorf("the last enabled admin must stay: %w", domain.ErrConflict)
	}
	return nil
}

// DeleteUser removes a user, their sessions and API keys (admin, web session). Admins cannot
// delete themselves, and the last enabled admin stays.
func (s *Service) DeleteUser(ctx context.Context, p Principal, id string) error {
	if err := s.userAdmin(p, id); err != nil {
		return err
	}
	if id == p.UserID {
		return fmt.Errorf("you cannot delete yourself: %w", domain.ErrConflict)
	}
	u, err := s.d.Store.Users().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("app.DeleteUser: %w", err)
	}
	if u.Role == domain.RoleAdmin && !u.Disabled {
		if err := s.ensureAnotherAdmin(ctx); err != nil {
			return err
		}
	}
	if n, err := s.d.Store.Accounts().CountOwnedBy(ctx, id); err != nil {
		return fmt.Errorf("app.DeleteUser: %w", err)
	} else if n > 0 {
		return fmt.Errorf("%s owns %d eToro account(s): delete them first: %w", u.Username, n, domain.ErrConflict)
	}
	if err := s.d.Store.Sessions().DeleteForUser(ctx, id); err != nil {
		return fmt.Errorf("app.DeleteUser: %w", err)
	}
	if err := s.d.Store.APIKeys().DeleteForOwner(ctx, id); err != nil {
		return fmt.Errorf("app.DeleteUser: %w", err)
	}
	if err := s.d.Store.Users().Delete(ctx, id); err != nil {
		return fmt.Errorf("app.DeleteUser: %w", err)
	}
	s.audit(ctx, p, "user.delete", id, "username="+u.Username)
	return nil
}
