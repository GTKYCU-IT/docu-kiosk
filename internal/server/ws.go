package server

import (
	"net/http"
)

// GET /ws
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	s.hub.Serve(w, r, s.realIP(r))
}
