package main

import (
	"log/slog"
	"net/http"
	"time"

	httpapi "github.com/albievan/cactustrade/internal/adapters/http"
)

// httpTimeouts are the web server's time limits. The live stream (GET /api/v1/live) outlives
// ReadTimeout and WriteTimeout by moving its own deadlines (http.ResponseController); every
// other request stays bounded by them.
type httpTimeouts struct {
	ReadHeader time.Duration
	Read       time.Duration
	Write      time.Duration
	Idle       time.Duration
}

// prodTimeouts are the production limits.
func prodTimeouts() httpTimeouts {
	return httpTimeouts{ReadHeader: 10 * time.Second, Read: 30 * time.Second, Write: 60 * time.Second, Idle: 120 * time.Second}
}

// maxHeaderBytes bounds a request's headers.
const maxHeaderBytes = 64 << 10

// newHTTPServer is the production web server for handler on cfg.Addr (TLS is set up by the
// caller). Tests build it through newHTTPServerWith with scaled timeouts, so they exercise
// the same settings.
func newHTTPServer(cfg config, handler *httpapi.Server, log *slog.Logger) *http.Server {
	return newHTTPServerWith(cfg.Addr, handler, prodTimeouts(), log)
}

// newHTTPServerWith builds the web server with the given timeouts. Shutdown ends the open
// live streams first (each says bye), so it only waits for the other requests in flight,
// a kill switch among them.
func newHTTPServerWith(addr string, handler *httpapi.Server, t httpTimeouts, log *slog.Logger) *http.Server {
	srv := &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: t.ReadHeader, ReadTimeout: t.Read, WriteTimeout: t.Write, IdleTimeout: t.Idle,
		MaxHeaderBytes: maxHeaderBytes,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(handler.CloseStreams)
	return srv
}
