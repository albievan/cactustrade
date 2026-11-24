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

// SessionCookie is the name of the session cookie.
const SessionCookie = "cactustrade_session"

// Config holds the HTTP adapter's settings.
type Config struct {
	// CookieSecure sets the Secure flag on the session cookie (true unless testing on a
	// plain-HTTP LAN).
	CookieSecure bool
	// TrustedProxies may set X-Forwarded-For (the ingress / reverse proxy).
	TrustedProxies []netip.Prefix
	// MetricsToken, when set, must be sent as a bearer token to read /metrics.
	MetricsToken string
	// Web is the built web app (nil = API only).
	Web fs.FS
	// OpenAPI is the contract served at /api/openapi.yaml.
	OpenAPI []byte
}

// Server implements ServerInterface on the use cases.
type Server struct {
	svc     *app.Service
	log     *slog.Logger
	cfg     Config
	limiter *loginLimiter
	metrics *metrics
}

var _ ServerInterface = (*Server)(nil)

// New builds the HTTP handler. reg receives the HTTP metrics (nil = a private registry).
func New(svc *app.Service, log *slog.Logger, cfg Config, reg *prometheus.Registry) http.Handler {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	s := &Server{svc: svc, log: log, cfg: cfg, limiter: newLoginLimiter(), metrics: newMetrics(reg)}
	mux := http.NewServeMux()
	HandlerWithOptions(s, StdHTTPServerOptions{
		BaseRouter:       mux,
		Middlewares:      []MiddlewareFunc{recordPattern},
		ErrorHandlerFunc: s.paramError,
	})
	mux.Handle("GET /metrics", s.metricsHandler(reg))
	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(cfg.OpenAPI)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "Not found", "no such endpoint", "")
	})
	web := s.webHandler()
	mux.Handle("GET /api/docs", web) // Swagger UI page of the web bundle
	mux.Handle("/", web)
	return s.chain(mux)
}

// chain wraps the mux: recover → state → log/metrics → security headers → auth → CSRF.
func (s *Server) chain(h http.Handler) http.Handler {
	h = s.csrf(h)
	h = s.authenticate(h)
	h = securityHeaders(h)
	h = s.observe(h)
	h = withState(h)
	return s.recoverer(h)
}

func (s *Server) metricsHandler(reg *prometheus.Registry) http.Handler {
	inner := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.MetricsToken != "" {
			got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.MetricsToken)) != 1 {
				writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "metrics need the metrics token", "")
				return
			}
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

// PurgeLoop removes expired sessions and stale limiter entries until ctx ends.
func PurgeLoop(ctx context.Context, svc *app.Service, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
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
