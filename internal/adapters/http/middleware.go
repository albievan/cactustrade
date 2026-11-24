package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// reqState is shared by the middlewares of one request through a pointer in the context,
// so inner layers can report back (principal, route) without copying the request.
type reqState struct {
	principal app.Principal
	csrf      string
	pattern   string
	ip        string
}

type stateKey struct{}

// withState gives the request its reqState and a place for the app to note the account the
// request is about (ports.NoteAccount): the request's log lines name it and its mode (PLAN 7.2).
func withState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := ports.WithAccountNote(context.WithValue(r.Context(), stateKey{}, &reqState{}))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func state(r *http.Request) *reqState {
	if st, ok := r.Context().Value(stateKey{}).(*reqState); ok {
		return st
	}
	return &reqState{}
}

// recordPattern runs inside the generated routes and notes the matched route for metrics.
func recordPattern(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state(r).pattern = r.Pattern
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.log.ErrorContext(r.Context(), "panic", "value", v, "stack", string(debug.Stack()))
				writeProblem(w, r, http.StatusInternalServerError, "Internal error", "", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	once   sync.Once
}

func (w *statusWriter) WriteHeader(code int) {
	w.once.Do(func() { w.status = code })
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { w.status = http.StatusOK })
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type metrics struct {
	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	liveStreams prometheus.Gauge
	rateLimited *prometheus.CounterVec
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cactustrade_http_requests_total", Help: "HTTP requests by route and status.",
		}, []string{"method", "route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "cactustrade_http_request_duration_seconds", Help: "HTTP request latency by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		liveStreams: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cactustrade_live_streams", Help: "Open live streams (GET /api/v1/live).",
		}),
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cactustrade_http_rate_limited_total", Help: "API requests refused by the rate limits (PLAN 7.3), by class.",
		}, []string{"class"}),
	}
	reg.MustRegister(m.requests, m.duration, m.liveStreams, m.rateLimited)
	return m
}

// observe logs every request (never bodies, cookies or tokens) and records metrics.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		st := state(r)
		st.ip = s.clientIP(r)
		next.ServeHTTP(sw, r)
		route := st.pattern
		if route == "" {
			route = "other"
		}
		d := time.Since(start)
		s.metrics.requests.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		if route != liveRoute { // a stream lasts up to 30 minutes: not a latency
			s.metrics.duration.WithLabelValues(r.Method, route).Observe(d.Seconds())
		}
		if route == "GET /healthz" || route == "GET /readyz" {
			return
		}
		attrs := []any{
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", d.Milliseconds(), "ip", st.ip,
		}
		if st.principal.UserID != "" {
			attrs = append(attrs, "user", st.principal.Username, "via", string(st.principal.Via))
		}
		s.log.InfoContext(r.Context(), "http", attrs...)
	})
}

// appCSP is the Content-Security-Policy of every response but the Swagger UI page: no
// 'unsafe-inline' anywhere (PLAN 7.4).
const appCSP = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// swaggerCSP is the Swagger UI page's policy: Swagger UI sets inline styles, so style-src
// allows 'unsafe-inline' there (never script-src).
const swaggerCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// permissionsPolicy turns off the browser features cactusTrade never uses.
const permissionsPolicy = "accelerometer=(), camera=(), display-capture=(), geolocation=(), gyroscope=(), hid=(), magnetometer=(), microphone=(), midi=(), payment=(), publickey-credentials-get=(), serial=(), usb=(), browsing-topics=()"

// SecurityHeaders sets the security headers on every response (the app, the API, the
// Swagger UI, the live stream, CSV exports, /metrics and cmd's HTTP→HTTPS redirect).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Content-Security-Policy", appCSP)
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate resolves the caller from an API key (Authorization: Bearer) or the session
// cookie. It never rejects: anonymous requests reach the handlers, and the use cases refuse
// what needs a principal. A bad bearer token is a 401 at once (no silent fallback).
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := state(r)
		if authz := r.Header.Get("Authorization"); authz != "" {
			bearer, ok := strings.CutPrefix(authz, "Bearer ")
			p, err := s.svc.APIKeyPrincipal(r.Context(), strings.TrimSpace(bearer), st.ip)
			if !ok || err != nil {
				if err != nil && !errors.Is(err, domain.ErrUnauthenticated) {
					s.fail(w, r, err)
					return
				}
				if r.URL.Path != "/metrics" { // /metrics checks its own token
					writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "invalid API key", "")
					return
				}
			}
			st.principal = p
		} else if c, err := r.Cookie(s.cookieName()); err == nil && c.Value != "" {
			p, csrf, err := s.svc.SessionPrincipal(r.Context(), c.Value, st.ip)
			switch {
			case err == nil:
				st.principal, st.csrf = p, csrf
			case errors.Is(err, domain.ErrUnauthenticated):
				s.clearCookie(w)
			default:
				s.fail(w, r, err)
				return
			}
		}
		if s.cfg.CookieSecure {
			if c, err := r.Cookie(InsecureSessionCookie); err == nil && c.Value != "" {
				// A session cookie from before PLAN 7.4 (no __Host- prefix): no longer
				// accepted; the browser is told to drop it.
				clearCookieNamed(w, InsecureSessionCookie, true)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf protects cookie-authenticated state changes: the request must carry the session's
// token in X-CSRF-Token, and a browser Origin header (when sent) must be this host.
// API-key requests are not cookie-based and need neither.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				writeProblem(w, r, http.StatusForbidden, "Forbidden", "cross-origin request refused", "")
				return
			}
		}
		st := state(r)
		if st.principal.Via == app.ViaSession {
			got := r.Header.Get("X-CSRF-Token")
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(st.csrf)) != 1 {
				writeProblem(w, r, http.StatusForbidden, "Forbidden", "missing or wrong X-CSRF-Token", "")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
