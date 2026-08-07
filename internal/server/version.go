package server

import (
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/version"
)

// GET /api/version — reports the running broker build. Used by the kiosk SPA
// footer and by ops to confirm what a deployment is actually running.
func (s *server) handleVersion(w http.ResponseWriter, r *http.Request) {
	s.respondWithJSON(w, http.StatusOK, struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}{
		Version: version.Version,
		Commit:  version.Commit,
	})
}
