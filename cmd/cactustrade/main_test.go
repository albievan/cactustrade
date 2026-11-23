package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig(t *testing.T) {
	c, err := loadConfig(nil, envOf(nil))
	if err != nil || c.DBDriver != "sqlite" || c.Addr != ":8443" || c.TLS != "auto" || !c.CookieSecure || !c.MigrateOnStart ||
		c.DBPath != filepath.Join("data", "cactustrade.db") {
		t.Fatalf("defaults %+v %v", c, err)
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "db_password"), []byte("pw\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "master_key"), []byte(strings.Repeat("ab", 32)), 0o600)
	c, err = loadConfig([]string{"--addr", ":9000"}, envOf(map[string]string{
		"CACTUSTRADE_CONTAINER": "1", "CACTUSTRADE_DB_DRIVER": "mariadb", "CACTUSTRADE_SECRETS_DIR": dir,
		"CACTUSTRADE_TLS": "off", "CACTUSTRADE_TRUSTED_PROXIES": "10.42.0.0/16, 192.0.2.1", "CACTUSTRADE_COOKIE_SECURE": "false",
		"CACTUSTRADE_LOG_LEVEL": "debug",
	}))
	if err != nil || c.DBPassword != "pw" || c.MasterKeyFile == "" || c.DataDir != "/data" ||
		c.Addr != ":9000" || len(c.TrustedProxies) != 2 || c.CookieSecure {
		t.Fatalf("container %+v %v", c, err)
	}
	bad := []map[string]string{
		{"CACTUSTRADE_DB_DRIVER": "postgres"},
		{"CACTUSTRADE_TLS": "maybe"},
		{"CACTUSTRADE_TLS_CERT": "/x"},
		{"CACTUSTRADE_DB_DRIVER": "mariadb"}, // no password
		{"CACTUSTRADE_COOKIE_SECURE": "perhaps"},
		{"CACTUSTRADE_TRUSTED_PROXIES": "nonsense"},
		{"CACTUSTRADE_LOG_LEVEL": "loud"},
		{"CACTUSTRADE_DB_PASSWORD_FILE": "/nope", "CACTUSTRADE_DB_DRIVER": "mariadb"},
	}
	for _, e := range bad {
		if _, err := loadConfig(nil, envOf(e)); err == nil {
			t.Errorf("accepted %v", e)
		}
	}
	if _, err := loadConfig([]string{"extra"}, envOf(nil)); err == nil {
		t.Error("extra argument accepted")
	}
}

func TestRunVersionAndUnknown(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, envOf(nil), &out); err != nil || !strings.Contains(out.String(), "(build ") {
		t.Fatalf("version %q %v", out.String(), err)
	}
	if err := run(context.Background(), []string{"frobnicate"}, envOf(nil), &out); err == nil {
		t.Fatal("unknown command")
	}
}

func TestMigrateAndTLS(t *testing.T) {
	dir := t.TempDir()
	e := envOf(map[string]string{"CACTUSTRADE_DATA_DIR": dir})
	var out bytes.Buffer
	_ = os.MkdirAll(dir, 0o700)
	for _, args := range [][]string{{"migrate", "up"}, {"migrate", "status"}, {"migrate", "down-to", "0"}, {"migrate", "status"}} {
		out.Reset()
		if err := run(context.Background(), args, e, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if st := out.String(); !strings.HasPrefix(st, "schema 0, newest ") || strings.HasPrefix(st, "schema 0, newest 0") || !strings.Contains(st, "database sqlite") {
		t.Fatalf("status %q", out.String())
	}
	for _, args := range [][]string{{"migrate"}, {"migrate", "sideways"}, {"migrate", "down-to", "x"}} {
		if err := run(context.Background(), args, e, &out); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	now := time.Now()
	cfg, gen, err := tlsConfig("", "", dir, now)
	if err != nil || !gen || cfg.MinVersion == 0 {
		t.Fatal("self-signed", err)
	}
	if _, gen, _ := tlsConfig("", "", dir, now); gen {
		t.Fatal("regenerated a valid certificate")
	}
	if _, gen, _ := tlsConfig("", "", dir, now.Add(380*24*time.Hour)); !gen {
		t.Fatal("expiring certificate not renewed")
	}
	if tc, err := trustOwnCert(filepath.Join(dir, "tls", "cert.pem")); err != nil || tc.ServerName != "localhost" {
		t.Fatal("trustOwnCert", err)
	}
}
