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

// Wire field order is mirrored manually in web/src/lib/protocol.ts.
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

// Status is the reported state of a live kiosk session.
type Status string

const (
	StatusReady   Status = "ready"
	StatusSigning Status = "signing"
)

// StatusSnapshot is a copy of the published kiosk statuses.
type StatusSnapshot map[uuid.UUID]Status

// LiveKioskIDs returns the Ready and Signing kiosk IDs.
func (statuses StatusSnapshot) LiveKioskIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(statuses))
	for id := range statuses {
		ids = append(ids, id)
	}
	return ids
}

type statusFrame struct {
	Type   string `json:"type"`
	Status Status `json:"status"`
}

func decodeStatusFrame(data []byte) (Status, error) {
	var f statusFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("decode status frame: %w", err)
	}
	if f.Type != "status" {
		return "", fmt.Errorf("decode status frame: expected type status, got %q", f.Type)
	}
	switch f.Status {
	case StatusReady, StatusSigning:
		return f.Status, nil
	default:
		return "", fmt.Errorf("decode status frame: unknown status %q", f.Status)
	}
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

// OriginPolicy decides whether a connection may be accepted. It receives the
// full request so the policy can inspect the Origin header and the request
// host. The policy is the hub's own security gate: it runs before the socket
// is accepted, so a Hub fails closed even when used without the server's CORS
// middleware.
type OriginPolicy func(r *http.Request) bool

// Option configures a Hub.
type Option func(*Hub)

// WithOriginPolicy injects the connection policy Serve enforces before
// accepting a socket. Without it a Hub rejects every connection (fail
// closed): opening the module to connections is an explicit caller decision.
func WithOriginPolicy(p OriginPolicy) Option {
	return func(h *Hub) {
		h.originPolicy = p
	}
}

type Hub struct {
	store        KioskStore
	logger       *slog.Logger
	originPolicy OriginPolicy
	mu           sync.Mutex
	sessionSlots map[uuid.UUID]*sessionSlot
	pingInterval time.Duration
	pingTimeout  time.Duration
}

// Hub.mu guards slot membership and reported statuses. sessionSlot.mu
// serializes Push with replacement/removal; lock the slot before Hub.mu.
type sessionGeneration struct {
	connection conn
	status     Status
}

type sessionSlot struct {
	mu      sync.Mutex
	current *sessionGeneration
}

// New returns a Hub that authenticates kiosks against store and logs through
// logger. pingInterval and pingTimeout default to 30s and 5s and are mutable
// by package-hub tests. By default the origin policy rejects every
// connection; callers must inject one via WithOriginPolicy.
func New(store KioskStore, logger *slog.Logger, opts ...Option) *Hub {
	h := &Hub{
		store:        store,
		logger:       logger,
		sessionSlots: make(map[uuid.UUID]*sessionSlot),
		pingInterval: 30 * time.Second,
		pingTimeout:  5 * time.Second,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Serve accepts the WebSocket, authenticates the kiosk IP against the store,
// and runs the session until disconnect. Blocks for the connection lifetime.
// The origin policy is the module's own security gate and runs first: a
// connection is never authenticated, let alone accepted, unless the injected
// policy admits it.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, kioskIP string) {
	if h.originPolicy == nil || !h.originPolicy(r) {
		h.logger.Warn("ws connect rejected: origin policy", "ip", kioskIP, "origin", r.Header.Get("Origin"))
		http.Error(w, "origin rejected", http.StatusForbidden)
		return
	}

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

	// InsecureSkipVerify is safe here because the origin policy above is the
	// gate: it runs before Accept and has already rejected every origin the
	// module does not trust. Accept performs no origin verification of its
	// own once the flag is set, so the injected policy is not a duplicate of
	// Accept's check but the replacement for it — the thing that makes
	// InsecureSkipVerify safe — and skipping Accept's check is what lets the
	// Vite dev proxy rewrite the Origin host during development.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		h.logger.Error("ws accept", "error", err, "kiosk_id", k.ID, "ip", kioskIP)
		return
	}

	h.runSession(r.Context(), k, c)
}

func (h *Hub) runSession(ctx context.Context, k kiosks.Kiosk, c conn) {
	var publishedGeneration *sessionGeneration
	defer func() {
		// Closing first interrupts a Push blocked on this connection.
		c.CloseNow()
		if publishedGeneration != nil {
			h.removeSessionIfCurrent(k.ID, publishedGeneration)
		}
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

	initialStatus, err := h.readStatusFrame(ctx, c)
	if err != nil {
		h.logger.Warn("kiosk handshake failed", "error", err, "kiosk_id", k.ID, "name", k.Name)
		return
	}

	publishedGeneration = &sessionGeneration{connection: c, status: initialStatus}
	h.publishSession(k.ID, publishedGeneration)
	h.logger.Info("kiosk connected", "kiosk_id", k.ID, "name", k.Name, "ip", k.IP, "status", initialStatus)

	for {
		reportedStatus, err := h.readStatusFrame(ctx, c)
		if err != nil {
			return
		}
		h.updateStatusIfCurrent(k.ID, publishedGeneration, reportedStatus)
	}
}

func (h *Hub) readStatusFrame(ctx context.Context, c conn) (Status, error) {
	_, data, err := c.Read(ctx)
	if err != nil {
		return "", err
	}
	return decodeStatusFrame(data)
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

func (h *Hub) publishSession(id uuid.UUID, generation *sessionGeneration) {
	h.mu.Lock()
	slot, found := h.sessionSlots[id]
	if !found {
		h.sessionSlots[id] = &sessionSlot{current: generation}
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	slot.mu.Lock()
	h.mu.Lock()
	if h.sessionSlots[id] == slot {
		slot.current = generation
	} else {
		h.sessionSlots[id] = &sessionSlot{current: generation}
	}
	h.mu.Unlock()
	slot.mu.Unlock()
}

func (h *Hub) removeSessionIfCurrent(id uuid.UUID, generation *sessionGeneration) {
	h.mu.Lock()
	slot, found := h.sessionSlots[id]
	h.mu.Unlock()
	if !found {
		return
	}

	slot.mu.Lock()
	h.mu.Lock()
	if h.sessionSlots[id] == slot && slot.current == generation {
		slot.current = nil
		delete(h.sessionSlots, id)
	}
	h.mu.Unlock()
	slot.mu.Unlock()
}

func (h *Hub) updateStatusIfCurrent(id uuid.UUID, generation *sessionGeneration, status Status) {
	h.mu.Lock()
	slot, found := h.sessionSlots[id]
	if !found {
		h.mu.Unlock()
		h.logger.Info("kiosk status ignored: no live session", "kiosk_id", id)
		return
	}
	if slot.current != generation {
		h.mu.Unlock()
		h.logger.Info("kiosk status ignored: stale generation", "kiosk_id", id)
		return
	}
	generation.status = status
	h.mu.Unlock()
}

// PushSign sends a signing instruction to the current kiosk session.
// Session replacement and removal cannot interleave with the socket write.
func (h *Hub) PushSign(ctx context.Context, id uuid.UUID, url string) error {
	data, err := marshal(newSign(url))
	if err != nil {
		h.logger.Error("push failed: marshal", "error", err, "kiosk_id", id)
		return err
	}

	h.mu.Lock()
	slot, found := h.sessionSlots[id]
	h.mu.Unlock()

	if !found {
		h.logger.Warn("push failed: kiosk not connected", "kiosk_id", id)
		return fmt.Errorf("%w: %s", ErrNotConnected, id)
	}

	slot.mu.Lock()
	defer slot.mu.Unlock()

	generation := slot.current
	if generation == nil {
		h.logger.Warn("push failed: kiosk not connected", "kiosk_id", id)
		return fmt.Errorf("%w: %s", ErrNotConnected, id)
	}

	if err := generation.connection.Write(ctx, websocket.MessageText, data); err != nil {
		h.logger.Error("push failed: write to kiosk", "error", err, "kiosk_id", id)
		return fmt.Errorf("%w: %w", ErrWriteFailed, err)
	}
	return nil
}

// Statuses returns a snapshot of each published kiosk's reported status.
func (h *Hub) Statuses() StatusSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	statuses := make(StatusSnapshot, len(h.sessionSlots))
	for id, slot := range h.sessionSlots {
		statuses[id] = slot.current.status
	}
	return statuses
}
