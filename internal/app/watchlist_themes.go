package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/albievan/cactustrade/internal/domain"
)

// Watchlist themes, auto-assign, export and import (PLAN R27). Themes keep their model (a
// theme's picked shares are Theme.Instruments); a watchlist share is in at most one theme of
// its account (R27-a).

// MaxThemeAssignments bounds one assignment request.
const MaxThemeAssignments = 200

// industryScreenSize is how many shares of one eToro industry auto-assign reads (the most
// popular first; eToro's page size limit is 100).
const industryScreenSize = 100

// ThemeAssignment puts one watchlist share into a theme ("" = no theme).
type ThemeAssignment struct {
	Instrument domain.InstrumentID
	ThemeID    string
}

// ThemeProposal is auto-assign's suggestion for one watchlist share.
type ThemeProposal struct {
	Instrument domain.InstrumentID
	Symbol     string
	ThemeID    string
	ThemeName  string
	Match      domain.ThemeMatch
}

// ThemeProposals are auto-assign's suggestions; Unmatched names the shares without a theme
// that no theme fits; Notes say what could not be scored.
type ThemeProposals struct {
	Proposals []ThemeProposal
	Unmatched []string
	Notes     []string
}

// ImportSkip is a share or theme of a file that was not imported, and why.
type ImportSkip struct {
	Instrument domain.InstrumentID // 0 for a theme
	Name       string
	Reason     string
}

// ImportResult is what an import did.
type ImportResult struct {
	Added           []string
	Already         []string
	Skipped         []ImportSkip
	ThemesCreated   []string
	ThemesUpdated   []string
	ThemesUnchanged []string
	ThemesSkipped   []ImportSkip
	Notes           []string
}

// AssignThemes puts watchlist shares of the current account into its themes (trade access):
// each share is added to its theme's picked shares and removed from every other theme of
// the account; ThemeID "" removes it from all. Everything is checked before anything is
// stored. It returns how many themes were stored.
func (s *Service) AssignThemes(ctx context.Context, p Principal, as []ThemeAssignment) (int, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return 0, err
	}
	if len(as) == 0 || len(as) > MaxThemeAssignments {
		return 0, domain.Invalid("assignments", "1-"+strconv.Itoa(MaxThemeAssignments))
	}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return 0, fmt.Errorf("app.AssignThemes: %w", err)
	}
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return 0, fmt.Errorf("app.AssignThemes: %w", err)
	}
	for _, a := range as {
		if !slices.ContainsFunc(items, func(w domain.WatchItem) bool { return w.Instrument.ID == a.Instrument }) {
			return 0, fmt.Errorf("instrument %d is not on the watchlist: %w", a.Instrument, domain.ErrNotFound)
		}
		if a.ThemeID != "" && !slices.ContainsFunc(themes, func(t domain.Theme) bool { return t.ID == a.ThemeID }) {
			return 0, fmt.Errorf("theme: %w", domain.ErrNotFound)
		}
	}
	changed := map[string]bool{}
	for _, a := range as {
		for id := range moveShare(themes, a.Instrument, a.ThemeID) {
			changed[id] = true
		}
	}
	n, err := s.storeThemes(ctx, themes, changed)
	if err != nil {
		return 0, err
	}
	s.audit(ctx, p, "theme.assign", v.ID, strconv.Itoa(len(as))+" shares, "+strconv.Itoa(n)+" themes changed")
	return n, nil
}

// moveShare puts share id into theme themeID ("" = none) and takes it out of every other
// theme, in place. It returns the ids of the themes it changed.
func moveShare(themes []domain.Theme, id domain.InstrumentID, themeID string) map[string]bool {
	changed := map[string]bool{}
	for i := range themes {
		t := &themes[i]
		has := slices.Contains(t.Instruments, id)
		switch want := t.ID == themeID; {
		case want && !has:
			t.Instruments = append(slices.Clone(t.Instruments), id)
			changed[t.ID] = true
		case !want && has:
			t.Instruments = slices.DeleteFunc(slices.Clone(t.Instruments), func(x domain.InstrumentID) bool { return x == id })
			changed[t.ID] = true
		}
	}
	return changed
}

// storeThemes checks and stores the changed themes. A theme may be left with no picked
// share and nothing else (R27-b): it then finds nothing until it is edited, which is what
// taking its last share away means.
func (s *Service) storeThemes(ctx context.Context, themes []domain.Theme, changed map[string]bool) (int, error) {
	for _, t := range themes {
		if changed[t.ID] && len(t.Instruments) > domain.MaxThemeInstruments {
			return 0, domain.Invalid("themeId", "the theme "+t.Name+" holds at most "+strconv.Itoa(domain.MaxThemeInstruments)+" shares")
		}
	}
	now := s.d.Clock.Now().UTC()
	n := 0
	for _, t := range themes {
		if !changed[t.ID] {
			continue
		}
		t.UpdatedAt = now
		if err := s.d.Store.Themes().Update(ctx, t); err != nil {
			return n, fmt.Errorf("app.storeThemes: %w", err)
		}
		n++
	}
	return n, nil
}

// ProposeThemes suggests the closest theme for every watchlist share of the current account
// that has none (trade access). Nothing is stored: the user accepts proposals through
// AssignThemes. The score is deterministic (domain.ScoreTheme), no LLM is asked. Industries
// are read from eToro (one screen per theme industry, read-only market data); when that
// fails the industry is not scored and a note says so.
func (s *Service) ProposeThemes(ctx context.Context, p Principal) (ThemeProposals, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return ThemeProposals{}, err
	}
	out := ThemeProposals{Proposals: []ThemeProposal{}, Unmatched: []string{}, Notes: []string{}}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return ThemeProposals{}, fmt.Errorf("app.ProposeThemes: %w", err)
	}
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return ThemeProposals{}, fmt.Errorf("app.ProposeThemes: %w", err)
	}
	themeOf := shareThemes(themes)
	var open []domain.WatchItem
	for _, it := range items {
		if _, ok := themeOf[it.Instrument.ID]; !ok {
			open = append(open, it)
		}
	}
	if len(open) == 0 {
		return out, nil
	}
	if len(themes) == 0 {
		out.Notes = append(out.Notes, "There are no themes yet: add one on the Themes page.")
		for _, it := range open {
			out.Unmatched = append(out.Unmatched, it.Instrument.Symbol)
		}
		return out, nil
	}
	industryOf, names, notes := s.shareIndustries(ctx, v, themes, open)
	out.Notes = append(out.Notes, notes...)
	for _, it := range open {
		f := domain.ShareFacts{Symbol: it.Instrument.Symbol, Name: it.Instrument.Name, IndustryID: industryOf[it.Instrument.ID]}
		f.IndustryName = names[f.IndustryID]
		t, m, ok := domain.BestTheme(f, themes)
		if !ok {
			out.Unmatched = append(out.Unmatched, it.Instrument.Symbol)
			continue
		}
		out.Proposals = append(out.Proposals, ThemeProposal{Instrument: it.Instrument.ID, Symbol: it.Instrument.Symbol, ThemeID: t.ID, ThemeName: t.Name, Match: m})
	}
	return out, nil
}

// shareIndustries finds the eToro industry of the open shares among the themes' industries
// and the names of those industries.
func (s *Service) shareIndustries(ctx context.Context, v domain.AccountView, themes []domain.Theme, open []domain.WatchItem) (map[domain.InstrumentID]int64, map[int64]string, []string) {
	var inds []int64
	for _, t := range themes {
		if t.IndustryID > 0 && !slices.Contains(inds, t.IndustryID) {
			inds = append(inds, t.IndustryID)
		}
	}
	slices.Sort(inds)
	industryOf, names := map[domain.InstrumentID]int64{}, map[int64]string{}
	if len(inds) == 0 {
		return industryOf, names, nil
	}
	if s.d.Research == nil {
		return industryOf, names, []string{"eToro's industries are not available: industries were not scored."}
	}
	wanted := map[domain.InstrumentID]bool{}
	for _, it := range open {
		wanted[it.Instrument.ID] = true
	}
	var notes []string
	bctx := brokerCtx(ctx, v)
	for _, ind := range inds {
		res, err := s.d.Research.Screen(bctx, ind, industryScreenSize)
		if err != nil {
			s.d.Log.WarnContext(ctx, "auto-assign: industry screen failed", "account", v.ID, "industry", ind, "err", err)
			notes = append(notes, "eToro industry #"+strconv.FormatInt(ind, 10)+" could not be read: it was not scored.")
			continue
		}
		for _, in := range res {
			if _, ok := industryOf[in.ID]; !ok && wanted[in.ID] {
				industryOf[in.ID] = ind
			}
		}
	}
	if len(industryOf) > 0 {
		all, err := s.d.Research.Industries(bctx)
		if err != nil {
			s.d.Log.WarnContext(ctx, "auto-assign: industries failed", "account", v.ID, "err", err)
			notes = append(notes, "eToro's industry names could not be read: search words were not matched against them.")
		}
		for _, x := range all {
			names[x.ID] = x.Name
		}
	}
	return industryOf, names, notes
}

// ExportWatchlist returns the current account's watchlist and themes as a file (trade
// access). It holds no secret and no account id; LLM profiles are named, never copied.
func (s *Service) ExportWatchlist(ctx context.Context, p Principal) (domain.WatchlistFile, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return domain.WatchlistFile{}, err
	}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return domain.WatchlistFile{}, fmt.Errorf("app.ExportWatchlist: %w", err)
	}
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return domain.WatchlistFile{}, fmt.Errorf("app.ExportWatchlist: %w", err)
	}
	_, profiles, err := s.loadLLM(ctx)
	if err != nil {
		return domain.WatchlistFile{}, fmt.Errorf("app.ExportWatchlist: %w", err)
	}
	f := domain.WatchlistFile{
		Version: domain.WatchlistFileVersion, ExportedAt: s.d.Clock.Now().UTC(), AccountName: v.Name,
		Watchlist: make([]domain.FileShare, 0, len(items)), Themes: make([]domain.FileTheme, 0, len(themes)),
	}
	for _, it := range items {
		f.Watchlist = append(f.Watchlist, domain.FileShare{Instrument: it.Instrument.ID, Symbol: it.Instrument.Symbol, Market: string(it.Instrument.Market)})
	}
	for _, t := range themes {
		ft := domain.FileTheme{
			Name: t.Name, Keywords: append([]string{}, t.Keywords...), IndustryID: t.IndustryID,
			Instruments: append([]domain.InstrumentID{}, t.Instruments...),
		}
		if i := slices.IndexFunc(profiles, func(x domain.LLMProfile) bool { return x.ID == t.LLMProfileID }); t.LLMProfileID != "" && i >= 0 {
			ft.LLMProfileName = profiles[i].Name
		}
		f.Themes = append(f.Themes, ft)
	}
	s.audit(ctx, p, "watchlist.export", v.ID, strconv.Itoa(len(f.Watchlist))+" shares, "+strconv.Itoa(len(f.Themes))+" themes")
	return f, nil
}

// ImportWatchlist merges a watchlist file into the current account (trade access, web
// session only). The file is checked whole first (domain.ValidateWatchlistFile). Every share
// not yet on the watchlist goes through the same eToro check as adding by hand
// (addToWatchlist); refused ones are reported, never added. A theme with the same name
// (case-insensitive) is updated, any other created, through the same path as the Themes
// page; each imported theme's shares leave every other theme (one theme per share).
// Importing the same file again changes nothing.
func (s *Service) ImportWatchlist(ctx context.Context, p Principal, f domain.WatchlistFile) (ImportResult, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return ImportResult{}, err
	}
	if p.Via != ViaSession {
		return ImportResult{}, fmt.Errorf("importing a watchlist is only possible from the web UI: %w", domain.ErrForbidden)
	}
	if f, err = domain.ValidateWatchlistFile(f); err != nil {
		return ImportResult{}, err
	}
	res := ImportResult{
		Added: []string{}, Already: []string{}, Skipped: []ImportSkip{}, ThemesCreated: []string{},
		ThemesUpdated: []string{}, ThemesUnchanged: []string{}, ThemesSkipped: []ImportSkip{}, Notes: []string{},
	}
	if err := s.importShares(ctx, v, p.Username, f.Watchlist, true, &res); err != nil {
		return ImportResult{}, err
	}
	if err := s.importThemes(ctx, p, v, f.Themes, &res); err != nil {
		return ImportResult{}, err
	}
	summary := fmt.Sprintf("%d added, %d already there, %d refused; themes %d created, %d updated, %d unchanged, %d refused",
		len(res.Added), len(res.Already), len(res.Skipped), len(res.ThemesCreated), len(res.ThemesUpdated), len(res.ThemesUnchanged), len(res.ThemesSkipped))
	s.audit(ctx, p, "watchlist.import", v.ID, summary)
	s.emitFor(ctx, v.ID, p.Username, "watchlist.imported", domain.SeverityInfo, "Watchlist file imported into "+v.Name, summary)
	return res, nil
}

// importShares adds the shares through addToWatchlist. fileMarket: the file names each
// share's market and an unsupported one is refused before eToro is asked (a platform
// watchlist names none; eToro's instrument decides, as when adding by hand).
func (s *Service) importShares(ctx context.Context, v domain.AccountView, actor string, shares []domain.FileShare, fileMarket bool, res *ImportResult) error {
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return fmt.Errorf("app.ImportWatchlist: %w", err)
	}
	on := map[domain.InstrumentID]string{}
	for _, it := range items {
		on[it.Instrument.ID] = it.Instrument.Symbol
	}
	for _, sh := range shares {
		name := cmp.Or(sh.Symbol, "#"+strconv.FormatInt(int64(sh.Instrument), 10))
		if sym, ok := on[sh.Instrument]; ok {
			res.Already = append(res.Already, cmp.Or(sym, name))
			continue
		}
		if fileMarket && !domain.SupportedMarket(sh.Market) {
			res.Skipped = append(res.Skipped, ImportSkip{
				Instrument: sh.Instrument, Name: name,
				Reason: "market “" + sh.Market + "” is not traded here: only London (LSE), New York (NYSE/NASDAQ) and Sydney (ASX)",
			})
			continue
		}
		it, err := s.addToWatchlist(ctx, v, actor, sh.Instrument)
		if err == nil {
			res.Added = append(res.Added, it.Instrument.Symbol)
			on[sh.Instrument] = it.Instrument.Symbol
			continue
		}
		reason, ok := importReason(err)
		if !ok {
			return fmt.Errorf("app.ImportWatchlist: %w", err)
		}
		if errors.Is(err, domain.ErrConflict) {
			res.Already = append(res.Already, name)
			continue
		}
		res.Skipped = append(res.Skipped, ImportSkip{Instrument: sh.Instrument, Name: name, Reason: reason})
	}
	return nil
}

// importReason is a refusal for people, and false for an error that must end the import
// (a store failure).
func importReason(err error) (string, bool) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return ve.Reason, true
	case errors.Is(err, domain.ErrConflict):
		return "already on the watchlist", true
	case errors.Is(err, domain.ErrUnavailable):
		return "eToro could not be asked just now: import the file again later", true
	}
	return "", false
}

// importThemes creates or updates the file's themes through saveTheme, then makes each
// imported theme's shares leave every other theme of the account.
func (s *Service) importThemes(ctx context.Context, p Principal, v domain.AccountView, fts []domain.FileTheme, res *ImportResult) error {
	if len(fts) == 0 {
		return nil
	}
	existing, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return fmt.Errorf("app.ImportWatchlist: %w", err)
	}
	_, profiles, err := s.loadLLM(ctx)
	if err != nil {
		return fmt.Errorf("app.ImportWatchlist: %w", err)
	}
	var kept []string // ids of the file's themes as stored now
	for _, ft := range fts {
		t := domain.Theme{Name: ft.Name, Keywords: ft.Keywords, IndustryID: ft.IndustryID, Instruments: ft.Instruments, Enabled: true}
		if ft.LLMProfileName != "" {
			if i := slices.IndexFunc(profiles, func(x domain.LLMProfile) bool { return strings.EqualFold(x.Name, ft.LLMProfileName) }); i >= 0 {
				t.LLMProfileID = profiles[i].ID
			} else {
				res.Notes = append(res.Notes, "LLM reviewer profile “"+ft.LLMProfileName+"” does not exist here: "+ft.Name+" uses the default reviewer.")
			}
		}
		i := slices.IndexFunc(existing, func(x domain.Theme) bool { return strings.EqualFold(x.Name, ft.Name) })
		if i >= 0 {
			t.ID, t.Enabled = existing[i].ID, existing[i].Enabled
			if sameTheme(existing[i], t) {
				res.ThemesUnchanged = append(res.ThemesUnchanged, existing[i].Name)
				kept = append(kept, t.ID)
				continue
			}
		}
		saved, err := s.saveTheme(ctx, p, t)
		if err != nil {
			reason, ok := importReason(err)
			if !ok {
				return fmt.Errorf("app.ImportWatchlist: %w", err)
			}
			res.ThemesSkipped = append(res.ThemesSkipped, ImportSkip{Name: ft.Name, Reason: reason})
			continue
		}
		kept = append(kept, saved.ID)
		if i >= 0 {
			res.ThemesUpdated = append(res.ThemesUpdated, saved.Name)
		} else {
			res.ThemesCreated = append(res.ThemesCreated, saved.Name)
		}
	}
	return s.oneThemePerShare(ctx, v, kept, res)
}

// oneThemePerShare takes the shares of the kept themes out of every other theme of the
// account and notes each move.
func (s *Service) oneThemePerShare(ctx context.Context, v domain.AccountView, kept []string, res *ImportResult) error {
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return fmt.Errorf("app.ImportWatchlist: %w", err)
	}
	changed := map[string]bool{}
	for _, id := range kept {
		i := slices.IndexFunc(themes, func(t domain.Theme) bool { return t.ID == id })
		if i < 0 {
			continue
		}
		for _, share := range slices.Clone(themes[i].Instruments) {
			for tid := range moveShare(themes, share, id) {
				changed[tid] = true
			}
		}
	}
	for _, t := range themes {
		if changed[t.ID] {
			res.Notes = append(res.Notes, "Shares of the imported themes were taken out of "+t.Name+" (one theme per share).")
		}
	}
	if _, err := s.storeThemes(ctx, themes, changed); err != nil {
		return fmt.Errorf("app.ImportWatchlist: %w", err)
	}
	return nil
}

// sameTheme reports whether storing b over a would change nothing a file carries.
func sameTheme(a, b domain.Theme) bool {
	return a.Name == b.Name && slices.Equal(a.Keywords, b.Keywords) && a.IndustryID == b.IndustryID &&
		slices.Equal(a.Instruments, b.Instruments) && a.LLMProfileID == b.LLMProfileID
}
