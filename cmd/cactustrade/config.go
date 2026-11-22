package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
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
	SetupToken     string
	TLS            string // auto | off
	TLSCert        string
	TLSKey         string
	CookieSecure   bool
	TrustedProxies []netip.Prefix
	MetricsToken   string
	LogLevel       slog.Level
	MigrateOnStart bool
	Container      bool
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
	if c.SetupToken, err = secret(getenv, "CACTUSTRADE_SETUP_TOKEN", c.SecretsDir, "setup_token"); err != nil {
		return c, err
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
	if err := c.LogLevel.UnmarshalText([]byte(env(getenv, "CACTUSTRADE_LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("CACTUSTRADE_LOG_LEVEL: %w", err)
	}
	return c, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
