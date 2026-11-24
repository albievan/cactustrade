package httpapi

import (
	"net/http"
	"strconv"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// Trading statements (R30). Who may do what is decided by the use cases (web session; own
// schedules and owned accounts, an admin every schedule); here only shapes are converted.

func toStatementRefs(rs []app.StatementAccountRef) []StatementAccountRef {
	out := make([]StatementAccountRef, 0, len(rs))
	for _, r := range rs {
		out = append(out, StatementAccountRef{Id: r.ID, Name: r.Name, Platform: Platform(r.Platform), Mode: TradingMode(r.Mode)})
	}
	return out
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toStatementSchedule(v app.StatementScheduleView) StatementSchedule {
	out := StatementSchedule{
		Id: v.ID, OwnerId: v.OwnerID, OwnerName: v.OwnerName, Period: StatementPeriod(v.Period), AccountIds: nonNil(v.AccountIDs),
		Accounts: toStatementRefs(v.Accounts), Recipients: nonNil(v.Recipients), IncludeCombined: v.IncludeCombined,
		IncludeAccounts: v.IncludeAccounts, IncludeExchanges: v.IncludeExchanges, Enabled: v.Enabled, LastPeriod: v.LastPeriod,
		LastError: v.LastError, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
	if !v.LastSentAt.IsZero() {
		out.LastSentAt = ptr(v.LastSentAt)
	}
	if !v.NextSendAt.IsZero() {
		out.NextSendAt = ptr(v.NextSendAt)
	}
	return out
}

func toScheduleInput(in StatementScheduleInput) app.StatementScheduleInput {
	return app.StatementScheduleInput{
		Period: domain.StatementPeriod(in.Period), AccountIDs: in.AccountIds, Recipients: in.Recipients, IncludeCombined: in.IncludeCombined,
		IncludeAccounts: in.IncludeAccounts, IncludeExchanges: in.IncludeExchanges, Enabled: in.Enabled,
	}
}

// ListStatementSchedules returns the statements section.
func (s *Server) ListStatementSchedules(w http.ResponseWriter, r *http.Request) {
	pg, err := s.svc.StatementSchedules(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := StatementsPage{
		Schedules: make([]StatementSchedule, 0, len(pg.Schedules)), Accounts: toStatementRefs(pg.Accounts),
		MailServer: pg.MailServer, CanFixMail: pg.CanFixMail,
	}
	for _, v := range pg.Schedules {
		out.Schedules = append(out.Schedules, toStatementSchedule(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateStatementSchedule adds a schedule.
func (s *Server) CreateStatementSchedule(w http.ResponseWriter, r *http.Request) {
	var req StatementScheduleInput
	if !decode(w, r, &req) {
		return
	}
	v, err := s.svc.CreateStatementSchedule(r.Context(), state(r).principal, toScheduleInput(req))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toStatementSchedule(v))
}

// UpdateStatementSchedule changes a schedule.
func (s *Server) UpdateStatementSchedule(w http.ResponseWriter, r *http.Request, id string) {
	var req StatementScheduleInput
	if !decode(w, r, &req) {
		return
	}
	v, err := s.svc.UpdateStatementSchedule(r.Context(), state(r).principal, id, toScheduleInput(req))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toStatementSchedule(v))
}

// DeleteStatementSchedule removes a schedule.
func (s *Server) DeleteStatementSchedule(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.svc.DeleteStatementSchedule(r.Context(), state(r).principal, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SendStatementNow e-mails the last complete period's statements now.
func (s *Server) SendStatementNow(w http.ResponseWriter, r *http.Request, id string) {
	res, err := s.svc.SendStatementNow(r.Context(), state(r).principal, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, StatementSendResult{
		Period: res.Window.Key, From: res.Window.From, To: res.Window.To, Title: res.Window.Title(), Recipients: res.Recipients,
		Files: nonNil(res.Files),
	})
}

// PreviewStatement sends one statement as a PDF attachment. Every error is answered as
// problem+json before the first byte of the file.
func (s *Server) PreviewStatement(w http.ResponseWriter, r *http.Request, params PreviewStatementParams) {
	req := app.StatementPreviewRequest{
		Period: domain.StatementPeriod(params.Period), From: deref(params.From), AccountID: deref(params.AccountId), ScheduleID: deref(params.ScheduleId),
	}
	scope := ""
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	sc, err := domain.ParseStatementScope(scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	req.Scope = sc
	if params.Exchange != nil {
		req.Exchange = string(*params.Exchange)
	}
	f, err := s.svc.PreviewStatement(r.Context(), state(r).principal, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/pdf")
	// The name is domain.StatementFileName: ASCII letters, digits, '.' and '-' only.
	h.Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
	h.Set("Content-Length", strconv.Itoa(len(f.Data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(f.Data); err != nil {
		s.log.WarnContext(r.Context(), "statement preview not written", "err", err)
	}
}
