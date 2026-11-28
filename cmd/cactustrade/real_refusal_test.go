package main

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PLAN 7.17: the source half of the real refusal matrix (here because only cmd may read files;
// the behaviour half is internal/{domain,app}/real_refusal_test.go,
// internal/adapters/{etoro,http}).

// modeSites lists every reference to domain.ModeReal / domain.ModeDemo in non-test Go code
// (PLAN 7.17), by file and the trimmed line, with what it is. A new reference fails
// TestRealRefusalModeReferences until it is added here — and reviewed: a new "!= ModeDemo"
// refusal is fine; since the lift (PLAN 7.19–7.23, D39) ModeReal may pass only towards an
// exit of ours (an exit permit) or an entry with realEntryBlocked silent and a fresh entry
// permit — anything else that lets ModeReal through is a BLOCKER.
var modeSites = map[string][]string{
	// S1, S2 and the types themselves.
	"internal/domain/credentials.go": {
		`ModeDemo TradingMode = "demo"`, // the constants
		`ModeReal TradingMode = "real"`,
		`if m == ModeReal {`, // EtoroKeys: the real key names
	},
	"internal/domain/orders.go": {
		`case o.Mode != ModeDemo && o.Mode != ModeReal:`,                            // validation
		`case o.Mode != ModeDemo && o.Mode != ModeReal:`,                            // S2 IntentFor: any other mode refused
		`case o.Mode == ModeReal && !permit.AllowsEntry(o.AccountID, o.CreatedAt):`, // S2 (7.19): real only with a valid entry permit
	},
	"internal/domain/risk.go": {
		`case in.Mode != ModeDemo && in.Mode != ModeReal:`,                          // S1 AssessEntry: any other mode refused
		`case in.Mode == ModeReal && !in.Permit.AllowsEntry(in.AccountID, in.Now):`, // S1 (7.19): real only with a valid entry permit
		`case in.Mode == ModeReal && l.ValidateFor(ModeReal) != nil:`,               // S1 (7.19): and limits within the real ceilings
		`return deny("real mode is gated (phase 7): the limits are not within the real-money ceilings: %v", l.ValidateFor(ModeReal))`,
		`if m == ModeReal {`, // DefaultLimitsFor (OD25)
		`if m != ModeReal {`, // ValidateFor (OD25)
	},
	"internal/domain/history.go": {`case t.Mode != ModeDemo && t.Mode != ModeReal:`}, // validation
	// R45 (D41 g): Trading 212 trades practice accounts only; real is refused everywhere.
	"internal/domain/account.go": {`return a.OnPlatform() == PlatformTrading212 && a.Mode == ModeDemo`}, // T212Trading: demo only
	"internal/domain/t212.go": {
		`case o.Mode != ModeDemo:`, // validateT212: any non-demo Trading 212 order refused
		`case o.Mode != ModeDemo:`, // T212IntentFor: any non-demo Trading 212 intent refused
	},
	// R28: presets are capped at the real ceilings on a real account; only demo starts itself.
	"internal/domain/style.go": {
		`if m == ModeReal {`, // PresetFor: RealCeilings (OD25)
		`p.AutoStart, p.AdaptAggressiveness = m == ModeDemo, true`, // Policy: full autonomy never starts a real agent (OD26)
	},
	"internal/domain/realgate.go": {
		`if o.Kind != OrderOpen || o.Mode != ModeDemo || o.Sends < 1 || (o.State != OrderFilled && o.State != OrderPartial) {`, // evidence: demo opens only
		`c(CheckRealAccount, in.Real.Tradable() && in.Real.Mode == ModeReal, "an eToro real-money account", "not an eToro real-money account"),`,
		`case !d.Tradable() || d.Mode != ModeDemo:`, // the linked demo account
		`if err := in.Limits.ValidateFor(ModeReal); err != nil {`,
		`case !a.Tradable() || a.Mode != ModeReal || a.ID == "":`, // PermitEntry (7.19): refuses anything but a real eToro account
		`case !a.Tradable() || a.Mode != ModeReal || a.ID == "":`, // PermitExit (7.19): the same
	},
	// S3, S4 and adapter plumbing (paths and keys by mode; reads stay allowed).
	"internal/adapters/etoro/execute.go": {
		`case mode == domain.ModeDemo && acct.Mode == domain.ModeDemo:`,                                                                  // S3: demo as before
		`case mode == domain.ModeReal && acct.Mode == domain.ModeReal && !acct.EnvKeys && realPermitted(ctx, op, acct.ID, c.cfg.Now()):`, // S3 (7.20): real only with its own keys and a permit for the account and operation
	},
	"internal/adapters/etoro/client.go":    {`if a.Mode == domain.ModeReal {`, `return a, domain.ModeReal, nil`, `return a, domain.ModeDemo, nil`},
	"internal/adapters/etoro/allowlist.go": {`if m == domain.ModeReal {`},
	"internal/adapters/etoro/stream.go":    {`mode := domain.ModeDemo`, `if acct.Mode == domain.ModeReal {`, `mode = domain.ModeReal`},
	"internal/ports/fakes/simbroker.go": {
		`case a.Mode == domain.ModeDemo:`, // S4: demo as before
		`case a.Mode != domain.ModeReal:`, // S4: no mode refused; real only with a permit (7.20)
	},
	"internal/adapters/saxo/client.go":       {`if a.Mode == domain.ModeReal {`}, // view-only: LIVE vs SIM host
	"internal/adapters/saxo/oauth.go":        {`if acct.Mode == domain.ModeReal {`},
	"internal/adapters/trading212/client.go": {`if acct.Mode == domain.ModeReal {`},
	"internal/adapters/http/metrics.go":      {`if mode != domain.ModeReal {`, `mode = domain.ModeDemo`},
	"internal/adapters/http/handlers_market.go": {
		`mode := domain.ModeDemo`, `if v, err := s.svc.CurrentAccount(r.Context(), state(r).principal); err == nil && v.Mode == domain.ModeReal {`, `mode = domain.ModeReal`,
	},
	// S5–S10 and the app's plumbing (PLAN 7.21: exits on real accounts whatever the gate;
	// 7.22: real entries only with realEntryBlocked silent and a fresh permit).
	"internal/app/executor.go": {
		`case a.Mode != domain.ModeDemo && a.Mode != domain.ModeReal:`, // S5 mayMutate: no mode refused
		`if a.Mode != domain.ModeReal {`,                               // S5 mayExit: a real exit needs an exit permit (ours only)
		`if a.Mode == domain.ModeReal && a.Tradable() {`,               // S5 mayOpen: a real entry needs a fresh entry permit
	},
	"internal/app/supervisor.go": {
		`if !a.Agentable() || a.Mode != domain.ModeDemo && a.Mode != domain.ModeReal || !s.hasKeys(ctx, a) {`, // S6 superviseTick: eToro accounts with keys, and Trading 212 practice accounts (R45.7: Agentable adds demo only)
		`if a.Mode == domain.ModeDemo {`,                               // S6: a demo account is always wanted
		`if a.Mode == domain.ModeReal {`,                               // S6: a real one only when realSupervised (exits)
		`case a.Mode != domain.ModeDemo && a.Mode != domain.ModeReal:`, // S6 supervise: no mode skipped
		`case a.Mode == domain.ModeReal && !s.realSupervised(ctx, a):`, // S6: real skipped when not armed and nothing of ours
	},
	"internal/app/agent_control.go": {
		`case a.Mode != domain.ModeDemo && a.Mode != domain.ModeReal:`,  // S8 startable: no mode refused
		`if a.Mode == domain.ModeReal && a.Tradable() {`,                // S8 startable: real only when entries may be made
		`if err == nil && a.Mode == domain.ModeReal {`,                  // S8 resume: a real agent never resumes (OD26)
		`if v.Mode == domain.ModeReal && !s.rt.realStartedHere(v.ID) {`, // S8 StartAgent: a person's start marks a real agent (OD26, review M2)
		`if v.Mode == domain.ModeReal {`,                                // S8 StartAgent: a person's start marks a real agent (OD26, review M2)
	},
	"internal/app/agent.go": {
		`case !a.Agentable() || a.Mode != domain.ModeDemo && a.Mode != domain.ModeReal:`,                       // S9 agentTickAll: eToro accounts, and Trading 212 practice accounts (R45.7: Agentable adds demo only)
		`case a.Mode == domain.ModeReal && (!s.rt.realStartedHere(a.ID) || s.realEntryBlocked(ctx, a) != ""):`, // S9 agentTickAll: real only when started by a person here and entries may be made
		`case a.Mode != domain.ModeDemo && a.Mode != domain.ModeReal:`,                                         // S9 agentGate: no mode refused
		`if a.Mode == domain.ModeReal {`, // S9 agentGate: real only when entries may be made and started by a person here (OD26)
		`if a.Mode == domain.ModeReal {`, // S1 input: a real entry is assessed with a fresh permit
	},
	"internal/app/agent_view.go": {
		`case domain.ModeDemo:`, // S10 controlsFor: demo not gated
		`case domain.ModeReal:`, // S10: real gated while realEntryBlocked says why (no mode: gated)
	},
	"internal/app/health.go": {
		`return a.Mode == domain.ModeDemo || a.Mode == domain.ModeReal && s.realSupervised(ctx, a)`, // S6 supervisable (exits, 7.21)
		`AccountID: a.ID, Mode: accountMode(a), Supervised: s.supervisable(ctx, a),`,                // health: reports the mode (a read)
	},
	"internal/app/accounts.go": {`if mode != domain.ModeDemo && mode != domain.ModeReal {`, `a := domain.Account{ID: domain.EnvAccountID, Name: "Demo (environment)", OwnerID: owner.ID, Mode: domain.ModeDemo, EnvKeys: true, CreatedAt: now, UpdatedAt: now}`},
	"internal/app/system.go": {
		`if a.Mode == domain.ModeReal {`, `return domain.ModeReal`, `return domain.ModeDemo`, `Version: s.cfg.Version, Build: s.cfg.Build, Mode: domain.ModeDemo, AgentState: domain.AgentStopped,`,
		`func accountMode(a domain.Account) domain.TradingMode {`,                                                                              // accountMode: real only when the account says so (display, limits)
		`st.Mode, st.AgentState, st.Account = accountMode(v.Account), s.agentState(ctx, v.ID), &v`,                                             // Status: the mode shown (R3's frame)
		`st := SystemStatus{Mode: domain.ModeDemo, AgentState: domain.AgentStopped, Database: s.d.Store.Driver(), Now: s.d.Clock.Now().UTC()}`, // bannerStatus: banner facts only
	},
	// accountMode for display and limits (review minor 5): never a permission to trade.
	"internal/app/risk.go": {
		`l := domain.DefaultLimitsFor(accountMode(a))`,                  // real accounts default to the real limits (OD25)
		`if err := l.ValidateFor(accountMode(v.Account)); err != nil {`, // real limits checked against the real ceilings (OD25)
	},
	// R45.5 (T212-l): a Trading 212 price source must be an eToro demo account (never real).
	"internal/app/t212_setup.go": {`case src.Mode != domain.ModeDemo:`},
	// R38: a minimums proposal is checked against the account's mode only to narrow it (real
	// ceilings); approving it goes through SetRiskLimits.
	"internal/app/minimums.go": {
		`fit := domain.FitMinimums(l, m, equity, accountMode(v.Account))`,
	},
	// R28: the style follows the account's mode only to narrow (presets capped, no real auto-start).
	"internal/app/style.go": {
		`return s.autonomyOf(ctx, a.ID).Policy(accountMode(a))`, // policyOf: real accounts never start themselves
		`m := accountMode(v.Account)`,                           // RiskPresets: capped presets (a read)
		`out := RiskPresetList{Current: domain.MatchPreset(s.riskLimitsFor(ctx, v.Account), m), Clamped: m == domain.ModeReal}`,
		`next, err := domain.ApplyPreset(s.riskLimitsFor(ctx, v.Account), preset, accountMode(v.Account))`, // capped, then SetRiskLimits' rules
		`return domain.MatchPreset(l, accountMode(a))`,                                                     // presetOf: the matching preset (a read)
	},
	"internal/app/autonomy.go": {
		`if !a.Tradable() || a.Mode != domain.ModeDemo || !s.policyOf(ctx, a).AutoStart {`, // autoStartAll: demo only (OD26)
	},
	"internal/app/market.go":    {`mode := accountMode(v.Account)`}, // TestConnection: reports the mode (a read)
	"internal/ports/account.go": {`if a.Mode != "" {`},              // log attributes name the mode (PLAN 7.2)
	// The gate machinery (7.13): only ever narrows (a gate exists for real accounts only).
	"internal/app/realgate.go": {
		`func isRealAccount(a domain.Account) bool { return a.Tradable() && a.Mode == domain.ModeReal }`,
		`if c.Tradable() && c.Mode == domain.ModeDemo && c.OwnerID == v.OwnerID {`,
		`if !isRealAccount(a) {`,         // realGateState: not real = not armed (a read)
		`if !isRealAccount(v.Account) {`, // realAccount: only real accounts have a gate (refuses)
		`if !isRealAccount(a) {`,         // disarmIfArmedQuietly: nothing to disarm
		`if !isRealAccount(a) {`,         // sweepRealGates: nothing to sweep
		`case !isRealAccount(a):`,        // realEntryBlocked (7.22): only a real account may have real entries
		`if err := s.riskLimitsFor(ctx, a).ValidateFor(domain.ModeReal); err != nil {`, // realEntryBlocked: limits within the real ceilings
	},
	// The data reset (R49): it only reads gates and evidence, latches disarms and refuses.
	"internal/app/data_reset.go": {
		`if isRealAccount(a) {`,  // dataResetFacts: a stored-armed gate refuses the reset
		`if !isRealAccount(a) {`, // realEvidenceWarnings: only real accounts have evidence to lose
		`if d.Tradable() && d.Mode == domain.ModeDemo && d.OwnerID == a.OwnerID && d.ID != a.ID {`, // the evidence's demo account (a read)
		`if isRealAccount(a) {`, // wipeLocked: the gate disarm is latched (only narrows)
	},
	// cmd: the environment account is demo; the research commands (backtest, etoro-probe)
	// read only with a demo account's keys and refuse any other before a request.
	"cmd/cactustrade/main.go": {`case x.Mode != domain.ModeReal:`},
	// D39: etoro-probe --real-read may read a real account (its reader has no mutating method)
	"cmd/cactustrade/research.go": {`if !ok || a.Mode != domain.ModeDemo {`, `case a.Mode != domain.ModeDemo && (!realRead || a.Mode != domain.ModeReal):`},
}

// modeRef matches a line that decides on a trading mode: the constants, the app's helpers
// isRealAccount and accountMode, and any comparison of a .Mode field (review minor 5).
var modeRef = regexp.MustCompile(`ModeReal|ModeDemo|isRealAccount|accountMode|\.Mode\s*(==|!=)`)

// TestRealRefusalModeReferences: every line of non-test code that references
// domain.ModeReal / domain.ModeDemo, calls isRealAccount or accountMode, or compares a .Mode
// field is in modeSites (the shared repository suite's fixtures, storetest, are test code in
// all but name and are skipped). This is a tripwire for review, not the guard: a mode check
// written another way (a switch on Mode, a string literal "real") passes it. The guard is the
// behaviour matrix (TestRealRefusalDomain, TestRealRefusalEtoroClient, TestRealRefusalApp,
// TestRealRefusalHTTP), which drives every refusal site with a gate armed and counts the
// broker calls and requests.
func TestRealRefusalModeReferences(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch name := d.Name(); {
			// Hidden folders (.git, .cache, .claude worktrees of other checkouts) are not
			// this build's code.
			case path != root && strings.HasPrefix(name, "."),
				name == "node_modules", name == "web", name == "storetest":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".gen.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(src), "\n") {
			if modeRef.MatchString(line) {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "//") {
					continue // a comment line
				}
				found[rel] = append(found[rel], stripComment(trim))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Compared as multisets: a second copy of a listed line is a new site too.
	count := func(ls []string) map[string]int {
		m := map[string]int{}
		for _, l := range ls {
			m[l]++
		}
		return m
	}
	for file, lines := range found {
		want := count(modeSites[file])
		for l, n := range count(lines) {
			if n > want[l] {
				t.Errorf("%s: a reference to ModeReal/ModeDemo not in modeSites (PLAN 7.17; review it, then add it):\n\t%s", file, l)
			}
		}
	}
	for file, want := range modeSites {
		got := count(found[file])
		for l, n := range count(want) {
			if got[l] < n {
				t.Errorf("%s: modeSites lists a reference that is gone (remove it):\n\t%s", file, l)
			}
		}
	}
	if len(found) < 10 {
		t.Fatalf("the scan found only %d files: is the root right?", len(found))
	}
}

// stripComment removes a trailing // comment from a line of code (not inside a string).
func stripComment(line string) string {
	inStr := false
	for i := 0; i < len(line)-1; i++ {
		switch {
		case line[i] == '"' && (i == 0 || line[i-1] != '\\'):
			inStr = !inStr
		case !inStr && line[i] == '/' && line[i+1] == '/':
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

// TestRealPermitCallSites (review m1 of the lift): a real-money permit is built and attached
// to a broker context only where the gate is checked. domain.PermitEntry is called only by
// internal/app/realgate.go (realEntryPermit), domain.PermitExit and ports.WithPermit only by
// internal/app/executor.go (mayExit, brokerCtxFor). Any other caller fails the build.
func TestRealPermitCallSites(t *testing.T) {
	root := filepath.Join("..", "..")
	want := map[string]map[string]int{
		"domain.PermitEntry(": {"internal/app/realgate.go": 1},
		"domain.PermitExit(":  {"internal/app/executor.go": 1},
		"ports.WithPermit(":   {"internal/app/executor.go": 1},
	}
	got := map[string]map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && strings.HasPrefix(name, ".") || name == "node_modules" || name == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for call := range want {
			pkg, bare, _ := strings.Cut(call, ".")
			n := strings.Count(string(src), call)
			// inside its own package the call has no qualifier
			if strings.HasPrefix(rel, "internal/"+pkg+"/") {
				n += strings.Count(string(src), " "+bare) + strings.Count(string(src), "\t"+bare) - strings.Count(string(src), "func "+bare)
			}
			if n > 0 {
				if got[call] == nil {
					got[call] = map[string]int{}
				}
				got[call][rel] += n
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for call, files := range want {
		if !maps.Equal(got[call], files) {
			t.Errorf("%s is called from %v; only %v may (a real permit only where the gate is checked)", call, got[call], files)
		}
	}
}
