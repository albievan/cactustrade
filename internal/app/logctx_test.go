package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// acctHandler is the test twin of adapters/logctx (the app may not import adapters): it adds
// ports.AccountLogAttrs to every record, unless the call set the key itself.
type acctHandler struct{ slog.Handler }

func (h acctHandler) Handle(ctx context.Context, r slog.Record) error {
	have := map[string]bool{}
	r.Attrs(func(a slog.Attr) bool { have[a.Key] = true; return true })
	r = r.Clone()
	for _, a := range ports.AccountLogAttrs(ctx) {
		if !have[a.Key] {
			r.AddAttrs(a)
		}
	}
	return h.Handler.Handle(ctx, r)
}

// logLines captures the service's log as parsed JSON lines.
type logLines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func captureLog(s *Service) *logLines {
	l := &logLines{}
	s.d.Log = slog.New(acctHandler{slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})})
	return l
}

func (l *logLines) lines(t *testing.T) (raw string, out []map[string]any) {
	t.Helper()
	l.mu.Lock()
	raw = l.buf.String()
	l.mu.Unlock()
	for _, s := range strings.Split(strings.TrimSpace(raw), "\n") {
		if s == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		out = append(out, m)
	}
	return raw, out
}

// requireAccountLines checks that msg was logged naming account and mode, that every line
// naming an account also names its mode, and that no key, token or cookie was logged.
func (l *logLines) requireAccountLines(t *testing.T, msg, account string, mode domain.TradingMode) {
	t.Helper()
	raw, ls := l.lines(t)
	found := false
	for _, m := range ls {
		if id, ok := m["account_id"]; ok {
			if m["mode"] != "demo" && m["mode"] != "real" {
				t.Errorf("line without a mode: %v", m)
			}
			if m["msg"] == msg && id == account && m["mode"] == string(mode) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no %q line for %s (%s) in:\n%s", msg, account, mode, raw)
	}
	for _, secret := range []string{"test-key-", "Bearer", "x-api-key", "x-user-key", "cactustrade_session", "csrf"} {
		if strings.Contains(raw, secret) {
			t.Errorf("the log holds %q:\n%s", secret, raw)
		}
	}
}

// TestLogLinesNameAccountAndMode (PLAN 7.2): executor and supervisor lines carry the
// account id and mode=demo / mode=real.
func TestLogLinesNameAccountAndMode(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, x *execEnv) (msg, account string, mode domain.TradingMode)
	}{
		{"executor: demo order lines", func(t *testing.T, x *execEnv) (string, string, domain.TradingMode) {
			x.b.OpenScript = []fakes.SimOutcome{fakes.SimFill}
			if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
				t.Fatal(err)
			}
			return "order", x.a.ID, domain.ModeDemo
		}},
		{"executor: demo 429 line from the context", func(t *testing.T, x *execEnv) (string, string, domain.TradingMode) {
			x.b.OpenScript = []fakes.SimOutcome{fakes.SimRateLimitedProcessed}
			if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
				t.Fatal(err)
			}
			return "open order rate limited: not resent, following it by lookup", x.a.ID, domain.ModeDemo
		}},
		{"executor: real account refused", func(t *testing.T, x *execEnv) (string, string, domain.TradingMode) {
			real := x.account(t, "Real", domain.ModeReal)
			if _, err := x.svc.placeEntry(x.ctx, real, x.entry(domain.Long), domain.DefaultRiskLimits); !errors.Is(err, domain.ErrRealModeGated) {
				t.Fatalf("real entry: %v", err)
			}
			if x.b.Opens != 0 {
				t.Fatal("broker called")
			}
			return "real-money entry refused", real.ID, domain.ModeReal
		}},
		{"supervisor: demo pass skipped", func(t *testing.T, x *execEnv) (string, string, domain.TradingMode) {
			x.svc.d.Broker = nil
			x.supervise(t)
			return "supervisor pass skipped", x.a.ID, domain.ModeDemo
		}},
		{"supervisor: demo flatten", func(t *testing.T, x *execEnv) (string, string, domain.TradingMode) {
			x.b.OpenScript = []fakes.SimOutcome{fakes.SimFill}
			if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
				t.Fatal(err)
			}
			x.clock.Advance(6 * time.Hour) // past NYSE close − 10 min
			x.supervise(t)
			if x.b.Closes != 1 {
				t.Fatalf("closes = %d", x.b.Closes)
			}
			return "order", x.a.ID, domain.ModeDemo
		}},
		{"supervisor: real pass skipped (not armed, nothing of ours)", func(t *testing.T, x *execEnv) (string, string, domain.TradingMode) {
			real := x.account(t, "Real", domain.ModeReal)
			if rep, err := x.svc.Supervise(x.ctx, real.ID); err != nil || rep.Skipped == "" {
				t.Fatalf("real: %+v %v", rep, err)
			}
			return "supervisor pass skipped", real.ID, domain.ModeReal
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
			l := captureLog(x.svc)
			msg, account, mode := tc.run(t, x)
			l.requireAccountLines(t, msg, account, mode)
		})
	}
}

// TestNoteAccountNamesTheRequest: resolving an account notes it for the request's log line
// (PLAN 7.2) and never puts it where an adapter would act on it.
func TestNoteAccountNamesTheRequest(t *testing.T) {
	x := newExecEnv(t)
	ctx := ports.WithAccountNote(context.Background())
	if _, err := x.svc.CurrentAccount(ctx, x.admin); err != nil {
		t.Fatal(err)
	}
	if _, ok := ports.AccountFrom(ctx); ok {
		t.Fatal("a note must not act as WithAccount")
	}
	as := ports.AccountLogAttrs(ctx)
	if len(as) != 2 || as[0].Value.String() != x.a.ID || as[1].Value.String() != "demo" {
		t.Fatalf("attrs = %v", as)
	}
}
