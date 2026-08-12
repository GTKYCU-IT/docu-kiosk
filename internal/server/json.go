package server

import (
	"encoding/json"
	"net/http"
)

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
