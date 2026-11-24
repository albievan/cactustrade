package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// The analysis page (R31): every trade of a chosen period on the accounts the caller can see
// (or a chosen few), grouped and explained, and the same as a one-page PDF. It is a view of
// stored records (trades, equity snapshots, themes, decisions, events, LLM calls): it calls no
// broker, and nothing here is read by an order path.

const (
	// analysisMaxAccounts bounds the accounts one request may name.
	analysisMaxAccounts = 50
	// analysisDecisionLookback is how long before the period decisions are read to name the
	// playbook of a trade opened before it closed (positions are flattened every session).
	analysisDecisionLookback = 3 * 24 * time.Hour
)

// AnalysisRequest chooses an analysis: a preset ("" = custom when From or To is set, else
// this month), a custom range (risk days YYYY-MM-DD, inclusive) and the accounts (none = every
// account the caller can see).
type AnalysisRequest struct {
	Preset     string
	From, To   string
	AccountIDs []string
}

// Analysis builds the analysis of the request (viewer; view access to every chosen account,
// any other account is "not found").
func (s *Service) Analysis(ctx context.Context, p Principal, req AnalysisRequest) (domain.Analysis, error) {
	in, err := s.analysisInput(ctx, p, req)
	if err != nil {
		return domain.Analysis{}, err
	}
	return domain.BuildAnalysis(in), nil
}

// AnalysisPDF renders the analysis of the request as a one-page PDF (as Analysis).
func (s *Service) AnalysisPDF(ctx context.Context, p Principal, req AnalysisRequest) (StatementFile, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return StatementFile{}, err
	}
	if s.d.Analysis == nil {
		return StatementFile{}, fmt.Errorf("the analysis PDF is not available: %w", domain.ErrUnavailable)
	}
	in, err := s.analysisInput(ctx, p, req)
	if err != nil {
		return StatementFile{}, err
	}
	an := domain.BuildAnalysis(in)
	data, err := s.d.Analysis.RenderAnalysis(ctx, an)
	if err != nil {
		return StatementFile{}, fmt.Errorf("app.AnalysisPDF: %w", err)
	}
	return StatementFile{Name: domain.AnalysisFileName(an.Window), Data: data}, nil
}

// analysisInput resolves the period and the accounts and reads their records.
func (s *Service) analysisInput(ctx context.Context, p Principal, req AnalysisRequest) (domain.AnalysisInput, error) {
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return domain.AnalysisInput{}, err
	}
	preset := domain.AnalysisPresetMonth
	switch {
	case req.Preset != "":
		if preset, err = domain.ParseAnalysisPreset(req.Preset); err != nil {
			return domain.AnalysisInput{}, err
		}
	case req.From != "" || req.To != "":
		preset = domain.AnalysisPresetCustom
	}
	now := s.d.Clock.Now().UTC()
	w, err := domain.AnalysisWindowFor(preset, req.From, req.To, now)
	if err != nil {
		return domain.AnalysisInput{}, err
	}
	accts, err := chosenAccounts(vs, req.AccountIDs)
	if err != nil {
		return domain.AnalysisInput{}, err
	}
	in := domain.AnalysisInput{Window: w, GeneratedAt: now, AllAccounts: len(req.AccountIDs) == 0}
	share := s.shareInfo(ctx)
	for _, v := range accts {
		a, err := s.analysisAccount(ctx, v.Account, w, share)
		if err != nil {
			return domain.AnalysisInput{}, err
		}
		in.Accounts = append(in.Accounts, a)
	}
	return in, nil
}

// chosenAccounts are the accounts named by ids among those the caller can see (all of them
// without ids). An id the caller cannot see is "not found", like every other account route.
func chosenAccounts(vs []domain.AccountView, ids []string) ([]domain.AccountView, error) {
	if len(ids) == 0 {
		return vs, nil
	}
	if len(ids) > analysisMaxAccounts {
		return nil, domain.Invalid("accountId", "at most 50 accounts")
	}
	var out []domain.AccountView
	for _, id := range ids {
		i := slices.IndexFunc(vs, func(v domain.AccountView) bool { return v.ID == id })
		if i < 0 || !vs[i].Access.AtLeast(domain.AccessView) {
			return nil, fmt.Errorf("account: %w", domain.ErrNotFound)
		}
		if !slices.ContainsFunc(out, func(v domain.AccountView) bool { return v.ID == id }) {
			out = append(out, vs[i])
		}
	}
	return out, nil
}

// analysisAccount reads one account's records for the window. A view-only account has
// equity snapshots only (its holdings are not read: an analysis is of recorded history).
func (s *Service) analysisAccount(ctx context.Context, a domain.Account, w domain.AnalysisWindow,
	share func(domain.InstrumentID) (string, domain.Market),
) (domain.AnalysisAccountInput, error) {
	in := domain.AnalysisAccountInput{Account: a}
	snaps, err := s.snapshots(ctx, a.ID, w.From, w.To)
	if err != nil {
		return in, err
	}
	in.Snapshots = snaps
	if !a.Tradable() {
		return in, nil
	}
	ts, err := s.closedTradeRows(ctx, a.ID, w.From, w.To, nil)
	if err != nil {
		return in, err
	}
	for _, t := range ts {
		sym, m := share(t.Instrument)
		in.Trades = append(in.Trades, domain.StatementTrade{Trade: t, Symbol: sym, Market: m})
	}
	if in.Themes, err = s.d.Store.Themes().List(ctx, a.ID); err != nil {
		return in, fmt.Errorf("app.analysis: %w", err)
	}
	ds, err := s.d.Store.Decisions().List(ctx, a.ID, w.To, "", statementActivityCap+1)
	if err != nil {
		return in, fmt.Errorf("app.analysis: %w", err)
	}
	if len(ds) > statementActivityCap {
		ds = ds[:statementActivityCap]
		in.ActivityCapped = !ds[len(ds)-1].At.Before(w.From)
	}
	since := w.From.Add(-analysisDecisionLookback)
	for _, d := range ds {
		if !d.At.Before(since) {
			in.Decisions = append(in.Decisions, d)
		}
	}
	es, err := s.d.Store.Events().List(ctx, []string{a.ID}, statementActivityCap)
	if err != nil {
		return in, fmt.Errorf("app.analysis: %w", err)
	}
	if len(es) == statementActivityCap && !es[len(es)-1].At.Before(w.From) {
		in.ActivityCapped = true
	}
	for _, e := range es {
		if e.AccountID == a.ID && w.Contains(e.At) {
			in.Events = append(in.Events, e)
		}
	}
	if in.LLM, err = s.d.Store.LLMCalls().UsageFor(ctx, []string{a.ID}, w.From, w.To); err != nil {
		return in, fmt.Errorf("app.analysis: %w", err)
	}
	return in, nil
}
