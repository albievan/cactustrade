// Package httpapi is the inbound HTTP adapter: the generated OpenAPI server (api.gen.go),
// authentication (session cookie or API key), CSRF, login rate limits, security headers,
// problem+json errors, health, metrics, Swagger UI and the embedded web app.
package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/albievan/cactustrade/internal/app"
)

// SessionCookie is the session cookie's name (OD32, PLAN 7.4): the __Host- prefix makes the
// browser accept it only with Secure, Path=/ and no Domain, so no sibling host or plain-HTTP
// page can set or shadow it. Changing the name signed everyone out once.
const SessionCookie = "__Host-cactustrade_session"

// InsecureSessionCookie is the session cookie's name when CookieSecure is off (a plain-HTTP
// LAN test): browsers refuse a __Host- cookie without Secure. It was also the name of every
// session cookie before PLAN 7.4; such a cookie is cleared when seen with CookieSecure on.
const InsecureSessionCookie = "cactustrade_session"

// cookieName is the session cookie's name under the current settings.
func (s *Server) cookieName() string {
	if s.cfg.CookieSecure {
		return SessionCookie
	}
	return InsecureSessionCookie
}

// Config holds the HTTP adapter's settings.
type Config struct {
	// CookieSecure sets the Secure flag on the session cookie (true unless testing on a
	// plain-HTTP LAN).
	CookieSecure bool
	// TrustedProxies may set X-Forwarded-For (the ingress / reverse proxy).
	TrustedProxies []netip.Prefix
	// MetricsToken must be sent as a bearer token to read /metrics. Empty = /metrics always
	// answers 401 (PLAN 7.1.14: cmd generates one into the data folder when none is set).
	MetricsToken string
	// Web is the built web app (nil = API only).
	Web fs.FS
	// OpenAPI is the contract served at /api/openapi.yaml.
	OpenAPI []byte
	// Live tunes the live stream (zero fields: PLAN 6.9's values).
	Live LiveLimits
	// RateLimits are the API rate limits (zero fields: OD33's values, PLAN 7.3).
	RateLimits RateLimits
}

// Server implements ServerInterface on the use cases. It is the HTTP handler (ServeHTTP)
// and owns the open live streams (CloseStreams).
type Server struct {
	svc     *app.Service
	log     *slog.Logger
	cfg     Config
	limiter *loginLimiter
	rates   *rateLimiter
	metrics *metrics
	streams *liveStreams
	handler http.Handler
}

var (
	_ ServerInterface = (*Server)(nil)
	_ http.Handler    = (*Server)(nil)
)

// New builds the HTTP handler. reg receives the HTTP metrics and the trading health
// collector, read at scrape time (PLAN 7.5; nil = a private registry).
// Register CloseStreams with the http.Server's RegisterOnShutdown so that open live streams
// do not hold up a shutdown.
func New(svc *app.Service, log *slog.Logger, cfg Config, reg *prometheus.Registry) *Server {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	cfg.Live = cfg.Live.withDefaults()
	s := &Server{svc: svc, log: log, cfg: cfg, limiter: newLoginLimiter(), rates: newRateLimiter(cfg.RateLimits), metrics: newMetrics(reg)}
	s.streams = newLiveStreams(cfg.Live, s.metrics.liveStreams)
	reg.MustRegister(newTradingCollector(svc))
	mux := http.NewServeMux()
	HandlerWithOptions(s, StdHTTPServerOptions{
		BaseRouter:       mux,
		Middlewares:      []MiddlewareFunc{s.rateLimit, recordPattern}, // the last is outermost
		ErrorHandlerFunc: s.paramError,
	})
	mux.Handle("GET /metrics", s.metricsHandler(reg))
	mux.Handle("GET /api/openapi.yaml", s.rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(cfg.OpenAPI)
	})))
	mux.Handle("/api/", s.rateLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "Not found", "no such endpoint", "")
	})))
	web := s.webHandler()
	mux.Handle("GET /api/docs", web) // Swagger UI page of the web bundle
	mux.Handle("/", web)
	s.handler = s.chain(mux)
	return s
}

// ServeHTTP serves a request through the middleware chain.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// chain wraps the mux: recover → state → log/metrics → security headers → body limit → auth
// → CSRF. The rate limits (PLAN 7.3) run per route inside the mux, where the route is known.
func (s *Server) chain(h http.Handler) http.Handler {
	h = s.csrf(h)
	h = s.authenticate(h)
	h = limitBody(h)
	h = SecurityHeaders(h)
	h = s.observe(h)
	h = withState(h)
	return s.recoverer(h)
}

func (s *Server) metricsHandler(reg *prometheus.Registry) http.Handler {
	inner := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always behind the token (PLAN 7.1.14); without one configured nothing matches.
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || s.cfg.MetricsToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.MetricsToken)) != 1 {
			writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "metrics need the metrics token", "")
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// webHandler serves the embedded single-page app; unknown paths get index.html so client
// routes work after a reload. /api/docs is the Swagger UI page of the same bundle.
func (s *Server) webHandler() http.Handler {
	if s.cfg.Web == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("cactusTrade API (built without the web app)\n"))
		})
	}
	files := http.FileServerFS(s.cfg.Web)
	serveFile := func(w http.ResponseWriter, r *http.Request, name string) {
		b, err := fs.ReadFile(s.cfg.Web, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeProblem(w, r, http.StatusMethodNotAllowed, "Method not allowed", "", "")
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "api/docs" {
			w.Header().Set("Content-Security-Policy", swaggerCSP)
			serveFile(w, r, "api-docs.html")
			return
		}
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(s.cfg.Web, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		serveFile(w, r, "index.html")
	})
}

// clientIP is the caller's address: the TCP peer, or the last untrusted hop of
// X-Forwarded-For when the peer is a trusted proxy.
func (s *Server) clientIP(r *http.Request) string {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	addr := ap.Addr().Unmap()
	if !s.trusted(addr) {
		return addr.String()
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !s.trusted(a) {
			return a.String()
		}
		addr = a
	}
	return addr.String()
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// PurgeLoop removes expired sessions, old events, mail and decisions, and stale sign-in
// limiter (PLAN 7.1.5) and API rate-limit (PLAN 7.3) keys until ctx ends.
func (s *Server) PurgeLoop(ctx context.Context, every time.Duration) {
	svc, log := s.svc, s.log
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := s.limiter.Sweep(); n > 0 {
				log.DebugContext(ctx, "swept the sign-in limiter", "keys", n)
			}
			if n := s.rates.Sweep(); n > 0 {
				log.DebugContext(ctx, "swept the API rate limiter", "keys", n)
			}
			if n, err := svc.PurgeExpiredSessions(ctx); err != nil {
				log.WarnContext(ctx, "purging sessions failed", "err", err)
			} else if n > 0 {
				log.InfoContext(ctx, "purged expired sessions", "count", n)
			}
			if n, err := svc.PurgeOldEvents(ctx); err != nil {
				log.WarnContext(ctx, "purging old events failed", "err", err)
			} else if n > 0 {
				log.InfoContext(ctx, "purged old events", "count", n)
			}
			if n, err := svc.PurgeOldMail(ctx); err != nil {
				log.WarnContext(ctx, "purging old mail failed", "err", err)
			} else if n > 0 {
				log.InfoContext(ctx, "purged old mail", "count", n)
			}
			if d, c, err := svc.PurgeDecisionLog(ctx); err != nil {
				log.WarnContext(ctx, "purging the decision log failed", "err", err)
			} else if d+c > 0 {
				log.InfoContext(ctx, "purged the decision log (OD12)", "decisions", d, "llm_calls", c)
			}
		}
	}
}
