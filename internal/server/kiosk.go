package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/calvertjadon/docu-kiosk/internal/kiosks"
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

	err := s.kiosks.Register(r.Context(), s.realIP(r), params.Name)
	if err != nil {
		if errors.Is(err, kiosks.ErrNameTaken) {
			s.respondWithError(w, "kiosk name already in use", http.StatusConflict, nil)
			return
		}
		s.respondWithError(w, "failed to register kiosk", http.StatusInternalServerError, err)
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

	ks, err := s.kiosks.ListLive(r.Context(), s.hub.Connected())
	if err != nil {
		s.respondWithError(w, "failed to list kiosks", http.StatusInternalServerError, err)
		return
	}

	kiosks := make([]KioskResponse, 0, len(ks))
	for _, k := range ks {
		kiosks = append(kiosks, KioskResponse{ID: k.ID, Name: k.Name})
	}

	s.respondWithJSON(w, http.StatusOK, kiosks)
}

// kioskStore adapts the kiosk module to the hub auth seam.
type kioskStore struct{ m *kiosks.Module }

func (ks kioskStore) GetKioskByIP(ctx context.Context, ip string) (hub.Kiosk, error) {
	k, err := ks.m.ResolveIdentity(ctx, ip)
	if err != nil {
		if errors.Is(err, kiosks.ErrNotFound) {
			return hub.Kiosk{}, hub.ErrKioskNotFound
		}
		return hub.Kiosk{}, fmt.Errorf("resolve kiosk identity: %w", err)
	}
	return hub.Kiosk{ID: k.ID, Name: k.Name}, nil
}
