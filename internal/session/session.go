// Package session manages the full WebSocket lifecycle for kiosk connections:
// IP validation, upgrade, registration, ping/pong keepalive, read loop,
// and disconnect cleanup.  Connected() returns kiosk names directly,
// eliminating the N+1 DB pattern in the list-kiosks handler.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// KioskInfo carries the id and name of a connected kiosk.
type KioskInfo struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Manager tracks live kiosk WebSocket sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]*session
	logger   *slog.Logger
}

type session struct {
	conn   *websocket.Conn
	kiosk  KioskInfo
	cancel context.CancelFunc
}

// NewManager creates a session manager that logs lifecycle events to logger.
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		sessions: make(map[uuid.UUID]*session),
		logger:   logger,
	}
}

// Accept validates the kiosk IP against the store, upgrades to WebSocket,
// registers the session, sends a "connected" message, and starts background
// keepalive + read-loop goroutines.  The read loop automatically unregisters
// the session on disconnect.
//
// The caller owns the HTTP handler lifecycle and should return after Accept
// succeeds or fails; Accept does not block past the initial handshake.
func (m *Manager) Accept(w http.ResponseWriter, r *http.Request, kioskIP string, store KioskStore) (KioskInfo, error) {
	k, err := store.GetKioskByIP(r.Context(), kioskIP)
	if err != nil {
		m.logger.Warn("ws connect rejected: unregistered ip", "ip", kioskIP)
		http.Error(w, "unregistered ip", http.StatusUnauthorized)
		return KioskInfo{}, err
	}

	// InsecureSkipVerify is safe: the broker runs on an internal network
	// and the Vite dev proxy changes the Origin host during development.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		m.logger.Error("ws accept", "error", err, "kiosk_id", k.ID, "ip", kioskIP)
		return KioskInfo{}, err
	}

	info := KioskInfo{ID: k.ID, Name: k.Name}
	sess := &session{
		conn:  conn,
		kiosk: info,
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel

	m.mu.Lock()
	m.sessions[k.ID] = sess
	m.mu.Unlock()

	m.logger.Info("kiosk connected", "kiosk_id", k.ID, "name", k.Name, "ip", kioskIP)

	// Send initial connected message.
	data, _ := json.Marshal(map[string]string{"type": "connected", "name": k.Name})
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		cancel()
		m.Unregister(k.ID)
		return KioskInfo{}, err
	}

	go sess.pingLoop(ctx)
	go sess.readLoop(ctx, m, k.ID)

	return info, nil
}

// Send marshals msg as JSON and writes it to the kiosk's WebSocket connection.
func (m *Manager) Send(ctx context.Context, id uuid.UUID, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("kiosk %s not connected", id)
	}

	return sess.conn.Write(ctx, websocket.MessageText, data)
}

// Connected returns the id and name of every connected kiosk.
func (m *Manager) Connected() []KioskInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]KioskInfo, 0, len(m.sessions))
	for _, sess := range m.sessions {
		result = append(result, sess.kiosk)
	}
	return result
}

// Unregister removes the session and closes the connection.
func (m *Manager) Unregister(id uuid.UUID) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	if ok {
		sess.cancel()
		sess.conn.CloseNow()
		m.logger.Info("kiosk disconnected", "kiosk_id", id, "name", sess.kiosk.Name)
	}
}

// KioskStore is the subset of database.Queries needed by the session manager.
type KioskStore interface {
	GetKioskByIP(ctx context.Context, ip string) (database.Kiosk, error)
}

// --- internal ---

func (s *session) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				s.cancel()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *session) readLoop(ctx context.Context, m *Manager, id uuid.UUID) {
	defer m.Unregister(id)
	for {
		if _, _, err := s.conn.Read(ctx); err != nil {
			return
		}
	}
}
