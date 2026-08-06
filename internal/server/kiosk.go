package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
)

func realIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if before, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(before)
		}
		return strings.TrimSpace(xff)
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

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

	kioskIP := realIP(r)

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
	connected := s.sessions.Connected()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(connected)
}
