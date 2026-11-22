package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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

// fail maps an error to problem+json. Unexpected errors are logged with their detail and
// answered with a generic 500 (no internals leak to the client).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeProblem(w, r, http.StatusBadRequest, "Invalid input", ve.Error(), ve.Field)
	case errors.Is(err, domain.ErrUnauthenticated):
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "sign in or send a valid API key", "")
	case errors.Is(err, domain.ErrForbidden):
		writeProblem(w, r, http.StatusForbidden, "Forbidden", detail(err, domain.ErrForbidden), "")
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "Not found", "", "")
	case errors.Is(err, domain.ErrSetupDone):
		writeProblem(w, r, http.StatusConflict, "Setup already completed", "sign in instead", "")
	case errors.Is(err, domain.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "Conflict", detail(err, domain.ErrConflict), "")
	case errors.Is(err, domain.ErrUnavailable):
		s.log.WarnContext(r.Context(), "unavailable", "path", r.URL.Path, "err", err)
		writeProblem(w, r, http.StatusServiceUnavailable, "Unavailable", detail(err, domain.ErrUnavailable), "")
	default:
		s.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal error", "", "")
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
