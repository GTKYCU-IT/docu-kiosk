package server

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
)

// POST /api/kiosks
func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	type Params struct {
		Name string `json:"name"`
	}

	var params Params
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, "failed to decode params", http.StatusBadRequest)
		return
	}

	if params.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	kioskIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "failed to get kiosk ip", http.StatusInternalServerError)
		return
	}

	_, err = s.db.CreateKiosk(r.Context(), database.CreateKioskParams{
		ID:   uuid.New(),
		IP:   kioskIP,
		Name: params.Name,
	})
	if err != nil {
		http.Error(w, "failed to create kiosk", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/kiosks
func (s *server) handleListKiosks(w http.ResponseWriter, r *http.Request) {
	type KioskResponse struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}

	connected := s.hub.Connected()
	kiosks := make([]KioskResponse, 0, len(connected))
	for _, id := range connected {
		k, err := s.db.GetKioskByID(r.Context(), id)
		if err == nil {
			kiosks = append(kiosks, KioskResponse{ID: k.ID, Name: k.Name})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(kiosks)
}
