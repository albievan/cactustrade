package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Trading health (PLAN 7.5): what the metrics collector reads at scrape time and what
// GET /api/v1/system/health answers. It reads; it never decides anything for trading.

// SupervisorStale is how old an account's latest completed supervisor pass may be before
// the account is unhealthy (six SupervisorIntervals). The same bound applies to the
// supervisor loop's own tick.
const SupervisorStale = 60 * time.Second

// AccountHealth is one eToro account's trading health. It holds ids, states and times only:
// never a key, token or amount.
type AccountHealth struct {
	AccountID string
	// Name is the account's name; SystemHealth fills it only for accounts the caller can
	// see (TradingHealth never does: metrics carry account ids only).
	Name string
	Mode domain.TradingMode
	// Supervised: the supervisor is expected to pass over the account (an eToro demo
	// account with keys, the same rule as SuperviseLoop).
	Supervised bool
	AgentState domain.AgentState
	Killed     bool
	// Reconciled is the latest reconciliation's OK in this process.
	Reconciled bool
	// LastPass is the end of the latest supervisor pass that ran (lease held, no store
	// failure), with or without broker errors; LastCleanPass the latest without any error.
	// Zero = none in this process.
	LastPass      time.Time
	LastCleanPass time.Time
	// LeaseHeld: this process holds the account's single-writer lease (D22) now.
	LeaseHeld bool
	// Orders counts our non-final orders by state (every non-final state is present).
	Orders map[domain.OrderState]int
	// RealGateArmed: real trading is armed for the account and in force (PLAN 7.13; always
	// false in this build, which cannot arm: 7.18).
	RealGateArmed bool
	// Problem says why the account is unhealthy ("" = healthy).
	Problem string
}

// LLMCallCount is how many LLM calls of one purpose had one outcome ("ok" or "error") in
// this process.
type LLMCallCount struct {
	Purpose domain.LLMPurpose
	Outcome string
	Count   uint64
}

// LLM call outcomes.
const (
	LLMOutcomeOK    = "ok"
	LLMOutcomeError = "error"
)

// TradingHealth is the trading side's health: every eToro account, the supervisor loop and
// the agent loop.
type TradingHealth struct {
	At        time.Time
	StartedAt time.Time
	// LastTick is the supervisor loop's latest tick (zero = none yet).
	LastTick time.Time
	// AgentLoop is false when the start-up resume failed and the agent loop does not run.
	AgentLoop bool
	Accounts  []AccountHealth
	LLMCalls  []LLMCallCount
	// Problems lists what is unhealthy, naming the account (empty = OK).
	Problems []string
}

// OK reports whether nothing is unhealthy.
func (h TradingHealth) OK() bool { return len(h.Problems) == 0 }

// NonFinalOrderStates are the order states counted in AccountHealth.Orders.
var NonFinalOrderStates = func() []domain.OrderState {
	var out []domain.OrderState
	for _, st := range domain.OrderStates {
		if !st.Final() {
			out = append(out, st)
		}
	}
	return out
}()

// healthLog is the in-process bookkeeping behind TradingHealth.
type healthLog struct {
	mu        sync.Mutex
	lastTick  time.Time
	firstSeen map[string]time.Time // first tick that wanted to supervise the account
	passes    map[string]passRecord
	llm       map[llmKey]uint64
}

type passRecord struct {
	last, lastClean time.Time
	skipped         string // why the latest pass did not run ("" = it ran)
}

type llmKey struct {
	purpose domain.LLMPurpose
	outcome string
}

func newHealthLog() *healthLog {
	return &healthLog{firstSeen: map[string]time.Time{}, passes: map[string]passRecord{}, llm: map[llmKey]uint64{}}
}

// tick records a supervisor loop tick.
func (h *healthLog) tick(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastTick = now
}

// want records that the supervisor loop wants to supervise the account (the first time
// only: it is the reference for an account that has had no pass yet).
func (h *healthLog) want(accountID string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.firstSeen[accountID]; !ok {
		h.firstSeen[accountID] = now
	}
}

// pass records the outcome of a supervisor pass that ended at now.
func (h *healthLog) pass(accountID string, now time.Time, rep SupervisorReport, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.passes[accountID]
	switch {
	case err != nil:
		r.skipped = "the pass failed: " + err.Error()
	case rep.Skipped != "":
		r.skipped = rep.Skipped
	default:
		r.skipped, r.last = "", now
		if len(rep.Errors) == 0 {
			r.lastClean = now
		}
	}
	h.passes[accountID] = r
}

// llmCall counts one LLM call.
func (h *healthLog) llmCall(purpose domain.LLMPurpose, failed bool) {
	k := llmKey{purpose: purpose, outcome: LLMOutcomeOK}
	if failed {
		k.outcome = LLMOutcomeError
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.llm[k]++
}

func (h *healthLog) snapshot() (lastTick time.Time, firstSeen map[string]time.Time, passes map[string]passRecord, llm []LLMCallCount) {
	h.mu.Lock()
	defer h.mu.Unlock()
	firstSeen, passes = make(map[string]time.Time, len(h.firstSeen)), make(map[string]passRecord, len(h.passes))
	for k, v := range h.firstSeen {
		firstSeen[k] = v
	}
	for k, v := range h.passes {
		passes[k] = v
	}
	for k, n := range h.llm {
		llm = append(llm, LLMCallCount{Purpose: k.purpose, Outcome: k.outcome, Count: n})
	}
	slices.SortFunc(llm, func(a, b LLMCallCount) int {
		return cmp.Or(cmp.Compare(a.Purpose, b.Purpose), cmp.Compare(a.Outcome, b.Outcome))
	})
	return h.lastTick, firstSeen, passes, llm
}

// supervisable reports whether the supervisor loop supervises the account: an eToro demo
// account with both keys (real accounts stay gated until PLAN 7.21).
func (s *Service) supervisable(ctx context.Context, a domain.Account) bool {
	return a.Tradable() && a.Mode == domain.ModeDemo && s.hasKeys(ctx, a)
}

// TradingHealth reports every eToro account's trading health and the loops' liveness, for
// the metrics collector (read at scrape time) and SystemHealth. An account the supervisor
// should cover is unhealthy when its latest completed pass is older than SupervisorStale
// (or, without one, when it has been wanted that long); the supervisor loop is unhealthy
// when it has not ticked for as long. A store failure is reported as a problem. It is not
// a use case for a principal: the caller (the metrics token, SystemHealth) controls access.
func (s *Service) TradingHealth(ctx context.Context) TradingHealth {
	now := s.d.Clock.Now().UTC()
	lastTick, firstSeen, passes, llm := s.health.snapshot()
	h := TradingHealth{At: now, StartedAt: s.startedAt.UTC(), LastTick: lastTick, AgentLoop: s.loop.Load() != loopDown, LLMCalls: llm}
	if !h.AgentLoop {
		h.Problems = append(h.Problems, msgLoopDown)
	}
	if ref := later(lastTick, s.startedAt); now.Sub(ref) > SupervisorStale {
		if lastTick.IsZero() {
			h.Problems = append(h.Problems, fmt.Sprintf("the supervisor loop has not run since the start %s ago", age(now, s.startedAt)))
		} else {
			h.Problems = append(h.Problems, fmt.Sprintf("the supervisor loop last ran %s ago", age(now, lastTick)))
		}
	}
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		h.Problems = append(h.Problems, "reading the accounts failed")
		s.d.Log.WarnContext(ctx, "trading health: listing accounts failed", "err", err)
		return h
	}
	for _, a := range as {
		if !a.Tradable() {
			continue
		}
		ah := s.accountHealth(ctx, a, now, firstSeen[a.ID], passes[a.ID])
		if ah.Problem != "" {
			h.Problems = append(h.Problems, fmt.Sprintf("account %s: %s", a.ID, ah.Problem))
		}
		h.Accounts = append(h.Accounts, ah)
	}
	return h
}

// accountHealth reads one eToro account's health. Read errors make it unhealthy.
func (s *Service) accountHealth(ctx context.Context, a domain.Account, now, firstSeen time.Time, pr passRecord) AccountHealth {
	ah := AccountHealth{
		AccountID: a.ID, Mode: accountMode(a), Supervised: s.supervisable(ctx, a),
		AgentState: s.agentState(ctx, a.ID), Reconciled: s.Reconciled(a.ID).OK,
		LastPass: pr.last, LastCleanPass: pr.lastClean, Orders: map[domain.OrderState]int{},
	}
	var problems []string
	armed, gerr := s.realGateState(ctx, a)
	ah.RealGateArmed = armed
	if gerr != nil {
		problems = append(problems, "reading the real gate failed: its state is unknown")
	}
	killed, err := s.isKilled(ctx, a.ID)
	ah.Killed = killed // a read error reads as killed (fail closed)
	if err != nil {
		problems = append(problems, "reading the kill switch failed")
	}
	if l, err := s.d.Store.AgentLeases().Get(ctx, a.ID); err == nil {
		ah.LeaseHeld = l.Holder == s.leaseHolder() && l.ExpiresAt.After(now)
	} else if !errors.Is(err, domain.ErrNotFound) {
		problems = append(problems, "reading the lease failed")
	}
	for _, st := range NonFinalOrderStates {
		ah.Orders[st] = 0
	}
	if os, err := s.d.Store.Orders().ListOpen(ctx, a.ID); err != nil {
		problems = append(problems, "reading the orders failed")
	} else {
		for _, o := range os {
			ah.Orders[o.State]++
		}
	}
	if ah.Supervised {
		switch {
		case !pr.last.IsZero() && now.Sub(pr.last) > SupervisorStale:
			problems = append(problems, fmt.Sprintf("the last supervisor pass was %s ago%s", age(now, pr.last), skippedNote(pr)))
		case pr.last.IsZero():
			if ref := later(firstSeen, s.startedAt); now.Sub(ref) > SupervisorStale {
				problems = append(problems, fmt.Sprintf("no supervisor pass for %s%s", age(now, ref), skippedNote(pr)))
			}
		}
	}
	for i, p := range problems {
		if i == 0 {
			ah.Problem = p
			continue
		}
		ah.Problem += "; " + p
	}
	return ah
}

func skippedNote(pr passRecord) string {
	if pr.skipped == "" {
		return ""
	}
	return " (" + pr.skipped + ")"
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func age(now, t time.Time) string { return now.Sub(t).Truncate(time.Second).String() }

// SystemHealth is TradingHealth for an administrator (GET /api/v1/system/health): the
// accounts the caller can see carry their names, the others only their ids.
func (s *Service) SystemHealth(ctx context.Context, p Principal) (TradingHealth, error) {
	if err := s.authorize(p, domain.PermViewAudit); err != nil {
		return TradingHealth{}, err
	}
	h := s.TradingHealth(ctx)
	vs, err := s.d.Store.Accounts().ForUser(ctx, p.UserID)
	if err != nil {
		return TradingHealth{}, fmt.Errorf("app.SystemHealth: %w", err)
	}
	names := make(map[string]string, len(vs))
	for _, v := range vs {
		names[v.ID] = v.Name
	}
	for i := range h.Accounts {
		h.Accounts[i].Name = names[h.Accounts[i].AccountID]
	}
	return h, nil
}
