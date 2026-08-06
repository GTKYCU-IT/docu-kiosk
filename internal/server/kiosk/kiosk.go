// Package kiosk groups handlers that need db + sessions but not auth.
package kiosk
import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/session"
	"github.com/google/uuid"
)

// Handlers groups kiosk-registration, listing, WebSocket, and push endpoints.
type Handlers struct {
	db     *database.Queries
	sessions *session.Manager
	logger *slog.Logger
}

// NewHandlers returns a Handlers wired with the given database, sessions, and logger.
func NewHandlers(db *database.Queries, sessions *session.Manager, logger *slog.Logger) *Handlers {
	return &Handlers{db: db, sessions: sessions, logger: logger}
}

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
func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
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

	_, err := h.db.CreateKiosk(r.Context(), database.CreateKioskParams{
		ID:   uuid.New(),
		IP:   kioskIP,
		Name: params.Name,
	})
	if err != nil {
		h.logger.Error("create kiosk", "error", err, "name", params.Name, "ip", kioskIP)
		http.Error(w, "failed to create kiosk", http.StatusInternalServerError)
		return
	}

	h.logger.Info("kiosk registered", "name", params.Name, "ip", kioskIP)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/kiosks
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	connected := h.sessions.Connected()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(connected)
}

// GET /ws
func (h *Handlers) WS(w http.ResponseWriter, r *http.Request) {
	h.sessions.Accept(w, r, realIP(r), h.db)
}

// POST /api/kiosks/{id}/sessions
func (h *Handlers) Push(w http.ResponseWriter, r *http.Request) {
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
	if err := h.sessions.Send(r.Context(), kioskID, msg); err != nil {
		h.logger.Warn("push failed: kiosk not connected", "kiosk_id", kioskID)
		http.Error(w, "kiosk not connected", http.StatusNotFound)
		return
	}

	h.logger.Info("push sent", "kiosk_id", kioskID, "url", body.URL)
	w.WriteHeader(http.StatusNoContent)
}
