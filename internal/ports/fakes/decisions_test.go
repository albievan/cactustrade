package fakes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestFakeDecisionLog checks the in-memory decision log and LLM call log follow the port
// contracts that the shared SQL suite (storetest) checks for SQLite and MariaDB.
func TestFakeDecisionLog(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	_ = s.Users().Create(ctx, domain.User{ID: "u", Username: "u", Role: domain.RoleAdmin})
	for _, id := range []string{"a", "b"} {
		_ = s.Accounts().Create(ctx, domain.Account{ID: id, Name: id, OwnerID: "u", Mode: domain.ModeDemo})
	}

	calls := s.LLMCalls()
	c := domain.LLMCall{ID: "c1", AccountID: "a", Purpose: domain.LLMPurposeScanner, Prompt: strings.Repeat("x", domain.MaxLLMText+1), Latency: 1500 * time.Microsecond, At: simT0}
	if err := calls.Add(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := calls.Add(ctx, c); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("duplicate call")
	}
	if err := calls.Add(ctx, domain.LLMCall{ID: "c2", Purpose: "chat"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown purpose")
	}
	if got, err := calls.Get(ctx, "c1"); err != nil || len(got.Prompt) != domain.MaxLLMText || got.Latency != time.Millisecond {
		t.Fatalf("Get = %d %v %v", len(got.Prompt), got.Latency, err)
	}
	if _, err := calls.Get(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing call")
	}
	for _, x := range []domain.LLMCall{
		{ID: "u1", Purpose: domain.LLMPurposeAgent, ProfileID: "p", InputTokens: 5, OutputTokens: 1, CostMicros: 9, Error: "x", At: simT0},
		{ID: "u2", Purpose: domain.LLMPurposeAgent, ProfileID: "p", InputTokens: 5, OutputTokens: 1, CostMicros: domain.CostUnknown, At: simT0.Add(time.Hour)},
	} {
		if err := calls.Add(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	us, err := calls.Usage(ctx, simT0)
	if err != nil || len(us) != 2 || us[0].ProfileID != "" || us[0].Purpose != domain.LLMPurposeScanner ||
		us[1] != (domain.LLMUsage{ProfileID: "p", Purpose: domain.LLMPurposeAgent, Calls: 2, Failed: 1, InputTokens: 10, OutputTokens: 2, Cost: 9, CostUnknownCalls: 1}) {
		t.Fatalf("Usage = %+v %v", us, err)
	}
	if us, _ := calls.Usage(ctx, simT0.Add(time.Minute)); len(us) != 1 || us[0].Calls != 1 {
		t.Fatalf("Usage later = %+v", us)
	}
	for _, id := range []string{"u1", "u2"} {
		delete(s.log.calls, id)
	}

	ds := s.Decisions()
	mk := func(id, account string, at time.Time, o domain.DecisionOutcome) domain.Decision {
		return domain.Decision{
			ID: id, AccountID: account, At: at, Outcome: o,
			Signal: domain.DecisionSignal{Direction: domain.Long, RegimeProbs: map[domain.Regime]float64{domain.RegimeTrending: 1}},
		}
	}
	for _, d := range []domain.Decision{
		mk("d1", "a", simT0, domain.DecisionPending), mk("d2", "a", simT0.Add(time.Minute), domain.DecisionRejectedRisk),
		mk("d3", "a", simT0.Add(time.Minute), domain.DecisionRejectedCost), mk("d9", "b", simT0, domain.DecisionPending),
	} {
		if err := ds.Add(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := ds.Add(ctx, mk("d1", "a", simT0, domain.DecisionPending)); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("duplicate decision")
	}
	if err := ds.Add(ctx, mk("dx", "nope", simT0, domain.DecisionPending)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}
	if err := ds.Add(ctx, mk("dy", "a", simT0, "won")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid decision")
	}
	if _, err := ds.Get(ctx, "b", "d1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("another account's decision")
	}
	got, _ := ds.Get(ctx, "a", "d1")
	got.Signal.RegimeProbs[domain.RegimeTrending] = 0 // a copy
	got.Outcome, got.OrderRequestID, got.Risk = domain.DecisionOrdered, "r1", domain.DecisionRisk{Allowed: true, Size: 5}
	if err := ds.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := ds.Get(ctx, "a", "d1"); again.OrderRequestID != "r1" || again.Signal.RegimeProbs[domain.RegimeTrending] != 1 {
		t.Fatalf("after Update = %+v", again)
	}
	wrong := got
	wrong.AccountID = "b"
	if err := ds.Update(ctx, wrong); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("update through another account")
	}
	list, _ := ds.List(ctx, "a", time.Time{}, "", 10)
	if len(list) != 3 || list[0].ID != "d3" || list[1].ID != "d2" || list[2].ID != "d1" {
		t.Fatalf("List = %+v", list)
	}
	if l, _ := ds.List(ctx, "a", simT0.Add(time.Minute), domain.DecisionOrdered, 10); len(l) != 1 || l[0].ID != "d1" {
		t.Fatalf("List filtered = %+v", l)
	}
	if l, _ := ds.List(ctx, "a", time.Time{}, "", 0); len(l) != 0 {
		t.Fatal("zero limit")
	}

	s.DecisionsErr = errors.New("disk full")
	if err := ds.Add(ctx, mk("d5", "a", simT0, domain.DecisionPending)); err == nil {
		t.Fatal("DecisionsErr ignored")
	}
	if err := ds.Update(ctx, got); err == nil {
		t.Fatal("DecisionsErr ignored on update")
	}
	s.DecisionsErr, s.LLMCallsErr = nil, errors.New("disk full")
	if err := calls.Add(ctx, domain.LLMCall{ID: "c3", Purpose: domain.LLMPurposeTest}); err == nil {
		t.Fatal("LLMCallsErr ignored")
	}

	if n, _ := ds.DeleteBefore(ctx, simT0.Add(time.Second)); n != 2 {
		t.Fatalf("DeleteBefore = %d", n)
	}
	if n, _ := calls.DeleteBefore(ctx, simT0.Add(time.Second)); n != 1 {
		t.Fatalf("calls DeleteBefore = %d", n)
	}
	s.LLMCallsErr = nil
	_ = calls.Add(ctx, domain.LLMCall{ID: "c4", AccountID: "a", Purpose: domain.LLMPurposeAgent, At: simT0})
	if err := s.Accounts().Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if l, _ := ds.List(ctx, "a", time.Time{}, "", 10); len(l) != 0 {
		t.Fatal("decisions survived their account")
	}
	if _, err := calls.Get(ctx, "c4"); err != nil {
		t.Fatal("an LLM call went with its account")
	}
}

func TestRequireDecisionHook(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	_ = s.Users().Create(ctx, domain.User{ID: "u", Username: "u", Role: domain.RoleAdmin})
	_ = s.Accounts().Create(ctx, domain.Account{ID: "a", Name: "a", OwnerID: "u", Mode: domain.ModeDemo})
	in := domain.OrderIntent{RequestID: "r1", Direction: domain.Long, Instrument: 7, Amount: 100}
	hook := AllHooks(RequireDecision(s.Decisions(), "a"))
	if err := hook(ctx, in); err == nil {
		t.Fatal("no decision accepted")
	}
	d := domain.Decision{
		ID: "d1", AccountID: "a", At: simT0, Outcome: domain.DecisionPending, OrderRequestID: "r1",
		Signal: domain.DecisionSignal{Instrument: 7, Direction: domain.Long}, Risk: domain.DecisionRisk{Allowed: true, Size: 100},
	}
	if err := s.Decisions().Add(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := hook(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Amount = 101
	if err := hook(ctx, in); err == nil {
		t.Fatal("an intent larger than its decision accepted")
	}
	s.DecisionsErr = nil
	d.Outcome = domain.DecisionOrdered
	_ = s.Decisions().Update(ctx, d)
	in.Amount = 100
	if err := hook(ctx, in); err == nil {
		t.Fatal("a decision no longer pending accepted")
	}
}
