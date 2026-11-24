// Package vault implements ports.SecretStore on HashiCorp Vault's KV version 2 secrets engine
// (PLAN 7.8, OD30): optional, off by default (CACTUSTRADE_SECRETS=db), standard library only
// (no Vault SDK).
//
// Each credential is one KV v2 secret <mount>/<prefix>/<name> holding {"value": "…"}. The
// store is write-only towards people like the database store: Get exists for the adapters
// that need a value (eToro, LLM, mail, Saxo), never for the API. The token comes from a file
// (written by Vault Agent or the operator) or from Vault's Kubernetes auth method (the pod's
// service-account JWT); it is renewed before it expires, and a refused token is fetched
// again once. Redirects are refused (a standby node's redirect would carry the token to
// another address); TLS is verified against the system roots plus an optional CA file. No
// token, JWT or value is ever logged or put into an error.
package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Defaults.
const (
	DefaultMount      = "secret"
	DefaultPrefix     = "cactustrade"
	DefaultK8sMount   = "kubernetes"
	DefaultK8sJWTFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultTimeout    = 10 * time.Second
	maxResponse       = 1 << 20
	// A token is renewed when two thirds of its lifetime have passed; a renewal that leaves
	// less than minLease (its maximum TTL is near) fetches a fresh token instead.
	minLease = 30 * time.Second
)

// ErrRedirect is returned when Vault answers with a redirect: it is never followed.
var ErrRedirect = errors.New("vault: redirect refused (point CACTUSTRADE_VAULT_ADDR at the active node or its load balancer)")

// Config configures the store. Exactly one token source: TokenFile, or K8sRole (with
// K8sJWTFile and K8sMount).
type Config struct {
	Addr       string // https://vault.example:8200 (http only for a loopback address)
	Mount      string // KV v2 mount ("" = secret)
	Prefix     string // folder under the mount ("" = cactustrade)
	TokenFile  string // a file holding the token (Vault Agent's sink, or the operator's)
	K8sRole    string // Kubernetes auth role
	K8sJWTFile string // the service-account JWT ("" = the pod's default)
	K8sMount   string // Kubernetes auth mount ("" = kubernetes)
	CAFile     string // PEM bundle trusted in addition to the system roots
	// Namespace is a Vault Enterprise namespace ("" = none).
	Namespace string

	HTTP     *http.Client // nil = built from CAFile (tests inject one; redirects are refused either way)
	Now      func() time.Time
	ReadFile func(string) ([]byte, error)
	Log      *slog.Logger
}

// Store is the Vault-backed ports.SecretStore.
type Store struct {
	cfg  Config
	base *url.URL
	http *http.Client

	mu        sync.Mutex
	token     string
	renewable bool
	renewAt   time.Time // zero: never renewed proactively (no TTL)
	expires   time.Time
	last      map[domain.CredentialName]string // the latest value read or written (memory only)
	stale     bool                             // serving from last since Vault failed
}

var _ ports.SecretStore = (*Store)(nil)

var (
	segment = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	name    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
)

// New checks the configuration (nothing is sent to Vault yet).
func New(cfg Config) (*Store, error) {
	if cfg.Mount == "" {
		cfg.Mount = DefaultMount
	}
	if cfg.Prefix == "" {
		cfg.Prefix = DefaultPrefix
	}
	if cfg.K8sMount == "" {
		cfg.K8sMount = DefaultK8sMount
	}
	if cfg.K8sJWTFile == "" {
		cfg.K8sJWTFile = DefaultK8sJWTFile
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ReadFile == nil {
		cfg.ReadFile = os.ReadFile
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	for k, v := range map[string]string{"CACTUSTRADE_VAULT_MOUNT": cfg.Mount, "CACTUSTRADE_VAULT_PREFIX": cfg.Prefix, "CACTUSTRADE_VAULT_K8S_MOUNT": cfg.K8sMount} {
		if !segment.MatchString(v) || strings.Contains("/"+v+"/", "/../") || strings.Contains("/"+v+"/", "/./") {
			return nil, fmt.Errorf("vault: %s %q: letters, digits, '.', '_', '-' and '/' between them", k, v)
		}
	}
	if cfg.Namespace != "" && !segment.MatchString(cfg.Namespace) {
		return nil, fmt.Errorf("vault: CACTUSTRADE_VAULT_NAMESPACE %q is not a namespace path", cfg.Namespace)
	}
	if (cfg.TokenFile == "") == (cfg.K8sRole == "") {
		return nil, errors.New("vault: set exactly one of CACTUSTRADE_VAULT_TOKEN_FILE and CACTUSTRADE_VAULT_K8S_ROLE")
	}
	base, err := url.Parse(strings.TrimRight(cfg.Addr, "/"))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, fmt.Errorf("vault: CACTUSTRADE_VAULT_ADDR %q is not an address such as https://vault.example:8200", cfg.Addr)
	}
	switch base.Scheme {
	case "https":
	case "http":
		if !loopback(base.Hostname()) {
			return nil, fmt.Errorf("vault: CACTUSTRADE_VAULT_ADDR must be https (plain http only to this machine): %q", cfg.Addr)
		}
	default:
		return nil, fmt.Errorf("vault: CACTUSTRADE_VAULT_ADDR must be https: %q", cfg.Addr)
	}
	base.Path = ""
	hc := cfg.HTTP
	if hc == nil {
		hc, err = newHTTPClient(cfg)
		if err != nil {
			return nil, err
		}
	} else {
		c := *hc
		hc = &c
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrRedirect }
	return &Store{cfg: cfg, base: base, http: hc, last: map[domain.CredentialName]string{}}, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newHTTPClient trusts the system roots plus the CA file, TLS 1.2 or newer.
func newHTTPClient(cfg Config) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if cfg.CAFile != "" {
		pem, err := cfg.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("vault: CACTUSTRADE_VAULT_CA_FILE: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("vault: CACTUSTRADE_VAULT_CA_FILE %s holds no PEM certificate", cfg.CAFile)
		}
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("vault: unexpected default transport")
	}
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: defaultTimeout, Transport: tr}, nil
}

// ---- the SecretStore ----------------------------------------------------------------------

// Put writes a value as a new version of its secret.
func (s *Store) Put(ctx context.Context, n domain.CredentialName, v string) error {
	p, err := s.dataPath(n)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"data": map[string]string{"value": v}})
	if err != nil {
		return fmt.Errorf("vault put: %w", err)
	}
	if _, _, err := s.call(ctx, "put", http.MethodPost, p, body, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	s.remember(n, v, true)
	return nil
}

// Get reads the newest version of a value; a missing or deleted one is domain.ErrNotFound.
// While Vault is unavailable (down, sealed, refusing the token) the value this process last
// read or wrote is used, so that exits keep working through an outage (CLAUDE.md §2.6); a
// name never read since the start fails.
func (s *Store) Get(ctx context.Context, n domain.CredentialName) (string, error) {
	v, err := s.get(ctx, n)
	switch {
	case err == nil:
		s.remember(n, v, true)
		return v, nil
	case errors.Is(err, domain.ErrNotFound):
		s.remember(n, "", false)
	case errors.Is(err, domain.ErrUnavailable):
		s.mu.Lock()
		last, ok := s.last[n]
		warn := ok && !s.stale
		s.stale = s.stale || ok
		s.mu.Unlock()
		if ok {
			if warn {
				s.cfg.Log.WarnContext(ctx, "Vault is unavailable: using the credentials last read from it", "credential", string(n), "err", err)
			}
			return last, nil
		}
	}
	return "", err
}

// remember keeps (ok) or forgets a value read from or written to Vault, and ends a "Vault
// unavailable" spell after a successful read.
func (s *Store) remember(n domain.CredentialName, v string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.last[n] = v
		s.stale = false
		return
	}
	delete(s.last, n)
}

func (s *Store) get(ctx context.Context, n domain.CredentialName) (string, error) {
	p, err := s.dataPath(n)
	if err != nil {
		return "", err
	}
	status, b, err := s.call(ctx, "get", http.MethodGet, p, nil, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("%s: %w", n, domain.ErrNotFound)
	}
	var r struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", fmt.Errorf("vault get %s: unreadable answer: %w", n, domain.ErrUnavailable)
	}
	v, ok := r.Data.Data["value"].(string)
	if !ok {
		return "", fmt.Errorf("vault get %s: the secret has no string field \"value\": %w", n, domain.ErrUnavailable)
	}
	return v, nil
}

// Delete removes a value with all its versions (KV v2 metadata delete). Deleting a missing
// value is not an error.
func (s *Store) Delete(ctx context.Context, n domain.CredentialName) error {
	p, err := s.metaPath(n)
	if err != nil {
		return err
	}
	if _, _, err = s.call(ctx, "delete", http.MethodDelete, p, nil, http.StatusOK, http.StatusNoContent, http.StatusNotFound); err != nil {
		return err
	}
	s.remember(n, "", false)
	return nil
}

// Status lists the stored names with when each last changed (never a value). A name that is
// not a credential name (another tool's secret in the folder) is skipped.
func (s *Store) Status(ctx context.Context) (map[domain.CredentialName]ports.SecretMeta, error) {
	folder := "/v1/" + s.cfg.Mount + "/metadata/" + s.cfg.Prefix + "/"
	status, b, err := s.call(ctx, "list", "LIST", folder, nil, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return nil, err
	}
	out := map[domain.CredentialName]ports.SecretMeta{}
	if status == http.StatusNotFound {
		return out, nil
	}
	var r struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("vault list: unreadable answer: %w", domain.ErrUnavailable)
	}
	for _, k := range r.Data.Keys {
		if !validName(k) {
			continue
		}
		n := domain.CredentialName(k)
		p, _ := s.metaPath(n)
		status, b, err := s.call(ctx, "metadata", http.MethodGet, p, nil, http.StatusOK, http.StatusNotFound)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound { // deleted meanwhile
			continue
		}
		var m struct {
			Data struct {
				UpdatedTime time.Time `json:"updated_time"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("vault metadata %s: unreadable answer: %w", n, domain.ErrUnavailable)
		}
		out[n] = ports.SecretMeta{Source: ports.SourceUI, UpdatedAt: m.Data.UpdatedTime.UTC()}
	}
	return out, nil
}

// Check signs in to Vault (or reads and checks the token file) without touching a secret.
func (s *Store) Check(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.currentToken(ctx)
	return err
}

func validName(n string) bool { return name.MatchString(n) && n != "." && n != ".." }

func (s *Store) dataPath(n domain.CredentialName) (string, error) {
	if !validName(string(n)) {
		return "", domain.Invalid("name", "not a credential name")
	}
	return "/v1/" + s.cfg.Mount + "/data/" + s.cfg.Prefix + "/" + string(n), nil
}

func (s *Store) metaPath(n domain.CredentialName) (string, error) {
	if !validName(string(n)) {
		return "", domain.Invalid("name", "not a credential name")
	}
	return "/v1/" + s.cfg.Mount + "/metadata/" + s.cfg.Prefix + "/" + string(n), nil
}

// ---- the token ----------------------------------------------------------------------------

// call runs one KV request with the current token; a 403 drops the token, fetches a fresh
// one and tries once more. ok lists the statuses that are answers (others are errors). The
// lock covers the token only, not the request.
func (s *Store) call(ctx context.Context, op, method, path string, body []byte, ok ...int) (int, []byte, error) {
	for attempt := 0; ; attempt++ {
		s.mu.Lock()
		tok, err := s.currentToken(ctx)
		s.mu.Unlock()
		if err != nil {
			return 0, nil, err
		}
		status, b, err := s.do(ctx, method, path, body, tok)
		if err != nil {
			return 0, nil, fmt.Errorf("vault %s: %w", op, err)
		}
		if status == http.StatusForbidden && attempt == 0 {
			s.cfg.Log.WarnContext(ctx, "Vault refused the token; fetching a new one", "op", op)
			s.mu.Lock()
			if s.token == tok {
				s.token = ""
			}
			s.mu.Unlock()
			continue
		}
		for _, o := range ok {
			if status == o {
				return status, b, nil
			}
		}
		return 0, nil, fmt.Errorf("vault %s: %s: %w", op, describe(status, b), domain.ErrUnavailable)
	}
}

// currentToken returns a usable token, signing in or renewing first when needed. The caller
// holds s.mu.
func (s *Store) currentToken(ctx context.Context) (string, error) {
	now := s.cfg.Now()
	if s.token != "" && !s.renewAt.IsZero() && !now.Before(s.renewAt) {
		if s.renewable && now.Before(s.expires) {
			if err := s.renew(ctx); err == nil {
				return s.token, nil
			}
			// fall through: a fresh token
		}
		s.token = ""
	}
	if s.token != "" {
		return s.token, nil
	}
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	return s.token, nil
}

// acquire reads the token file (and looks the token up for its lifetime) or signs in with
// the Kubernetes service-account JWT. The caller holds s.mu.
func (s *Store) acquire(ctx context.Context) error {
	if s.cfg.TokenFile != "" {
		b, err := s.cfg.ReadFile(s.cfg.TokenFile)
		if err != nil {
			return fmt.Errorf("vault: reading the token file: %w: %w", unwrapPath(err), domain.ErrUnavailable)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return fmt.Errorf("vault: the token file %s is empty: %w", s.cfg.TokenFile, domain.ErrUnavailable)
		}
		status, body, err := s.do(ctx, http.MethodGet, "/v1/auth/token/lookup-self", nil, tok)
		if err != nil {
			return fmt.Errorf("vault token lookup: %w", err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("vault token lookup: %s: %w", describe(status, body), domain.ErrUnavailable)
		}
		var r struct {
			Data struct {
				TTL       int64 `json:"ttl"`
				Renewable bool  `json:"renewable"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return fmt.Errorf("vault token lookup: unreadable answer: %w", domain.ErrUnavailable)
		}
		s.adopt(ctx, tok, r.Data.TTL, r.Data.Renewable, "token file")
		return nil
	}
	jwt, err := s.cfg.ReadFile(s.cfg.K8sJWTFile)
	if err != nil {
		return fmt.Errorf("vault: reading the service-account token: %w: %w", unwrapPath(err), domain.ErrUnavailable)
	}
	req, err := json.Marshal(map[string]string{"role": s.cfg.K8sRole, "jwt": strings.TrimSpace(string(jwt))})
	if err != nil {
		return fmt.Errorf("vault login: %w", err)
	}
	status, body, err := s.do(ctx, http.MethodPost, "/v1/auth/"+s.cfg.K8sMount+"/login", req, "")
	if err != nil {
		return fmt.Errorf("vault login: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("vault login (role %s): %s: %w", s.cfg.K8sRole, describe(status, body), domain.ErrUnavailable)
	}
	a, err := authOf(body)
	if err != nil {
		return fmt.Errorf("vault login: %w", err)
	}
	s.adopt(ctx, a.ClientToken, a.LeaseDuration, a.Renewable, "kubernetes")
	return nil
}

type auth struct {
	ClientToken   string `json:"client_token"`
	LeaseDuration int64  `json:"lease_duration"`
	Renewable     bool   `json:"renewable"`
}

func authOf(body []byte) (auth, error) {
	var r struct {
		Auth *auth `json:"auth"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Auth == nil || r.Auth.ClientToken == "" {
		return auth{}, fmt.Errorf("the answer has no token: %w", domain.ErrUnavailable)
	}
	return *r.Auth, nil
}

// renew extends the token's lease (auth/token/renew-self). The caller holds s.mu.
func (s *Store) renew(ctx context.Context) error {
	status, body, err := s.do(ctx, http.MethodPost, "/v1/auth/token/renew-self", []byte("{}"), s.token)
	if err != nil {
		s.cfg.Log.WarnContext(ctx, "renewing the Vault token failed", "err", err)
		return err
	}
	if status != http.StatusOK {
		s.cfg.Log.WarnContext(ctx, "Vault refused to renew the token", "status", status)
		return fmt.Errorf("vault renew: HTTP %d: %w", status, domain.ErrUnavailable)
	}
	a, err := authOf(body)
	if err != nil {
		return err
	}
	if time.Duration(a.LeaseDuration)*time.Second < minLease {
		s.cfg.Log.InfoContext(ctx, "the Vault token is near its maximum lifetime; fetching a new one", "ttl_seconds", a.LeaseDuration)
		return fmt.Errorf("vault renew: lease too short: %w", domain.ErrUnavailable)
	}
	s.adopt(ctx, a.ClientToken, a.LeaseDuration, a.Renewable, "renewal")
	return nil
}

// adopt keeps a token and when to renew it (two thirds into its lease; never for a token
// without a TTL). Only the lifetime is logged.
func (s *Store) adopt(ctx context.Context, tok string, ttl int64, renewable bool, how string) {
	now := s.cfg.Now()
	s.token, s.renewable = tok, renewable
	s.renewAt, s.expires = time.Time{}, time.Time{}
	if ttl > 0 {
		lease := time.Duration(ttl) * time.Second
		s.expires = now.Add(lease)
		s.renewAt = now.Add(lease * 2 / 3)
	}
	s.cfg.Log.InfoContext(ctx, "Vault token ready", "via", how, "ttl_seconds", ttl, "renewable", renewable)
}

// ---- HTTP ---------------------------------------------------------------------------------

// do sends one request. The token travels only in the X-Vault-Token header; errors carry
// the method, path and status, never a header or a body.
func (s *Store) do(ctx context.Context, method, path string, body []byte, tok string) (int, []byte, error) {
	u := *s.base
	u.Path = path
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", err, domain.ErrUnavailable)
	}
	if tok != "" {
		req.Header.Set("X-Vault-Token", tok)
	}
	if s.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", s.cfg.Namespace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		if errors.Is(err, ErrRedirect) {
			return 0, nil, fmt.Errorf("%s %s: %w: %w", method, path, ErrRedirect, domain.ErrUnavailable)
		}
		return 0, nil, fmt.Errorf("%s %s: Vault does not answer: %w", method, path, domain.ErrUnavailable)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(b) > maxResponse {
		return 0, nil, fmt.Errorf("%s %s: unreadable or oversized answer: %w", method, path, domain.ErrUnavailable)
	}
	return resp.StatusCode, b, nil
}

// describe is "HTTP <status>" plus Vault's first error message, clipped and limited to safe
// characters (Vault's messages name paths and policies, never tokens).
func describe(status int, body []byte) string {
	var r struct {
		Errors []string `json:"errors"`
	}
	out := fmt.Sprintf("HTTP %d", status)
	if json.Unmarshal(body, &r) == nil && len(r.Errors) > 0 {
		var b strings.Builder
		for _, c := range r.Errors[0] {
			if b.Len() >= 120 {
				break
			}
			if c == ' ' || c == '/' || c == '.' || c == '_' || c == '-' || c == ':' || c == '"' ||
				(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				b.WriteRune(c)
			}
		}
		if b.Len() > 0 {
			out += " (" + b.String() + ")"
		}
	}
	return out
}

// unwrapPath keeps a file error's reason without its contents (os errors name the path and
// the reason only).
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Path, pe.Err)
	}
	return err
}
