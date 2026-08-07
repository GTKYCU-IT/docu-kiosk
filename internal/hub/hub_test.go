package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/protocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// fakeConn is a scriptable conn used in place of a real WebSocket. Writes are
// recorded (as copies) under a mutex; Read blocks on readCh and returns an
// error once it is closed, but also returns ctx.Err() when ctx is done —
// mirroring real conn semantics.
type fakeConn struct {
	mu         sync.Mutex
	writes     [][]byte
	writeErr   error
	pingErr    error
	readCh     chan struct{}
	readClosed bool
	closed     bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{readCh: make(chan struct{})}
}

// closeRead releases a blocked Read. Safe to call more than once.
func (f *fakeConn) closeRead() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.readClosed {
		close(f.readCh)
		f.readClosed = true
	}
}

func (f *fakeConn) setWriteErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeErr = err
}

func (f *fakeConn) Write(_ context.Context, _ websocket.MessageType, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}

func (f *fakeConn) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case <-f.readCh:
		return 0, nil, errors.New("connection closed")
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (f *fakeConn) CloseNow() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConn) recordedWrites() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

func (f *fakeConn) lastWrite() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return nil
	}
	return f.writes[len(f.writes)-1]
}

// fakeStore is a map-backed KioskStore: unknown IPs yield ErrKioskNotFound,
// and an injected lookupErr overrides everything.
type fakeStore struct {
	kiosks    map[string]Kiosk
	lookupErr error
}

func newFakeStore(kiosks map[string]Kiosk) *fakeStore {
	return &fakeStore{kiosks: kiosks}
}

func (s *fakeStore) GetKioskByIP(_ context.Context, ip string) (Kiosk, error) {
	if s.lookupErr != nil {
		return Kiosk{}, s.lookupErr
	}
	k, ok := s.kiosks[ip]
	if !ok {
		return Kiosk{}, ErrKioskNotFound
	}
	return k, nil
}

// syncBuffer is a bytes.Buffer safe for concurrent log writes from session
// goroutines and reads from the test goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestHub(store KioskStore) (*Hub, *syncBuffer) {
	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	return New(store, logger), buf
}

// waitFor polls condition until it holds or the deadline passes.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2 seconds")
}

// startSession runs runSession in a goroutine and returns the conn, a done
// channel closed when the session exits, and a cleanup that releases the
// session's read and waits for it to finish.
func startSession(t *testing.T, h *Hub, k Kiosk, ip string, c *fakeConn) chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runSession(context.Background(), k, ip, c)
	}()
	t.Cleanup(func() {
		c.closeRead()
		<-done
	})
	return done
}

func TestServeRejectsUnregisteredIP(t *testing.T) {
	h, _ := newTestHub(newFakeStore(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	h.Serve(rec, req, "10.0.0.99")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestServeAuthLookupErrorIsServerError(t *testing.T) {
	h, _ := newTestHub(&fakeStore{lookupErr: errors.New("db down")})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	h.Serve(rec, req, "10.0.0.5")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestRunSessionGreetsAndRegisters(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(fc.recordedWrites()) >= 1
	})

	want, err := protocol.Marshal(protocol.NewGreeting("lobby"))
	if err != nil {
		t.Fatalf("marshal greeting: %v", err)
	}
	if got := fc.lastWrite(); !bytes.Equal(got, want) {
		t.Errorf("expected greeting %q, got %q", want, got)
	}
}

func TestRunSessionDisconnectRemovesSession(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	waitFor(t, func() bool { return slices.Contains(h.Connected(), id) })

	fc.closeRead()
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
}

func TestRunSessionPingFailureClosesSession(t *testing.T) {
	h, _ := newTestHub(nil)
	h.pingInterval = 5 * time.Millisecond
	h.pingTimeout = 5 * time.Millisecond
	id := uuid.New()
	fc := newFakeConn()
	fc.pingErr = errors.New("ping timeout")
	startSession(t, h, Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	waitFor(t, func() bool { return slices.Contains(h.Connected(), id) })
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
}

func TestRunSessionReconnectReplacesSession(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()

	connA := newFakeConn()
	startSession(t, h, Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connA)
	waitFor(t, func() bool { return slices.Contains(h.Connected(), id) })

	connB := newFakeConn()
	startSession(t, h, Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connB)

	// B's greeting proves B registered and replaced A in the sessions map.
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })

	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("expected exactly session %v, got %v", id, got)
	}

	connB.closeRead()
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
}

func TestSendWritesProtocolMessage(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(fc.recordedWrites()) >= 1
	})

	want := protocol.NewSign("https://docusign.example.com/signing/abc123")
	if err := h.Send(context.Background(), id, want); err != nil {
		t.Fatalf("send: %v", err)
	}

	var got protocol.Sign
	if err := json.Unmarshal(fc.lastWrite(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestSendToUnregistered(t *testing.T) {
	h, _ := newTestHub(nil)
	err := h.Send(context.Background(), uuid.New(), protocol.NewSign("https://example.com"))
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
	if errors.Is(err, ErrWriteFailed) {
		t.Error("unregistered send must not be ErrWriteFailed")
	}
}

func TestSendWriteFailureIsServerError(t *testing.T) {
	h, buf := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	// Fail only the Send write, after the greeting succeeded.
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(fc.recordedWrites()) >= 1
	})
	fc.setWriteErr(errors.New("socket closed"))

	err := h.Send(context.Background(), id, protocol.NewSign("https://example.com"))
	if !errors.Is(err, ErrWriteFailed) {
		t.Errorf("expected ErrWriteFailed, got %v", err)
	}
	if errors.Is(err, ErrNotConnected) {
		t.Error("write failure must not be ErrNotConnected")
	}

	logged := buf.String()
	if !strings.Contains(logged, "write to kiosk") {
		t.Errorf("expected log to mention write to kiosk, got: %s", logged)
	}
	if !strings.Contains(logged, id.String()) {
		t.Errorf("expected log to contain kiosk id %s, got: %s", id, logged)
	}
}

func TestConcurrent(t *testing.T) {
	h, _ := newTestHub(newFakeStore(nil))
	const sessions = 20
	const churners = 20
	const iterations = 50

	conns := make([]*fakeConn, sessions)
	ids := make([]uuid.UUID, sessions)
	var sessionsWG sync.WaitGroup
	for i := range sessions {
		id := uuid.New()
		ids[i] = id
		fc := newFakeConn()
		conns[i] = fc
		sessionsWG.Add(1)
		go func() {
			defer sessionsWG.Done()
			h.runSession(context.Background(), Kiosk{ID: id, Name: "kiosk"}, "10.0.0.5", fc)
		}()
	}

	var churnWG sync.WaitGroup
	for range churners {
		churnWG.Add(1)
		go func() {
			defer churnWG.Done()
			for range iterations {
				h.Connected()
				_ = h.Send(context.Background(), ids[rand.IntN(sessions)], protocol.NewSign("https://example.com"))
			}
		}()
	}
	churnWG.Wait()

	for _, fc := range conns {
		fc.closeRead()
	}
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
	sessionsWG.Wait()
}
