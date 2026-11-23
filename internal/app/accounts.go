package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

func currentAccountKey(userID string) string { return "user." + userID + ".account" }

// effective caps an account access level by the principal's role: a viewer only ever views,
// and trading needs the agent.control permission.
func effective(p Principal, a domain.AccountAccess) domain.AccountAccess {
	if a.AtLeast(domain.AccessTrade) && !p.Can(domain.PermAgentControl) {
		return domain.AccessView
	}
	return a
}

// Accounts lists the accounts the caller owns or was given, with the caller's access.
func (s *Service) Accounts(ctx context.Context, p Principal) ([]domain.AccountView, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	vs, err := s.d.Store.Accounts().ForUser(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("app.Accounts: %w", err)
	}
	for i := range vs {
		vs[i].Access = effective(p, vs[i].Access)
	}
	return vs, nil
}

// account resolves an account for the caller and checks the access it needs. id "" means
// the caller's current account. An account the caller cannot see is "not found".
func (s *Service) account(ctx context.Context, p Principal, id string, need domain.AccountAccess) (domain.AccountView, error) {
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return domain.AccountView{}, err
	}
	if id == "" {
		id = s.currentAccountID(ctx, p, vs)
		if id == "" {
			return domain.AccountView{}, fmt.Errorf("no eToro account yet: add one under Accounts: %w", domain.ErrNotFound)
		}
	}
	i := slices.IndexFunc(vs, func(v domain.AccountView) bool { return v.ID == id })
	if i < 0 {
		return domain.AccountView{}, fmt.Errorf("eToro account: %w", domain.ErrNotFound)
	}
	if !vs[i].Access.AtLeast(need) {
		return domain.AccountView{}, fmt.Errorf("you need %s access to %s: %w", need, vs[i].Name, domain.ErrForbidden)
	}
	return vs[i], nil
}

func (s *Service) currentAccountID(ctx context.Context, p Principal, vs []domain.AccountView) string {
	if v, err := s.d.Store.Settings().Get(ctx, currentAccountKey(p.UserID)); err == nil {
		if slices.ContainsFunc(vs, func(a domain.AccountView) bool { return a.ID == v }) {
			return v
		}
	}
	for _, v := range vs { // owned first, then shared
		if v.Access == domain.AccessOwner {
			return v.ID
		}
	}
	if len(vs) > 0 {
		return vs[0].ID
	}
	return ""
}

// CurrentAccount is the account the caller is working on ("" view when there is none).
func (s *Service) CurrentAccount(ctx context.Context, p Principal) (domain.AccountView, error) {
	return s.account(ctx, p, "", domain.AccessView)
}

// SetCurrentAccount switches the caller's current account.
func (s *Service) SetCurrentAccount(ctx context.Context, p Principal, id string) (domain.AccountView, error) {
	v, err := s.account(ctx, p, id, domain.AccessView)
	if err != nil {
		return domain.AccountView{}, err
	}
	if err := s.d.Store.Settings().Set(ctx, currentAccountKey(p.UserID), v.ID, s.d.Clock.Now().UTC()); err != nil {
		return domain.AccountView{}, fmt.Errorf("app.SetCurrentAccount: %w", err)
	}
	return v, nil
}

// brokerCtx is ctx carrying the account for the eToro adapter.
func brokerCtx(ctx context.Context, a domain.AccountView) context.Context {
	return ports.WithAccount(ctx, a.Account)
}

// CreateAccount adds an eToro account owned by the caller (operator or admin). A real
// account can be added and its keys stored, but it trades only after the phase-7 gate.
func (s *Service) CreateAccount(ctx context.Context, p Principal, name string, mode domain.TradingMode) (domain.AccountView, error) {
	if err := s.authorize(p, domain.PermAgentControl); err != nil {
		return domain.AccountView{}, err
	}
	name, err := domain.ValidateAccountName(name)
	if err != nil {
		return domain.AccountView{}, err
	}
	if mode != domain.ModeDemo && mode != domain.ModeReal {
		return domain.AccountView{}, domain.Invalid("mode", "demo or real")
	}
	now := s.d.Clock.Now().UTC()
	a := domain.Account{ID: s.d.Random.ID(), Name: name, OwnerID: p.UserID, OwnerName: p.Username, Mode: mode, CreatedAt: now, UpdatedAt: now}
	if err := s.d.Store.Accounts().Create(ctx, a); err != nil {
		return domain.AccountView{}, fmt.Errorf("app.CreateAccount: %w", err)
	}
	s.audit(ctx, p, "account.create", a.ID, "name="+name+" mode="+string(mode))
	s.emitFor(ctx, a.ID, p.Username, "account.created", domain.SeverityInfo, "eToro account "+name+" added ("+string(mode)+")", "")
	return domain.AccountView{Account: a, Access: domain.AccessOwner}, nil
}

// RenameAccount changes an account's name (owner).
func (s *Service) RenameAccount(ctx context.Context, p Principal, id, name string) (domain.AccountView, error) {
	v, err := s.account(ctx, p, id, domain.AccessOwner)
	if err != nil {
		return domain.AccountView{}, err
	}
	if v.Name, err = domain.ValidateAccountName(name); err != nil {
		return domain.AccountView{}, err
	}
	v.UpdatedAt = s.d.Clock.Now().UTC()
	if err := s.d.Store.Accounts().Update(ctx, v.Account); err != nil {
		return domain.AccountView{}, fmt.Errorf("app.RenameAccount: %w", err)
	}
	s.audit(ctx, p, "account.rename", v.ID, "name="+v.Name)
	return v, nil
}

// DeleteAccount removes an account, its keys, shares and watchlist (owner, web session).
// The environment account comes back on the next start while its variables are set.
func (s *Service) DeleteAccount(ctx context.Context, p Principal, id string) error {
	v, err := s.account(ctx, p, id, domain.AccessOwner)
	if err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	if st := s.agentState(ctx, v.ID); st == domain.AgentRunning || st == domain.AgentPaused {
		return fmt.Errorf("stop the agent of %s first: %w", v.Name, domain.ErrConflict)
	}
	if !v.EnvKeys {
		ak, uk := domain.AccountKeyNames(v.Account)
		for _, n := range []domain.CredentialName{ak, uk} {
			if err := s.d.Secrets.Delete(ctx, n); err != nil && !errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("app.DeleteAccount: %w", err)
			}
		}
	}
	if err := s.d.Store.Accounts().Delete(ctx, v.ID); err != nil {
		return fmt.Errorf("app.DeleteAccount: %w", err)
	}
	s.audit(ctx, p, "account.delete", v.ID, "name="+v.Name)
	s.emit(ctx, p.Username, "account.deleted", domain.SeverityWarning, "eToro account "+v.Name+" deleted", "")
	return nil
}

// AccountKeys says whether an account's key pair is set and where from (never the values).
type AccountKeys struct {
	APIKeySet, UserKeySet bool
	FromEnvironment       bool
}

// AccountKeysStatus reports the key pair's state (view).
func (s *Service) AccountKeysStatus(ctx context.Context, p Principal, id string) (AccountKeys, error) {
	v, err := s.account(ctx, p, id, domain.AccessView)
	if err != nil {
		return AccountKeys{}, err
	}
	st, err := s.d.Secrets.Status(ctx)
	if err != nil {
		return AccountKeys{}, fmt.Errorf("app.AccountKeysStatus: %w", err)
	}
	ak, uk := domain.AccountKeyNames(v.Account)
	a, aok := st[ak]
	u, uok := st[uk]
	return AccountKeys{APIKeySet: aok, UserKeySet: uok, FromEnvironment: a.Source == ports.SourceEnv || u.Source == ports.SourceEnv}, nil
}

// SetAccountKeys stores an account's eToro key pair, write-only (owner, web session).
// Either value may be empty to keep the stored one.
func (s *Service) SetAccountKeys(ctx context.Context, p Principal, id, apiKey, userKey string) error {
	v, err := s.account(ctx, p, id, domain.AccessOwner)
	if err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("keys can only be set in the web UI: %w", domain.ErrForbidden)
	}
	ak, uk := domain.AccountKeyNames(v.Account)
	for _, kv := range []struct {
		name domain.CredentialName
		val  string
	}{{ak, apiKey}, {uk, userKey}} {
		if kv.val == "" {
			continue
		}
		val, err := domain.NormalizeCredential(kv.val)
		if err != nil {
			return err
		}
		if err := s.d.Secrets.Put(ctx, kv.name, val); err != nil {
			return fmt.Errorf("app.SetAccountKeys: %w", err)
		}
	}
	s.audit(ctx, p, "account.keys_set", v.ID, "") // never the values
	return nil
}

// AccountShares lists who an account is shared with (owner).
func (s *Service) AccountShares(ctx context.Context, p Principal, id string) ([]domain.AccountShare, error) {
	v, err := s.account(ctx, p, id, domain.AccessOwner)
	if err != nil {
		return nil, err
	}
	return s.d.Store.Accounts().Shares(ctx, v.ID)
}

// ShareAccount gives another user view or trade access (owner, web session).
func (s *Service) ShareAccount(ctx context.Context, p Principal, id, username string, access domain.AccountAccess) error {
	v, err := s.account(ctx, p, id, domain.AccessOwner)
	if err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	if !access.Shareable() {
		return domain.Invalid("access", "view or trade")
	}
	u, err := s.d.Store.Users().GetByUsername(ctx, domain.NormalizeUsername(username))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Invalid("username", "no such user")
	}
	if err != nil {
		return fmt.Errorf("app.ShareAccount: %w", err)
	}
	if u.ID == v.OwnerID {
		return domain.Invalid("username", "the owner already has full access")
	}
	if err := s.d.Store.Accounts().SetShare(ctx, domain.AccountShare{AccountID: v.ID, UserID: u.ID, Access: access, CreatedAt: s.d.Clock.Now().UTC()}); err != nil {
		return fmt.Errorf("app.ShareAccount: %w", err)
	}
	s.audit(ctx, p, "account.share", v.ID, "user="+u.Username+" access="+string(access))
	s.emitFor(ctx, v.ID, p.Username, "account.shared", domain.SeverityInfo, v.Name+" shared with "+u.Username+" ("+string(access)+")", "")
	return nil
}

// UnshareAccount removes a user's access (owner, web session).
func (s *Service) UnshareAccount(ctx context.Context, p Principal, id, userID string) error {
	v, err := s.account(ctx, p, id, domain.AccessOwner)
	if err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	if err := s.d.Store.Accounts().RemoveShare(ctx, v.ID, userID); err != nil {
		return fmt.Errorf("app.UnshareAccount: %w", err)
	}
	s.audit(ctx, p, "account.unshare", v.ID, "user="+userID)
	return nil
}

// EnsureEnvAccount creates the "Demo (environment)" account, owned by the first
// administrator, when the deployment provides demo keys (ETORO_DEMO_PUB / _PK or the
// stored legacy pair) and the account does not exist yet. Called at start-up.
func (s *Service) EnsureEnvAccount(ctx context.Context) (bool, error) {
	if _, err := s.d.Store.Accounts().Get(ctx, domain.EnvAccountID); err == nil {
		return false, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return false, fmt.Errorf("app.EnsureEnvAccount: %w", err)
	}
	st, err := s.d.Secrets.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("app.EnsureEnvAccount: %w", err)
	}
	if _, ok := st[domain.CredEtoroDemoAPIKey]; !ok {
		return false, nil
	}
	if _, ok := st[domain.CredEtoroDemoUserKey]; !ok {
		return false, nil
	}
	users, err := s.d.Store.Users().List(ctx)
	if err != nil {
		return false, fmt.Errorf("app.EnsureEnvAccount: %w", err)
	}
	var owner *domain.User
	for i, u := range users {
		if u.Role == domain.RoleAdmin && (owner == nil || u.CreatedAt.Before(owner.CreatedAt)) {
			owner = &users[i]
		}
	}
	if owner == nil {
		return false, nil
	}
	now := s.d.Clock.Now().UTC()
	a := domain.Account{ID: domain.EnvAccountID, Name: "Demo (environment)", OwnerID: owner.ID, Mode: domain.ModeDemo, EnvKeys: true, CreatedAt: now, UpdatedAt: now}
	if err := s.d.Store.Accounts().Create(ctx, a); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return false, nil
		}
		return false, fmt.Errorf("app.EnsureEnvAccount: %w", err)
	}
	s.audit(ctx, Principal{}, "account.create", a.ID, "from the environment keys, owner="+owner.Username)
	return true, nil
}
