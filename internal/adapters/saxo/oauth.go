package saxo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Token lifetimes and margins. Saxo's examples show expires_in 1200 s and
// refresh_token_expires_in 2400 s (not a documented policy), so the client refreshes early:
// when the access token has less than accessMargin left, or the refresh token less than
// refreshMargin (the 15-minute equity snapshot then keeps a connection alive).
const (
	accessMargin  = 5 * time.Minute
	refreshMargin = 20 * time.Minute
	// DevTokenLifetime is how long Saxo's developer token works (SIM only).
	DevTokenLifetime = 24 * time.Hour
	maxTokenResponse = 64 << 10
)

// tokenResponse is the sign-in host's answer (OAuth 2.0 plus Saxo's refresh lifetime).
type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// AuthorizeURL is the SIM or LIVE sign-in page for the account in ctx, with its app key as
// client_id and the PKCE S256 challenge (PLAN 7.1.13). Nothing is sent to Saxo.
func (c *Client) AuthorizeURL(ctx context.Context, redirectURI, state, codeChallenge string) (string, error) {
	acct, err := account(ctx)
	if err != nil {
		return "", err
	}
	if codeChallenge == "" {
		return "", fmt.Errorf("saxo authorize: no PKCE code challenge: %w", domain.ErrInvalid)
	}
	appKey, err := c.secret(ctx, acct, domain.SaxoAppKey)
	if err != nil {
		return "", err
	}
	if appKey == "" {
		return "", fmt.Errorf("set the Saxo app key of %s first: %w: %w", acct.Name, domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	_, auth := c.hosts(acct)
	auth.Path = strings.TrimRight(auth.Path, "/") + AuthorizePath
	auth.RawQuery = url.Values{
		"response_type": {"code"}, "client_id": {appKey}, "state": {state}, "redirect_uri": {redirectURI},
		"code_challenge": {codeChallenge}, "code_challenge_method": {"S256"},
	}.Encode()
	return auth.String(), nil
}

// Exchange trades an authorization code for tokens (grant_type=authorization_code, with the
// PKCE code_verifier) and stores the refresh token, encrypted, before the access token is
// used.
func (c *Client) Exchange(ctx context.Context, code, redirectURI, codeVerifier string) error {
	acct, err := account(ctx)
	if err != nil {
		return err
	}
	if !domain.ValidPKCEVerifier(codeVerifier) {
		return fmt.Errorf("saxo token: no valid PKCE code verifier: %w", domain.ErrInvalid)
	}
	s := c.session(acct.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	tr, err := c.tokenRequest(ctx, acct, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {codeVerifier},
	})
	if err != nil {
		return err
	}
	return c.adopt(ctx, acct, s, tr)
}

// adopt stores a new refresh token and only then caches the access token (Saxo rotates the
// refresh token: the old one is spent). The caller holds s.mu.
func (c *Client) adopt(ctx context.Context, acct domain.Account, s *session, tr tokenResponse) error {
	if tr.AccessToken == "" || tr.RefreshToken == "" {
		return fmt.Errorf("saxo token: the answer has no access or refresh token: %w", domain.ErrUnavailable)
	}
	n, _ := domain.AccountCredentialName(acct, domain.SaxoRefreshToken)
	if err := c.cfg.Secrets.Put(ctx, n, tr.RefreshToken); err != nil {
		s.access = ""
		return fmt.Errorf("saxo token: storing the new refresh token: %w", err)
	}
	now := c.cfg.Now()
	s.access, s.from, s.dead = tr.AccessToken, hash(tr.RefreshToken), ""
	s.accessExp = now.Add(time.Duration(max(tr.ExpiresIn, 0)) * time.Second)
	s.refreshExp = time.Time{}
	if tr.RefreshTokenExpiresIn > 0 {
		s.refreshExp = now.Add(time.Duration(tr.RefreshTokenExpiresIn) * time.Second)
	}
	return nil
}

// accessToken is the bearer token for acct: from the stored refresh token (refreshed early,
// rotated, stored), else the 24-hour developer token (SIM only, refused when older than a
// day). A refused or lapsed sign-in is domain.ErrReconnect; no credentials at all is
// domain.ErrCredentialsMissing.
func (c *Client) accessToken(ctx context.Context, acct domain.Account, s *session) (string, error) {
	rt, err := c.secret(ctx, acct, domain.SaxoRefreshToken)
	if err != nil {
		return "", err
	}
	if rt == "" {
		return c.devToken(ctx, acct)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hash(rt)
	if s.dead == h {
		return "", fmt.Errorf("the stored Saxo sign-in of %s was refused: %w: %w", acct.Name, domain.ErrReconnect, domain.ErrUnavailable)
	}
	now := c.cfg.Now()
	if s.access != "" && s.from == h && now.Add(accessMargin).Before(s.accessExp) &&
		(s.refreshExp.IsZero() || now.Add(refreshMargin).Before(s.refreshExp)) {
		return s.access, nil
	}
	tr, err := c.tokenRequest(ctx, acct, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}})
	if err != nil {
		if apiErr, ok := errors.AsType[*APIError](err); ok && (apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusUnauthorized) {
			s.dead, s.access = h, ""
			return "", fmt.Errorf("the Saxo sign-in of %s has lapsed or was refused: %w: %w", acct.Name, domain.ErrReconnect, err)
		}
		return "", err
	}
	if err := c.adopt(ctx, acct, s, tr); err != nil {
		return "", err
	}
	return s.access, nil
}

// devToken returns the account's 24-hour developer token: SIM (demo) accounts only, and only
// while younger than a day (by when it was stored).
func (c *Client) devToken(ctx context.Context, acct domain.Account) (string, error) {
	dt, err := c.secret(ctx, acct, domain.SaxoDevToken)
	if err != nil {
		return "", err
	}
	if dt == "" {
		return "", fmt.Errorf("%s is not connected to Saxo yet: %w: %w", acct.Name, domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	if acct.Mode == domain.ModeReal {
		return "", fmt.Errorf("a 24-hour developer token works on SIM (demo) accounts only; connect %s to Saxo instead: %w", acct.Name, domain.ErrUnavailable)
	}
	st, err := c.cfg.Secrets.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("reading when the Saxo token was stored: %w", err)
	}
	n, _ := domain.AccountCredentialName(acct, domain.SaxoDevToken)
	if at := st[n].UpdatedAt; !at.IsZero() && !c.cfg.Now().Before(at.Add(DevTokenLifetime)) {
		return "", fmt.Errorf("the 24-hour Saxo token of %s is older than 24 hours: paste a new one or connect: %w: %w", acct.Name, domain.ErrReconnect, domain.ErrUnavailable)
	}
	return dt, nil
}

// tokenRequest POSTs a form to the sign-in host's /token with the account's app key and
// secret — the client's only POST. The answer is never logged.
func (c *Client) tokenRequest(ctx context.Context, acct domain.Account, form url.Values) (tokenResponse, error) {
	appKey, err := c.secret(ctx, acct, domain.SaxoAppKey)
	if err != nil {
		return tokenResponse{}, err
	}
	appSecret, err := c.secret(ctx, acct, domain.SaxoAppSecret)
	if err != nil {
		return tokenResponse{}, err
	}
	if appKey == "" || appSecret == "" {
		return tokenResponse{}, fmt.Errorf("the Saxo app key and secret of %s are not both set: %w: %w", acct.Name, domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	form.Set("client_id", appKey)
	form.Set("client_secret", appSecret)
	_, auth := c.hosts(acct)
	auth.Path, auth.RawQuery = strings.TrimRight(auth.Path, "/")+TokenPath, ""
	actx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, auth.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("saxo token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	start := c.cfg.Now()
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		c.cfg.Log.WarnContext(ctx, "Saxo sign-in request failed", "op", "token", "err", redact(err))
		if ctx.Err() != nil {
			return tokenResponse{}, ctx.Err()
		}
		return tokenResponse{}, fmt.Errorf("saxo token: %w: %w", domain.ErrUnavailable, redact(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponse+1))
	c.cfg.Log.DebugContext(ctx, "Saxo sign-in request", "op", "token", "status", resp.StatusCode,
		"duration_ms", c.cfg.Now().Sub(start).Milliseconds())
	if err != nil || len(data) > maxTokenResponse {
		return tokenResponse{}, fmt.Errorf("saxo token: unreadable or oversized answer: %w", domain.ErrUnavailable)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		c.cfg.Log.WarnContext(ctx, "Saxo refused a sign-in request", "op", "token", "status", resp.StatusCode)
		return tokenResponse{}, &APIError{Op: "token", Status: resp.StatusCode, Code: errorCode(data)}
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("saxo token: unexpected answer: %w", domain.ErrUnavailable)
	}
	return tr, nil
}
