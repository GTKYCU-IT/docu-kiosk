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

	"github.com/calvertjadon/docu-kiosk/internal/kiosks"
	"github.com/calvertjadon/docu-kiosk/internal/protocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// fakeConn is a scriptable conn used in place of a real WebSocket. Writes are
// recorded (as copies) under a mutex; Read blocks on readCh and returns an
// error once it is closed, but also returns ctx.Err() when ctx is done —
// mirroring real conn semantics.
type fakeConn struct {
	mu           sync.Mutex
	writes       [][]byte
	writeErr     error
	pingErr      error
	readCh       chan struct{}
	readClosed   bool
	closed       bool
	writeGate    chan struct{} // non-nil: Write blocks on it before recording
	writeEntered chan struct{} // non-nil: closed when the first gated Write begins
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

// setWriteGate makes subsequent Writes signal entered and block until gate is
// closed, letting a test hold a write in flight while the hub state changes.
func (f *fakeConn) setWriteGate(gate, entered chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeGate = gate
	f.writeEntered = entered
}

func (f *fakeConn) Write(_ context.Context, _ websocket.MessageType, data []byte) error {
	f.mu.Lock()
	gate, entered := f.writeGate, f.writeEntered
	f.mu.Unlock()
	if gate != nil {
		if entered != nil {
			close(entered)
		}
		<-gate
	}
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

// isClosed reports whether CloseNow has been observed, i.e. the session's
// deferred teardown has fully run.
func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
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

// fakeStore is a map-backed KioskStore: unknown IPs yield kiosks.ErrNotFound,
// and an injected lookupErr overrides everything.
type fakeStore struct {
	kiosks    map[string]kiosks.Kiosk
	lookupErr error
}

func newFakeStore(kiosks map[string]kiosks.Kiosk) *fakeStore {
	return &fakeStore{kiosks: kiosks}
}

func (s *fakeStore) GetKioskByIP(_ context.Context, ip string) (kiosks.Kiosk, error) {
	if s.lookupErr != nil {
		return kiosks.Kiosk{}, s.lookupErr
	}
	k, ok := s.kiosks[ip]
	if !ok {
		return kiosks.Kiosk{}, kiosks.ErrNotFound
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5 seconds")
}

// startSession runs runSession in a goroutine and returns the conn, a done
// channel closed when the session exits, and a cleanup that releases the
// session's read and waits for it to finish.
func startSession(t *testing.T, h *Hub, k kiosks.Kiosk, ip string, c *fakeConn) chan struct{} {
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
	if rec.Body.String() != "unregistered ip\n" {
		t.Errorf("expected body %q, got %q", "unregistered ip\n", rec.Body.String())
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
	if rec.Body.String() != "internal error\n" {
		t.Errorf("expected body %q, got %q", "internal error\n", rec.Body.String())
	}
}

func TestRunSessionGreetsAndRegisters(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

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
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

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
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	// The session may register and tear down (5ms ping interval) before the
	// first poll, so assert on the recorded greeting write instead: it is
	// written after registration and persists after teardown.
	waitFor(t, func() bool { return len(fc.recordedWrites()) >= 1 })
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
}

func TestRunSessionGreetingWriteFailureTearsDown(t *testing.T) {
	h, buf := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	// The greeting write fails before the session ever serves a message.
	fc.setWriteErr(errors.New("socket closed"))
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	// The session registers, the greeting write fails, and teardown removes it.
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
	waitFor(t, func() bool { return fc.isClosed() })

	logged := buf.String()
	if !strings.Contains(logged, "write greeting") {
		t.Errorf("expected log to mention write greeting, got: %s", logged)
	}
	if !strings.Contains(logged, id.String()) {
		t.Errorf("expected log to contain kiosk id %s, got: %s", id, logged)
	}
}

func TestRunSessionReconnectReplacesSession(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()

	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connA)
	waitFor(t, func() bool { return slices.Contains(h.Connected(), id) })

	connB := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connB)

	// B's greeting proves B registered and replaced A in the sessions map.
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })

	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("expected exactly session %v, got %v", id, got)
	}

	// Stale-teardown ordering: A's session ends only after B has replaced it.
	// A's deferred cleanup must not delete B's entry. Close A's read, then
	// wait until A's CloseNow has been observed, which proves A's cleanup ran
	// in full (delete happens before CloseNow in the deferred chain).
	connA.closeRead()
	waitFor(t, func() bool { return connA.isClosed() })

	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("stale teardown removed live session: expected exactly %v, got %v", id, got)
	}

	// The live session B must still accept writes.
	msg := protocol.NewSign("https://docusign.example.com/signing/abc123")
	if err := h.Send(context.Background(), id, msg); err != nil {
		t.Fatalf("send after stale teardown: %v", err)
	}
	var got protocol.Sign
	if err := json.Unmarshal(connB.lastWrite(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != msg {
		t.Errorf("expected %+v, got %+v", msg, got)
	}

	// B's death removes the session entirely.
	connB.closeRead()
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
}

func TestSendWritesProtocolMessage(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

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
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", fc)

	// Fail only the Send write, after the greeting succeeded.
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(fc.recordedWrites()) >= 1
	})
	underlying := errors.New("socket closed")
	fc.setWriteErr(underlying)

	err := h.Send(context.Background(), id, protocol.NewSign("https://example.com"))
	if !errors.Is(err, ErrWriteFailed) {
		t.Errorf("expected ErrWriteFailed, got %v", err)
	}
	if errors.Is(err, ErrNotConnected) {
		t.Error("write failure must not be ErrNotConnected")
	}
	if !errors.Is(err, underlying) {
		t.Errorf("expected %v to wrap underlying write error %v", err, underlying)
	}

	logged := buf.String()
	if !strings.Contains(logged, "write to kiosk") {
		t.Errorf("expected log to mention write to kiosk, got: %s", logged)
	}
	if !strings.Contains(logged, id.String()) {
		t.Errorf("expected log to contain kiosk id %s, got: %s", id, logged)
	}
}

func TestSendReconnectMidWriteIsWriteFailed(t *testing.T) {
	h, buf := newTestHub(nil)
	id := uuid.New()

	// connA is the live session; its greeting succeeds before we arm the gate.
	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	// Gate connA's Writes: Send grabs connA and blocks inside Write.
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	entered := make(chan struct{})
	connA.setWriteGate(release, entered)
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- h.Send(context.Background(), id, protocol.NewSign("https://example.com"))
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("send did not block inside Write")
	}

	// While Send is blocked, the kiosk reconnects and replaces the session.
	connB := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connB)
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })

	// Release the stale write; Send must detect the swap and fail loudly.
	close(release)
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrWriteFailed) {
			t.Errorf("expected ErrWriteFailed, got %v", err)
		}
		if errors.Is(err, ErrNotConnected) {
			t.Error("mid-write reconnect must not be ErrNotConnected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send did not return after write released")
	}

	logged := buf.String()
	if !strings.Contains(logged, "push lost: kiosk reconnected mid-write") {
		t.Errorf("expected log to mention reconnected mid-write, got: %s", logged)
	}
	if !strings.Contains(logged, id.String()) {
		t.Errorf("expected log to contain kiosk id %s, got: %s", id, logged)
	}

	// The replacement session is still live and still accepts writes.
	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("expected exactly session %v, got %v", id, got)
	}
	msg := protocol.NewSign("https://docusign.example.com/signing/def456")
	if err := h.Send(context.Background(), id, msg); err != nil {
		t.Fatalf("send to replacement session: %v", err)
	}
	var got protocol.Sign
	if err := json.Unmarshal(connB.lastWrite(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != msg {
		t.Errorf("expected %+v, got %+v", msg, got)
	}
}

func TestSendReconnectMidWriteWriteErrorIsWriteFailed(t *testing.T) {
	h, buf := newTestHub(nil)
	id := uuid.New()

	// connA is the live session; its greeting succeeds before we arm the gate.
	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	// The write itself will fail; the reconnect must not mask the underlying
	// error or drop it from the log.
	underlying := errors.New("socket closed")
	connA.setWriteErr(underlying)

	// Gate connA's Writes: Send grabs connA and blocks inside Write.
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	entered := make(chan struct{})
	connA.setWriteGate(release, entered)
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- h.Send(context.Background(), id, protocol.NewSign("https://example.com"))
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("send did not block inside Write")
	}

	// While Send is blocked, the kiosk reconnects and replaces the session.
	connB := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connB)
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })

	// Release the stale write; Send must report the failed write against the
	// lost session, wrapping both ErrWriteFailed and the underlying error.
	close(release)
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrWriteFailed) {
			t.Errorf("expected ErrWriteFailed, got %v", err)
		}
		if errors.Is(err, ErrNotConnected) {
			t.Error("mid-write reconnect must not be ErrNotConnected")
		}
		if !errors.Is(err, underlying) {
			t.Errorf("expected %v to wrap underlying write error %v", err, underlying)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send did not return after write released")
	}

	logged := buf.String()
	if !strings.Contains(logged, "push lost: kiosk reconnected mid-write") {
		t.Errorf("expected log to mention reconnected mid-write, got: %s", logged)
	}
	if !strings.Contains(logged, underlying.Error()) {
		t.Errorf("expected log to contain underlying error %q, got: %s", underlying, logged)
	}
	if !strings.Contains(logged, id.String()) {
		t.Errorf("expected log to contain kiosk id %s, got: %s", id, logged)
	}

	// The replacement session is still live and still accepts writes.
	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("expected exactly session %v, got %v", id, got)
	}
	msg := protocol.NewSign("https://docusign.example.com/signing/def456")
	if err := h.Send(context.Background(), id, msg); err != nil {
		t.Fatalf("send to replacement session: %v", err)
	}
	var got protocol.Sign
	if err := json.Unmarshal(connB.lastWrite(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != msg {
		t.Errorf("expected %+v, got %+v", msg, got)
	}
}

func TestSendDisconnectMidWriteIsWriteFailed(t *testing.T) {
	h, buf := newTestHub(nil)
	id := uuid.New()

	// connA is the live session; its greeting succeeds before we arm the gate.
	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby"}, "10.0.0.5", connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	// Gate connA's Writes: Send grabs connA and blocks inside Write.
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	entered := make(chan struct{})
	connA.setWriteGate(release, entered)
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- h.Send(context.Background(), id, protocol.NewSign("https://example.com"))
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("send did not block inside Write")
	}

	// While Send is blocked, the kiosk disconnects: teardown deletes the
	// session entry. CloseNow runs after the delete in the deferred chain, so
	// observing isClosed proves the entry is gone before we release the write.
	connA.closeRead()
	waitFor(t, func() bool { return connA.isClosed() })

	// Release the write; the session is gone, so Send must report the loss
	// instead of silently reporting success.
	close(release)
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrWriteFailed) {
			t.Errorf("expected ErrWriteFailed, got %v", err)
		}
		if errors.Is(err, ErrNotConnected) {
			t.Error("mid-write disconnect must not be ErrNotConnected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send did not return after write released")
	}

	logged := buf.String()
	if !strings.Contains(logged, "push lost: kiosk disconnected mid-write") {
		t.Errorf("expected log to mention disconnected mid-write, got: %s", logged)
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
			h.runSession(context.Background(), kiosks.Kiosk{ID: id, Name: "kiosk"}, "10.0.0.5", fc)
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
