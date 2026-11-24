package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Web research for the watchlist (PLAN R29). An LLM with its provider's built-in web search
// proposes shares; its answer is untrusted data. The code makes the content irrelevant to
// what is traded: the answer is parsed strictly, every share takes the same eligibility path
// as a share added by hand (checkAndAddToWatchlist), and a watchlist share is only something
// the deterministic model looks at. Entries still come only from the regime model and the
// playbooks, the risk engine and the reviewer (which can only shrink or reject). Research
// never calls an order operation.

const (
	settingResearch = "research.config"
	// researchMinGap is the shortest time between two runs for one account (pacing).
	researchMinGap = 10 * time.Minute
	// researchLead: the daily run happens in the hour before a market opens…
	researchLead = time.Hour
	// …at most once in researchScheduledGap, so in practice before the day's first open.
	researchScheduledGap = 20 * time.Hour
	// researchStateMax bounds the stored state of one account (MariaDB TEXT holds 65 535
	// bytes); the oldest proposals give way first.
	researchStateMax = 60_000
	researchWindow   = 24 * time.Hour
)

// researchStateKey is the setting holding an account's runs and waiting proposals.
func researchStateKey(accountID string) string { return "research.state." + accountID }

// researchLocks serialises the read-modify-write of the stored state and marks the accounts
// whose research is running in this process.
type researchLocks struct {
	mu      sync.Mutex
	running map[string]bool
}

func newResearchLocks() *researchLocks { return &researchLocks{running: map[string]bool{}} }

type researchConfigJSON struct {
	ProfileID  string `json:"profileId"`
	MaxShares  int    `json:"maxShares"`
	RunsPerDay int    `json:"runsPerDay"`
}

type researchProposalJSON struct {
	ID         string    `json:"id"`
	Instrument int64     `json:"instrument"`
	Symbol     string    `json:"symbol"`
	Exchange   string    `json:"exchange"`
	Name       string    `json:"name"`
	Theme      string    `json:"theme,omitempty"`
	Reason     string    `json:"reason"`
	Sources    []string  `json:"sources"`
	At         time.Time `json:"at"`
}

type researchStateJSON struct {
	Runs        []time.Time            `json:"runs"`
	ScheduledAt time.Time              `json:"scheduledAt,omitzero"`
	Proposals   []researchProposalJSON `json:"proposals"`
}

func (j researchProposalJSON) proposal() domain.ResearchProposal {
	return domain.ResearchProposal{ID: j.ID, Instrument: domain.InstrumentID(j.Instrument), At: j.At, ResearchPick: domain.ResearchPick{
		Symbol: j.Symbol, Exchange: j.Exchange, Name: j.Name, Theme: j.Theme, Reason: j.Reason, Sources: j.Sources,
	}}
}

func proposalJSON(p domain.ResearchProposal) researchProposalJSON {
	return researchProposalJSON{
		ID: p.ID, Instrument: int64(p.Instrument), Symbol: p.Symbol, Exchange: p.Exchange, Name: p.Name, Theme: p.Theme,
		Reason: p.Reason, Sources: p.Sources, At: p.At,
	}
}

// researchConfig reads the research settings (the defaults when none are stored). Stored
// settings that are unreadable or invalid are an error: research does not run (fail closed).
func (s *Service) researchConfig(ctx context.Context) (domain.ResearchConfig, error) {
	v, err := s.d.Store.Settings().Get(ctx, settingResearch)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.DefaultResearchConfig, nil
	}
	if err != nil {
		return domain.ResearchConfig{}, err
	}
	var j researchConfigJSON
	if err := json.Unmarshal([]byte(v), &j); err != nil {
		return domain.ResearchConfig{}, fmt.Errorf("stored research settings are unreadable: %w", err)
	}
	c := domain.ResearchConfig{ProfileID: j.ProfileID, MaxShares: j.MaxShares, RunsPerDay: j.RunsPerDay}
	if err := c.Validate(); err != nil {
		return domain.ResearchConfig{}, fmt.Errorf("stored research settings are invalid: %w", err)
	}
	return c, nil
}

// researchProfile finds the LLM profile research uses (the chosen one, else the default
// reviewer profile) and its key. Anything unusable is a *domain.MissingError (llm_profile)
// explaining what to fix; other errors are storage failures.
func (s *Service) researchProfile(ctx context.Context, cfg domain.ResearchConfig) (domain.LLMProfile, string, error) {
	st, ps, err := s.loadLLM(ctx)
	if err != nil {
		return domain.LLMProfile{}, "", err
	}
	missing := func(msg string) error {
		return &domain.MissingError{What: domain.MissingLLMProfile, Msg: msg, Err: domain.Invalid("researchProfileId", msg)}
	}
	id := cmpOr(cfg.ProfileID, st.Active)
	if id == "" {
		return domain.LLMProfile{}, "", missing("Research needs an LLM profile: add an Anthropic or OpenRouter profile on the LLM page")
	}
	i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == id })
	if i < 0 {
		return domain.LLMProfile{}, "", missing("The research profile no longer exists: choose one on the LLM page")
	}
	pr := ps[i]
	prov, _ := domain.LLMProviderByID(pr.Provider)
	if !prov.WebSearch {
		return domain.LLMProfile{}, "", missing(pr.Name + " uses " + cmpOr(prov.Label, string(pr.Provider)) +
			", which has no built-in web search here (research not supported): choose an Anthropic or OpenRouter profile for research on the LLM page")
	}
	if err := pr.Validate(); err != nil {
		return domain.LLMProfile{}, "", missing("The research profile " + pr.Name + " is not set up completely: " + err.Error())
	}
	key, err := s.llmKey(ctx, pr.ID)
	if err != nil {
		return domain.LLMProfile{}, "", err
	}
	if prov.KeyRequired && key == "" {
		return domain.LLMProfile{}, "", missing(pr.Name + " has no API key yet")
	}
	if key != "" {
		if err := pr.ValidateKeyTransport(); err != nil {
			return domain.LLMProfile{}, "", missing("The research profile " + pr.Name + " would send its key unencrypted: " + err.Error())
		}
	}
	return pr, key, nil
}

func (s *Service) loadResearchState(ctx context.Context, accountID string) (researchStateJSON, error) {
	v, err := s.d.Store.Settings().Get(ctx, researchStateKey(accountID))
	if errors.Is(err, domain.ErrNotFound) {
		return researchStateJSON{}, nil
	}
	if err != nil {
		return researchStateJSON{}, err
	}
	var st researchStateJSON
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return researchStateJSON{}, fmt.Errorf("the stored research state is unreadable: %w", err)
	}
	return st, nil
}

// saveResearchState stores the state, dropping the oldest proposals while it is too large.
func (s *Service) saveResearchState(ctx context.Context, accountID string, st researchStateJSON) error {
	for {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(st); err != nil {
			return err
		}
		if buf.Len() <= researchStateMax || len(st.Proposals) == 0 {
			return s.d.Store.Settings().Set(ctx, researchStateKey(accountID), strings.TrimSpace(buf.String()), s.d.Clock.Now().UTC())
		}
		st.Proposals = st.Proposals[1:]
	}
}

// recentRuns keeps the runs of the last 24 hours.
func recentRuns(runs []time.Time, now time.Time) []time.Time {
	return slices.DeleteFunc(slices.Clone(runs), func(t time.Time) bool { return !t.After(now.Add(-researchWindow)) })
}

// claimResearch counts a run for the account before the LLM is asked (a failed call costs
// too): at most cfg.RunsPerDay in 24 hours, researchMinGap apart, one at a time. release
// ends the run.
func (s *Service) claimResearch(ctx context.Context, accountID string, cfg domain.ResearchConfig, scheduled bool) (func(), error) {
	l := s.research
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running[accountID] {
		return nil, fmt.Errorf("research is already running for this account: %w", domain.ErrConflict)
	}
	st, err := s.loadResearchState(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("app.Research: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	st.Runs = recentRuns(st.Runs, now)
	if len(st.Runs) >= cfg.RunsPerDay {
		return nil, fmt.Errorf("research ran %d times in the last 24 hours, the limit set on the LLM page: %w", len(st.Runs), domain.ErrConflict)
	}
	if n := len(st.Runs); n > 0 && now.Sub(st.Runs[n-1]) < researchMinGap {
		return nil, fmt.Errorf("research ran less than %d minutes ago: %w", int(researchMinGap/time.Minute), domain.ErrConflict)
	}
	st.Runs = append(st.Runs, now)
	if scheduled {
		st.ScheduledAt = now
	}
	if err := s.saveResearchState(ctx, accountID, st); err != nil {
		return nil, fmt.Errorf("app.Research: %w", err)
	}
	l.running[accountID] = true
	return func() {
		l.mu.Lock()
		delete(l.running, accountID)
		l.mu.Unlock()
	}, nil
}

// ResearchAdded is a share research put on the watchlist.
type ResearchAdded struct {
	Instrument domain.InstrumentID
	Symbol     string
	Name       string
	Theme      string // the theme it was put in ("" = none)
	Reason     string
}

// ResearchRun is what one research run did. A provider failure is Error (nothing added),
// not an error of the use case.
type ResearchRun struct {
	ResearchMode domain.ResearchMode
	Added        []ResearchAdded
	Proposed     []domain.ResearchProposal
	Refused      []domain.ResearchRefusal
	Error        string
	// The logged LLM call ("" when logging it failed), its tokens and cost (USD micros;
	// domain.CostUnknown without prices; web search fees are billed by the provider on top).
	CallID       string
	Model        string
	InputTokens  int64
	OutputTokens int64
	Cost         domain.Micros
}

// runResearch asks the research profile for shares and proposes or adds them, depending on
// the account's mode. It never calls an order operation.
func (s *Service) runResearch(ctx context.Context, v domain.AccountView, actor string, scheduled bool) (ResearchRun, error) {
	mode := s.ResearchMode(ctx, v.ID)
	if mode == domain.ResearchOff {
		return ResearchRun{}, fmt.Errorf("research is off for %s: %w", v.Name, domain.ErrConflict)
	}
	cfg, err := s.researchConfig(ctx)
	if err != nil {
		return ResearchRun{}, fmt.Errorf("app.Research: %w", err)
	}
	if cfg.RunsPerDay == 0 {
		return ResearchRun{}, fmt.Errorf("research is switched off (0 runs a day on the LLM page): %w", domain.ErrConflict)
	}
	if s.d.Researcher == nil {
		return ResearchRun{}, fmt.Errorf("web research is not available: %w", domain.ErrUnavailable)
	}
	if err := s.needBroker(); err != nil {
		return ResearchRun{}, err
	}
	pr, key, err := s.researchProfile(ctx, cfg)
	if err != nil {
		return ResearchRun{}, err
	}
	release, err := s.claimResearch(ctx, v.ID, cfg, scheduled)
	if err != nil {
		return ResearchRun{}, err
	}
	defer release()
	req, err := s.researchRequest(ctx, v, cfg)
	if err != nil {
		return ResearchRun{}, fmt.Errorf("app.Research: %w", err)
	}
	start := s.d.Clock.Now()
	res, call, err := s.d.Researcher.Research(ctx, pr.LLMConfig, key, req)
	if err != nil && call.Error == "" {
		call.Error = err.Error()
	}
	if lerr := s.logCall(ctx, &call, v.ID, domain.LLMPurposeResearch, pr, start); lerr != nil {
		s.d.Log.WarnContext(ctx, "research: logging the LLM call failed", "account", v.ID, "profile", pr.ID, "err", lerr)
		call.ID = ""
	}
	run := ResearchRun{
		ResearchMode: mode, CallID: call.ID, Model: call.Model, InputTokens: call.InputTokens, OutputTokens: call.OutputTokens, Cost: call.CostMicros,
	}
	if err != nil {
		run.Error = err.Error()
		s.emitFor(ctx, v.ID, domain.WatchAddedByResearch, "research.failed", domain.SeverityWarning,
			"Web research for "+v.Name+" failed: nothing was added", domain.Truncate(run.Error, 500))
		return run, nil
	}
	run.Refused = append(run.Refused, res.Refused...)
	s.applyResearch(ctx, v, actor, mode, cfg, res.Picks, &run)
	s.emitFor(ctx, v.ID, domain.WatchAddedByResearch, "research.done", domain.SeverityInfo, researchSummary(v, run), "")
	return run, nil
}

func researchSummary(v domain.AccountView, run ResearchRun) string {
	parts := []string{}
	if run.ResearchMode == domain.ResearchPropose {
		parts = append(parts, strconv.Itoa(len(run.Proposed))+" proposed")
	} else {
		parts = append(parts, strconv.Itoa(len(run.Added))+" added")
	}
	parts = append(parts, strconv.Itoa(len(run.Refused))+" refused")
	return "Web research for " + v.Name + ": " + strings.Join(parts, ", ")
}

// researchRequest is what the model is told: the enabled themes, the exchanges, the shares
// already on the watchlist or proposed, and how many to find.
func (s *Service) researchRequest(ctx context.Context, v domain.AccountView, cfg domain.ResearchConfig) (ports.ResearchRequest, error) {
	req := ports.ResearchRequest{Date: s.d.Clock.Now().UTC(), Exchanges: slices.Clone(domain.ResearchExchanges), Max: cfg.MaxShares}
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return req, err
	}
	for _, t := range themes {
		if t.Enabled {
			req.Themes = append(req.Themes, ports.ResearchTheme{Name: t.Name, Keywords: t.Keywords})
		}
	}
	wl, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return req, err
	}
	for _, w := range wl {
		req.Watchlist = append(req.Watchlist, w.Instrument.Symbol)
	}
	st, err := s.loadResearchState(ctx, v.ID)
	if err != nil {
		return req, err
	}
	for _, p := range st.Proposals {
		req.Watchlist = append(req.Watchlist, p.proposal().EToroSymbol())
	}
	return req, nil
}

// applyResearch checks every pick again (the port's answer is untrusted too), finds eToro's
// share for it and proposes it or adds it through the watchlist's eligibility path.
func (s *Service) applyResearch(ctx context.Context, v domain.AccountView, actor string, mode domain.ResearchMode, cfg domain.ResearchConfig, picks []domain.ResearchPick, run *ResearchRun) {
	refuse := func(sym, why string) {
		run.Refused = append(run.Refused, domain.ResearchRefusal{Symbol: sym, Reason: why})
	}
	wl, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		run.Error = "reading the watchlist failed: nothing was added"
		s.d.Log.WarnContext(ctx, "research: reading the watchlist failed", "account", v.ID, "err", err)
		return
	}
	onList := map[string]bool{}
	seen := map[domain.InstrumentID]bool{}
	for _, w := range wl {
		onList[strings.ToUpper(w.Instrument.Symbol)] = true
		seen[w.Instrument.ID] = true
	}
	var proposals []domain.ResearchProposal
	for i, raw := range picks {
		if i >= cfg.MaxShares {
			refuse(domain.PrintableSymbol(raw.Symbol), "more shares than asked for")
			continue
		}
		p, err := domain.ValidateResearchPick(raw)
		if err != nil {
			refuse(domain.PrintableSymbol(raw.Symbol), err.Error())
			continue
		}
		if onList[p.EToroSymbol()] {
			refuse(p.Symbol, "already on the watchlist")
			continue
		}
		in, why := s.researchInstrument(ctx, v, p)
		switch {
		case why != "":
			refuse(p.Symbol, why)
			continue
		case seen[in.ID]:
			refuse(p.Symbol, "already on the watchlist")
			continue
		}
		seen[in.ID] = true
		if mode == domain.ResearchPropose {
			proposals = append(proposals, domain.ResearchProposal{ID: s.d.Random.ID(), Instrument: in.ID, ResearchPick: p, At: s.d.Clock.Now().UTC()})
			continue
		}
		if added, err := s.researchAdd(ctx, v, actor, in.ID, p); err != nil {
			refuse(p.Symbol, researchRefusal(err))
		} else {
			run.Added = append(run.Added, added)
		}
	}
	if len(proposals) > 0 {
		stored, err := s.storeProposals(ctx, v.ID, proposals)
		if err != nil {
			run.Error = "storing the proposals failed"
			s.d.Log.WarnContext(ctx, "research: storing proposals failed", "account", v.ID, "err", err)
			return
		}
		run.Proposed = stored
	}
}

// researchRefusal is why a share was not added, for people.
func researchRefusal(err error) string {
	if r, ok := importReason(err); ok {
		return r
	}
	return "adding it failed: " + err.Error()
}

// researchInstrument finds eToro's share with the pick's symbol on its exchange by eToro's
// search (read-only). why is the reason when there is none.
func (s *Service) researchInstrument(ctx context.Context, v domain.AccountView, p domain.ResearchPick) (domain.Instrument, string) {
	sym := p.EToroSymbol()
	want, _ := domain.ExchangeMarket(p.Exchange)
	res, err := s.d.Market.Search(brokerCtx(ctx, v), sym)
	if err != nil {
		s.d.Log.WarnContext(ctx, "research: eToro search failed", "account", v.ID, "symbol", sym, "err", err)
		return domain.Instrument{}, "eToro could not be asked just now"
	}
	for _, in := range res {
		if strings.EqualFold(in.Symbol, sym) && in.ID > 0 && domain.IsShare(in.Type) && in.Market == want {
			return in, ""
		}
	}
	return domain.Instrument{}, "eToro lists no share " + sym + " on " + p.Exchange
}

// researchAdd puts one share on the watchlist through the eligibility path, with origin
// "research", gives it a theme and tells the feed. acceptedBy names the person who accepted a
// proposal ("" = added by research itself).
func (s *Service) researchAdd(ctx context.Context, v domain.AccountView, acceptedBy string, id domain.InstrumentID, p domain.ResearchPick) (ResearchAdded, error) {
	w, err := s.checkAndAddToWatchlist(ctx, v, domain.WatchAddedByResearch, id)
	if err != nil {
		return ResearchAdded{}, err
	}
	in := w.Instrument
	theme := s.researchTheme(ctx, v, in, p)
	who := domain.WatchAddedByResearch
	if acceptedBy != "" && acceptedBy != "system" {
		who = acceptedBy
	}
	s.audit(ctx, Principal{Username: who}, "watchlist.add", v.ID+"/"+strconv.FormatInt(int64(id), 10), in.Symbol+" (web research)")
	detail := p.Name + " (" + p.Exchange + ")"
	if theme != "" {
		detail += " · theme " + theme
	}
	if acceptedBy != "" && acceptedBy != "system" {
		detail += " · accepted by " + acceptedBy
	}
	detail += " · sources: " + strings.Join(p.Sources, " ")
	s.emitFor(ctx, v.ID, domain.WatchAddedByResearch, "watchlist.research_added", domain.SeverityInfo,
		domain.Truncate("Research added "+in.Symbol+" ("+p.Reason+")", 255), detail)
	return ResearchAdded{Instrument: id, Symbol: in.Symbol, Name: in.Name, Theme: theme, Reason: p.Reason}, nil
}

// researchTheme puts a new share in a theme unless it already is in one: the theme the pick
// names when the account has one by that name, else the closest by R27's deterministic
// scoring (symbol and name only: no eToro call). A failure is only logged.
func (s *Service) researchTheme(ctx context.Context, v domain.AccountView, in domain.Instrument, p domain.ResearchPick) string {
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil || len(themes) == 0 {
		return ""
	}
	if i := slices.IndexFunc(themes, func(t domain.Theme) bool { return slices.Contains(t.Instruments, in.ID) }); i >= 0 {
		return themes[i].Name
	}
	var chosen domain.Theme
	if i := slices.IndexFunc(themes, func(t domain.Theme) bool { return p.Theme != "" && strings.EqualFold(t.Name, p.Theme) }); i >= 0 {
		chosen = themes[i]
	} else if best, _, ok := domain.BestTheme(domain.ShareFacts{Symbol: in.Symbol, Name: in.Name}, themes); ok {
		chosen = best
	} else {
		return ""
	}
	if _, err := s.storeThemes(ctx, themes, moveShare(themes, in.ID, chosen.ID)); err != nil {
		s.d.Log.WarnContext(ctx, "research: assigning a theme failed", "account", v.ID, "symbol", in.Symbol, "err", err)
		return ""
	}
	return chosen.Name
}

// storeProposals adds proposals to the account's waiting list (none twice for one share; at
// most domain.MaxResearchProposals, the oldest give way) and returns the ones stored.
func (s *Service) storeProposals(ctx context.Context, accountID string, ps []domain.ResearchProposal) ([]domain.ResearchProposal, error) {
	s.research.mu.Lock()
	defer s.research.mu.Unlock()
	st, err := s.loadResearchState(ctx, accountID)
	if err != nil {
		return nil, err
	}
	var stored []domain.ResearchProposal
	for _, p := range ps {
		if slices.ContainsFunc(st.Proposals, func(x researchProposalJSON) bool { return x.Instrument == int64(p.Instrument) }) {
			continue
		}
		st.Proposals = append(st.Proposals, proposalJSON(p))
		stored = append(stored, p)
	}
	if n := len(st.Proposals); n > domain.MaxResearchProposals {
		st.Proposals = st.Proposals[n-domain.MaxResearchProposals:]
	}
	return stored, s.saveResearchState(ctx, accountID, st)
}

// takeProposal removes a proposal and returns it (ErrNotFound when there is none).
func (s *Service) takeProposal(ctx context.Context, accountID, id string) (domain.ResearchProposal, error) {
	s.research.mu.Lock()
	defer s.research.mu.Unlock()
	st, err := s.loadResearchState(ctx, accountID)
	if err != nil {
		return domain.ResearchProposal{}, err
	}
	i := slices.IndexFunc(st.Proposals, func(x researchProposalJSON) bool { return x.ID == id && id != "" })
	if i < 0 {
		return domain.ResearchProposal{}, fmt.Errorf("research proposal: %w", domain.ErrNotFound)
	}
	p := st.Proposals[i].proposal()
	st.Proposals = slices.Delete(st.Proposals, i, i+1)
	return p, s.saveResearchState(ctx, accountID, st)
}

// putBackProposal returns a proposal that could not be accepted just now (eToro down).
func (s *Service) putBackProposal(ctx context.Context, accountID string, p domain.ResearchProposal) {
	if _, err := s.storeProposals(ctx, accountID, []domain.ResearchProposal{p}); err != nil {
		s.d.Log.WarnContext(ctx, "research: keeping a proposal failed", "account", accountID, "err", err)
	}
}

// ResearchNow runs web research for the current account now: permission agent.control,
// trade access, web session only (it spends money at the LLM provider).
func (s *Service) ResearchNow(ctx context.Context, p Principal) (ResearchRun, error) {
	if err := s.authorize(p, domain.PermAgentControl); err != nil {
		return ResearchRun{}, err
	}
	if p.Via != ViaSession {
		return ResearchRun{}, fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return ResearchRun{}, err
	}
	run, err := s.runResearch(ctx, v, p.Username, false)
	if err != nil {
		return ResearchRun{}, s.fixable(p, v, err)
	}
	s.audit(ctx, p, "research.run", v.ID, fmt.Sprintf("added=%d proposed=%d refused=%d", len(run.Added), len(run.Proposed), len(run.Refused)))
	return run, nil
}

// AcceptResearchProposal puts a waiting proposal on the current account's watchlist through
// the eligibility path (trade access). A share eToro refuses is dropped with the reason; when
// eToro cannot be asked, the proposal stays.
func (s *Service) AcceptResearchProposal(ctx context.Context, p Principal, id string) (ResearchAdded, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return ResearchAdded{}, err
	}
	prop, err := s.takeProposal(ctx, v.ID, id)
	if err != nil {
		return ResearchAdded{}, err
	}
	added, err := s.researchAdd(ctx, v, p.Username, prop.Instrument, prop.ResearchPick)
	if err != nil {
		if !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrConflict) {
			s.putBackProposal(ctx, v.ID, prop) // eToro or the database could not be asked: keep it
		}
		return ResearchAdded{}, err
	}
	s.audit(ctx, p, "research.accept", v.ID, prop.Symbol)
	return added, nil
}

// DismissResearchProposal drops a waiting proposal (trade access).
func (s *Service) DismissResearchProposal(ctx context.Context, p Principal, id string) error {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return err
	}
	prop, err := s.takeProposal(ctx, v.ID, id)
	if err != nil {
		return err
	}
	s.audit(ctx, p, "research.dismiss", v.ID, prop.Symbol)
	return nil
}

// ResearchView is the Watchlist page's Research card for the current account.
type ResearchView struct {
	ResearchMode domain.ResearchMode
	Config       domain.ResearchConfig
	ProfileName  string
	// Ready: research can run (mode not off, runs per day > 0, a usable profile, the
	// researcher available); else Note says why, and Missing what to fix.
	Ready     bool
	Note      string
	Missing   []MissingItem
	RunsUsed  int // in the last 24 hours
	LastRunAt time.Time
	Proposals []domain.ResearchProposal
	// CanRun: the caller may press "Research now"; CanDecide: accept or dismiss proposals.
	CanRun    bool
	CanDecide bool
}

// Research returns the Research card of the current account (view).
func (s *Service) Research(ctx context.Context, p Principal) (ResearchView, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return ResearchView{}, err
	}
	out := ResearchView{ResearchMode: s.ResearchMode(ctx, v.ID), Missing: []MissingItem{}, Proposals: []domain.ResearchProposal{}}
	cfg, err := s.researchConfig(ctx)
	if err != nil {
		return ResearchView{}, fmt.Errorf("app.Research: %w", err)
	}
	out.Config = cfg
	st, err := s.loadResearchState(ctx, v.ID)
	if err != nil {
		return ResearchView{}, fmt.Errorf("app.Research: %w", err)
	}
	runs := recentRuns(st.Runs, s.d.Clock.Now().UTC())
	out.RunsUsed = len(runs)
	if n := len(st.Runs); n > 0 {
		out.LastRunAt = st.Runs[n-1]
	}
	for i := len(st.Proposals) - 1; i >= 0; i-- { // newest first
		out.Proposals = append(out.Proposals, st.Proposals[i].proposal())
	}
	pr, _, perr := s.researchProfile(ctx, cfg)
	if me, ok := errors.AsType[*domain.MissingError](perr); ok {
		out.Missing = append(out.Missing, MissingItem{What: me.What, CanFix: s.canFix(p, v, me.What)})
	} else if perr != nil {
		return ResearchView{}, fmt.Errorf("app.Research: %w", perr)
	}
	out.ProfileName = pr.Name
	switch {
	case out.ResearchMode == domain.ResearchOff:
		out.Note = "Web research is off for " + v.Name + "."
	case cfg.RunsPerDay == 0:
		out.Note = "Web research is switched off (0 runs a day on the LLM page)."
	case perr != nil:
		out.Note = perr.Error()
	case s.d.Researcher == nil || s.needBroker() != nil:
		out.Note = "Web research is not available in this deployment."
	default:
		out.Ready = true
	}
	out.CanDecide = v.Access.AtLeast(domain.AccessTrade)
	out.CanRun = out.CanDecide && p.Via == ViaSession && p.Can(domain.PermAgentControl)
	return out, nil
}

// ResearchSetup is the Research section of the LLM page.
type ResearchSetup struct {
	Config domain.ResearchConfig
	// ProfileID and ProfileName are the profile research uses now ("" = none usable); Note
	// says why research cannot run, for people ("" = it can).
	ProfileID   string
	ProfileName string
	Note        string
	Missing     []MissingItem
}

// ResearchSettings returns the research settings (viewer, like the LLM page).
func (s *Service) ResearchSettings(ctx context.Context, p Principal) (ResearchSetup, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return ResearchSetup{}, err
	}
	cfg, err := s.researchConfig(ctx)
	if err != nil {
		return ResearchSetup{}, fmt.Errorf("app.ResearchSettings: %w", err)
	}
	out := ResearchSetup{Config: cfg, Missing: []MissingItem{}}
	pr, _, err := s.researchProfile(ctx, cfg)
	if me, ok := errors.AsType[*domain.MissingError](err); ok {
		out.Note = me.Msg
		out.Missing = append(out.Missing, MissingItem{What: me.What, CanFix: s.canFix(p, domain.AccountView{}, me.What)})
	} else if err != nil {
		return ResearchSetup{}, fmt.Errorf("app.ResearchSettings: %w", err)
	} else {
		out.ProfileID, out.ProfileName = pr.ID, pr.Name
	}
	if cfg.RunsPerDay == 0 && out.Note == "" {
		out.Note = "Web research is switched off (0 runs a day)."
	}
	return out, nil
}

// SetResearchSettings stores the research settings (admin, web session; audited). A chosen
// profile must exist and its provider must have a built-in web search.
func (s *Service) SetResearchSettings(ctx context.Context, p Principal, c domain.ResearchConfig) (ResearchSetup, error) {
	if err := s.llmAdmin(p); err != nil {
		return ResearchSetup{}, err
	}
	c.ProfileID = strings.TrimSpace(c.ProfileID)
	if err := c.Validate(); err != nil {
		return ResearchSetup{}, err
	}
	if c.ProfileID != "" {
		_, ps, err := s.loadLLM(ctx)
		if err != nil {
			return ResearchSetup{}, fmt.Errorf("app.SetResearchSettings: %w", err)
		}
		i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == c.ProfileID })
		if i < 0 {
			return ResearchSetup{}, domain.Invalid("profileId", "no such profile")
		}
		if prov, _ := domain.LLMProviderByID(ps[i].Provider); !prov.WebSearch {
			return ResearchSetup{}, domain.Invalid("profileId", ps[i].Name+": research not supported by "+cmpOr(prov.Label, string(ps[i].Provider))+" (no built-in web search here)")
		}
	}
	b, err := json.Marshal(researchConfigJSON{ProfileID: c.ProfileID, MaxShares: c.MaxShares, RunsPerDay: c.RunsPerDay})
	if err != nil {
		return ResearchSetup{}, err
	}
	if err := s.d.Store.Settings().Set(ctx, settingResearch, string(b), s.d.Clock.Now().UTC()); err != nil {
		return ResearchSetup{}, fmt.Errorf("app.SetResearchSettings: %w", err)
	}
	s.audit(ctx, p, "research.settings", cmpOr(c.ProfileID, "default"), fmt.Sprintf("maxShares=%d runsPerDay=%d", c.MaxShares, c.RunsPerDay))
	return s.ResearchSettings(ctx, p)
}

// researchDue reports whether the daily run is due: a market opens within researchLead and
// the last scheduled run is at least researchScheduledGap old. Without a calendar it is
// never due (only "Research now" runs).
func (s *Service) researchDue(now, last time.Time) bool {
	if s.d.Calendar == nil || (!last.IsZero() && now.Sub(last) < researchScheduledGap) {
		return false
	}
	for _, m := range domain.Markets {
		open, _, trading, err := s.d.Calendar.Session(m, now.Add(researchLead))
		if err == nil && trading && open.After(now) && !open.After(now.Add(researchLead)) {
			return true
		}
	}
	return false
}

// ResearchAll is the background runner: for every eToro account whose research is not off,
// one run a day before the first market open. Accounts run one after another; errors are
// logged and become events, nothing is added then.
func (s *Service) ResearchAll(ctx context.Context) {
	cfg, err := s.researchConfig(ctx)
	if err != nil || cfg.RunsPerDay == 0 || s.d.Researcher == nil {
		return
	}
	if _, _, err := s.researchProfile(ctx, cfg); err != nil {
		s.d.Log.DebugContext(ctx, "research: no usable profile", "err", err)
		return
	}
	accts, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "research: listing accounts failed", "err", err)
		return
	}
	now := s.d.Clock.Now().UTC()
	for _, a := range accts {
		if ctx.Err() != nil {
			return
		}
		if !a.Tradable() || s.ResearchMode(ctx, a.ID) == domain.ResearchOff {
			continue
		}
		st, err := s.loadResearchState(ctx, a.ID)
		if err != nil || !s.researchDue(now, st.ScheduledAt) {
			continue
		}
		v := domain.AccountView{Account: a, Access: domain.AccessOwner}
		run, err := s.runResearch(ctx, v, "system", true)
		actx := ports.WithAccount(ctx, a)
		if err != nil {
			s.d.Log.WarnContext(actx, "research run refused", "account", a.ID, "err", err)
			continue
		}
		s.d.Log.InfoContext(actx, "research run", "account", a.ID, "added", len(run.Added), "proposed", len(run.Proposed), "refused", len(run.Refused), "failed", run.Error != "")
	}
}
