// Command cactustrade is the cactusTrade server and its admin commands. It is the only
// composition root: every adapter is wired here.
//
//	cactustrade [serve] [flags]     run the server (default)
//	cactustrade version             print the version
//	cactustrade healthcheck         exit 0 when the local server is healthy (containers)
//	cactustrade migrate status|up|down-to N
//	cactustrade backtest --instrument ID | --watchlist --account ID [--json]
//	                                        walk-forward backtest on eToro candles (read-only)
//	cactustrade etoro-probe --account ID --symbol SYM [--raw] [--record DIR]
//	                                        what eToro says about one share (read-only, PLAN 2.7)
//	cactustrade etoro-lookup --account ID --order-id N   eToro's answer about one order (read-only)
//	cactustrade llm-test [--profile NAME]   test an LLM reviewer profile (one made-up review)
//	cactustrade backup --out FILE           encrypted standalone backup (age; PLAN 7.6a)
//	cactustrade restore --in FILE --yes     restore a standalone backup (server stopped)
//	cactustrade encrypt | decrypt           age filter for deploy.sh's MariaDB dumps (7.6b)
//	cactustrade secrets migrate --to vault  copy the stored credentials into Vault (7.8)
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/albievan/cactustrade/api"
	"github.com/albievan/cactustrade/internal/adapters/argon2"
	"github.com/albievan/cactustrade/internal/adapters/calendar"
	"github.com/albievan/cactustrade/internal/adapters/etoro"
	"github.com/albievan/cactustrade/internal/adapters/eventbus"
	httpapi "github.com/albievan/cactustrade/internal/adapters/http"
	"github.com/albievan/cactustrade/internal/adapters/llm"
	"github.com/albievan/cactustrade/internal/adapters/logctx"
	"github.com/albievan/cactustrade/internal/adapters/mail"
	"github.com/albievan/cactustrade/internal/adapters/pdf"
	"github.com/albievan/cactustrade/internal/adapters/saxo"
	"github.com/albievan/cactustrade/internal/adapters/secretstore"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/adapters/tiger"
	"github.com/albievan/cactustrade/internal/adapters/trading212"
	"github.com/albievan/cactustrade/internal/adapters/vault"
	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
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
	case "backtest":
		return backtestCmd(ctx, args, getenv, out)
	case "llm-test":
		return llmTestCmd(ctx, args, getenv, out)
	case "etoro-lookup":
		return etoroLookupCmd(ctx, args, getenv, out)
	case "etoro-probe":
		return etoroProbeCmd(ctx, args, getenv, out)
	case "backup":
		return backupCmd(ctx, args, getenv, out, time.Now)
	case "restore":
		return restoreCmd(ctx, args, getenv, out, time.Now)
	case "encrypt":
		return encryptCmd(args, getenv, os.Stdin, out)
	case "decrypt":
		return decryptCmd(args, getenv, os.Stdin, out)
	case "secrets":
		return secretsCmd(ctx, args, getenv, out)
	case "serve":
		cfg, err := loadConfig(args, getenv)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = serve(ctx, cfg, getenv)
		if errors.Is(err, errRestart) {
			return restartSelf(cfg.Container, args)
		}
		return err
	}
	return fmt.Errorf("unknown command %q (serve, version, healthcheck, migrate, backtest, etoro-probe, etoro-lookup, llm-test, backup, restore, encrypt, decrypt, secrets)", cmd)
}

// newLogger writes JSON lines to stdout; every line logged with a context names the account
// it is for and its mode (PLAN 7.2).
func newLogger(level slog.Level) *slog.Logger {
	return newLoggerTo(os.Stdout, level)
}

func newLoggerTo(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(logctx.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})))
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

// errRestart is returned by serve after a restart was requested from the UI.
var errRestart = errors.New("restart requested")

// restartSelf starts a fresh copy of this binary with the same arguments (standalone), or
// just exits in a container, where the orchestrator's restart policy starts it again.
func restartSelf(container bool, args []string) error {
	if container {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	cmd := exec.CommandContext(context.Background(), exe, append([]string{"serve"}, args...)...) // never cancelled: the child outlives us
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, os.Environ()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	return cmd.Process.Release()
}

func serve(ctx context.Context, cfg config, getenv func(string) string) error {
	log := newLogger(cfg.LogLevel)
	ctx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	var restarting atomic.Bool
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data folder: %w", err)
	}
	if cfg.MetricsToken == "" { // /metrics always needs a token (PLAN 7.1.14)
		f := filepath.Join(cfg.DataDir, metricsTokenFile)
		tok, created, err := ensureMetricsToken(f)
		if err != nil {
			return err
		}
		if created {
			log.Info("generated the /metrics bearer token (CACTUSTRADE_METRICS_TOKEN unset)", "file", f)
		}
		cfg.MetricsToken = tok
	}
	if cfg.DBDriver == sqlstore.DriverSQLite {
		// A restore never replaces the database under a running server (PLAN 7.6a).
		lk, err := lockServe(cfg.DataDir, time.Now)
		if err != nil {
			return err
		}
		defer lk.release()
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

	clock := system.Clock{}
	secrets, envCreds, err := openSecrets(ctx, cfg, store, getenv, clock, log)
	if err != nil {
		return err
	}
	for n := range envCreds {
		log.Info("credential provided by the environment", "credential", string(n))
	}

	cal, err := calendar.New()
	if err != nil {
		return err
	}
	// The eToro client asks the service for the mode on every call (demo unless the gated
	// switch set real); svc is assigned just below, before the server takes any request.
	var svc *app.Service
	// Metrics (PLAN 7.5): the HTTP adapter owns every Prometheus series; the eToro client
	// reports its requests and limiter waits through plain hooks.
	reg := prometheus.NewRegistry()
	obs := httpapi.NewObservers(reg)
	broker, err := etoro.New(etoro.Config{
		HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Random: system.Random{}, Log: log,
		Observe: func(op etoro.Op, mode domain.TradingMode, class string) {
			obs.EtoroRequest(string(op), mode, class)
		},
		ObserveWait: func(q etoro.QuotaGroup, wait time.Duration) { obs.LimiterWait(string(q), wait) },
	})
	if err != nil {
		return err
	}
	// Prices: the WebSocket stream when fresh (≤ 30 s), REST rates otherwise.
	stream := etoro.NewStream(broker, func(ctx context.Context) (domain.Account, bool) {
		return streamAccount(ctx, store, secrets)
	})
	feed := etoro.NewFeed(broker, stream, 30*time.Second)
	// Trading 212: read-only (account summary and positions), shown in the overview only.
	t212, err := trading212.New(trading212.Config{HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Log: log})
	if err != nil {
		return err
	}
	// Saxo and Tiger Brokers (R21): read-only (balance/assets and positions), overview only.
	// Saxo also signs in through the browser (OAuth code flow; needs CACTUSTRADE_PUBLIC_URL).
	saxoClient, err := saxo.New(saxo.Config{HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Log: log})
	if err != nil {
		return err
	}
	tigerClient, err := tiger.New(tiger.Config{HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Log: log})
	if err != nil {
		return err
	}
	fallback := &mailFallback{}
	if err := fallback.load(getenv); err != nil {
		return err
	}
	host, _ := os.Hostname()
	reviewer := llm.New(&http.Client{Timeout: 10 * time.Minute})
	// The live bus (PLAN 6.10): stored events, order changes, agent states and portfolio
	// reads for the dashboard streams. A view only: nothing decides on it, and a full stream
	// misses events instead of holding up an order.
	bus := eventbus.New()
	svc, err = app.New(app.Deps{
		Store: store, Secrets: secrets, Hasher: argon2.NewLimited(argon2.New(argon2.Default), argon2.DefaultSlots, argon2.DefaultWait),
		Random: system.Random{}, Clock: clock, Log: log,
		Broker: broker, Market: feed, Calendar: cal,
		Holdings: map[domain.Platform]ports.Holdings{
			domain.PlatformTrading212: t212, domain.PlatformSaxo: saxoClient, domain.PlatformTiger: tigerClient,
		},
		Connectors: map[domain.Platform]ports.OAuthConnector{domain.PlatformSaxo: saxoClient},
		LLM:        reviewer,
		Research:   broker,
		Watchlists: broker,
		// Web research (PLAN R29): the same LLM client, with its provider's web search as
		// its only tool; it returns data only and has no broker.
		Researcher:   reviewer,
		Mailer:       mail.New(time.Now, host),
		Statements:   pdf.New(),
		Analysis:     pdf.New(), // R31: the analysis page as a one-page PDF
		Bus:          bus,
		MailFallback: fallback.get,
		Reload: func(ctx context.Context) error {
			if err := fallback.load(getenv); err != nil {
				return err
			}
			vals, err := secretstore.FromEnvironment(getenv, os.ReadFile)
			if err != nil {
				return err
			}
			secrets.Replace(vals)
			if _, err := svc.EnsureEnvAccount(ctx); err != nil {
				return err
			}
			log.Info("settings reloaded", "credentials_from_environment", len(vals))
			return nil
		},
		Restart: func() {
			restarting.Store(true)
			go func() { // let the HTTP answer reach the browser first
				time.Sleep(500 * time.Millisecond)
				cancelServe()
			}()
		},
	}, app.Config{Version: version, Build: build, PublicURL: cfg.PublicURL, LeaseHolder: leaseHolderID(host, system.Random{})})
	if err != nil {
		return err
	}
	for _, m := range domain.Markets {
		if y := time.Now().Year(); !slices.Contains(cal.Years(m), y) {
			log.Warn("the market calendar has no holiday table for this year: the market is treated as closed", "market", string(m), "year", y)
		}
	}
	if created, err := svc.EnsureDefaultAdmin(ctx); err != nil {
		return err
	} else if created {
		log.Warn("first run: created the administrator admin/admin; sign in and change its password (required before anything else)")
	}
	if migrated, err := svc.MigrateLLMSetup(ctx); err != nil {
		return err
	} else if migrated {
		log.Info("moved the LLM reviewer setup into its first profile")
	}
	if created, err := svc.EnsureEnvAccount(ctx); err != nil {
		return err
	} else if created {
		log.Info("created the eToro account \"Demo (environment)\" from the environment's demo keys")
	}

	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "cactustrade_live_bus_dropped_total",
			Help: "Live events not delivered to a stream that was behind (the stream was told to resync).",
		}, func() float64 { return float64(bus.Dropped()) }))
	handler := httpapi.New(svc, log, httpapi.Config{
		CookieSecure: cfg.CookieSecure, TrustedProxies: cfg.TrustedProxies, MetricsToken: cfg.MetricsToken,
		Web: web.FS(), OpenAPI: api.OpenAPI,
	}, reg)

	srv := newHTTPServer(cfg, handler, log)
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
	go handler.PurgeLoop(ctx, 10*time.Minute)
	obs.CountOrders(ctx, svc) // subscribed before trading starts
	if ids, err := store.Watchlist().Watched(ctx); err == nil {
		stream.Watch(ids...)
	}
	go stream.Run(ctx)
	go scannerLoop(ctx, svc, cal, app.ScanTick) // each account by its autonomy (R28)
	go equityLoop(ctx, svc, time.Minute, 15*time.Minute)
	go researchLoop(ctx, svc, 5*time.Minute)
	go mailLoop(ctx, svc, log, 15*time.Second)
	go statementsLoop(ctx, svc, log, 2*time.Minute, 5*time.Minute) // R30
	// Trading (PLAN 4.14): start-up reconciliation → OD5 resume → supervisor → agent, and the
	// read-only trade-history loop (6.10). The web server is up meanwhile, so the kill switch
	// works from the first second.
	stopTrading := startTrading(ctx, svc, log, prodSchedule())

	errc := make(chan error, 2)
	var redirect *http.Server
	if srv.TLSConfig != nil {
		// One port, both schemes: plain HTTP on the HTTPS port is redirected to HTTPS.
		d := newDemux(ln, 10*time.Second)
		redirect = &http.Server{
			Handler: redirectHandler(), ReadHeaderTimeout: 10 * time.Second,
			MaxHeaderBytes: 16 << 10, ErrorLog: srv.ErrorLog,
		}
		go func() { errc <- srv.ServeTLS(d.tls, "", "") }()
		go func() { errc <- redirect.Serve(d.plain) }()
	} else {
		go func() { errc <- srv.Serve(ln) }()
	}
	log.Info("cactusTrade started", "version", version, "build", build, "addr", cfg.Addr, "scheme", scheme,
		"database", store.Driver(), "container", cfg.Container)
	var serveErr error
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if redirect != nil {
		_ = redirect.Shutdown(sctx)
	}
	shutErr := srv.Shutdown(sctx) // requests in flight (a kill) finish first
	// Then the loops stop and the leases are released; positions stay as they are (OD6).
	stopTrading()
	if serveErr != nil {
		return serveErr
	}
	if shutErr != nil {
		return shutErr
	}
	if restarting.Load() {
		log.Info("restarting")
		return errRestart
	}
	return nil
}

// scannerLoop runs the scanner for every account that has it on, every interval while any
// supported market (LSE, NYSE/NASDAQ, ASX) is in session; each share is reviewed only while
// its own market is open (read-only on eToro: it reviews stocks and fills watchlists).
func scannerLoop(ctx context.Context, svc *app.Service, cal *calendar.Calendar, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			if slices.ContainsFunc(domain.Markets, func(m domain.Market) bool { return cal.IsOpen(m, now) }) {
				svc.ScanAll(ctx)
			}
		}
	}
}

// researchLoop asks every interval whether an account's daily web research is due (the hour
// before a market opens, once a day; PLAN R29). It never trades: research only proposes or
// adds watchlist shares through eToro's eligibility check.
func researchLoop(ctx context.Context, svc *app.Service, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			svc.ResearchAll(ctx)
		}
	}
}

// equityLoop records every account's equity for the overview chart: once first after
// (giving the price stream and the start-up a moment), then every interval.
func equityLoop(ctx context.Context, svc *app.Service, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			svc.RecordEquity(ctx)
			t.Reset(every)
		}
	}
}

// streamAccount picks the account whose keys the price stream uses: a demo account with
// both keys set (the environment account first), else any account with keys.
func streamAccount(ctx context.Context, store *sqlstore.Store, secrets ports.SecretStore) (domain.Account, bool) {
	accts, err := store.Accounts().List(ctx)
	if err != nil {
		return domain.Account{}, false
	}
	slices.SortStableFunc(accts, func(a, b domain.Account) int {
		rank := func(x domain.Account) int {
			switch {
			case x.ID == domain.EnvAccountID:
				return 0
			case x.Mode != domain.ModeReal:
				return 1
			}
			return 2
		}
		return rank(a) - rank(b)
	})
	for _, a := range accts {
		if !a.Tradable() { // Trading 212 keys are not eToro keys
			continue
		}
		ak, uk := domain.AccountKeyNames(a)
		if v, err := secrets.Get(ctx, ak); err != nil || v == "" {
			continue
		}
		if v, err := secrets.Get(ctx, uk); err != nil || v == "" {
			continue
		}
		return a, true
	}
	return domain.Account{}, false
}

// openSecrets returns the credential store, overlaid by credentials from the environment
// (ETORO_DEMO_PUB / _PK…): encrypted in the database under the master key (default), or in
// Vault (CACTUSTRADE_SECRETS=vault, PLAN 7.8).
func openSecrets(ctx context.Context, cfg config, store *sqlstore.Store, getenv func(string) string, clock system.Clock, log *slog.Logger) (*secretstore.Env, map[domain.CredentialName]string, error) {
	envCreds, err := secretstore.FromEnvironment(getenv, os.ReadFile)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Secrets == secretsVault {
		vs, err := openVault(ctx, cfg, clock, log)
		if err != nil {
			return nil, nil, err
		}
		return secretstore.WithEnv(vs, envCreds), envCreds, nil
	}
	cipher, err := openCipher(cfg, log)
	if err != nil {
		return nil, nil, err
	}
	return secretstore.WithEnv(secretstore.NewDB(store.Secrets(), cipher, clock), envCreds), envCreds, nil
}

// openVault builds the Vault store and signs in once. Vault not answering does not stop the
// start (the kill switch and the web UI must work); until it answers no credential can be
// read, so nothing trades (fail closed). A wrong setting is an error.
func openVault(ctx context.Context, cfg config, clock system.Clock, log *slog.Logger) (*vault.Store, error) {
	vc := cfg.Vault
	vc.Now, vc.Log = clock.Now, log
	vs, err := vault.New(vc)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := vs.Check(cctx); err != nil {
		log.Warn("Vault is not usable yet: no credential can be read (nothing trades) until it is", "addr", vc.Addr, "err", err)
	} else {
		log.Info("credentials are kept in Vault", "addr", vc.Addr)
	}
	return vs, nil
}

// openCipher loads the master key: explicit (containers) or, standalone, master.key in the
// data folder, generated there on first start.
func openCipher(cfg config, log *slog.Logger) (*secretstore.Cipher, error) {
	gen := ""
	if !cfg.Container {
		gen = filepath.Join(cfg.DataDir, "master.key")
	}
	key, created, err := secretstore.LoadMasterKey(cfg.MasterKey, cfg.MasterKeyFile, gen)
	if err != nil {
		return nil, err
	}
	if created {
		log.Warn("generated a new master key: back it up; without it the stored credentials cannot be read", "file", gen)
	}
	return secretstore.NewCipher(key)
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
