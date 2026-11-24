package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The browser sign-in of view-only platforms that have one (Saxo: OAuth 2.0 authorization
// code grant). The owner starts it from the web UI; the platform sends the browser back to
// the public callback with a code and the state we issued. The state is random, stored only
// as a hash, single-use, valid OAuthStateTTL, and bound to the user and the account. It is
// also bound to the browser that started it (PLAN 7.1.13): a random nonce goes into a cookie
// there, and the callback must bring the same cookie back (its hash is kept with the state).
// PKCE (RFC 7636, S256) binds the code to us: the verifier never leaves this process until
// the code exchange.

// OAuthStateTTL is how long a sign-in may take.
const OAuthStateTTL = 10 * time.Minute

// maxOAuthStates bounds the pending sign-ins kept in memory.
const maxOAuthStates = 1000

type oauthState struct {
	userID, accountID string
	platform          domain.Platform
	expires           time.Time
	nonceHash         string // hash of the browser nonce (the cookie)
	verifier          string // the PKCE code verifier (memory only)
}

// oauthStates holds the pending sign-ins, keyed by the state's hash. They live in memory: a
// restart only means connecting again (cactusTrade runs one replica).
type oauthStates struct {
	mu sync.Mutex
	m  map[string]oauthState
}

func newOAuthStates() *oauthStates { return &oauthStates{m: map[string]oauthState{}} }

// put stores a state after dropping expired ones; false when too many are pending.
func (o *oauthStates) put(hash string, st oauthState, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for h, x := range o.m {
		if !now.Before(x.expires) {
			delete(o.m, h)
		}
	}
	if len(o.m) >= maxOAuthStates {
		return false
	}
	o.m[hash] = st
	return true
}

// take removes and returns a state (single use), whether or not it has expired.
func (o *oauthStates) take(hash string) (oauthState, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st, ok := o.m[hash]
	delete(o.m, hash)
	return st, ok
}

// PlatformInfo describes a platform for the UI: whether it is view-only, signs in through the
// browser, and which credentials its accounts have (never values).
type PlatformInfo struct {
	Platform    domain.Platform
	Label       string
	ViewOnly    bool
	Connectable bool
	Credentials []domain.CredentialSpec
}

// Platforms lists every platform an account can be on, eToro first (viewer).
func (s *Service) Platforms(p Principal) ([]PlatformInfo, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	out := make([]PlatformInfo, 0, len(domain.Platforms))
	for _, pl := range domain.Platforms {
		_, conn := s.d.Connectors[pl]
		out = append(out, PlatformInfo{
			Platform: pl, Label: pl.Label(), ViewOnly: !(domain.Account{Platform: pl}).Tradable(),
			Connectable: conn, Credentials: domain.PlatformCredentials(pl),
		})
	}
	return out, nil
}

// OAuthCallbackPath is the callback path for a platform; the full redirect URI is
// CACTUSTRADE_PUBLIC_URL followed by it.
func OAuthCallbackPath(pl domain.Platform) string { return "/api/v1/oauth/" + string(pl) + "/callback" }

// redirectURI is the address the platform sends the browser back to ("" without a public URL).
func (s *Service) redirectURI(pl domain.Platform) string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + OAuthCallbackPath(pl)
}

// ConnectStart is where to send the owner's browser to sign in, and the redirect address the
// platform must have registered. BrowserNonce goes into a cookie in that browser (the HTTP
// adapter's job; never into a response body): the callback is refused without it.
type ConnectStart struct {
	AuthorizeURL string
	RedirectURI  string
	BrowserNonce string
}

// OAuthCallback is what the platform's redirect brought back: the code, our state, the
// platform's own error parameter ("" when it sent a code), the nonce cookie the browser sent
// ("" = none) and the caller's address.
type OAuthCallback struct {
	Code, State, ProviderError string
	BrowserNonce               string
	RemoteIP                   string
}

// ConnectAccount starts the browser sign-in of an account on platform pl that has one (owner,
// web session). It needs CACTUSTRADE_PUBLIC_URL (the redirect address) and the account's app
// key; nothing is sent to the platform yet.
func (s *Service) ConnectAccount(ctx context.Context, p Principal, pl domain.Platform, id string) (ConnectStart, error) {
	v, err := s.anyAccount(ctx, p, id, domain.AccessOwner)
	if err != nil {
		return ConnectStart{}, err
	}
	if p.Via != ViaSession {
		return ConnectStart{}, fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	if v.OnPlatform() != pl {
		return ConnectStart{}, domain.Invalid("accountId", "not a "+platformLabel(pl)+" account")
	}
	conn, ok := s.d.Connectors[pl]
	if !ok {
		return ConnectStart{}, domain.Invalid("platform", platformLabel(pl)+" has no browser sign-in")
	}
	redirect := s.redirectURI(pl)
	if redirect == "" {
		return ConnectStart{}, domain.MissingSetup(domain.MissingPublicURL, "Set CACTUSTRADE_PUBLIC_URL (the address the browser comes back to) first",
			fmt.Errorf("CACTUSTRADE_PUBLIC_URL is not set: %w", domain.ErrUnavailable))
	}
	state, nonce, verifier := s.d.Random.Token(), s.d.Random.Token(), s.d.Random.Token()
	if !domain.ValidPKCEVerifier(verifier) {
		return ConnectStart{}, fmt.Errorf("app.ConnectAccount: the random source gave no valid PKCE verifier: %w", domain.ErrUnavailable)
	}
	now := s.d.Clock.Now().UTC()
	u, err := conn.AuthorizeURL(ports.WithAccount(ctx, v.Account), redirect, state, domain.PKCEChallenge(verifier))
	if err != nil {
		return ConnectStart{}, fmt.Errorf("app.ConnectAccount: %w", err)
	}
	st := oauthState{
		userID: p.UserID, accountID: v.ID, platform: pl, expires: now.Add(OAuthStateTTL),
		nonceHash: hashToken(nonce), verifier: verifier,
	}
	if !s.oauth.put(hashToken(state), st, now) {
		return ConnectStart{}, fmt.Errorf("too many sign-ins in progress, try again in a few minutes: %w", domain.ErrUnavailable)
	}
	s.audit(ctx, p, "account.connect_started", v.ID, "platform="+string(pl))
	return ConnectStart{AuthorizeURL: u, RedirectURI: redirect, BrowserNonce: nonce}, nil
}

// ErrConnectRefused means the platform refused the sign-in or the code exchange. The state
// was used up; the owner connects again.
var ErrConnectRefused = errors.New("the platform refused the sign-in")

// CompleteConnect finishes a browser sign-in (public: the platform redirects the browser
// here, and the session cookie may not come along). The state must be one we issued for this
// platform, unused and younger than OAuthStateTTL, for an account its user still owns, and
// the browser must bring back the nonce cookie of the browser that started it; the state is
// used up whatever happens. It returns the account that was connected.
func (s *Service) CompleteConnect(ctx context.Context, pl domain.Platform, cb OAuthCallback) (domain.Account, error) {
	if cb.State == "" || len(cb.State) > 512 {
		return domain.Account{}, domain.Invalid("state", "missing or malformed")
	}
	st, ok := s.oauth.take(hashToken(cb.State))
	now := s.d.Clock.Now().UTC()
	if !ok || st.platform != pl || !now.Before(st.expires) {
		return domain.Account{}, domain.Invalid("state", "the sign-in has expired or was already used: connect again")
	}
	if cb.BrowserNonce == "" || len(cb.BrowserNonce) > 512 ||
		subtle.ConstantTimeCompare([]byte(hashToken(cb.BrowserNonce)), []byte(st.nonceHash)) != 1 {
		return domain.Account{}, domain.Invalid("state", "this sign-in was started in another browser (or its cookie is gone): connect again from this browser")
	}
	code, remoteIP := cb.Code, cb.RemoteIP
	if cb.ProviderError != "" {
		return domain.Account{}, fmt.Errorf("%s answered %q: %w", platformLabel(pl), clip(cb.ProviderError, 64), ErrConnectRefused)
	}
	if !validCode(code) {
		return domain.Account{}, domain.Invalid("code", "missing or malformed")
	}
	a, err := s.d.Store.Accounts().Get(ctx, st.accountID)
	if err != nil || a.OwnerID != st.userID || a.OnPlatform() != pl {
		return domain.Account{}, fmt.Errorf("the account is gone or changed hands: %w", domain.ErrNotFound)
	}
	u, err := s.d.Store.Users().Get(ctx, st.userID)
	if err != nil || u.Disabled {
		return domain.Account{}, fmt.Errorf("the user is gone or disabled: %w", domain.ErrForbidden)
	}
	conn, ok := s.d.Connectors[pl]
	if !ok {
		return domain.Account{}, fmt.Errorf("the %s client is not configured: %w", platformLabel(pl), domain.ErrUnavailable)
	}
	p := Principal{UserID: u.ID, Username: u.Username, Role: u.Role, RemoteIP: remoteIP}
	if err := conn.Exchange(ports.WithAccount(ctx, a), code, s.redirectURI(pl), st.verifier); err != nil {
		s.d.Log.WarnContext(ctx, "connecting an account failed", "account", a.ID, "platform", string(pl), "err", err)
		s.audit(ctx, p, "account.connect_failed", a.ID, "platform="+string(pl))
		return domain.Account{}, fmt.Errorf("app.CompleteConnect: %w: %w", ErrConnectRefused, err)
	}
	s.audit(ctx, p, "account.connected", a.ID, "platform="+string(pl))
	s.emitFor(ctx, a.ID, u.Username, "account.connected", domain.SeverityInfo, platformLabel(pl)+" account "+a.Name+" connected", "")
	return a, nil
}

// validCode accepts an authorization code: 1–2048 printable characters.
func validCode(code string) bool {
	if code == "" || len(code) > 2048 {
		return false
	}
	for _, r := range code {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// clip keeps at most n characters of a provider's error code, and only letters, digits and
// "_.-" (it ends up in logs and the audit trail).
func clip(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= n {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
