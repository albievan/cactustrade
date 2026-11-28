package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// setCookie sends the session cookie. Its Max-Age is the session's own lifetime as the app's
// clock measured it at sign-in (ExpiresAt − CreatedAt), never the wall clock: mixing the two
// made the cookie expire at once whenever they disagreed (an injected clock in tests).
func (s *Server) setCookie(w http.ResponseWriter, token string, sess domain.Session) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/", Expires: sess.ExpiresAt,
		MaxAge: max(1, int(sess.ExpiresAt.Sub(sess.CreatedAt)/time.Second)), HttpOnly: true, Secure: s.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	clearCookieNamed(w, s.cookieName(), s.cfg.CookieSecure)
}

// OAuthCookie binds a platform sign-in (Saxo) to the browser that started it (PLAN 7.1.13):
// it holds the random nonce whose hash the app keeps with the OAuth state. SameSite=Lax,
// because the platform's redirect back is a cross-site top-level navigation (a Strict cookie
// would not come along); HttpOnly; it lives as long as a sign-in may take. With
// CookieSecure off the __Host- prefix is dropped (browsers refuse it without Secure).
const OAuthCookie = "__Host-cactustrade_oauth"

func (s *Server) oauthCookieName() string {
	if s.cfg.CookieSecure {
		return OAuthCookie
	}
	return "cactustrade_oauth"
}

func (s *Server) setOAuthCookie(w http.ResponseWriter, nonce string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.oauthCookieName(), Value: nonce, Path: "/", MaxAge: int(app.OAuthStateTTL / time.Second),
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearOAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.oauthCookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func clearCookieNamed(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

func toUser(u domain.User) User {
	return User{Id: u.ID, Username: u.Username, Role: Role(u.Role), Disabled: u.Disabled, MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

func toKey(k domain.APIKey) ApiKey {
	return ApiKey{Id: k.ID, OwnerId: k.OwnerID, Name: k.Name, Role: Role(k.Role), CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt, ExpiresAt: k.ExpiresAt}
}

func ptr[T any](v T) *T { return &v }

// Healthz reports liveness.
func (s *Server) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{Status: Ok})
}

// Readyz reports readiness (database reachable).
func (s *Server) Readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, Health{Status: Unavailable, Detail: ptr("database not reachable")})
		return
	}
	writeJSON(w, http.StatusOK, Health{Status: Ok})
}

// GetPublicStatus gives the sign-in page the version.
func (s *Server) GetPublicStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, PublicStatus{Version: s.svc.Version(), PasswordResetAvailable: s.svc.PasswordResetAvailable(r.Context())})
}

func tooMany(w http.ResponseWriter, r *http.Request, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	writeProblem(w, r, http.StatusTooManyRequests, "Too many attempts", "try again later", "")
}

// signInAttempt counts a password check for user from ip before the password is hashed
// (PLAN 7.1.4, 7.1.5) and waits out the per-user slow-down. false: the answer (429, or the
// client went away) is written.
func (s *Server) signInAttempt(w http.ResponseWriter, r *http.Request, ip, user string) bool {
	ok, wait, delay := s.limiter.SignIn(ip, user)
	if !ok {
		tooMany(w, r, wait)
		return false
	}
	if delay > 0 {
		if err := s.limiter.sleep(r.Context(), delay); err != nil {
			s.limiter.Refund(ip, user)
			return false
		}
	}
	return true
}

// Login signs in and sets the session cookie.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if !decode(w, r, &req) {
		return
	}
	ip, user := state(r).ip, domain.NormalizeUsername(req.Username)
	if !s.signInAttempt(w, r, ip, user) {
		return
	}
	res, err := s.svc.Login(r.Context(), req.Username, req.Password, ip)
	if err != nil {
		if errors.Is(err, domain.ErrUnauthenticated) {
			writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "wrong username or password", "")
			return
		}
		s.limiter.Refund(ip, user)
		s.fail(w, r, err)
		return
	}
	s.limiter.Success(ip, user)
	s.setCookie(w, res.Token, res.Session)
	writeJSON(w, http.StatusOK, Me{
		User: toUser(res.User), Via: Session, CsrfToken: ptr(res.CSRFToken),
		Permissions: perms(res.User.Role),
	})
}

func perms(r domain.Role) []string {
	ps := r.Permissions()
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

// Logout ends the session (CSRF-checked when signed in) and clears the cookie.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName()); err == nil && c.Value != "" {
		if err := s.svc.Logout(r.Context(), c.Value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// GetMe returns the caller.
func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	u, err := s.svc.Me(r.Context(), st.principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	me := Me{User: toUser(u), Via: MeVia(st.principal.Via), Permissions: perms(st.principal.Role)}
	if st.principal.Via == app.ViaSession {
		me.CsrfToken = ptr(st.csrf)
	}
	writeJSON(w, http.StatusOK, me)
}

// isWrongPassword reports whether err refused a current password (it counts as a failed
// sign-in attempt; every other refusal is refunded).
func isWrongPassword(err error) bool {
	ve, ok := errors.AsType[*domain.ValidationError](err)
	return ok && ve.Field == "currentPassword"
}

// ChangePassword changes the caller's password; their sessions end. The current-password
// check is limited like a sign-in (PLAN 7.1.10).
func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req ChangePasswordRequest
	if !decode(w, r, &req) {
		return
	}
	st := state(r)
	if !s.signInAttempt(w, r, st.ip, st.principal.Username) {
		return
	}
	if err := s.svc.ChangePassword(r.Context(), st.principal, req.CurrentPassword, req.NewPassword); err != nil {
		if !isWrongPassword(err) {
			s.limiter.Refund(st.ip, st.principal.Username)
		}
		s.fail(w, r, err)
		return
	}
	s.limiter.Success(st.ip, st.principal.Username)
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// GetSystemStatus reports version, mode and agent state.
func (s *Server) GetSystemStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.Status(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := SystemStatus{
		Version: st.Version, Build: st.Build, Mode: TradingMode(st.Mode), AgentState: AgentState(st.AgentState),
		Database: SystemStatusDatabase(st.Database), StartedAt: st.StartedAt, Now: st.Now,
	}
	if st.Account != nil {
		// with its currency when known (R45.11: pages show a Trading 212 account's money in it)
		out.Account = ptr(withExtra(toAccount(*st.Account), s.svc.AccountExtras(r.Context(), []domain.AccountView{*st.Account})[st.Account.ID]))
	}
	armed := make([]RealArmedAccount, 0, len(st.RealArmed))
	for _, a := range st.RealArmed {
		armed = append(armed, RealArmedAccount{Id: a.ID, Name: a.Name})
	}
	out.RealArmed = &armed
	out.RealArmedUnknown = &st.RealArmedUnknown
	writeJSON(w, http.StatusOK, out)
}

// ListUsers lists users.
func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.svc.ListUsers(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]User, 0, len(us))
	for _, u := range us {
		out = append(out, toUser(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateUser creates a user.
func (s *Server) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if !decode(w, r, &req) {
		return
	}
	u, err := s.svc.CreateUser(r.Context(), state(r).principal, req.Username, req.Password, domain.Role(req.Role))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUser(u))
}

// UpdateUser changes a user.
func (s *Server) UpdateUser(w http.ResponseWriter, r *http.Request, userID string) {
	var req UpdateUserRequest
	if !decode(w, r, &req) {
		return
	}
	up := app.UserUpdate{Disabled: req.Disabled, Password: req.Password}
	if req.Role != nil {
		up.Role = ptr(domain.Role(*req.Role))
	}
	u, err := s.svc.UpdateUser(r.Context(), state(r).principal, userID, up)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUser(u))
}

// DeleteUser deletes a user.
func (s *Server) DeleteUser(w http.ResponseWriter, r *http.Request, userID string) {
	if err := s.svc.DeleteUser(r.Context(), state(r).principal, userID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListApiKeys lists API keys.
func (s *Server) ListApiKeys(w http.ResponseWriter, r *http.Request) {
	ks, err := s.svc.ListAPIKeys(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]ApiKey, 0, len(ks))
	for _, k := range ks {
		out = append(out, toKey(k))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateApiKey creates an API key and returns its secret once.
func (s *Server) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	var req CreateApiKeyRequest
	if !decode(w, r, &req) {
		return
	}
	days := 0
	if req.ExpiresInDays != nil {
		days = *req.ExpiresInDays
	}
	ck, err := s.svc.CreateAPIKey(r.Context(), state(r).principal, req.Name, domain.Role(req.Role), days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreatedApiKey{ApiKey: toKey(ck.Key), Secret: ck.Secret})
}

// RevokeApiKey revokes an API key.
func (s *Server) RevokeApiKey(w http.ResponseWriter, r *http.Request, keyID string) {
	if err := s.svc.RevokeAPIKey(r.Context(), state(r).principal, keyID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListCredentials says which credentials are set.
func (s *Server) ListCredentials(w http.ResponseWriter, r *http.Request) {
	cs, err := s.svc.Credentials(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Credential, 0, len(cs))
	for _, c := range cs {
		x := Credential{Name: CredentialName(c.Name), Label: c.Label, Set: c.Set, UpdatedAt: c.UpdatedAt}
		if c.Source != "" {
			x.Source = ptr(CredentialSource(c.Source))
		}
		if c.EnvVar != "" {
			x.EnvVar = ptr(c.EnvVar)
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// ListAudit lists audit entries.
func (s *Server) ListAudit(w http.ResponseWriter, r *http.Request, params ListAuditParams) {
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	es, err := s.svc.AuditLog(r.Context(), state(r).principal, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]AuditEntry, 0, len(es))
	for _, e := range es {
		x := AuditEntry{Id: e.ID, At: e.At, Actor: e.Actor, Action: e.Action, Target: e.Target, Details: e.Details, RemoteIp: e.RemoteIP}
		if e.ActorID != "" {
			x.ActorId = ptr(e.ActorID)
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}
