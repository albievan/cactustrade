package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/albievan/cactustrade/internal/adapters/vault"
)

// config is read from CACTUSTRADE_* environment variables, then flags.
type config struct {
	Addr           string
	DataDir        string
	DBDriver       string // sqlite | mariadb
	DBPath         string
	DBHost         string
	DBName         string
	DBUser         string
	DBPassword     string
	SecretsDir     string
	MasterKey      string
	MasterKeyFile  string
	TLS            string // auto | off
	TLSCert        string
	TLSKey         string
	CookieSecure   bool
	TrustedProxies []netip.Prefix
	MetricsToken   string
	LogLevel       slog.Level
	MigrateOnStart bool
	Container      bool
	// PublicURL is where users open the app (links in e-mails); "" = no password reset.
	PublicURL string
	// Secrets is where credentials are kept: "db" (default; AES-256-GCM in the database) or
	// "vault" (HashiCorp Vault KV v2, PLAN 7.8, OD30). Vault holds its settings.
	Secrets string
	Vault   vault.Config
	// RealTrading is the deployment's opt-in to real-money entries (OD28, the second key next
	// to each account's armed gate): CACTUSTRADE_REAL_TRADING=on. Off by default; .env keeps it
	// off and deploy.sh never sets it. Exits on real accounts never depend on it.
	RealTrading bool
}

// Secret stores (CACTUSTRADE_SECRETS).
const (
	secretsDB    = "db"
	secretsVault = "vault"
)

// loadVaultConfig reads CACTUSTRADE_VAULT_*. The token file defaults to vault_token in the
// secrets folder when it exists and no Kubernetes role is set.
func loadVaultConfig(getenv func(string) string, secretsDir string) (vault.Config, error) {
	v := vault.Config{
		Addr: getenv("CACTUSTRADE_VAULT_ADDR"), Mount: getenv("CACTUSTRADE_VAULT_MOUNT"), Prefix: getenv("CACTUSTRADE_VAULT_PREFIX"),
		TokenFile: getenv("CACTUSTRADE_VAULT_TOKEN_FILE"), K8sRole: getenv("CACTUSTRADE_VAULT_K8S_ROLE"),
		K8sJWTFile: getenv("CACTUSTRADE_VAULT_K8S_JWT_FILE"), K8sMount: getenv("CACTUSTRADE_VAULT_K8S_MOUNT"),
		CAFile: getenv("CACTUSTRADE_VAULT_CA_FILE"), Namespace: getenv("CACTUSTRADE_VAULT_NAMESPACE"),
	}
	if v.Addr == "" {
		return v, errors.New("CACTUSTRADE_SECRETS=vault needs CACTUSTRADE_VAULT_ADDR (https://vault.example:8200)")
	}
	if v.TokenFile == "" && v.K8sRole == "" && secretsDir != "" {
		if f := filepath.Join(secretsDir, "vault_token"); fileExists(f) {
			v.TokenFile = f
		}
	}
	if _, err := vault.New(v); err != nil { // checks the settings; nothing is sent
		return v, err
	}
	return v, nil
}

// env returns the variable, or the trimmed content of the file named by NAME_FILE, or the
// file NAME (lower-cased, without the prefix) in the secrets folder.
func env(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}

func secret(getenv func(string) string, name, secretsDir, fileName string) (string, error) {
	if v := getenv(name); v != "" {
		return strings.TrimSpace(v), nil
	}
	f := getenv(name + "_FILE")
	if f == "" && secretsDir != "" && fileName != "" {
		f = filepath.Join(secretsDir, fileName)
		if _, err := os.Stat(f); errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
	}
	if f == "" {
		return "", nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func boolEnv(getenv func(string) string, name string, def bool) (bool, error) {
	switch strings.ToLower(getenv(name)) {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s must be true or false", name)
}

func loadConfig(args []string, getenv func(string) string) (config, error) {
	var c config
	var err error
	c.Container = getenv("CACTUSTRADE_CONTAINER") == "1"
	defData := "data"
	if c.Container {
		defData = "/data"
	}
	c.DataDir = env(getenv, "CACTUSTRADE_DATA_DIR", defData)
	c.DBDriver = env(getenv, "CACTUSTRADE_DB_DRIVER", "sqlite")
	c.TLS = env(getenv, "CACTUSTRADE_TLS", "auto")
	c.TLSCert, c.TLSKey = getenv("CACTUSTRADE_TLS_CERT"), getenv("CACTUSTRADE_TLS_KEY")
	c.Addr = getenv("CACTUSTRADE_ADDR")
	c.DBPath = getenv("CACTUSTRADE_DB_PATH")
	c.DBHost = env(getenv, "CACTUSTRADE_DB_HOST", "localhost")
	c.DBName = env(getenv, "CACTUSTRADE_DB_NAME", "cactustrade")
	c.DBUser = env(getenv, "CACTUSTRADE_DB_USER", "cactustrade")
	c.SecretsDir = getenv("CACTUSTRADE_SECRETS_DIR")
	c.MasterKey = getenv("CACTUSTRADE_MASTER_KEY")
	c.MasterKeyFile = getenv("CACTUSTRADE_MASTER_KEY_FILE")
	if c.MasterKey == "" && c.MasterKeyFile == "" && c.SecretsDir != "" {
		if f := filepath.Join(c.SecretsDir, "master_key"); fileExists(f) {
			c.MasterKeyFile = f
		}
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.StringVar(&c.Addr, "addr", c.Addr, "listen address (default :8443 with TLS, :8080 without)")
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "data folder (SQLite database, master key, TLS certificate)")
	fs.StringVar(&c.DBDriver, "db", c.DBDriver, "database: sqlite or mariadb")
	fs.StringVar(&c.TLS, "tls", c.TLS, "auto (certificate from CACTUSTRADE_TLS_CERT/KEY or self-signed) or off (behind a TLS proxy)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	if c.DBDriver != "sqlite" && c.DBDriver != "mariadb" {
		return c, errors.New("CACTUSTRADE_DB_DRIVER must be sqlite or mariadb")
	}
	if c.TLS != "auto" && c.TLS != "off" {
		return c, errors.New("CACTUSTRADE_TLS must be auto or off")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("set both CACTUSTRADE_TLS_CERT and CACTUSTRADE_TLS_KEY, or neither")
	}
	if c.Addr == "" {
		c.Addr = ":8443"
		if c.TLS == "off" {
			c.Addr = ":8080"
		}
	}
	if c.DBPath == "" {
		c.DBPath = filepath.Join(c.DataDir, "cactustrade.db")
	}
	if c.DBDriver == "mariadb" {
		if c.DBPassword, err = secret(getenv, "CACTUSTRADE_DB_PASSWORD", c.SecretsDir, "db_password"); err != nil {
			return c, err
		}
		if c.DBPassword == "" {
			return c, errors.New("mariadb needs CACTUSTRADE_DB_PASSWORD, CACTUSTRADE_DB_PASSWORD_FILE or db_password in CACTUSTRADE_SECRETS_DIR")
		}
	}
	if c.MetricsToken, err = secret(getenv, "CACTUSTRADE_METRICS_TOKEN", c.SecretsDir, "metrics_token"); err != nil {
		return c, err
	}
	if c.CookieSecure, err = boolEnv(getenv, "CACTUSTRADE_COOKIE_SECURE", true); err != nil {
		return c, err
	}
	if c.MigrateOnStart, err = boolEnv(getenv, "CACTUSTRADE_MIGRATE_ON_START", true); err != nil {
		return c, err
	}
	for _, p := range strings.FieldsFunc(getenv("CACTUSTRADE_TRUSTED_PROXIES"), func(r rune) bool { return r == ',' || r == ' ' }) {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			a, aerr := netip.ParseAddr(p)
			if aerr != nil {
				return c, fmt.Errorf("CACTUSTRADE_TRUSTED_PROXIES: %q is not an address or CIDR", p)
			}
			pfx = netip.PrefixFrom(a, a.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx)
	}
	if c.PublicURL = strings.TrimRight(strings.TrimSpace(getenv("CACTUSTRADE_PUBLIC_URL")), "/"); c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("CACTUSTRADE_PUBLIC_URL: %q is not an http(s) address such as https://trader.example:8453", c.PublicURL)
		}
	}
	if err := c.LogLevel.UnmarshalText([]byte(env(getenv, "CACTUSTRADE_LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("CACTUSTRADE_LOG_LEVEL: %w", err)
	}
	if c.RealTrading, err = realTradingEnv(getenv); err != nil {
		return c, err
	}
	switch c.Secrets = env(getenv, "CACTUSTRADE_SECRETS", secretsDB); c.Secrets {
	case secretsDB:
	case secretsVault:
		if c.Vault, err = loadVaultConfig(getenv, c.SecretsDir); err != nil {
			return c, err
		}
	default:
		return c, errors.New("CACTUSTRADE_SECRETS must be db or vault")
	}
	return c, nil
}

// realTradingEnv reads CACTUSTRADE_REAL_TRADING (OD28): exactly "on" switches real-money
// entries on for this deployment; unset or "off" leaves them off; anything else stops the
// start-up (a typo must never be read as either).
func realTradingEnv(getenv func(string) string) (bool, error) {
	switch v := strings.TrimSpace(getenv("CACTUSTRADE_REAL_TRADING")); v {
	case "", "off":
		return false, nil
	case "on":
		return true, nil
	}
	return false, errors.New("CACTUSTRADE_REAL_TRADING must be on or off (default off)")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// metricsTokenFile is the generated /metrics bearer token's file in the data folder.
const metricsTokenFile = "metrics.token"

// ensureMetricsToken returns the /metrics bearer token kept in path, creating it (32 random
// bytes, hex, mode 0600) when the file does not exist yet (PLAN 7.1.14: /metrics always needs
// a token). created reports that it was generated now. The token is never logged.
func ensureMetricsToken(path string) (token string, created bool, err error) {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if token = strings.TrimSpace(string(b)); token == "" {
			return "", false, fmt.Errorf("the metrics token file %s is empty: delete it to generate a new one", path)
		}
		return token, false, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", false, fmt.Errorf("metrics token: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", false, fmt.Errorf("metrics token: %w", err)
	}
	token = hex.EncodeToString(raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("metrics token: %w", err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		return "", false, fmt.Errorf("metrics token: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", false, fmt.Errorf("metrics token: %w", err)
	}
	return token, true, nil
}
