package etoro

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// TestRealRefusalEtoroClient (PLAN 7.17, S3): before the owner's written consent (7.18) the
// eToro client sends nothing that changes a real-money account, whatever else is in place —
// an eToro real account with its own keys, the deployment opt-in variable set — for every
// operation of the allowlist that mutates, through the client's methods and through mutate
// itself. Reads are not part of this matrix (they stay allowed).
func TestRealRefusalEtoroClient(t *testing.T) {
	t.Setenv("CACTUSTRADE_REAL_TRADING", "on") // OD28's opt-in (7.23) is read by nothing here
	realAcct := domain.Account{ID: "real-1", Name: "Real", Mode: domain.ModeReal, Platform: domain.PlatformEtoro}
	e := newEnv(t)
	_ = e.sec.Put(context.Background(), "etoro.real-1.api_key", "real-api")
	_ = e.sec.Put(context.Background(), "etoro.real-1.user_key", "real-user")
	ctx := ports.WithAccount(context.Background(), realAcct)
	calls := map[string]func() error{
		"open long":  func() error { _, err := e.c.Open(ctx, longIntent()); return err },
		"open short": func() error { _, err := e.c.Open(ctx, shortIntent()); return err },
		"close":      func() error { _, err := e.c.Close(ctx, 1001, 1001, testRequestID); return err },
		"cancel":     func() error { return e.c.Cancel(ctx, 700001, testRequestID) },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, domain.ErrRealModeGated) {
			t.Errorf("%s: err = %v, want real mode gated", name, err)
		}
	}
	var mutating []Op
	for op, ep := range Allowlist {
		if ep.Mutates {
			mutating = append(mutating, op)
		}
	}
	slices.Sort(mutating)
	if len(mutating) == 0 {
		t.Fatal("no mutating operation in the allowlist")
	}
	for _, op := range mutating {
		err := e.c.mutate(ctx, op, map[string]string{"orderId": "1", "positionId": "1"}, testRequestID, nil, nil)
		if !errors.Is(err, domain.ErrRealModeGated) && !errors.Is(err, ErrNotEnabled) {
			t.Errorf("mutate %s: err = %v, want refused", op, err)
		}
	}
	if n := len(e.fake.requests()); n != 0 {
		t.Fatalf("%d requests reached eToro for a real-money account", n)
	}
}
