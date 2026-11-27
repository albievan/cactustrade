package etoro

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// realFixtures answers the real paths of the mutating operations with the demo fixtures (the
// answers' shape is the same; the paths differ).
var realFixtures = map[string]string{
	"/api/v2/trading/execution/orders":                             "open.json",
	"/api/v1/trading/execution/market-close-orders/positions/1001": "close.json",
}

// realEnv is a client with a real-money account that has its own keys, the gate armed at
// envNow, and the permits a test may pick.
type realEnv struct {
	*env
	acct  domain.Account
	gate  domain.RealGate
	entry domain.RealPermit
	exit  domain.RealPermit
}

var envNow = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC) // newEnv's clock

func newRealEnv(t *testing.T) *realEnv {
	t.Helper()
	e := newEnv(t)
	e.mode.Store(domain.ModeReal)
	_ = e.sec.Put(context.Background(), "etoro.real-1.api_key", "real-api")
	_ = e.sec.Put(context.Background(), "etoro.real-1.user_key", "real-user")
	e.fake.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/trading/execution/orders/") {
			return 200, `{"token":"ad74d0fd-ca92-4974-a932-9668d29ec366"}`, nil
		}
		name, ok := realFixtures[r.URL.Path]
		if !ok {
			return 404, `{"errorCode":"NotFound","errorMessage":"no route"}`, nil
		}
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Errorf("fixture %s: %v", name, err)
		}
		return 200, string(data), nil
	}
	re := &realEnv{env: e, acct: e.account()}
	re.gate = domain.LockedGate(re.acct.ID).Arm("demo-1", "u-admin", domain.GateEvidence{}, envNow.Add(-time.Hour))
	var err error
	if re.entry, err = domain.PermitEntry(re.gate, re.acct, envNow, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if re.exit, err = domain.PermitExit(re.acct, true); err != nil {
		t.Fatal(err)
	}
	return re
}

func (re *realEnv) with(p domain.RealPermit) context.Context { return ports.WithPermit(re.ctx(), p) }

// TestRealMutationsWithPermit (PLAN 7.20, S3): with a permit for the account and the operation
// the client sends a real-money account change exactly as on demo — the same two order shapes
// at leverage 1, byte for byte — to the allowlist's real path, with the account's own keys.
func TestRealMutationsWithPermit(t *testing.T) {
	demoEnv := newEnv(t)
	for _, in := range []domain.OrderIntent{longIntent(), shortIntent()} {
		if _, err := demoEnv.c.Open(demoEnv.ctx(), in); err != nil {
			t.Fatal(err)
		}
	}
	re := newRealEnv(t)
	for i, in := range []domain.OrderIntent{longIntent(), shortIntent()} {
		st, err := re.c.Open(re.with(re.entry), in)
		if err != nil || st.OrderID != 700001 || st.State != domain.OrderPending {
			t.Fatalf("real open %s = %+v %v", in.Direction, st, err)
		}
		r, body := re.fake.reqs[i], re.fake.bodies[i]
		if r.Method != Allowlist[OpOpenOrder].Method || r.URL.Path != Allowlist[OpOpenOrder].Real {
			t.Fatalf("real open went to %s %s", r.Method, r.URL.Path)
		}
		if body != demoEnv.fake.bodies[i] {
			t.Fatalf("the real body differs from the demo one:\nreal %s\ndemo %s", body, demoEnv.fake.bodies[i])
		}
		if r.Header.Get("x-api-key") != "real-api" || r.Header.Get("x-user-key") != "real-user" || r.Header.Get("x-request-id") != in.RequestID {
			t.Fatalf("headers %v", r.Header)
		}
	}
	// Close and cancel accept an exit permit, and an entry permit.
	for _, p := range []domain.RealPermit{re.exit, re.entry} {
		n := len(re.fake.reqs)
		if _, err := re.c.Close(re.with(p), 1001, 1001, testRequestID); err != nil {
			t.Fatalf("real close: %v", err)
		}
		if err := re.c.Cancel(re.with(p), 700001, testRequestID); err != nil {
			t.Fatalf("real cancel: %v", err)
		}
		cl, ca := re.fake.reqs[n], re.fake.reqs[n+1]
		if cl.URL.Path != strings.ReplaceAll(Allowlist[OpClosePosition].Real, "{positionId}", "1001") || re.fake.bodies[n] != `{"InstrumentID":1001}` ||
			ca.Method != http.MethodDelete || ca.URL.Path != strings.ReplaceAll(Allowlist[OpCancelOrder].Real, "{orderId}", "700001") {
			t.Fatalf("real close %s, cancel %s %s", cl.URL.Path, ca.Method, ca.URL.Path)
		}
		if cl.Header.Get("x-api-key") != "real-api" || ca.Header.Get("x-user-key") != "real-user" {
			t.Fatal("a real exit used other keys")
		}
	}
}

// TestRealMutationsRefusedWithoutPermit (PLAN 7.20, S3): a real-money account change without a
// permit valid for the account and the operation sends nothing: no permit, another account's,
// an expired entry permit, an exit permit for an open, the deployment's demo keys on a real
// account, and set_sl_tp (never executable) even with an entry permit.
func TestRealMutationsRefusedWithoutPermit(t *testing.T) {
	re := newRealEnv(t)
	other := domain.Account{ID: "real-2", Name: "Other", Mode: domain.ModeReal}
	og := re.gate
	og.AccountID = other.ID
	otherEntry, err := domain.PermitEntry(og, other, envNow, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	otherExit, err := domain.PermitExit(other, true)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := domain.PermitEntry(re.gate, re.acct, envNow.Add(-domain.RealPermitTTL-time.Second), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context) error { _, err := re.c.Open(ctx, longIntent()); return err }
	closePos := func(ctx context.Context) error { _, err := re.c.Close(ctx, 1001, 1001, testRequestID); return err }
	cancel := func(ctx context.Context) error { return re.c.Cancel(ctx, 700001, testRequestID) }
	envKeys := ports.WithPermit(ports.WithAccount(context.Background(), domain.Account{ID: "real-1", Name: "Real", Mode: domain.ModeReal, EnvKeys: true}), re.entry)
	tests := []struct {
		name string
		ctx  context.Context
		do   func(context.Context) error
		is   error
	}{
		{"open, no permit", re.ctx(), open, domain.ErrRealModeGated},
		{"close, no permit", re.ctx(), closePos, domain.ErrRealModeGated},
		{"cancel, no permit", re.ctx(), cancel, domain.ErrRealModeGated},
		{"open, zero permit", re.with(domain.RealPermit{}), open, domain.ErrRealModeGated},
		{"open, another account's entry permit", re.with(otherEntry), open, domain.ErrRealModeGated},
		{"close, another account's exit permit", re.with(otherExit), closePos, domain.ErrRealModeGated},
		{"cancel, another account's entry permit", re.with(otherEntry), cancel, domain.ErrRealModeGated},
		{"open, expired entry permit", re.with(stale), open, domain.ErrRealModeGated},
		{"open, exit permit only", re.with(re.exit), open, domain.ErrRealModeGated},
		{"open with the deployment's demo keys", envKeys, open, domain.ErrRealModeGated},
		{"close with the deployment's demo keys", envKeys, closePos, domain.ErrRealModeGated},
		{"set_sl_tp, entry permit", re.with(re.entry), func(ctx context.Context) error {
			return re.c.mutate(ctx, OpSetStopLossTake, map[string]string{"positionId": "1"}, testRequestID, nil, nil)
		}, ErrNotEnabled},
	}
	for _, tt := range tests {
		if err := tt.do(tt.ctx); !errors.Is(err, tt.is) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.is)
		}
	}
	if n := len(re.fake.requests()); n != 0 {
		t.Fatalf("%d requests reached eToro without a valid permit", n)
	}
	// The permit is checked at the moment of the send: one that was valid expires.
	_ = re.c.cfg.Sleep(context.Background(), domain.RealPermitTTL+time.Second) // moves the client's clock
	if err := open(re.with(re.entry)); !errors.Is(err, domain.ErrRealModeGated) || len(re.fake.requests()) != 0 {
		t.Fatalf("an entry permit older than its TTL: %v", err)
	}
	if err := closePos(re.with(re.exit)); err != nil {
		t.Fatalf("an exit permit does not expire: %v", err)
	}
	// A demo account ignores any permit.
	demo := newEnv(t)
	if _, err := demo.c.Open(ports.WithPermit(demo.ctx(), otherEntry), longIntent()); err != nil {
		t.Fatalf("demo with a stray permit: %v", err)
	}
}
