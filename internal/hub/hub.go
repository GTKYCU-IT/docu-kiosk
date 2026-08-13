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
// coordinated browser-client update (web/src/lib/protocol.ts, the manual
// mirror of these shapes).
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
	identities   map[uuid.UUID]*identity
	pingInterval time.Duration
	pingTimeout  time.Duration
}

// identity is the per-Kiosk ordering boundary. Every lifecycle operation for
// one kiosk identity — session publication and replacement, generation-
// conditional teardown, and Push — serializes on mu, so different operations
// on the same identity never interleave while different identities stay fully
// independent. The identities map (guarded by Hub.mu) is only for lookup and
// creation of these boundaries and holds exactly the identities with a live
// session. Whenever a path needs both locks it acquires the identity's mu
// first and Hub.mu second — never the other way around — so publication and
// teardown can hold an identity boundary across an atomic membership
// re-check without creating a lock cycle with each other or with Push.
type identity struct {
	mu   sync.Mutex
	conn conn // current session; nil only inside teardown, atomically removed
}

// New returns a Hub that authenticates kiosks against store and logs through
// logger. pingInterval and pingTimeout default to 30s and 5s and are mutable
// by package-hub tests. By default the origin policy rejects every
// connection; callers must inject one via WithOriginPolicy.
func New(store KioskStore, logger *slog.Logger, opts ...Option) *Hub {
	h := &Hub{
		store:        store,
		logger:       logger,
		identities:   make(map[uuid.UUID]*identity),
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

// runSession drives one kiosk connection through register, greeting, ping, and
// read, cleaning up the session on exit.
func (h *Hub) runSession(ctx context.Context, k kiosks.Kiosk, c conn) {
	defer func() {
		// Close the socket before tearing the session down: teardown waits on
		// the identity boundary, which a Push may hold across a stalled
		// Write. CloseNow interrupts that Write first, so the boundary is
		// released and teardown does not queue behind it.
		c.CloseNow()
		h.teardownSession(k.ID, c)
		h.logger.Info("kiosk disconnected", "kiosk_id", k.ID, "name", k.Name)
	}()

	h.publishSession(k.ID, c)
	h.logger.Info("kiosk connected", "kiosk_id", k.ID, "name", k.Name, "ip", k.IP)

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

// publishSession registers c as the current session for id, replacing any
// prior generation. Publication enters the identity's ordering boundary, so a
// Push or a teardown of the previous generation cannot interleave with it. A
// fresh identity publishes its conn atomically with the map insert, so
// Connected and Push never observe an identity without a session. A
// replacement re-checks membership after acquiring the boundary: if a
// concurrent teardown removed that exact identity object while publication
// waited, the object is detached and a fresh live identity is installed
// instead, so the replacement is never orphaned outside h.identities. The
// re-check holds the identity boundary across the map lock (identity-mu ->
// hub-mu, the same nesting teardown uses); like two replacements racing on
// the same identity, the last completed publication wins.
func (h *Hub) publishSession(id uuid.UUID, c conn) {
	h.mu.Lock()
	idn, ok := h.identities[id]
	if !ok {
		h.identities[id] = &identity{conn: c}
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	idn.mu.Lock()
	// Verify membership atomically with publication: teardown may have
	// removed this exact identity while we waited for its boundary. Writing
	// into the detached object would leave the session unreachable from the
	// map, so install a fresh live identity instead.
	h.mu.Lock()
	if h.identities[id] == idn {
		idn.conn = c
	} else {
		h.identities[id] = &identity{conn: c}
	}
	h.mu.Unlock()
	idn.mu.Unlock()
}

// teardownSession removes c as the current session for id, but only if c is
// still the current generation: a stale teardown from a replaced connection
// must not remove its replacement. Teardown enters the identity's ordering
// boundary, so it cannot interleave with a Push or a later publication, and
// verifies under the map lock that this identity object is still the map's
// entry before deleting: a replacement publication that installed a fresh
// identity while teardown waited is never removed by the stale generation's
// cleanup. Clearing the session and dropping the map entry happen under the
// same critical section (identity-mu -> hub-mu, the same nesting
// publication uses), so Connected and Push both observe an atomic removal.
func (h *Hub) teardownSession(id uuid.UUID, c conn) {
	h.mu.Lock()
	idn, ok := h.identities[id]
	h.mu.Unlock()
	if !ok {
		return
	}
	idn.mu.Lock()
	// Clear and remove under the map lock, verifying the map still points at
	// this identity: the entry leaves the map exactly when its session stops
	// being current, and a concurrent publication that replaced the entry is
	// left intact.
	h.mu.Lock()
	if h.identities[id] == idn && idn.conn == c {
		idn.conn = nil
		delete(h.identities, id)
	}
	h.mu.Unlock()
	idn.mu.Unlock()
}

// PushSign instructs a connected kiosk to open a signing session at url.
// Unregistered -> ErrNotConnected (Warn-logged). Registered but write fails
// -> ErrWriteFailed wrapping the write error (Error-logged with kiosk_id).
// PushSign enters the identity's ordering boundary and holds it across
// selecting the current session and completing the write, so no replacement
// or removal can linearize while the write is in flight: a Push ordered
// before a replacement completes against the prior generation, while one
// ordered at or after the replacement sees only the replacement, and one
// ordered after removal gets ErrNotConnected. The map lock is dropped after
// lookup and never reacquired under the boundary, so the only lock held
// across the write is the identity's own: a stalled Write cannot block
// Connected or a concurrent publication, and CloseNow from the session's
// cleanup interrupts it so teardown is not left waiting on the boundary.
// Logging of failures lives INSIDE PushSign.
func (h *Hub) PushSign(ctx context.Context, id uuid.UUID, url string) error {
	data, err := marshal(newSign(url))
	if err != nil {
		h.logger.Error("push failed: marshal", "error", err, "kiosk_id", id)
		return err
	}

	h.mu.Lock()
	idn, ok := h.identities[id]
	h.mu.Unlock()

	if !ok {
		h.logger.Warn("push failed: kiosk not connected", "kiosk_id", id)
		return fmt.Errorf("%w: %s", ErrNotConnected, id)
	}

	// Hold the identity boundary across session selection and the write so
	// the selected session cannot be replaced or removed mid-write.
	idn.mu.Lock()
	defer idn.mu.Unlock()

	c := idn.conn
	if c == nil {
		// The session ended between the lookup and the boundary acquisition;
		// this push is ordered after the removal.
		h.logger.Warn("push failed: kiosk not connected", "kiosk_id", id)
		return fmt.Errorf("%w: %s", ErrNotConnected, id)
	}

	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		h.logger.Error("push failed: write to kiosk", "error", err, "kiosk_id", id)
		return fmt.Errorf("%w: %w", ErrWriteFailed, err)
	}
	return nil
}

// Connected returns the UUIDs of all connected kiosks.
func (h *Hub) Connected() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]uuid.UUID, 0, len(h.identities))
	for id := range h.identities {
		ids = append(ids, id)
	}
	return ids
}
