package httpapi

import (
	"encoding/json"
	"net/http"
)

// GetMyPreferences returns the caller's own view preferences (R50; web session only).
func (s *Server) GetMyPreferences(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.MyPreferences(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage(p))
}

// SetMyPreferences applies a merge patch to the caller's own view preferences (R50). The
// body is read as raw JSON values so the use case validates exactly what was sent; the
// session's CSRF token is checked by the middleware like every cookie write.
func (s *Server) SetMyPreferences(w http.ResponseWriter, r *http.Request) {
	var patch map[string]json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	if patch == nil { // the body was "null"
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON", "a JSON object of preferences expected", "preferences")
		return
	}
	p, err := s.svc.SetMyPreferences(r.Context(), state(r).principal, patch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage(p))
}
