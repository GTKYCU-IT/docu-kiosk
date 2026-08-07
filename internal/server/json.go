package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// respondWithError writes a JSON error body. Error details are only appended
// for server-side failures (5xx); client-facing messages stay opaque so
// internal state is not leaked to callers.
func (s *server) respondWithError(w http.ResponseWriter, msg string, code int, err error) {
	if err != nil {
		msg = fmt.Sprintf("%s: %s", msg, err)
	}

	if code >= 500 {
		s.logger.Error("request failed", "code", code, "message", msg)
	} else {
		s.logger.Debug("request rejected", "code", code, "message", msg)
	}

	s.respondWithJSON(w, code, struct {
		Error string `json:"error"`
	}{
		Error: msg,
	})
}

func (s *server) respondWithJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")

	data, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("marshal json response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(code)
	w.Write(data)
}
