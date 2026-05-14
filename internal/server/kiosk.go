package server

import (
	"encoding/json"
	"net/http"
)

// POST /api/kiosks
func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	type Params struct {
		Name            string `json:"name"`
		RegistrationKey string `json:"key"`
	}

	var params Params
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, "failed to decode params", http.StatusBadRequest)
		return
	}

	if params.RegistrationKey != s.registrationKey {
		http.Error(w, "invalid registration key", http.StatusUnauthorized)
		return
	}

	if params.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	token, err := s.auth.GenerateToken(params.Name)
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "kiosk-token",
		Value:    token,
		Path:     "/",
		MaxAge:   315360000, // 10 years
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/kiosks
func (s *server) handleListKiosks(w http.ResponseWriter, r *http.Request) {
	type KioskResponse struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	connected := s.hub.Connected()
	resp := make([]KioskResponse, len(connected))
	for i, k := range connected {
		resp[i] = KioskResponse{ID: k.ID.String(), Name: k.Name}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
