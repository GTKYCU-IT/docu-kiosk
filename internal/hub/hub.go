// Package hub manages live kiosk WebSocket connections.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/protocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// ErrNotConnected is returned when sending to a kiosk with no live session.
var ErrNotConnected = errors.New("kiosk not connected")

// ErrWriteFailed is returned when a session exists but the socket write fails.
var ErrWriteFailed = errors.New("write to kiosk failed")

// ErrKioskNotFound is returned when auth lookup finds no kiosk for the IP.
var ErrKioskNotFound = errors.New("kiosk not found")

// Kiosk identifies a kiosk authorized to connect.
type Kiosk struct {
	ID   uuid.UUID
	Name string
}

// KioskStore is the auth seam. Implemented by the server's DB adapter
// (production) and by a map-based fake (tests).
type KioskStore interface {
	GetKioskByIP(ctx context.Context, ip string) (Kiosk, error)
}

// conn is the subset of *websocket.Conn the hub drives.
type conn interface {
	Write(ctx context.Context, typ websocket.MessageType, data []byte) error
	Ping(ctx context.Context) error
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	CloseNow() error
}

type Hub struct {
	store        KioskStore
	logger       *slog.Logger
	mu           sync.RWMutex
	sessions     map[uuid.UUID]conn
	pingInterval time.Duration
	pingTimeout  time.Duration
}

// New returns a Hub that authenticates kiosks against store and logs through
// logger. pingInterval and pingTimeout default to 30s and 5s and are mutable
// by package-hub tests.
func New(store KioskStore, logger *slog.Logger) *Hub {
	return &Hub{
		store:        store,
		logger:       logger,
		sessions:     make(map[uuid.UUID]conn),
		pingInterval: 30 * time.Second,
		pingTimeout:  5 * time.Second,
	}
}

// Serve accepts the WebSocket, authenticates the kiosk IP against the store,
// and runs the session until disconnect. Blocks for the connection lifetime.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, kioskIP string) {
	k, err := h.store.GetKioskByIP(r.Context(), kioskIP)
	if err != nil {
		if errors.Is(err, ErrKioskNotFound) {
			h.logger.Warn("ws connect rejected: unregistered ip", "ip", kioskIP)
			http.Error(w, "unregistered ip", http.StatusUnauthorized)
			return
		}
		h.logger.Error("ws connect auth failed", "error", err, "ip", kioskIP)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// InsecureSkipVerify is safe here: the broker runs on an internal network
	// and the Vite dev proxy changes the Origin host during development.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		h.logger.Error("ws accept", "error", err, "kiosk_id", k.ID, "ip", kioskIP)
		return
	}

	h.runSession(r.Context(), k, kioskIP, c)
}

// runSession drives one kiosk connection through register, greeting, ping, and
// read, cleaning up the session on exit.
func (h *Hub) runSession(ctx context.Context, k Kiosk, ip string, c conn) {
	defer c.CloseNow()

	h.mu.Lock()
	h.sessions[k.ID] = c
	h.mu.Unlock()
	h.logger.Info("kiosk connected", "kiosk_id", k.ID, "name", k.Name, "ip", ip)
	defer func() {
		h.mu.Lock()
		delete(h.sessions, k.ID)
		h.mu.Unlock()
		h.logger.Info("kiosk disconnected", "kiosk_id", k.ID, "name", k.Name)
	}()

	data, err := protocol.Marshal(protocol.NewGreeting(k.Name))
	if err != nil {
		h.logger.Error("marshal greeting", "error", err, "kiosk_id", k.ID)
		return
	}
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go h.pingLoop(ctx, cancel, c)

	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}

// pingLoop keeps the connection alive with periodic pings. A failed ping
// cancels the session context, which unblocks the read loop and tears the
// session down.
func (h *Hub) pingLoop(ctx context.Context, cancel context.CancelFunc, c conn) {
	ticker := time.NewTicker(h.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, h.pingTimeout)
			err := c.Ping(pingCtx)
			pingCancel()
			if err != nil {
				cancel()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// Send writes a protocol message to the kiosk session. Unregistered ->
// ErrNotConnected (Warn-logged). Registered but write fails -> ErrWriteFailed
// wrapping the write error (Error-logged with kiosk_id). Logging of failures
// lives INSIDE Send.
func (h *Hub) Send(ctx context.Context, id uuid.UUID, msg protocol.Message) error {
	data, err := protocol.Marshal(msg)
	if err != nil {
		return err
	}

	h.mu.RLock()
	c, ok := h.sessions[id]
	h.mu.RUnlock()

	if !ok {
		h.logger.Warn("push failed: kiosk not connected", "kiosk_id", id)
		return fmt.Errorf("%w: %s", ErrNotConnected, id)
	}

	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		h.logger.Error("push failed: write to kiosk", "error", err, "kiosk_id", id)
		return fmt.Errorf("%w: %v", ErrWriteFailed, err)
	}
	return nil
}

// Connected returns the UUIDs of all connected kiosks.
func (h *Hub) Connected() []uuid.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]uuid.UUID, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}
