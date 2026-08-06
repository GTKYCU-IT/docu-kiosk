package server

import (
	"net/http"
)

// GET /ws
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	s.sessions.Accept(w, r, realIP(r), s.db)
}
