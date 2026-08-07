// Package hub manages live kiosk WebSocket connections.
package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/calvertjadon/docu-kiosk/internal/protocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// ErrNotConnected is returned when sending to a kiosk with no live session.
var ErrNotConnected = errors.New("kiosk not connected")

type Hub struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]*websocket.Conn
}

func New() *Hub {
	return &Hub{
		sessions: make(map[uuid.UUID]*websocket.Conn),
	}
}

// Register adds a kiosk connection keyed by its UUID.
func (h *Hub) Register(id uuid.UUID, conn *websocket.Conn) uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[id] = conn
	return id
}

func (h *Hub) Unregister(id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
}

func (h *Hub) Send(ctx context.Context, id uuid.UUID, msg protocol.Message) error {
	data, err := protocol.Marshal(msg)
	if err != nil {
		return err
	}

	h.mu.RLock()
	conn, ok := h.sessions[id]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrNotConnected, id)
	}

	return conn.Write(ctx, websocket.MessageText, data)
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
