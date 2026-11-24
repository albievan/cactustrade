package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/albievan/cactustrade/internal/app"
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

func toCredentialSpec(c domain.CredentialSpec) CredentialSpec {
	return CredentialSpec{Key: c.Key, Label: c.Label, Help: c.Help, Multiline: c.Multiline, Optional: c.Optional, SetByFlow: c.SetByFlow}
}

// GetAccountKeys says which credentials are set (never their values).
func (s *Server) GetAccountKeys(w http.ResponseWriter, r *http.Request, accountID string) {
	k, err := s.svc.AccountKeysStatus(r.Context(), state(r).principal, accountID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := AccountKeys{
		ApiKeySet: k.APIKeySet, UserKeySet: k.UserKeySet, FromEnvironment: k.FromEnvironment, Complete: k.Complete,
		Connectable: k.Connectable, Credentials: make([]AccountCredential, 0, len(k.Credentials)),
	}
	if k.ConnectRedirectURI != "" {
		out.ConnectRedirectUri = ptr(k.ConnectRedirectURI)
	}
	for _, c := range k.Credentials {
		x := AccountCredential{Key: c.Key, Label: c.Label, Help: c.Help, Multiline: c.Multiline, Optional: c.Optional, SetByFlow: c.SetByFlow, Set: c.Set}
		if !c.UpdatedAt.IsZero() {
			x.UpdatedAt = ptr(c.UpdatedAt)
		}
		out.Credentials = append(out.Credentials, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// SetAccountKeys stores credentials: by key (values) and/or the older first-two fields.
func (s *Server) SetAccountKeys(w http.ResponseWriter, r *http.Request, accountID string) {
	var req SetAccountKeysRequest
	if !decode(w, r, &req) {
		return
	}
	p := state(r).principal
	var err error
	switch {
	case req.Values != nil && (req.ApiKey != nil || req.UserKey != nil):
		err = domain.Invalid("values", "send either values or apiKey/userKey, not both")
	case req.Values != nil:
		err = s.svc.SetAccountCredentials(r.Context(), p, accountID, *req.Values)
	default:
		err = s.svc.SetAccountKeys(r.Context(), p, accountID, deref(req.ApiKey), deref(req.UserKey))
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ListPlatforms describes the platforms and their credentials.
func (s *Server) ListPlatforms(w http.ResponseWriter, r *http.Request) {
	ps, err := s.svc.Platforms(state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]PlatformInfo, 0, len(ps))
	for _, p := range ps {
		x := PlatformInfo{Platform: Platform(p.Platform), Label: p.Label, ViewOnly: p.ViewOnly, Connectable: p.Connectable, Credentials: make([]CredentialSpec, 0, len(p.Credentials))}
		for _, c := range p.Credentials {
			x.Credentials = append(x.Credentials, toCredentialSpec(c))
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// ConnectSaxoAccount starts the Saxo sign-in of an account.
func (s *Server) ConnectSaxoAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	cs, err := s.svc.ConnectAccount(r.Context(), state(r).principal, domain.PlatformSaxo, accountID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ConnectStart{AuthorizeUrl: cs.AuthorizeURL, RedirectUri: cs.RedirectURI})
}

// SaxoCallback finishes the Saxo sign-in and sends the browser back to the accounts page.
// Only a fixed reason goes into the address, never Saxo's text.
func (s *Server) SaxoCallback(w http.ResponseWriter, r *http.Request, params SaxoCallbackParams) {
	ip := state(r).ip
	if ok, _ := s.limiter.Allowed(oauthIP, ip); !ok {
		http.Redirect(w, r, "/accounts?connect_error=failed", http.StatusSeeOther)
		return
	}
	a, err := s.svc.CompleteConnect(r.Context(), domain.PlatformSaxo, deref(params.Code), deref(params.State), deref(params.Error), ip)
	if err != nil {
		reason := "failed"
		var ve *domain.ValidationError
		switch {
		case errors.As(err, &ve) && ve.Field == "state":
			reason = "expired"
			s.limiter.Failed(oauthIP, ip) // forged or replayed states count
		case errors.Is(err, app.ErrConnectRefused):
			reason = "refused"
		}
		s.log.WarnContext(r.Context(), "Saxo sign-in not completed", "reason", reason, "err", err)
		http.Redirect(w, r, "/accounts?connect_error="+reason, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/accounts?connected="+url.QueryEscape(a.ID), http.StatusSeeOther)
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
