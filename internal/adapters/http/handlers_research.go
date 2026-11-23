package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/domain"
)

func toTheme(t domain.Theme) Theme {
	ids := make([]int64, len(t.Instruments))
	for i, id := range t.Instruments {
		ids[i] = int64(id)
	}
	kws := t.Keywords
	if kws == nil {
		kws = []string{}
	}
	return Theme{Id: t.ID, Name: t.Name, Keywords: kws, IndustryId: t.IndustryID, Enabled: t.Enabled, Instruments: ids, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

func fromThemeInput(id string, in ThemeInput) domain.Theme {
	ids := make([]domain.InstrumentID, len(in.Instruments))
	for i, x := range in.Instruments {
		ids[i] = domain.InstrumentID(x)
	}
	return domain.Theme{ID: id, Name: in.Name, Keywords: in.Keywords, IndustryID: in.IndustryId, Enabled: in.Enabled, Instruments: ids}
}

// ListThemes lists the current account's themes.
func (s *Server) ListThemes(w http.ResponseWriter, r *http.Request) {
	ts, err := s.svc.Themes(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Theme, 0, len(ts))
	for _, t := range ts {
		out = append(out, toTheme(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateTheme adds a theme.
func (s *Server) CreateTheme(w http.ResponseWriter, r *http.Request) {
	var req ThemeInput
	if !decode(w, r, &req) {
		return
	}
	t, err := s.svc.SaveTheme(r.Context(), state(r).principal, fromThemeInput("", req))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTheme(t))
}

// UpdateTheme changes a theme.
func (s *Server) UpdateTheme(w http.ResponseWriter, r *http.Request, themeID string) {
	var req ThemeInput
	if !decode(w, r, &req) {
		return
	}
	t, err := s.svc.SaveTheme(r.Context(), state(r).principal, fromThemeInput(themeID, req))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTheme(t))
}

// DeleteTheme removes a theme.
func (s *Server) DeleteTheme(w http.ResponseWriter, r *http.Request, themeID string) {
	if err := s.svc.DeleteTheme(r.Context(), state(r).principal, themeID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListIndustries lists eToro's stock industries.
func (s *Server) ListIndustries(w http.ResponseWriter, r *http.Request) {
	inds, err := s.svc.Industries(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Industry, 0, len(inds))
	for _, i := range inds {
		out = append(out, Industry{Id: i.ID, Name: i.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

// ListReviews returns the review board.
func (s *Server) ListReviews(w http.ResponseWriter, r *http.Request, params ListReviewsParams) {
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	rs, on, err := s.svc.Reviews(r.Context(), state(r).principal, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := ReviewBoard{ScannerEnabled: on, Reviews: make([]Review, 0, len(rs))}
	for _, v := range rs {
		x := Review{InstrumentId: int64(v.Instrument), Symbol: v.Symbol, Theme: v.Theme, At: v.At, Outcome: ReviewOutcome(v.Outcome), Summary: v.Summary, Checks: []ReviewCheck{}}
		if v.Direction != "" {
			d := ReviewDirection(v.Direction)
			x.Direction = &d
		}
		if len(v.Probs) > 0 {
			p := map[string]float32{}
			for k, f := range v.Probs {
				p[string(k)] = float32(f)
			}
			x.Probs = &p
		}
		for _, c := range v.Checks {
			rc := ReviewCheck{Rule: c.Rule, Pass: c.Pass}
			if c.Detail != "" {
				rc.Detail = ptr(c.Detail)
			}
			x.Checks = append(x.Checks, rc)
		}
		out.Reviews = append(out.Reviews, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// SetScanner switches the scanner.
func (s *Server) SetScanner(w http.ResponseWriter, r *http.Request) {
	var req ScannerSwitch
	if !decode(w, r, &req) {
		return
	}
	if err := s.svc.SetScanner(r.Context(), state(r).principal, req.Enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunScanner reviews now.
func (s *Server) RunScanner(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.ScanNow(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	added := res.Added
	if added == nil {
		added = []string{}
	}
	writeJSON(w, http.StatusOK, ScanResult{Reviewed: res.Reviewed, Added: added})
}
