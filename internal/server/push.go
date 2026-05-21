package server

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

// POST /api/kiosks/{id}/sessions
func (s *server) handlePush(w http.ResponseWriter, r *http.Request) {
	kioskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid kiosk id", http.StatusBadRequest)
		return
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	msg := map[string]string{"type": "sign", "url": body.URL}
	if err := s.hub.Send(r.Context(), kioskID, msg); err != nil {
		s.logger.Warn("push failed: kiosk not connected", "kiosk_id", kioskID)
		http.Error(w, "kiosk not connected", http.StatusNotFound)
		return
	}

	s.logger.Info("push sent", "kiosk_id", kioskID, "url", body.URL)
	w.WriteHeader(http.StatusNoContent)
}