// Package kiosk groups handlers that need db + hub but not auth.
package kiosk
import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Handlers groups kiosk-registration, listing, WebSocket, and push endpoints.
type Handlers struct {
	db     *database.Queries
	hub    *hub.Hub
	logger *slog.Logger
}

// NewHandlers returns a Handlers wired with the given database, hub, and logger.
func NewHandlers(db *database.Queries, hub *hub.Hub, logger *slog.Logger) *Handlers {
	return &Handlers{db: db, hub: hub, logger: logger}
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
	type KioskResponse struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}

	connected := h.hub.Connected()
	kiosks := make([]KioskResponse, 0, len(connected))
	for _, id := range connected {
		k, err := h.db.GetKioskByID(r.Context(), id)
		if err == nil {
			kiosks = append(kiosks, KioskResponse{ID: k.ID, Name: k.Name})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(kiosks)
}

// GET /ws
func (h *Handlers) WS(w http.ResponseWriter, r *http.Request) {
	kioskIP := realIP(r)

	k, err := h.db.GetKioskByIP(r.Context(), kioskIP)
	if err != nil {
		h.logger.Warn("ws connect rejected: unregistered ip", "ip", kioskIP)
		http.Error(w, "unregistered ip", http.StatusUnauthorized)
		return
	}

	// InsecureSkipVerify is safe here: the broker runs on an internal network
	// and the Vite dev proxy changes the Origin host during development.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		h.logger.Error("ws accept", "error", err, "kiosk_id", k.ID, "ip", kioskIP)
		return
	}
	defer conn.CloseNow()

	h.hub.Register(k.ID, conn)
	h.logger.Info("kiosk connected", "kiosk_id", k.ID, "name", k.Name, "ip", kioskIP)
	defer func() {
		h.hub.Unregister(k.ID)
		h.logger.Info("kiosk disconnected", "kiosk_id", k.ID, "name", k.Name)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	data, _ := json.Marshal(map[string]string{"type": "connected", "name": k.Name})
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
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
	if err := h.hub.Send(r.Context(), kioskID, msg); err != nil {
		h.logger.Warn("push failed: kiosk not connected", "kiosk_id", kioskID)
		http.Error(w, "kiosk not connected", http.StatusNotFound)
		return
	}

	h.logger.Info("push sent", "kiosk_id", kioskID, "url", body.URL)
	w.WriteHeader(http.StatusNoContent)
}
