package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/domain"
)

// miniVault is just enough of Vault's KV v2 for the wiring tests: token lookup, data
// GET/POST under secret/cactustrade.
type miniVault struct {
	mu    sync.Mutex
	token string
	kv    map[string]string
	puts  int
}

func newMiniVault(t *testing.T, token string) (*miniVault, *httptest.Server) {
	t.Helper()
	v := &miniVault{token: token, kv: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		if r.Header.Get("X-Vault-Token") != v.token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		const data = "/v1/secret/data/cactustrade/"
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			_, _ = io.WriteString(w, `{"data":{"ttl":0,"renewable":false}}`)
		case strings.HasPrefix(r.URL.Path, data) && r.Method == http.MethodPost:
			var in struct {
				Data map[string]string `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			v.kv[strings.TrimPrefix(r.URL.Path, data)] = in.Data["value"]
			v.puts++
			_, _ = io.WriteString(w, `{}`)
		case strings.HasPrefix(r.URL.Path, data) && r.Method == http.MethodGet:
			val, ok := v.kv[strings.TrimPrefix(r.URL.Path, data)]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"value": val}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return v, srv
}

func TestVaultConfig(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "vault_token")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(nil, envOf(nil))
	if err != nil || c.Secrets != "db" {
		t.Fatalf("default = %q %v", c.Secrets, err)
	}
	c, err = loadConfig(nil, envOf(map[string]string{
		"CACTUSTRADE_SECRETS": "vault", "CACTUSTRADE_VAULT_ADDR": "https://vault.example:8200", "CACTUSTRADE_SECRETS_DIR": dir,
		"CACTUSTRADE_VAULT_MOUNT": "kv", "CACTUSTRADE_VAULT_PREFIX": "trade", "CACTUSTRADE_VAULT_NAMESPACE": "team",
	}))
	if err != nil || c.Secrets != "vault" || c.Vault.TokenFile != tok || c.Vault.Mount != "kv" || c.Vault.Prefix != "trade" || c.Vault.Namespace != "team" {
		t.Fatalf("vault = %+v %v", c.Vault, err)
	}
	c, err = loadConfig(nil, envOf(map[string]string{
		"CACTUSTRADE_SECRETS": "vault", "CACTUSTRADE_VAULT_ADDR": "https://vault.example:8200", "CACTUSTRADE_SECRETS_DIR": dir,
		"CACTUSTRADE_VAULT_K8S_ROLE": "cactustrade",
	}))
	if err != nil || c.Vault.TokenFile != "" || c.Vault.K8sRole != "cactustrade" {
		t.Fatalf("k8s = %+v %v", c.Vault, err)
	}
	bad := []map[string]string{
		{"CACTUSTRADE_SECRETS": "files"},
		{"CACTUSTRADE_SECRETS": "vault"}, // no address
		{"CACTUSTRADE_SECRETS": "vault", "CACTUSTRADE_VAULT_ADDR": "https://vault.example"},                                      // no token source
		{"CACTUSTRADE_SECRETS": "vault", "CACTUSTRADE_VAULT_ADDR": "http://vault.example", "CACTUSTRADE_VAULT_TOKEN_FILE": "/t"}, // plain http
	}
	for _, e := range bad {
		if _, err := loadConfig(nil, envOf(e)); err == nil {
			t.Errorf("accepted %v", e)
		}
	}
}

// TestServeSecretsInVault: with CACTUSTRADE_SECRETS=vault the server's credential store is
// Vault (with the environment overlay on top), and Vault being down does not stop the start.
func TestServeSecretsInVault(t *testing.T) {
	ctx := context.Background()
	mv, srv := newMiniVault(t, "hvs.serve")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vault_token"), []byte("hvs.serve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{
		"CACTUSTRADE_DATA_DIR": dir, "CACTUSTRADE_SECRETS": "vault", "CACTUSTRADE_VAULT_ADDR": srv.URL,
		"CACTUSTRADE_VAULT_TOKEN_FILE": filepath.Join(dir, "vault_token"), "ETORO_DEMO_PUB": "env-pub-key",
	})
	cfg, err := loadConfig(nil, env)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	sec, _, err := openSecrets(ctx, cfg, nil, env, system.Clock{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := sec.Put(ctx, "llm.p1.api_key", "sk-in-vault"); err != nil {
		t.Fatal(err)
	}
	if mv.kv["llm.p1.api_key"] != "sk-in-vault" {
		t.Fatalf("not in Vault: %v", mv.kv)
	}
	if v, err := sec.Get(ctx, domain.CredEtoroDemoAPIKey); err != nil || v != "env-pub-key" {
		t.Fatalf("the environment overlay: %q %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "master.key")); !os.IsNotExist(err) {
		t.Fatal("a master key was generated although Vault keeps the credentials")
	}
	srv.Close()
	if _, _, err := openSecrets(ctx, cfg, nil, env, system.Clock{}, slog.New(slog.NewJSONHandler(&logs, nil))); err != nil {
		t.Fatalf("Vault down stopped the start: %v", err)
	}
	if !strings.Contains(logs.String(), "Vault is not usable yet") || strings.Contains(logs.String(), "hvs.serve") || strings.Contains(logs.String(), "sk-in-vault") {
		t.Fatalf("log: %s", logs.String())
	}
}

// TestSecretsMigrateToVault is `cactustrade secrets migrate --to vault`: every stored
// credential is copied and read back; only names are printed; the dry run writes nothing.
func TestSecretsMigrateToVault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := map[string]string{"CACTUSTRADE_DATA_DIR": dir}
	cfg, err := loadConfig(nil, envOf(base))
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlstore.OpenSQLite(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db, _, err := openSecrets(ctx, cfg, st, envOf(nil), system.Clock{}, slog.New(slog.DiscardHandler)) // generates master.key
	if err != nil {
		t.Fatal(err)
	}
	stored := map[domain.CredentialName]string{"etoro.a1.api_key": "pub-VALUE-1", "etoro.a1.user_key": "user-VALUE-2", "llm.p1.api_key": "sk-VALUE-3"}
	for n, v := range stored {
		if err := db.Put(ctx, n, v); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	mv, srv := newMiniVault(t, "hvs.migrate")
	tok := filepath.Join(dir, "vault.token")
	if err := os.WriteFile(tok, []byte("hvs.migrate"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CACTUSTRADE_DATA_DIR": dir, "CACTUSTRADE_VAULT_ADDR": srv.URL, "CACTUSTRADE_VAULT_TOKEN_FILE": tok}

	for _, args := range [][]string{{"secrets"}, {"secrets", "rotate"}, {"secrets", "migrate"}, {"secrets", "migrate", "--to", "db"}, {"secrets", "migrate", "--to", "vault", "extra"}} {
		if err := run(ctx, args, envOf(env), io.Discard); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	var out bytes.Buffer
	if err := run(ctx, []string{"secrets", "migrate", "--to", "vault", "--dry-run"}, envOf(env), &out); err != nil {
		t.Fatal(err)
	}
	if mv.puts != 0 || !strings.Contains(out.String(), "would copy etoro.a1.api_key") {
		t.Fatalf("dry run wrote %d: %s", mv.puts, out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"secrets", "migrate", "--to", "vault"}, envOf(env), &out); err != nil {
		t.Fatal(err)
	}
	for n, v := range stored {
		if mv.kv[string(n)] != v {
			t.Errorf("%s in Vault = %q", n, mv.kv[string(n)])
		}
		if strings.Contains(out.String(), v) {
			t.Errorf("the output holds the value of %s", n)
		}
	}
	if !strings.Contains(out.String(), "copied 3 credential(s)") || strings.Contains(out.String(), "hvs.migrate") {
		t.Fatalf("output: %s", out.String())
	}
	// the database copies are unchanged
	st2, err := sqlstore.OpenSQLite(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if rs, err := st2.Secrets().List(ctx); err != nil || len(rs) != 3 {
		t.Fatalf("database copies %d %v", len(rs), err)
	}

	// a wrong token, a missing master key: refused, nothing written
	mv.puts = 0
	if err := os.WriteFile(tok, []byte("hvs.wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secrets", "migrate", "--to", "vault"}, envOf(env), io.Discard); err == nil || strings.Contains(err.Error(), "hvs.wrong") {
		t.Fatalf("wrong token: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tok, []byte("hvs.migrate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secrets", "migrate", "--to", "vault"}, envOf(env), io.Discard); err == nil {
		t.Fatal("migrated without the master key")
	}
	if _, err := os.Stat(filepath.Join(dir, "master.key")); !os.IsNotExist(err) {
		t.Fatal("migrate generated a master key")
	}
	if mv.puts != 0 {
		t.Fatalf("%d writes after a refusal", mv.puts)
	}
}
