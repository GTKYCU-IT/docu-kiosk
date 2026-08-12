package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
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

	kioskIP := s.realIP(r)

	_, err := s.db.CreateKiosk(r.Context(), database.CreateKioskParams{
		ID:   uuid.New(),
		IP:   kioskIP,
		Name: params.Name,
	})
	if err != nil {
		s.logger.Error("create kiosk", "error", err, "name", params.Name, "ip", kioskIP)
		http.Error(w, "failed to create kiosk", http.StatusInternalServerError)
		return
	}

	s.logger.Info("kiosk registered", "name", params.Name, "ip", kioskIP)
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

// kioskStore adapts the database to the hub auth seam.
type kioskStore struct{ db *database.Queries }

func (ks kioskStore) GetKioskByIP(ctx context.Context, ip string) (hub.Kiosk, error) {
	k, err := ks.db.GetKioskByIP(ctx, ip)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return hub.Kiosk{}, hub.ErrKioskNotFound
		}
		return hub.Kiosk{}, err
	}
	return hub.Kiosk{ID: k.ID, Name: k.Name}, nil
}
