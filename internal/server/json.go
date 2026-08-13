package server

import (
	"encoding/json"
	"net/http"
)

// problem is an RFC 9457 problem details body. Client-facing details stay
// opaque: internal state is logged server-side only and never included in
// the response body, so it is not leaked to callers.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// respondWithError writes a JSON error body. Client-facing messages stay
// opaque: error details are logged server-side only and never included in the
// response body, so internal state is not leaked to callers.
func (s *server) respondWithError(w http.ResponseWriter, msg string, code int, err error) {
	if code >= 500 {
		args := []any{"code", code, "message", msg}
		if err != nil {
			args = append(args, "error", err)
		}
		s.logger.Error("request failed", args...)
	} else {
		s.logger.Debug("request rejected", "code", code, "message", msg)
	}

	s.respondWithJSON(w, code, struct {
		Error string `json:"error"`
	}{
		Error: msg,
	})
}

// writeProblem writes an RFC 9457 problem details response with Content-Type
// application/problem+json. Client-facing details stay opaque: internal
// failures are logged server-side (with err when non-nil) and never included
// in the response body, so internal state is not leaked to callers.
func (s *server) writeProblem(w http.ResponseWriter, p problem, err error) {
	if p.Status >= 500 {
		args := []any{"code", p.Status, "type", p.Type, "title", p.Title}
		if err != nil {
			args = append(args, "error", err)
		}
		s.logger.Error("request failed", args...)
	} else {
		s.logger.Debug("request rejected", "code", p.Status, "type", p.Type, "title", p.Title)
	}

	s.writeJSON(w, "application/problem+json", p.Status, p)
}

func (s *server) respondWithJSON(w http.ResponseWriter, code int, payload any) {
	s.writeJSON(w, "application/json", code, payload)
}

func (s *server) writeJSON(w http.ResponseWriter, contentType string, code int, payload any) {
	w.Header().Set("Content-Type", contentType)

	data, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("marshal json response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(code)
	w.Write(data)
}
