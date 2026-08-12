// Package hub manages live kiosk WebSocket connections.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/kiosks"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Wire message shapes. The broker is the only sender: a greeting is written
// when a kiosk connects, and sign instructions are pushed on demand. The
// field order below is the wire order, so it must not change without a
// coordinated browser-client update (web/src/lib/broker.ts).
type greeting struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func newGreeting(name string) greeting {
	return greeting{Name: name, Type: "connected"}
}

type sign struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func newSign(url string) sign {
	return sign{Type: "sign", URL: url}
}

// marshal is the single wire-marshal path for kiosk messages. All messages
// sent to kiosks must be serialized through this function.
func marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// ErrNotConnected is returned when sending to a kiosk with no live session.
var ErrNotConnected = errors.New("kiosk not connected")

// ErrWriteFailed is returned when a session exists but the socket write fails.
var ErrWriteFailed = errors.New("write to kiosk failed")

// KioskStore is the auth seam. Implemented by the kiosks module (production)
// and by a map-based fake (tests).
type KioskStore interface {
	GetKioskByIP(ctx context.Context, ip string) (kiosks.Kiosk, error)
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
		if errors.Is(err, kiosks.ErrNotFound) {
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

	h.runSession(r.Context(), k, c)
}

// runSession drives one kiosk connection through register, greeting, ping, and
// read, cleaning up the session on exit.
func (h *Hub) runSession(ctx context.Context, k kiosks.Kiosk, c conn) {
	defer c.CloseNow()

	h.mu.Lock()
	h.sessions[k.ID] = c
	h.mu.Unlock()
	h.logger.Info("kiosk connected", "kiosk_id", k.ID, "name", k.Name, "ip", k.IP)
	defer func() {
		h.mu.Lock()
		if h.sessions[k.ID] == c {
			delete(h.sessions, k.ID)
		}
		h.mu.Unlock()
		h.logger.Info("kiosk disconnected", "kiosk_id", k.ID, "name", k.Name)
	}()

	data, err := marshal(newGreeting(k.Name))
	if err != nil {
		h.logger.Error("marshal greeting", "error", err, "kiosk_id", k.ID)
		return
	}
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		h.logger.Error("write greeting", "error", err, "kiosk_id", k.ID)
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go h.pingLoop(ctx, cancel, c)

	// Kiosks are receive-only: they never send application frames, so every
	// inbound frame is intentionally discarded. Reads exist purely to observe
	// connection errors and context cancellation.
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

// PushSign instructs a connected kiosk to open a signing session at url.
// Unregistered -> ErrNotConnected (Warn-logged). Registered but write fails
// -> ErrWriteFailed wrapping the write error (Error-logged with kiosk_id).
// PushSign reports success only when the session that received the write is
// still the live session on completion; a kiosk that reconnects or
// disconnects mid-write yields ErrWriteFailed (Warn-logged), even when the
// stale conn accepted the write. Logging of failures lives INSIDE PushSign.
func (h *Hub) PushSign(ctx context.Context, id uuid.UUID, url string) error {
	data, err := marshal(newSign(url))
	if err != nil {
		h.logger.Error("push failed: marshal", "error", err, "kiosk_id", id)
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
		// The write went to c; confirm c is still the live session for id.
		if _, replaced := h.sessionState(id, c); replaced {
			// A replacement conn took over while the write was in flight, so
			// the message did not reach the live session.
			h.logger.Warn("push lost: kiosk reconnected mid-write", "kiosk_id", id, "error", err)
			return fmt.Errorf("%w: %w", ErrWriteFailed, err)
		}
		h.logger.Error("push failed: write to kiosk", "error", err, "kiosk_id", id)
		return fmt.Errorf("%w: %w", ErrWriteFailed, err)
	}

	// The write went to c; confirm c is still the live session on completion.
	if still, replaced := h.sessionState(id, c); replaced {
		// The stale conn accepted the write, but a replacement conn now owns
		// the session, so the message never reached the live kiosk.
		h.logger.Warn("push lost: kiosk reconnected mid-write", "kiosk_id", id)
		return fmt.Errorf("%w: kiosk reconnected mid-write", ErrWriteFailed)
	} else if !still {
		// The session was torn down while the write was in flight, so the
		// message cannot be delivered.
		h.logger.Warn("push lost: kiosk disconnected mid-write", "kiosk_id", id)
		return fmt.Errorf("%w: kiosk disconnected mid-write", ErrWriteFailed)
	}
	return nil
}

// sessionState reports whether id still maps to a live session and, if so,
// whether that session is a different conn than c (i.e. a replacement took
// over). PushSign calls it only after a Write completes; no lock is held
// across the Write itself.
func (h *Hub) sessionState(id uuid.UUID, c conn) (still, replaced bool) {
	h.mu.RLock()
	cur, ok := h.sessions[id]
	h.mu.RUnlock()
	return ok, ok && cur != c
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
