package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// Reset all trading data (PLAN R49): the use cases decide everything; these decode, map the
// refusals and encode.

// failDataReset maps a refused reset: the failing checks as 409, the rest as the agent
// actions do (a wrong phrase 422).
func (s *Server) failDataReset(w http.ResponseWriter, r *http.Request, err error) {
	if rr, ok := errors.AsType[*domain.DataResetRefusal](err); ok {
		details := make([]string, 0, len(rr.Failed))
		for _, c := range rr.Failed {
			details = append(details, c.Detail)
		}
		problemFor(w, r, err, http.StatusConflict, "All trading data cannot be reset now", strings.Join(details, "; "), "")
		return
	}
	s.failAgent(w, r, err)
}

func toDataResetChecks(cs []domain.GateCheck) []DataResetCheck {
	out := make([]DataResetCheck, 0, len(cs))
	for _, c := range cs {
		out = append(out, DataResetCheck{Name: DataResetCheckName(c.Name), Pass: c.Pass, Detail: c.Detail})
	}
	return out
}

func toTableCounts(m map[string]int64) TableCounts {
	out := TableCounts{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

func toDataResetPreview(pv app.DataResetPreview) DataResetPreview {
	out := DataResetPreview{
		Checks: toDataResetChecks(pv.Checks), CanReset: pv.CanReset, Wiped: toTableCounts(pv.Wiped), Kept: toTableCounts(pv.Kept),
		RealAccounts: make([]RealEvidenceWarning, 0, len(pv.RealAccounts)), BackupReady: pv.BackupReady, Phrase: pv.Phrase,
	}
	for _, w := range pv.RealAccounts {
		rw := RealEvidenceWarning{AccountId: w.AccountID, Name: w.Name, DaysNow: w.DaysNow, Required: w.Required}
		if w.DemoAccountID != "" {
			rw.DemoAccountId, rw.DemoName = ptr(w.DemoAccountID), ptr(w.DemoName)
		}
		out.RealAccounts = append(out.RealAccounts, rw)
	}
	if pv.BackupWhy != "" {
		out.BackupWhy = ptr(pv.BackupWhy)
	}
	if pv.BackupFolder != "" {
		out.BackupFolder = ptr(pv.BackupFolder)
	}
	return out
}

// GetDataResetPreview answers what a reset would do now (admin, web session).
func (s *Server) GetDataResetPreview(w http.ResponseWriter, r *http.Request) {
	pv, err := s.svc.DataResetPreview(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toDataResetPreview(pv))
}

// ResetAllData resets all trading data (admin, web session, phrase, password).
func (s *Server) ResetAllData(w http.ResponseWriter, r *http.Request) {
	var req DataResetRequest
	if !decode(w, r, &req) {
		return
	}
	wipe := req.WipeWatchlists != nil && *req.WipeWatchlists
	var res app.DataResetResult
	ok, err := s.passwordChecked(w, r, func() (err error) {
		res, err = s.svc.ResetAllData(r.Context(), state(r).principal, req.Confirm, req.Password, wipe)
		return err
	})
	switch {
	case !ok:
	case err != nil:
		s.failDataReset(w, r, err)
	default:
		writeJSON(w, http.StatusOK, DataResetResult{BackupFile: res.BackupFile, Counts: toTableCounts(res.Counts), WipedWatchlists: res.WipedWatchlists, At: res.At})
	}
}
