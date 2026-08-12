package server

import (
	"encoding/json"
	"errors"
	"net/http"

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

	err := s.kiosks.Register(r.Context(), s.realIP(r), params.Name)
	if err != nil {
		switch {
		case errors.Is(err, kiosks.ErrNameRequired):
			http.Error(w, "name is required", http.StatusBadRequest)
		case errors.Is(err, kiosks.ErrNameTaken):
			s.respondWithError(w, "kiosk name already in use", http.StatusConflict, nil)
		default:
			s.respondWithError(w, "failed to register kiosk", http.StatusInternalServerError, err)
		}
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

	list := make([]KioskResponse, 0, len(ks))
	for _, k := range ks {
		list = append(list, KioskResponse{ID: k.ID, Name: k.Name})
	}

	s.respondWithJSON(w, http.StatusOK, list)
}
