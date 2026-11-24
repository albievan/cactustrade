package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Planted secrets: none may appear in an error or a log line.
const (
	fileToken = "hvs.FILE-TOKEN-never-logged"
	jwt       = "eyJ.SERVICE-ACCOUNT-JWT.never-logged"
	value     = "etoro-user-key-VALUE-never-logged"
)

type entry struct {
	value   string
	updated time.Time
}

type tokenInfo struct {
	ttl       int64
	renewable bool
}

// fakeVault is an httptest Vault: KV v2 under secret/cactustrade, token lookup and renewal,
// and Kubernetes login.
type fakeVault struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	now        func() time.Time
	kv         map[string]entry
	tokens     map[string]tokenInfo
	logins     int
	renewals   int
	renewLease int64 // lease_duration a renewal gives (0 = the token's ttl)
	refuseRenw bool
	loginTTL   int64
	reqs       []*http.Request
	override   func(r *http.Request) (int, string, bool)
}

func newFake(t *testing.T, now func() time.Time) *fakeVault {
	t.Helper()
	f := &fakeVault{t: t, now: now, kv: map[string]entry{}, tokens: map[string]tokenInfo{fileToken: {ttl: 0}}, loginTTL: 60}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeVault) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if (method == "" || r.Method == method) && (path == "" || r.URL.Path == path) {
			n++
		}
	}
	return n
}

func (f *fakeVault) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.reqs = append(f.reqs, r.Clone(context.Background()))
	if f.override != nil {
		if code, b, ok := f.override(r); ok {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, b)
			return
		}
	}
	answer := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	denied := map[string][]string{"errors": {"permission denied"}}
	if r.URL.Path == "/v1/auth/kubernetes/login" {
		var in struct{ Role, JWT string }
		_ = json.Unmarshal(body, &in)
		if in.Role != "cactustrade" || in.JWT != jwt {
			answer(http.StatusForbidden, denied)
			return
		}
		f.logins++
		tok := fmt.Sprintf("hvs.LOGIN-%d-never-logged", f.logins)
		f.tokens[tok] = tokenInfo{ttl: f.loginTTL, renewable: true}
		answer(http.StatusOK, map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": f.loginTTL, "renewable": true}})
		return
	}
	tok := r.Header.Get("X-Vault-Token")
	info, ok := f.tokens[tok]
	if !ok {
		answer(http.StatusForbidden, denied)
		return
	}
	const data, meta = "/v1/secret/data/cactustrade/", "/v1/secret/metadata/cactustrade/"
	switch {
	case r.URL.Path == "/v1/auth/token/lookup-self" && r.Method == http.MethodGet:
		answer(http.StatusOK, map[string]any{"data": map[string]any{"ttl": info.ttl, "renewable": info.renewable}})
	case r.URL.Path == "/v1/auth/token/renew-self" && r.Method == http.MethodPost:
		if f.refuseRenw {
			answer(http.StatusForbidden, denied)
			return
		}
		f.renewals++
		lease := info.ttl
		if f.renewLease != 0 {
			lease = f.renewLease
		}
		answer(http.StatusOK, map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": lease, "renewable": info.renewable}})
	case strings.HasPrefix(r.URL.Path, data) && r.Method == http.MethodGet:
		e, ok := f.kv[strings.TrimPrefix(r.URL.Path, data)]
		if !ok {
			answer(http.StatusNotFound, map[string][]string{"errors": {}})
			return
		}
		answer(http.StatusOK, map[string]any{"data": map[string]any{"data": map[string]string{"value": e.value}, "metadata": map[string]any{"version": 1}}})
	case strings.HasPrefix(r.URL.Path, data) && r.Method == http.MethodPost:
		var in struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			answer(http.StatusBadRequest, map[string][]string{"errors": {"bad json"}})
			return
		}
		f.kv[strings.TrimPrefix(r.URL.Path, data)] = entry{value: in.Data["value"], updated: f.now()}
		answer(http.StatusOK, map[string]any{"data": map[string]any{"version": 1}})
	case r.URL.Path == meta && r.Method == "LIST":
		if len(f.kv) == 0 {
			answer(http.StatusNotFound, map[string][]string{"errors": {}})
			return
		}
		keys := []string{"subfolder/", "not a name"}
		for k := range f.kv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		answer(http.StatusOK, map[string]any{"data": map[string]any{"keys": keys}})
	case strings.HasPrefix(r.URL.Path, meta) && r.Method == http.MethodGet:
		e, ok := f.kv[strings.TrimPrefix(r.URL.Path, meta)]
		if !ok {
			answer(http.StatusNotFound, nil)
			return
		}
		answer(http.StatusOK, map[string]any{"data": map[string]any{"updated_time": e.updated.Format(time.RFC3339Nano)}})
	case strings.HasPrefix(r.URL.Path, meta) && r.Method == http.MethodDelete:
		delete(f.kv, strings.TrimPrefix(r.URL.Path, meta))
		answer(http.StatusNoContent, nil)
	default:
		answer(http.StatusMethodNotAllowed, map[string][]string{"errors": {"unsupported"}})
	}
}

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// files is an in-memory file system for the token, JWT and CA files.
type files struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (f *files) read(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.m[name]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}
	return b, nil
}

func (f *files) set(name, v string) { f.mu.Lock(); f.m[name] = []byte(v); f.mu.Unlock() }

type env struct {
	fv    *fakeVault
	clk   *clock
	files *files
	logs  *bytes.Buffer
	s     *Store
}

func newEnv(t *testing.T, mut func(*Config)) *env {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)}
	e := &env{clk: clk, files: &files{m: map[string][]byte{}}, logs: &bytes.Buffer{}}
	e.fv = newFake(t, clk.Now)
	e.files.set("/run/secrets/vault_token", fileToken+"\n")
	e.files.set(DefaultK8sJWTFile, jwt)
	cfg := Config{
		Addr: e.fv.srv.URL, TokenFile: "/run/secrets/vault_token", HTTP: e.fv.srv.Client(),
		Now: clk.Now, ReadFile: e.files.read,
		Log: slog.New(slog.NewJSONHandler(&lockedWriter{w: e.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mut != nil {
		mut(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	return e
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func k8s(c *Config) { c.TokenFile, c.K8sRole = "", "cactustrade" }

// noSecrets fails when a planted secret or a minted token is in s.
func noSecrets(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{fileToken, jwt, value, "hvs.LOGIN", "never-logged"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s holds a secret (%q): %s", what, bad, s)
		}
	}
}

func TestNewValidates(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "ca.txt")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok := Config{Addr: "https://vault.example:8200", TokenFile: "/t"}
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"valid https", func(*Config) {}, ""},
		{"valid loopback http", func(c *Config) { c.Addr = "http://127.0.0.1:8200" }, ""},
		{"valid localhost http", func(c *Config) { c.Addr = "http://localhost:8200/" }, ""},
		{"valid k8s", func(c *Config) { c.TokenFile, c.K8sRole = "", "r" }, ""},
		{"valid nested mount and namespace", func(c *Config) { c.Mount, c.Prefix, c.Namespace = "kv/apps", "trading/cactus", "team-a" }, ""},
		{"no address", func(c *Config) { c.Addr = "" }, "not an address"},
		{"plain http elsewhere", func(c *Config) { c.Addr = "http://vault.example:8200" }, "must be https"},
		{"other scheme", func(c *Config) { c.Addr = "ftp://vault.example" }, "must be https"},
		{"user info", func(c *Config) { c.Addr = "https://u:p@vault.example" }, "not an address"},
		{"a path", func(c *Config) { c.Addr = "https://vault.example/v1" }, "not an address"},
		{"a query", func(c *Config) { c.Addr = "https://vault.example?x=1" }, "not an address"},
		{"both token sources", func(c *Config) { c.K8sRole = "r" }, "exactly one"},
		{"no token source", func(c *Config) { c.TokenFile = "" }, "exactly one"},
		{"mount climbs out", func(c *Config) { c.Mount = "secret/../sys" }, "CACTUSTRADE_VAULT_MOUNT"},
		{"prefix with a dot segment", func(c *Config) { c.Prefix = "./x" }, "CACTUSTRADE_VAULT_PREFIX"},
		{"prefix with a space", func(c *Config) { c.Prefix = "a b" }, "CACTUSTRADE_VAULT_PREFIX"},
		{"bad namespace", func(c *Config) { c.Namespace = "a b" }, "NAMESPACE"},
		{"missing CA file", func(c *Config) { c.CAFile = filepath.Join(dir, "missing.pem") }, "CA_FILE"},
		{"CA file without PEM", func(c *Config) { c.CAFile = notPEM }, "no PEM"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ok
			tt.mut(&c)
			_, err := New(c)
			if tt.want == "" && err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestKV is get/put/delete/list through the token file.
func TestKV(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	n := domain.CredentialName("etoro.acct-1.user_key")
	if _, err := e.s.Get(ctx, n); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if st, err := e.s.Status(ctx); err != nil || len(st) != 0 {
		t.Fatalf("empty status = %v %v", st, err)
	}
	if err := e.s.Put(ctx, n, value); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	if err := e.s.Put(ctx, "llm.p1.api_key", "sk-other"); err != nil {
		t.Fatal(err)
	}
	if v, err := e.s.Get(ctx, n); err != nil || v != value {
		t.Fatalf("get = %q %v", v, err)
	}
	st, err := e.s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	if len(st) != 2 || !st[n].UpdatedAt.Equal(want) || st[n].Source != "ui" || !st["llm.p1.api_key"].UpdatedAt.Equal(want.Add(time.Minute)) {
		t.Fatalf("status = %+v", st)
	}
	if err := e.s.Delete(ctx, n); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Get(ctx, n); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete = %v", err)
	}
	if err := e.s.Delete(ctx, n); err != nil {
		t.Fatalf("deleting a missing value: %v", err)
	}
	// the token went only in the header, the value only in the body
	if got := e.fv.reqs[0].Header.Get("X-Vault-Token"); got != fileToken {
		t.Fatalf("token header %q", got)
	}
	for _, r := range e.fv.reqs {
		noSecrets(t, "a request URL", r.URL.String())
	}
	if e.fv.count(http.MethodGet, "/v1/auth/token/lookup-self") != 1 {
		t.Fatal("the token file should be looked up once")
	}
	if err := e.s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	noSecrets(t, "the log", e.logs.String())
}

func TestInvalidNamesSendNothing(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	for _, n := range []domain.CredentialName{"", "..", ".", "../sys/x", "a/b", "a b", domain.CredentialName(strings.Repeat("x", 201))} {
		if err := e.s.Put(ctx, n, value); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("put %q = %v", n, err)
		}
		if _, err := e.s.Get(ctx, n); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("get %q = %v", n, err)
		}
		if err := e.s.Delete(ctx, n); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("delete %q = %v", n, err)
		}
	}
	if n := e.fv.count("", ""); n != 0 {
		t.Fatalf("%d requests for invalid names", n)
	}
}

// TestErrors: 403, 404, 5xx, unreadable answers, Vault down, a cancelled context. Every
// failure is ErrUnavailable (fail closed: no keys, no trading) and holds no secret.
func TestErrors(t *testing.T) {
	ctx := context.Background()
	n := domain.CredentialName("etoro.a.api_key")
	tests := []struct {
		name     string
		override func(r *http.Request) (int, string, bool)
		op       func(s *Store) error
		want     error
	}{
		{"5xx on get", func(r *http.Request) (int, string, bool) {
			return 503, `{"errors":["Vault is sealed"]}`, strings.Contains(r.URL.Path, "/data/")
		}, func(s *Store) error { _, err := s.Get(ctx, n); return err }, domain.ErrUnavailable},
		{
			"5xx on put", func(r *http.Request) (int, string, bool) {
				return 500, `{"errors":["internal error"]}`, strings.Contains(r.URL.Path, "/data/")
			},
			func(s *Store) error { return s.Put(ctx, n, value) }, domain.ErrUnavailable,
		},
		{
			"5xx on delete", func(r *http.Request) (int, string, bool) { return 502, "", strings.Contains(r.URL.Path, "/metadata/") },
			func(s *Store) error { return s.Delete(ctx, n) }, domain.ErrUnavailable,
		},
		{
			"5xx on list", func(r *http.Request) (int, string, bool) { return 500, "", r.Method == "LIST" },
			func(s *Store) error { _, err := s.Status(ctx); return err }, domain.ErrUnavailable,
		},
		{"403 always", func(r *http.Request) (int, string, bool) {
			return 403, `{"errors":["1 error occurred:\n\t* permission denied\n\n"]}`, strings.Contains(r.URL.Path, "/data/")
		}, func(s *Store) error { _, err := s.Get(ctx, n); return err }, domain.ErrUnavailable},
		{
			"unreadable get", func(r *http.Request) (int, string, bool) {
				return 200, `<html>`, strings.Contains(r.URL.Path, "/data/")
			},
			func(s *Store) error { _, err := s.Get(ctx, n); return err }, domain.ErrUnavailable,
		},
		{"no value field", func(r *http.Request) (int, string, bool) {
			return 200, `{"data":{"data":{"other":"x"}}}`, strings.Contains(r.URL.Path, "/data/")
		}, func(s *Store) error { _, err := s.Get(ctx, n); return err }, domain.ErrUnavailable},
		{"oversized answer", func(r *http.Request) (int, string, bool) {
			return 200, `"` + strings.Repeat("x", maxResponse) + `"`, strings.Contains(r.URL.Path, "/data/")
		}, func(s *Store) error { _, err := s.Get(ctx, n); return err }, domain.ErrUnavailable},
		{
			"unreadable list", func(r *http.Request) (int, string, bool) { return 200, `[`, r.Method == "LIST" },
			func(s *Store) error { _, err := s.Status(ctx); return err }, domain.ErrUnavailable,
		},
		{"unreadable metadata", func(r *http.Request) (int, string, bool) {
			return 200, `{"data":{"updated_time":"yesterday"}}`, r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/metadata/")
		}, func(s *Store) error { _, err := s.Status(ctx); return err }, domain.ErrUnavailable},
		{"metadata fails", func(r *http.Request) (int, string, bool) {
			return 500, ``, r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/metadata/")
		}, func(s *Store) error { _, err := s.Status(ctx); return err }, domain.ErrUnavailable},
		{"metadata gone meanwhile", func(r *http.Request) (int, string, bool) {
			return 404, ``, r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/metadata/")
		}, func(s *Store) error {
			st, err := s.Status(ctx)
			if err == nil && len(st) != 0 {
				return fmt.Errorf("status %v", st)
			}
			return err
		}, nil},
		{
			"token lookup fails", func(r *http.Request) (int, string, bool) {
				return 500, ``, strings.HasSuffix(r.URL.Path, "lookup-self")
			},
			func(s *Store) error { forget(s); return s.Put(ctx, n, value) }, domain.ErrUnavailable,
		},
		{
			"token lookup unreadable", func(r *http.Request) (int, string, bool) {
				return 200, `nope`, strings.HasSuffix(r.URL.Path, "lookup-self")
			},
			func(s *Store) error { forget(s); return s.Put(ctx, n, value) }, domain.ErrUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, nil)
			_ = e.s.Put(ctx, n, value)
			forgetValues(e.s) // a fresh process: nothing read yet to fall back on
			e.fv.override = tt.override
			err := tt.op(e.s)
			if !errors.Is(err, tt.want) || (tt.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if err != nil {
				noSecrets(t, "the error", err.Error())
			}
			noSecrets(t, "the log", e.logs.String())
		})
	}
}

// TestForbiddenRefetchesTokenOnce: a 403 re-reads the token file (the operator or Vault
// Agent rotated it) and tries once more; a second 403 is the answer.
func TestForbiddenRefetchesTokenOnce(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	if err := e.s.Put(ctx, "a", value); err != nil {
		t.Fatal(err)
	}
	// rotate: the old token is revoked, the file holds a new one
	e.fv.mu.Lock()
	delete(e.fv.tokens, fileToken)
	e.fv.tokens["hvs.ROTATED-never-logged"] = tokenInfo{ttl: 3600, renewable: true}
	e.fv.mu.Unlock()
	e.files.set("/run/secrets/vault_token", "hvs.ROTATED-never-logged")
	if v, err := e.s.Get(ctx, "a"); err != nil || v != value {
		t.Fatalf("after rotation = %q %v", v, err)
	}
	if n := e.fv.count(http.MethodGet, "/v1/secret/data/cactustrade/a"); n != 2 {
		t.Fatalf("%d reads, want the refused one and one retry", n)
	}
	// revoked with no new token: one retry with the same file, then refused
	e.fv.mu.Lock()
	delete(e.fv.tokens, "hvs.ROTATED-never-logged")
	e.fv.mu.Unlock()
	before := e.fv.count(http.MethodGet, "/v1/secret/data/cactustrade/a")
	forgetValues(e.s)
	if _, err := e.s.Get(ctx, "a"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("revoked = %v", err)
	}
	if n := e.fv.count(http.MethodGet, "/v1/secret/data/cactustrade/a") - before; n != 1 {
		t.Fatalf("%d more reads (the re-read token is refused at lookup)", n)
	}
	// the token file is gone or empty
	e.files.mu.Lock()
	delete(e.files.m, "/run/secrets/vault_token")
	e.files.mu.Unlock()
	if err := e.s.Check(ctx); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "vault_token") {
		t.Fatalf("no token file = %v", err)
	}
	e.files.set("/run/secrets/vault_token", "  \n")
	if err := e.s.Check(ctx); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty token file = %v", err)
	}
	noSecrets(t, "the log", e.logs.String())
}

// TestKubernetesLoginAndRenewal: the service-account JWT buys a token; it is renewed two
// thirds into its lease; a refused renewal, a lease near its maximum or a non-renewable token
// sign in again.
func TestKubernetesLoginAndRenewal(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, k8s)
	if err := e.s.Put(ctx, "a", value); err != nil {
		t.Fatal(err)
	}
	if e.fv.logins != 1 || e.fv.renewals != 0 {
		t.Fatalf("logins %d renewals %d", e.fv.logins, e.fv.renewals)
	}
	e.clk.Advance(39 * time.Second) // before 2/3 of 60 s
	_, _ = e.s.Get(ctx, "a")
	if e.fv.renewals != 0 {
		t.Fatal("renewed too early")
	}
	e.clk.Advance(2 * time.Second) // 41 s
	if _, err := e.s.Get(ctx, "a"); err != nil || e.fv.renewals != 1 || e.fv.logins != 1 {
		t.Fatalf("renewal: %v renewals %d logins %d", err, e.fv.renewals, e.fv.logins)
	}
	// the renewal gave a fresh 60 s: the next one is 40 s later
	e.clk.Advance(39 * time.Second)
	_, _ = e.s.Get(ctx, "a")
	if e.fv.renewals != 1 {
		t.Fatal("renewed before the new lease's 2/3")
	}
	// a refused renewal signs in again
	e.fv.refuseRenw = true
	e.clk.Advance(2 * time.Second)
	if _, err := e.s.Get(ctx, "a"); err != nil || e.fv.logins != 2 {
		t.Fatalf("after a refused renewal: %v logins %d", err, e.fv.logins)
	}
	// a renewal that leaves less than minLease (max TTL near) signs in again
	e.fv.refuseRenw, e.fv.renewLease = false, 10
	e.clk.Advance(41 * time.Second)
	if _, err := e.s.Get(ctx, "a"); err != nil || e.fv.logins != 3 {
		t.Fatalf("after a short renewal: %v logins %d", err, e.fv.logins)
	}
	// past expiry (the process slept): no renewal attempt, a fresh login
	renewals := e.fv.renewals
	e.clk.Advance(2 * time.Hour)
	if _, err := e.s.Get(ctx, "a"); err != nil || e.fv.logins != 4 || e.fv.renewals != renewals {
		t.Fatalf("after expiry: %v logins %d renewals %d", err, e.fv.logins, e.fv.renewals)
	}
	// renewal answers that cannot be used
	for _, bad := range []string{`{"auth":null}`, `nope`} {
		e.fv.override = func(r *http.Request) (int, string, bool) {
			return 200, bad, strings.HasSuffix(r.URL.Path, "renew-self")
		}
		e.clk.Advance(41 * time.Second)
		logins := e.fv.logins
		if _, err := e.s.Get(ctx, "a"); err != nil || e.fv.logins != logins+1 {
			t.Fatalf("renewal answer %q: %v logins %d", bad, err, e.fv.logins)
		}
	}
	noSecrets(t, "the log", e.logs.String())
	if !strings.Contains(e.logs.String(), `"ttl_seconds":60`) {
		t.Fatalf("the token lifetime is not logged: %s", e.logs.String())
	}
}

func TestKubernetesLoginRefused(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		mut  func(e *env)
		want string
	}{
		{"wrong role", func(e *env) { e.s.cfg.K8sRole = "other" }, "HTTP 403 (permission denied)"},
		{"wrong JWT", func(e *env) { e.files.set(DefaultK8sJWTFile, "eyJ.other") }, "HTTP 403"},
		{"no JWT file", func(e *env) {
			e.files.mu.Lock()
			delete(e.files.m, DefaultK8sJWTFile)
			e.files.mu.Unlock()
		}, "service-account token"},
		{"no token in the answer", func(e *env) {
			e.fv.override = func(r *http.Request) (int, string, bool) { return 200, `{"auth":{"client_token":""}}`, true }
		}, "no token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, k8s)
			tt.mut(e)
			err := e.s.Check(ctx)
			if !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			noSecrets(t, "the error", err.Error())
			noSecrets(t, "the log", e.logs.String())
		})
	}
}

// TestRedirectRefused: Vault's redirect (a standby node) is never followed, so the token
// never reaches another address.
func TestRedirectRefused(t *testing.T) {
	ctx := context.Background()
	var hits int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	t.Cleanup(other.Close)
	e := newEnv(t, nil)
	if err := e.s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	e.fv.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	_, err := e.s.Get(ctx, "a")
	if !errors.Is(err, ErrRedirect) || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("the redirect target got %d requests", hits)
	}
	noSecrets(t, "the error", err.Error())
}

func TestVaultDownAndCancelled(t *testing.T) {
	e := newEnv(t, nil)
	e.fv.srv.Close()
	err := e.s.Put(context.Background(), "a", value)
	if !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "does not answer") {
		t.Fatalf("down = %v", err)
	}
	noSecrets(t, "the error", err.Error())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.s.Put(ctx, "a", value); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
	e2 := newEnv(t, k8s)
	e2.fv.srv.Close()
	if err := e2.s.Check(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("login, Vault down = %v", err)
	}
}

func TestNamespaceAndMount(t *testing.T) {
	ctx := context.Background()
	var seen []*http.Request
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "lookup-self") {
			_, _ = io.WriteString(w, `{"data":{"ttl":0}}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	fs := &files{m: map[string][]byte{"/t": []byte(fileToken)}}
	s, err := New(Config{Addr: srv.URL, TokenFile: "/t", Mount: "kv", Prefix: "apps/trade", Namespace: "team-a", HTTP: srv.Client(), ReadFile: fs.read})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "x", value); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	last := seen[len(seen)-1]
	if last.URL.Path != "/v1/kv/data/apps/trade/x" || last.Header.Get("X-Vault-Namespace") != "team-a" || last.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %v", last.URL.Path, last.Header)
	}
}

// TestTLSWithCAFile: the CA file is trusted; without it the same server is refused.
func TestTLSWithCAFile(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)}
	fv := &fakeVault{t: t, now: clk.Now, kv: map[string]entry{}, tokens: map[string]tokenInfo{fileToken: {}}}
	fv.srv = httptest.NewUnstartedServer(http.HandlerFunc(fv.serve))
	fv.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake below
	fv.srv.StartTLS()
	t.Cleanup(fv.srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fv.srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := &files{m: map[string][]byte{"/t": []byte(fileToken)}}
	read := func(n string) ([]byte, error) {
		if n == ca {
			return os.ReadFile(n)
		}
		return fs.read(n)
	}
	s, err := New(Config{Addr: fv.srv.URL, TokenFile: "/t", CAFile: ca, ReadFile: read})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "a", value); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get(ctx, "a"); err != nil || v != value {
		t.Fatalf("get over TLS = %q %v", v, err)
	}
	untrusted, err := New(Config{Addr: fv.srv.URL, TokenFile: "/t", ReadFile: read})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untrusted.Get(ctx, "a"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("an unknown certificate authority was trusted: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{``, "HTTP 500"},
		{`{"errors":[]}`, "HTTP 500"},
		{`{"errors":["permission denied"]}`, "HTTP 500 (permission denied)"},
		{`{"errors":["<script>\n"]}`, "HTTP 500 (script)"},
		{`{"errors":["` + strings.Repeat("a", 300) + `"]}`, "HTTP 500 (" + strings.Repeat("a", 120) + ")"},
	}
	for _, tt := range tests {
		if got := describe(500, []byte(tt.body)); got != tt.want {
			t.Errorf("describe(%q) = %q", tt.body, got)
		}
	}
}

// forget drops the store's token, so the next call fetches one.
func forget(s *Store) {
	s.mu.Lock()
	s.token = ""
	s.mu.Unlock()
}
