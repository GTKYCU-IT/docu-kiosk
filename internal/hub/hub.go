// Package hub manages live kiosk WebSocket connections.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type Kiosk struct {
	ID   uuid.UUID
	Name string
}

type session struct {
	name string
	conn *websocket.Conn
}

type Hub struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]session
}

func New() *Hub {
	return &Hub{
		sessions: make(map[uuid.UUID]session),
	}
}

// Register adds a kiosk connection and returns its session UUID.
func (h *Hub) Register(name string, conn *websocket.Conn) uuid.UUID {
	id := uuid.New()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[id] = session{name: name, conn: conn}
	return id
}

func (h *Hub) Unregister(id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
}

func (h *Hub) Send(ctx context.Context, id uuid.UUID, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	h.mu.RLock()
	s, ok := h.sessions[id]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("kiosk %s not connected", id)
	}

	return s.conn.Write(ctx, websocket.MessageText, data)
}

func (h *Hub) Connected() []Kiosk {
	h.mu.RLock()
	defer h.mu.RUnlock()
	kiosks := make([]Kiosk, 0, len(h.sessions))
	for id, s := range h.sessions {
		kiosks = append(kiosks, Kiosk{ID: id, Name: s.name})
	}
	return kiosks
}
