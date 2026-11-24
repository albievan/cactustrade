package main

import (
	"io/fs"
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
// refusal is fine, anything that lets ModeReal through is a 7.19+ change.
var modeSites = map[string][]string{
	// S1, S2 and the types themselves.
	"internal/domain/credentials.go": {
		`ModeDemo TradingMode = "demo"`, // the constants
		`ModeReal TradingMode = "real"`,
		`if m == ModeReal {`, // EtoroKeys: the real key names
	},
	"internal/domain/orders.go": {
		`case o.Mode != ModeDemo && o.Mode != ModeReal:`, // validation
		`case o.Mode != ModeDemo:`,                       // S2 IntentFor
	},
	"internal/domain/risk.go": {
		`case in.Mode != ModeDemo:`, // S1 AssessEntry
		`if m == ModeReal {`,        // DefaultLimitsFor (OD25)
		`if m != ModeReal {`,        // ValidateFor (OD25)
	},
	"internal/domain/history.go": {`case t.Mode != ModeDemo && t.Mode != ModeReal:`}, // validation
	"internal/domain/realgate.go": {
		`if o.Kind != OrderOpen || o.Mode != ModeDemo || o.Sends < 1 || (o.State != OrderFilled && o.State != OrderPartial) {`, // evidence: demo opens only
		`c(CheckRealAccount, in.Real.Tradable() && in.Real.Mode == ModeReal, "an eToro real-money account", "not an eToro real-money account"),`,
		`case !d.Tradable() || d.Mode != ModeDemo:`, // the linked demo account
		`if err := in.Limits.ValidateFor(ModeReal); err != nil {`,
	},
	// S3, S4 and adapter plumbing (paths and keys by mode; reads stay allowed).
	"internal/adapters/etoro/execute.go":     {`if mode != domain.ModeDemo || acct.Mode != domain.ModeDemo {`}, // S3
	"internal/adapters/etoro/client.go":      {`if a.Mode == domain.ModeReal {`, `return a, domain.ModeReal, nil`, `return a, domain.ModeDemo, nil`},
	"internal/adapters/etoro/allowlist.go":   {`if m == domain.ModeReal {`},
	"internal/adapters/etoro/stream.go":      {`mode := domain.ModeDemo`, `if acct.Mode == domain.ModeReal {`, `mode = domain.ModeReal`},
	"internal/ports/fakes/simbroker.go":      {`case a.Mode != domain.ModeDemo:`}, // S4
	"internal/adapters/saxo/client.go":       {`if a.Mode == domain.ModeReal {`},  // view-only: LIVE vs SIM host
	"internal/adapters/saxo/oauth.go":        {`if acct.Mode == domain.ModeReal {`},
	"internal/adapters/trading212/client.go": {`if acct.Mode == domain.ModeReal {`},
	"internal/adapters/http/metrics.go":      {`if mode != domain.ModeReal {`, `mode = domain.ModeDemo`},
	"internal/adapters/http/handlers_market.go": {
		`mode := domain.ModeDemo`, `if v, err := s.svc.CurrentAccount(r.Context(), state(r).principal); err == nil && v.Mode == domain.ModeReal {`, `mode = domain.ModeReal`,
	},
	// S5–S10 and the app's plumbing.
	"internal/app/executor.go":      {`case a.Mode != domain.ModeDemo:`},                                                                                                                               // S5 mayMutate
	"internal/app/supervisor.go":    {`case a.Mode != domain.ModeDemo:`},                                                                                                                               // S6
	"internal/app/agent_control.go": {`case a.Mode != domain.ModeDemo:`, `if a.Mode != domain.ModeDemo || !a.Tradable() || s.d.Broker == nil {`},                                                       // S8 startable, S7 flatten
	"internal/app/agent.go":         {`if !a.Tradable() || a.Mode != domain.ModeDemo || s.agentState(ctx, a.ID) != domain.AgentRunning || !s.rt.claimTick(a.ID) {`, `case a.Mode != domain.ModeDemo:`}, // S9
	"internal/app/agent_view.go":    {`c := AgentControls{Gated: v.Mode != domain.ModeDemo}`},                                                                                                          // S10
	"internal/app/force_close.go":   {`if v.Mode != domain.ModeDemo {`, `if v.Tradable() && v.Mode == domain.ModeDemo && v.Access.AtLeast(domain.AccessTrade) {`},                                      // S7
	"internal/app/health.go": {
		`return a.Tradable() && a.Mode == domain.ModeDemo && s.hasKeys(ctx, a)`,      // S6 supervisable
		`AccountID: a.ID, Mode: accountMode(a), Supervised: s.supervisable(ctx, a),`, // health: reports the mode (a read)
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
		`return domain.DefaultLimitsFor(accountMode(a))`,                // real accounts default to the real limits (OD25)
		`if err := l.ValidateFor(accountMode(v.Account)); err != nil {`, // real limits checked against the real ceilings (OD25)
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
	},
	// cmd: the environment account is demo; the backtester refuses a real account.
	"cmd/cactustrade/main.go":     {`case x.Mode != domain.ModeReal:`},
	"cmd/cactustrade/backtest.go": {`if !ok || acct.Mode == domain.ModeReal {`},
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
