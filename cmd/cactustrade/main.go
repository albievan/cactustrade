// Command cactustrade is the cactusTrade server and its admin commands. It is the only
// composition root: every adapter is wired here.
//
//	cactustrade [serve] [flags]     run the server (default)
//	cactustrade version             print the version
//	cactustrade healthcheck         exit 0 when the local server is healthy (containers)
//	cactustrade migrate status|up|down-to N
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/albievan/cactustrade/api"
	"github.com/albievan/cactustrade/internal/adapters/argon2"
	httpapi "github.com/albievan/cactustrade/internal/adapters/http"
	"github.com/albievan/cactustrade/internal/adapters/secretstore"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/web"
)

// Set by the build (-ldflags "-X main.version=… -X main.build=…").
var (
	version = "dev"
	build   = "0"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "cactustrade:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		_, err := fmt.Fprintf(out, "%s (build %s)\n", version, build)
		return err
	case "healthcheck":
		return healthcheck(ctx, getenv)
	case "migrate":
		return migrate(ctx, args, getenv, out)
	case "serve":
		cfg, err := loadConfig(args, getenv)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serve(ctx, cfg, getenv)
	}
	return fmt.Errorf("unknown command %q (serve, version, healthcheck, migrate)", cmd)
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func openStore(ctx context.Context, cfg config) (*sqlstore.Store, error) {
	if cfg.DBDriver == sqlstore.DriverMariaDB {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return sqlstore.OpenMariaDB(ctx, sqlstore.MariaDBConfig{Host: cfg.DBHost, Name: cfg.DBName, User: cfg.DBUser, Password: cfg.DBPassword})
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return nil, err
	}
	return sqlstore.OpenSQLite(ctx, cfg.DBPath)
}

func serve(ctx context.Context, cfg config, getenv func(string) string) error {
	log := newLogger(cfg.LogLevel)
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data folder: %w", err)
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.MigrateOnStart {
		if err := store.Migrate(ctx); err != nil {
			return err
		}
	}

	// Master key: explicit (containers) or generated in the data folder (standalone only).
	gen := ""
	if !cfg.Container {
		gen = filepath.Join(cfg.DataDir, "master.key")
	}
	key, created, err := secretstore.LoadMasterKey(cfg.MasterKey, cfg.MasterKeyFile, gen)
	if err != nil {
		return err
	}
	if created {
		log.Warn("generated a new master key: back it up; without it the stored credentials cannot be read", "file", gen)
	}
	cipher, err := secretstore.NewCipher(key)
	if err != nil {
		return err
	}
	envCreds, err := secretstore.FromEnvironment(getenv, os.ReadFile)
	if err != nil {
		return err
	}
	clock := system.Clock{}
	secrets := secretstore.WithEnv(secretstore.NewDB(store.Secrets(), cipher, clock), envCreds)
	for n := range envCreds {
		log.Info("credential provided by the environment", "credential", string(n))
	}

	svc, err := app.New(app.Deps{
		Store: store, Secrets: secrets, Hasher: argon2.New(argon2.Default),
		Random: system.Random{}, Clock: clock, Log: log,
	}, app.Config{SetupToken: cfg.SetupToken, Version: version, Build: build})
	if err != nil {
		return err
	}
	mode := svc.Mode(ctx)
	log = log.With("mode", string(mode))

	if need, err := svc.SetupRequired(ctx); err != nil {
		return err
	} else if need {
		if err := ensureSetupToken(&cfg); err != nil {
			return err
		}
		svc, err = app.New(app.Deps{
			Store: store, Secrets: secrets, Hasher: argon2.New(argon2.Default),
			Random: system.Random{}, Clock: clock, Log: log,
		}, app.Config{SetupToken: cfg.SetupToken, Version: version, Build: build})
		if err != nil {
			return err
		}
		log.Warn("first run: open the web UI and create the administrator with the setup token",
			"setup_token_file", setupTokenHint(cfg))
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	handler := httpapi.New(svc, log, httpapi.Config{
		CookieSecure: cfg.CookieSecure, TrustedProxies: cfg.TrustedProxies, MetricsToken: cfg.MetricsToken,
		Web: web.FS(), OpenAPI: api.OpenAPI,
	}, reg)

	srv := &http.Server{
		Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	scheme := "http"
	if cfg.TLS == "auto" {
		tc, generated, err := tlsConfig(cfg.TLSCert, cfg.TLSKey, cfg.DataDir, time.Now())
		if err != nil {
			return err
		}
		if generated {
			log.Warn("generated a self-signed TLS certificate (for local use; set CACTUSTRADE_TLS_CERT/KEY for your own)")
		}
		srv.TLSConfig, scheme = tc, "https"
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}
	go httpapi.PurgeLoop(ctx, svc, log, 10*time.Minute)

	errc := make(chan error, 1)
	go func() {
		if srv.TLSConfig != nil {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	log.Info("cactusTrade started", "version", version, "build", build, "addr", cfg.Addr, "scheme", scheme,
		"database", store.Driver(), "container", cfg.Container)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

// ensureSetupToken makes sure a first-run setup token exists: the configured one, or a new
// one written (0600) to setup_token in the data folder. Its value is never logged.
func ensureSetupToken(cfg *config) error {
	if cfg.SetupToken != "" {
		return nil
	}
	p := filepath.Join(cfg.DataDir, "setup_token")
	if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) != "" {
		cfg.SetupToken = strings.TrimSpace(string(b))
		return nil
	}
	buf := make([]byte, 18)
	_, _ = rand.Read(buf)
	cfg.SetupToken = base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(p, []byte(cfg.SetupToken+"\n"), 0o600); err != nil {
		return fmt.Errorf("setup token: %w", err)
	}
	return nil
}

func setupTokenHint(cfg config) string {
	if cfg.SecretsDir != "" && fileExists(filepath.Join(cfg.SecretsDir, "setup_token")) {
		return filepath.Join(cfg.SecretsDir, "setup_token")
	}
	return filepath.Join(cfg.DataDir, "setup_token")
}

// healthcheck asks the local server's /healthz (the container HEALTHCHECK). Over TLS it
// trusts the server's own certificate file (and the system roots) and verifies the name in
// it, dialling 127.0.0.1.
func healthcheck(ctx context.Context, getenv func(string) string) error {
	cfg, err := loadConfig(nil, func(k string) string {
		if strings.HasSuffix(k, "PASSWORD") || strings.HasSuffix(k, "PASSWORD_FILE") {
			return "unused"
		}
		return getenv(k)
	})
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return err
	}
	target, tr := "http://127.0.0.1:"+port, &http.Transport{}
	if cfg.TLS == "auto" {
		certFile := cfg.TLSCert
		if certFile == "" {
			certFile = filepath.Join(cfg.DataDir, "tls", "cert.pem")
		}
		tc, err := trustOwnCert(certFile)
		if err != nil {
			return err
		}
		tr.TLSClientConfig = tc
		addr := "127.0.0.1:" + port
		tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		target = "https://" + net.JoinHostPort(tc.ServerName, port)
	}
	c := &http.Client{Timeout: 4 * time.Second, Transport: tr}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

// trustOwnCert returns a TLS client config that trusts certFile's leaf (besides the system
// roots) and expects its first DNS name.
func trustOwnCert(certFile string) (*tls.Config, error) {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("healthcheck: %w", err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("healthcheck: no certificate in " + certFile)
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("healthcheck: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	pool.AddCert(leaf)
	name := "localhost"
	if len(leaf.DNSNames) > 0 {
		name = leaf.DNSNames[0]
	}
	return &tls.Config{RootCAs: pool, ServerName: name, MinVersion: tls.VersionTLS12}, nil
}

func migrate(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("migrate status | up | down-to N")
	}
	cfg, err := loadConfig(nil, getenv)
	if err != nil {
		return err
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	p, err := store.Migrator()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		cur, target, err := p.GetVersions(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "schema %d, newest %d, database %s\n", cur, target, store.Driver())
		return err
	case "up":
		_, err := p.Up(ctx)
		return err
	case "down-to":
		if len(args) != 2 {
			return errors.New("migrate down-to N")
		}
		v, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || v < 0 {
			return errors.New("migrate down-to needs a schema version ≥ 0")
		}
		_, err = p.DownTo(ctx, v)
		return err
	}
	return fmt.Errorf("unknown migrate command %q", args[0])
}
