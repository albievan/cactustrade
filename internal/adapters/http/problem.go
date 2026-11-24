package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// maxBody bounds a JSON request body.
const maxBody = 64 << 10

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, _ *http.Request, status int, title, detail, field string) {
	p := Problem{Status: status, Title: title}
	if detail != "" {
		p.Detail = &detail
	}
	if field != "" {
		p.Field = &field
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// problemFor writes a problem for err like writeProblem. When err carries a
// *domain.MissingError (R22), the detail is its message for people (no field names) and the
// problem names what is missing and whether the caller may fix it; status, title and field
// stay as the caller chose them.
func problemFor(w http.ResponseWriter, r *http.Request, err error, status int, title, detail, field string) {
	me, ok := errors.AsType[*domain.MissingError](err)
	if !ok {
		writeProblem(w, r, status, title, detail, field)
		return
	}
	p := Problem{Status: status, Title: title, Detail: ptr(me.Msg), Missing: ptr(Missing(me.What)), CanFix: ptr(me.CanFix)}
	if field != "" {
		p.Field = &field
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// fail maps an error to problem+json. Unexpected errors are logged with their detail and
// answered with a generic 500 (no internals leak to the client).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		problemFor(w, r, err, http.StatusBadRequest, "Invalid input", ve.Error(), ve.Field)
	case errors.Is(err, domain.ErrUnauthenticated):
		problemFor(w, r, err, http.StatusUnauthorized, "Unauthorized", "sign in or send a valid API key", "")
	case errors.Is(err, domain.ErrViewOnly):
		problemFor(w, r, err, http.StatusForbidden, "View-only account",
			"Trading 212, Saxo and Tiger Brokers accounts are view-only: cactusTrade shows them in the overview but does not trade them", "")
	case errors.Is(err, domain.ErrForbidden):
		problemFor(w, r, err, http.StatusForbidden, "Forbidden", detail(err, domain.ErrForbidden), "")
	case errors.As(err, new(*app.MailError)):
		s.log.WarnContext(r.Context(), "mail server refused", "path", r.URL.Path, "err", err)
		problemFor(w, r, err, http.StatusBadGateway, "The mail server refused", err.Error(), "")
	case errors.Is(err, domain.ErrNoAccount):
		problemFor(w, r, err, http.StatusNotFound, "No eToro account",
			"you have no eToro account yet: add one under eToro accounts, or ask an account owner to share theirs", "")
	case errors.Is(err, domain.ErrNotFound):
		problemFor(w, r, err, http.StatusNotFound, "Not found", detail(err, domain.ErrNotFound), "")
	case errors.Is(err, domain.ErrPasswordChangeRequired):
		problemFor(w, r, err, http.StatusForbidden, "Password change required", "change the default password first", "")
	case errors.Is(err, domain.ErrConflict):
		problemFor(w, r, err, http.StatusConflict, "Conflict", detail(err, domain.ErrConflict), "")
	case errors.Is(err, domain.ErrUnavailable):
		s.log.WarnContext(r.Context(), "unavailable", "path", r.URL.Path, "err", err)
		problemFor(w, r, err, http.StatusServiceUnavailable, "Unavailable", detail(err, domain.ErrUnavailable), "")
	default:
		s.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "err", err)
		problemFor(w, r, err, http.StatusInternalServerError, "Internal error", "", "")
	}
}

// detail is the human part of a wrapped sentinel: "app.DeleteUser: you cannot delete
// yourself: conflict" → "you cannot delete yourself". Segments naming Go functions
// ("app.DeleteUser", "users.Create") are internal and dropped.
func detail(err, sentinel error) string {
	var keep []string
	for _, seg := range strings.Split(err.Error(), ": ") {
		if seg == sentinel.Error() || (strings.Contains(seg, ".") && !strings.Contains(seg, " ")) {
			continue
		}
		keep = append(keep, seg)
	}
	return strings.Join(keep, ": ")
}

func (s *Server) paramError(w http.ResponseWriter, r *http.Request, err error) {
	writeProblem(w, r, http.StatusBadRequest, "Invalid parameter", err.Error(), "")
}

// decode reads a JSON body strictly: size-limited, unknown fields refused, one value only.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeProblem(w, r, http.StatusUnsupportedMediaType, "Unsupported media type", "send application/json", "")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON", err.Error(), "")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON", "one JSON value expected", "")
		return false
	}
	return true
}
