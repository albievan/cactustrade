package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/domain"
)

func toAccount(v domain.AccountView) Account {
	return Account{
		Id: v.ID, Name: v.Name, OwnerId: v.OwnerID, OwnerName: v.OwnerName, Mode: TradingMode(v.Mode),
		Platform: Platform(v.OnPlatform()), EnvKeys: v.EnvKeys, Access: AccountAccess(v.Access), CreatedAt: v.CreatedAt,
	}
}

// ListAccounts lists the caller's accounts.
func (s *Server) ListAccounts(w http.ResponseWriter, r *http.Request) {
	vs, err := s.svc.Accounts(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Account, 0, len(vs))
	for _, v := range vs {
		out = append(out, toAccount(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateAccount adds an account owned by the caller.
func (s *Server) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req CreateAccountRequest
	if !decode(w, r, &req) {
		return
	}
	platform := domain.PlatformEtoro
	if req.Platform != nil {
		platform = domain.Platform(*req.Platform)
	}
	v, err := s.svc.CreateAccountOn(r.Context(), state(r).principal, platform, req.Name, domain.TradingMode(req.Mode))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAccount(v))
}

// SetCurrentAccount switches the caller's account.
func (s *Server) SetCurrentAccount(w http.ResponseWriter, r *http.Request) {
	var req AccountRef
	if !decode(w, r, &req) {
		return
	}
	v, err := s.svc.SetCurrentAccount(r.Context(), state(r).principal, req.AccountId)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccount(v))
}

// RenameAccount renames an account.
func (s *Server) RenameAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	var req RenameAccountRequest
	if !decode(w, r, &req) {
		return
	}
	v, err := s.svc.RenameAccount(r.Context(), state(r).principal, accountID, req.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccount(v))
}

// DeleteAccount deletes an account.
func (s *Server) DeleteAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := s.svc.DeleteAccount(r.Context(), state(r).principal, accountID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetAccountKeys says whether the keys are set.
func (s *Server) GetAccountKeys(w http.ResponseWriter, r *http.Request, accountID string) {
	k, err := s.svc.AccountKeysStatus(r.Context(), state(r).principal, accountID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, AccountKeys{ApiKeySet: k.APIKeySet, UserKeySet: k.UserKeySet, FromEnvironment: k.FromEnvironment})
}

// SetAccountKeys stores the key pair.
func (s *Server) SetAccountKeys(w http.ResponseWriter, r *http.Request, accountID string) {
	var req SetAccountKeysRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.svc.SetAccountKeys(r.Context(), state(r).principal, accountID, req.ApiKey, req.UserKey); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestAccount tests an account's connection.
func (s *Server) TestAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	s.writeBrokerCheck(w, r, accountID)
}

// ListAccountShares lists the shares.
func (s *Server) ListAccountShares(w http.ResponseWriter, r *http.Request, accountID string) {
	sh, err := s.svc.AccountShares(r.Context(), state(r).principal, accountID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]AccountShare, 0, len(sh))
	for _, x := range sh {
		out = append(out, AccountShare{UserId: x.UserID, Username: x.Username, Access: AccountShareAccess(x.Access), CreatedAt: x.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// ShareAccount gives a user access.
func (s *Server) ShareAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	var req ShareAccountRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.svc.ShareAccount(r.Context(), state(r).principal, accountID, req.Username, domain.AccountAccess(req.Access)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnshareAccount removes a user's access.
func (s *Server) UnshareAccount(w http.ResponseWriter, r *http.Request, accountID, userID string) {
	if err := s.svc.UnshareAccount(r.Context(), state(r).principal, accountID, userID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
