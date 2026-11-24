package storetest

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// runDecisionLog covers the phase-5 repositories (migration 00010): the LLM call log and the
// decision log.
func runDecisionLog(t *testing.T, s ports.Store, ownerID string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	acct := domain.Account{ID: "acc-dec", Name: "Decisions", OwnerID: ownerID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
	other := domain.Account{ID: "acc-dec-2", Name: "Decisions 2", OwnerID: ownerID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
	for _, a := range []domain.Account{acct, other} {
		if err := s.Accounts().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("llm_calls", func(t *testing.T) { runLLMCalls(t, s, acct.ID, t0) })
	t.Run("decisions", func(t *testing.T) { runDecisions(t, s, acct.ID, other.ID, t0) })

	t.Run("decision_log_cascade", func(t *testing.T) {
		call := domain.LLMCall{ID: "call-keep", AccountID: acct.ID, Purpose: domain.LLMPurposeAgent, CostMicros: domain.CostUnknown, At: t0}
		if err := s.LLMCalls().Add(ctx, call); err != nil {
			t.Fatal(err)
		}
		if err := s.Accounts().Delete(ctx, acct.ID); err != nil {
			t.Fatal(err)
		}
		if l, err := s.Decisions().List(ctx, acct.ID, time.Time{}, "", 100); err != nil || len(l) != 0 {
			t.Fatalf("decisions survived their account: %d %v", len(l), err)
		}
		if l, _ := s.Decisions().List(ctx, other.ID, time.Time{}, "", 100); len(l) != 1 {
			t.Fatalf("another account's decisions were deleted: %+v", l)
		}
		if _, err := s.LLMCalls().Get(ctx, call.ID); err != nil {
			t.Fatalf("an LLM call went with its account (it must stay for the usage figures): %v", err)
		}
	})
}

func runLLMCalls(t *testing.T, s ports.Store, accountID string, t0 time.Time) {
	ctx := context.Background()
	r := s.LLMCalls()
	full := domain.LLMCall{
		ID: "call-1", AccountID: accountID, Purpose: domain.LLMPurposeAgent, ProfileID: "prof-1", Provider: "anthropic",
		Model: "claude-opus-5-5", Prompt: `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"Proposed trade: LONG — €"}]}`,
		Response: `{"content":[{"type":"text","text":"{\"verdict\":\"approve\"}"}]}`, InputTokens: 1_234, OutputTokens: 56,
		Latency: 1_500 * time.Millisecond, CostMicros: 12_345, At: t0,
	}
	system := domain.LLMCall{
		ID: "call-2", Purpose: domain.LLMPurposeTest, ProfileID: "prof-2", Provider: "openai", Model: "m",
		Prompt: strings.Repeat("p", domain.MaxLLMText+100), Response: strings.Repeat("€", domain.MaxLLMText),
		Latency: 1_999_999 * time.Microsecond, CostMicros: domain.CostUnknown, Error: strings.Repeat("e", 700) + "\xff", At: t0.Add(time.Hour),
	}
	for _, c := range []domain.LLMCall{full, system} {
		if err := r.Add(ctx, c); err != nil {
			t.Fatalf("Add %s: %v", c.ID, err)
		}
	}
	if err := r.Add(ctx, full); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range []domain.LLMCall{{Purpose: domain.LLMPurposeAgent, At: t0}, {ID: "call-x", Purpose: "chat", At: t0}} {
		if err := r.Add(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid call %+v: %v", bad, err)
		}
	}
	got, err := r.Get(ctx, full.ID)
	if err != nil || got != full {
		t.Fatalf("Get = %+v %v\nwant %+v", got, err, full)
	}
	got, err = r.Get(ctx, system.ID)
	switch {
	case err != nil:
		t.Fatal(err)
	case got.AccountID != "" || got.CostMicros != domain.CostUnknown || got.Latency != 1_999*time.Millisecond:
		t.Fatalf("system call = %+v", got)
	case len(got.Prompt) > domain.MaxLLMText || len(got.Response) > domain.MaxLLMText || !utf8.ValidString(got.Response) || !strings.HasSuffix(got.Prompt, "…"):
		t.Fatalf("prompt %d / response %d bytes, valid %v", len(got.Prompt), len(got.Response), utf8.ValidString(got.Response))
	case len(got.Error) > 500 || !utf8.ValidString(got.Error):
		t.Fatalf("error %d bytes", len(got.Error))
	}
	if _, err := r.Get(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	// Usage: per profile and purpose, known costs summed, unknown ones counted.
	later := domain.LLMCall{
		ID: "call-3", AccountID: accountID, Purpose: domain.LLMPurposeAgent, ProfileID: "prof-1", Provider: "anthropic",
		InputTokens: 10, OutputTokens: 2, CostMicros: 100, Error: "timeout", At: t0.Add(2 * time.Hour),
	}
	scan := domain.LLMCall{
		ID: "call-4", AccountID: accountID, Purpose: domain.LLMPurposeScanner, ProfileID: "prof-1", Provider: "anthropic",
		InputTokens: 7, OutputTokens: 3, CostMicros: domain.CostUnknown, At: t0.Add(3 * time.Hour),
	}
	for _, c := range []domain.LLMCall{later, scan} {
		if err := r.Add(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	us, err := r.Usage(ctx, t0)
	want := []domain.LLMUsage{
		{ProfileID: "prof-1", Purpose: domain.LLMPurposeAgent, Calls: 2, Failed: 1, InputTokens: 1_244, OutputTokens: 58, Cost: 12_445},
		{ProfileID: "prof-1", Purpose: domain.LLMPurposeScanner, Calls: 1, InputTokens: 7, OutputTokens: 3, CostUnknownCalls: 1},
		{ProfileID: "prof-2", Purpose: domain.LLMPurposeTest, Calls: 1, Failed: 1, CostUnknownCalls: 1},
	}
	if err != nil || !reflect.DeepEqual(us, want) {
		t.Fatalf("Usage = %+v %v\nwant %+v", us, err, want)
	}
	us, err = r.Usage(ctx, t0.Add(2*time.Hour))
	if err != nil || len(us) != 2 || us[0].Calls != 1 || us[0].Cost != 100 || us[1].Purpose != domain.LLMPurposeScanner {
		t.Fatalf("Usage since +2h = %+v %v", us, err)
	}
	if us, err := r.Usage(ctx, t0.Add(24*time.Hour)); err != nil || len(us) != 0 {
		t.Fatalf("Usage of nothing = %+v %v", us, err)
	}
	if n, err := r.DeleteBefore(ctx, t0.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("DeleteBefore = %d %v", n, err)
	}
	if _, err := r.Get(ctx, full.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("old call kept")
	}
	if _, err := r.Get(ctx, system.ID); err != nil {
		t.Fatal("newer call deleted")
	}
}

func runDecisions(t *testing.T, s ports.Store, accountID, otherID string, t0 time.Time) {
	ctx := context.Background()
	r := s.Decisions()
	mk := func(id, account string, at time.Time, outcome domain.DecisionOutcome) domain.Decision {
		d := domain.Decision{
			ID: id, AccountID: account, At: at, Outcome: outcome,
			Signal: domain.DecisionSignal{
				Instrument: 1001, Symbol: "AAPL", Direction: domain.Long, Playbook: "trend_breakout", Reason: "close above the 20-bar high",
				Entry: 189_200_000, StopLoss: 187_000_000, TakeProfit: 193_600_000, SuggestedSize: 50_000,
				RegimeProbs: map[domain.Regime]float64{domain.RegimeTrending: 0.8123456789, domain.RegimeChoppy: 0.1876543211},
			},
			Verdict: domain.DecisionVerdict{Kind: domain.VerdictReduce, SizeBp: 5_000, Reason: "wide stop", LLMCallID: "call-1"},
			Risk:    domain.DecisionRisk{Allowed: true, Size: 25_000, Reason: "within limits"},
		}
		if outcome == domain.DecisionOrdered {
			d.OrderRequestID = "req-" + id
		}
		return d
	}
	pending := mk("dec-1", accountID, t0, domain.DecisionPending)
	short := mk("dec-2", accountID, t0.Add(time.Minute), domain.DecisionRejectedReviewer)
	short.Signal.Direction, short.Signal.RegimeProbs = domain.Short, nil
	short.Verdict, short.Risk = domain.DecisionVerdict{Kind: domain.VerdictReject, Reason: "no"}, domain.DecisionRisk{}
	same := mk("dec-3", accountID, t0.Add(time.Minute), domain.DecisionRejectedRisk) // same time as dec-2
	same.Verdict = domain.DecisionVerdict{}
	same.Risk = domain.DecisionRisk{Reason: strings.Repeat("ü", 400)}
	odd := mk("dec-4", accountID, t0.Add(2*time.Minute), domain.DecisionRejectedCost)
	odd.Signal.RegimeProbs = map[domain.Regime]float64{domain.RegimeTrending: math.NaN(), domain.RegimeChoppy: 1}
	theirs := mk("dec-9", otherID, t0.Add(3*time.Minute), domain.DecisionOrdered)
	for _, d := range []domain.Decision{pending, short, same, odd, theirs} {
		if err := r.Add(ctx, d); err != nil {
			t.Fatalf("Add %s: %v", d.ID, err)
		}
	}
	if err := r.Add(ctx, pending); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := r.Add(ctx, mk("dec-x", "nope", t0, domain.DecisionPending)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	if err := r.Add(ctx, mk("dec-y", accountID, t0, "won")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid outcome: %v", err)
	}

	got, err := r.Get(ctx, accountID, pending.ID)
	if err != nil || !reflect.DeepEqual(got, pending) {
		t.Fatalf("Get = %+v %v\nwant %+v", got, err, pending)
	}
	if got, _ := r.Get(ctx, accountID, short.ID); !reflect.DeepEqual(got, short) {
		t.Fatalf("short = %+v\nwant %+v", got, short)
	}
	if got, _ := r.Get(ctx, accountID, same.ID); len(got.Risk.Reason) > domain.MaxDecisionReason || !utf8.ValidString(got.Risk.Reason) {
		t.Fatalf("reason %d bytes", len(got.Risk.Reason))
	}
	if got, _ := r.Get(ctx, accountID, odd.ID); len(got.Signal.RegimeProbs) != 1 || got.Signal.RegimeProbs[domain.RegimeChoppy] != 1 {
		t.Fatalf("non-finite probability kept: %+v", got.Signal.RegimeProbs)
	}
	if _, err := r.Get(ctx, otherID, pending.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another account's decision: %v", err)
	}
	if _, err := r.Get(ctx, accountID, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	// the pending decision ends: ordered with its request id
	done := pending
	done.Outcome, done.OrderRequestID = domain.DecisionOrdered, "req-1"
	done.Risk = domain.DecisionRisk{Allowed: true, Size: 24_000, Reason: "sized down"}
	done.Signal.Reason = "changed" // the signal is immutable: Update ignores it
	if err := r.Update(ctx, done); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(ctx, accountID, pending.ID)
	if got.Outcome != domain.DecisionOrdered || got.OrderRequestID != "req-1" || got.Risk.Size != 24_000 || got.Signal.Reason != pending.Signal.Reason {
		t.Fatalf("after Update = %+v", got)
	}
	wrong := done
	wrong.AccountID = otherID
	if err := r.Update(ctx, wrong); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update through another account: %v", err)
	}
	missing := done
	missing.ID = "nope"
	if err := r.Update(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update of a missing decision: %v", err)
	}
	invalid := done
	invalid.OrderRequestID = ""
	if err := r.Update(ctx, invalid); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid update: %v", err)
	}

	ids := func(ds []domain.Decision) string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.ID
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		name    string
		before  time.Time
		outcome domain.DecisionOutcome
		limit   int
		want    string
	}{
		{"all, newest first, ties by id", time.Time{}, "", 100, "dec-4,dec-3,dec-2,dec-1"},
		{"limit", time.Time{}, "", 2, "dec-4,dec-3"},
		{"zero limit", time.Time{}, "", 0, ""},
		{"before (exclusive)", t0.Add(2 * time.Minute), "", 100, "dec-3,dec-2,dec-1"},
		{"outcome", time.Time{}, domain.DecisionRejectedReviewer, 100, "dec-2"},
		{"outcome and before", t0.Add(time.Minute), domain.DecisionOrdered, 100, "dec-1"},
		{"no match", time.Time{}, domain.DecisionOrderFailed, 100, ""},
	} {
		got, err := r.List(ctx, accountID, c.before, c.outcome, c.limit)
		if err != nil || ids(got) != c.want {
			t.Errorf("List %s = %s %v, want %s", c.name, ids(got), err, c.want)
		}
	}
	if got, _ := r.List(ctx, otherID, time.Time{}, "", 100); ids(got) != "dec-9" || got[0].OrderRequestID != "req-dec-9" {
		t.Fatalf("other account = %+v", got)
	}

	if n, err := r.DeleteBefore(ctx, t0.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("DeleteBefore = %d %v", n, err)
	}
	if got, _ := r.List(ctx, accountID, time.Time{}, "", 100); ids(got) != "dec-4,dec-3,dec-2" {
		t.Fatalf("after DeleteBefore = %s", ids(got))
	}
}
