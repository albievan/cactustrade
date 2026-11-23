package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// mailFallback is the deployment's mail server (CACTUSTRADE_SMTP_HOST, _PORT, _TLS, _USER,
// _FROM and the password in CACTUSTRADE_SMTP_PASSWORD / _FILE), used while no server is
// switched on in the app. It is re-read on "reload settings".
type mailFallback struct {
	mu       sync.RWMutex
	server   domain.SMTPSettings
	password string
	ok       bool
}

func (m *mailFallback) get() (domain.SMTPSettings, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.server, m.password, m.ok
}

// load reads the variables; an invalid set is an error (and the old values stay).
func (m *mailFallback) load(getenv func(string) string) error {
	host := strings.TrimSpace(getenv("CACTUSTRADE_SMTP_HOST"))
	if host == "" {
		m.mu.Lock()
		m.server, m.password, m.ok = domain.SMTPSettings{}, "", false
		m.mu.Unlock()
		return nil
	}
	port := 587
	if v := getenv("CACTUSTRADE_SMTP_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("CACTUSTRADE_SMTP_PORT: %q is not a number", v)
		}
		port = p
	}
	pw := getenv("CACTUSTRADE_SMTP_PASSWORD")
	if f := getenv("CACTUSTRADE_SMTP_PASSWORD_FILE"); pw == "" && f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("CACTUSTRADE_SMTP_PASSWORD_FILE: %w", err)
		}
		pw = strings.TrimSpace(string(b))
	}
	st := domain.SMTPSettings{
		Enabled: true, Host: host, Port: port, Username: getenv("CACTUSTRADE_SMTP_USER"),
		From: getenv("CACTUSTRADE_SMTP_FROM"), TLS: getenv("CACTUSTRADE_SMTP_TLS"),
	}.Normalize()
	if st.From == "" {
		st.From = "cactusTrade <cactustrade@" + host + ">"
	}
	if err := st.Validate(); err != nil {
		return fmt.Errorf("CACTUSTRADE_SMTP_*: %w", err)
	}
	m.mu.Lock()
	m.server, m.password, m.ok = st, pw, true
	m.mu.Unlock()
	return nil
}

// mailLoop sends the outbox's due mail every interval.
func mailLoop(ctx context.Context, svc *app.Service, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := svc.DeliverMail(ctx); err != nil && ctx.Err() == nil {
				log.WarnContext(ctx, "mail delivery failed", "err", err)
			} else if n > 0 {
				log.InfoContext(ctx, "mail sent", "count", n)
			}
		}
	}
}
