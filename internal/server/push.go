package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/calvertjadon/docu-kiosk/internal/protocol"
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

	msg := protocol.NewSign(body.URL)
	if err := s.hub.Send(r.Context(), kioskID, msg); err != nil {
		if errors.Is(err, hub.ErrNotConnected) {
			s.logger.Warn("push failed: kiosk not connected", "kiosk_id", kioskID)
			http.Error(w, "kiosk not connected", http.StatusNotFound)
			return
		}
		s.logger.Error("push failed", "error", err, "kiosk_id", kioskID)
		http.Error(w, "push failed", http.StatusInternalServerError)
		return
	}

	s.logger.Info("push sent", "kiosk_id", kioskID, "url", body.URL)
	w.WriteHeader(http.StatusNoContent)
}
