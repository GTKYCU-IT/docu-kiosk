package hub

import (
	"bytes"
	"context"
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
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// fakeConn is a scriptable conn used in place of a real WebSocket. Writes are
// recorded (as copies) under a mutex; Read blocks on readCh and returns an
// error once it is closed, but also returns ctx.Err() when ctx is done —
// mirroring real conn semantics.
type fakeConn struct {
	mu             sync.Mutex
	writes         [][]byte
	writeErr       error
	pingErr        error
	readCh         chan struct{}
	readClosed     bool
	closed         bool
	writeGate      chan struct{} // non-nil: Write blocks on it before recording
	writeEntered   chan struct{} // non-nil: closed when the first gated Write begins
	writeCloseGate chan struct{} // non-nil: Write blocks until CloseNow releases it
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

// setWriteGate makes the next Write signal entered and block until gate is
// closed, letting a test hold a write in flight while the hub state changes.
// The gate is one-shot: the first Write consumes it, so later writes on the
// same conn proceed ungated. Mutually exclusive with the close-release mode.
func (f *fakeConn) setWriteGate(gate, entered chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeGate = gate
	f.writeEntered = entered
	f.writeCloseGate = nil
}

// setWriteBlockedUntilClose arms close-release mode: subsequent Writes signal
// entered and block until CloseNow releases them, whereupon they fail with
// the configured write error (or a default connection-closed error). This
// mirrors a real socket, whose CloseNow interrupts an in-flight Write.
// Mutually exclusive with the gate mode.
func (f *fakeConn) setWriteBlockedUntilClose(entered chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCloseGate = make(chan struct{})
	f.writeEntered = entered
	f.writeGate = nil
}

func (f *fakeConn) Write(_ context.Context, _ websocket.MessageType, data []byte) error {
	f.mu.Lock()
	gate, closeGate, entered := f.writeGate, f.writeCloseGate, f.writeEntered
	// Each armed gate is one-shot: the first Write atomically consumes the
	// gate and its entered signal under the lock, so a later write on the
	// same conn is ungated and never re-signals — and thus never re-closes —
	// an entered channel the first write already closed. Close-release mode
	// keeps blocking every write until CloseNow, but only the first write
	// announces itself on entered.
	if gate != nil || closeGate != nil {
		f.writeEntered = nil
	}
	if gate != nil {
		f.writeGate = nil
	}
	f.mu.Unlock()
	if gate != nil {
		if entered != nil {
			close(entered)
		}
		<-gate
	}
	if closeGate != nil {
		if entered != nil {
			close(entered)
		}
		// Block until CloseNow interrupts the write, then fail: an
		// interrupted write never reaches the socket.
		<-closeGate
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.writeErr != nil {
			return f.writeErr
		}
		return errors.New("connection closed")
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
	// Release any Write blocked in close-release mode. Idempotent: a second
	// CloseNow must not panic on a channel it already closed.
	if f.writeCloseGate != nil {
		select {
		case <-f.writeCloseGate:
		default:
			close(f.writeCloseGate)
		}
	}
	return nil
}

// isClosed reports whether CloseNow has been observed. Because runSession's
// deferred cleanup calls CloseNow before teardownSession, this is not proof
// the session's teardown completed: wait on startSession's done channel for
// that.
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
	// Tests of the session machinery opt in to a permissive policy; the
	// origin gate itself is pinned by the TestServeOriginPolicy* tests.
	return New(store, logger, WithOriginPolicy(func(r *http.Request) bool { return true })), buf
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

// assertBlockedWhileGated fails if the completion signal fires while the
// write gate remains closed. It asserts that a lifecycle transition queued
// behind a blocked Push cannot complete while the Push still holds the
// identity boundary: the bounded window is only the negative probe, and
// callers must observe the same signal positively after the gate opens (see
// assertCompletesAfterRelease).
func assertBlockedWhileGated(t *testing.T, what string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
		t.Fatalf("%s completed while a push held the identity boundary", what)
	case <-time.After(300 * time.Millisecond):
	}
}

// assertCompletesAfterRelease fails if the completion signal does not fire
// once the write gate has been opened: the transition queued behind the
// blocked Push must complete after the boundary is released.
func assertCompletesAfterRelease(t *testing.T, what string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not complete after the write gate was released", what)
	}
}

// assertDone fails if the session's done channel does not close before the
// deadline: runSession fully returned, so CloseNow and the conditional
// teardown both completed.
func assertDone(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not finish within 2 seconds", what)
	}
}

// startSession runs runSession in a goroutine and returns the conn, a done
// channel closed when the session exits, and a cleanup that releases the
// session's read and waits for it to finish.
func startSession(t *testing.T, h *Hub, k kiosks.Kiosk, c *fakeConn) chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runSession(context.Background(), k, c)
	}()
	t.Cleanup(func() {
		c.closeRead()
		<-done
	})
	return done
}

// assertPushedSign asserts that the conn's last write is exactly the sign
// wire JSON for the given url.
func assertPushedSign(t *testing.T, fc *fakeConn, url string) {
	t.Helper()
	want := `{"type":"sign","url":"` + url + `"}`
	if got := string(fc.lastWrite()); got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
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

// TestServeOriginPolicyRejects pins the fail-closed default: a Hub refuses
// every connection with 403 before the store lookup or accept — whether no
// policy was injected at all (nil) or the injected policy returned false —
// even one that would otherwise authenticate.
func TestServeOriginPolicyRejects(t *testing.T) {
	tests := []struct {
		name   string
		policy OriginPolicy
	}{
		{name: "no policy injected"},
		{name: "policy returns false", policy: func(r *http.Request) bool { return false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &syncBuffer{}
			logger := slog.New(slog.NewTextHandler(buf, nil))
			var opts []Option
			if tt.policy != nil {
				opts = append(opts, WithOriginPolicy(tt.policy))
			}
			h := New(newFakeStore(nil), logger, opts...)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			h.Serve(rec, req, "10.0.0.99")
			if rec.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d", rec.Code)
			}
			if rec.Body.String() != "origin rejected\n" {
				t.Errorf("expected body %q, got %q", "origin rejected\n", rec.Body.String())
			}
		})
	}
}

// TestServeOriginPolicyAllowsRealConnection proves an admitting policy lets
// the connection proceed all the way through accept and session start: the
// kiosk receives the greeting over a real WebSocket.
func TestServeOriginPolicyAllowsRealConnection(t *testing.T) {
	id := uuid.New()
	h, _ := newTestHub(newFakeStore(map[string]kiosks.Kiosk{
		"10.0.0.5": {ID: id, Name: "lobby"},
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		h.Serve(w, r, "10.0.0.5")
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{ts.URL}},
	})
	if err != nil {
		t.Fatalf("dial with admitting policy: %v", err)
	}
	defer conn.CloseNow()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"connected"`; !strings.Contains(string(data), want) {
		t.Errorf("first message = %s, want it to contain %s", data, want)
	}
}

func TestRunSessionGreetsAndRegisters(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, fc)

	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(fc.recordedWrites()) >= 1
	})

	want := `{"name":"lobby","type":"connected"}`
	if got := string(fc.lastWrite()); got != want {
		t.Errorf("expected greeting %s, got %s", want, got)
	}
}

func TestRunSessionDisconnectRemovesSession(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, fc)

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
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, fc)

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
	done := startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, fc)

	// The session registers, the greeting write fails, and teardown removes
	// it. The done channel closes only after CloseNow and the conditional
	// teardown both completed.
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
	assertDone(t, "session teardown", done)

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
	doneA := startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
	waitFor(t, func() bool { return slices.Contains(h.Connected(), id) })

	connB := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connB)

	// B's greeting proves B registered and replaced A in the sessions map.
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })

	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("expected exactly session %v, got %v", id, got)
	}

	// Stale-teardown ordering: A's session ends only after B has replaced it.
	// A's deferred cleanup must not delete B's entry. Close A's read, then
	// wait for A's runSession to return: the done channel closes only after
	// CloseNow and the conditional teardown both completed.
	connA.closeRead()
	assertDone(t, "stale session teardown", doneA)

	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("stale teardown removed live session: expected exactly %v, got %v", id, got)
	}

	// The live session B must still accept writes.
	url := "https://docusign.example.com/signing/abc123"
	if err := h.PushSign(context.Background(), id, url); err != nil {
		t.Fatalf("push sign after stale teardown: %v", err)
	}
	assertPushedSign(t, connB, url)

	// B's death removes the session entirely.
	connB.closeRead()
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
}

func TestPushSignWritesSignMessage(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, fc)

	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(fc.recordedWrites()) >= 1
	})

	url := "https://docusign.example.com/signing/abc123"
	if err := h.PushSign(context.Background(), id, url); err != nil {
		t.Fatalf("push sign: %v", err)
	}

	assertPushedSign(t, fc, url)
}

func TestPushSignToUnregistered(t *testing.T) {
	h, _ := newTestHub(nil)
	// Unknown identities yield ErrNotConnected without allocating state:
	// repeated pushes to many distinct unknown UUIDs must keep the hub empty
	// and never be misclassified as write failures.
	for range 50 {
		err := h.PushSign(context.Background(), uuid.New(), "https://example.com")
		if !errors.Is(err, ErrNotConnected) {
			t.Errorf("expected ErrNotConnected, got %v", err)
		}
		if errors.Is(err, ErrWriteFailed) {
			t.Error("unregistered push must not be ErrWriteFailed")
		}
	}
	if got := h.Connected(); len(got) != 0 {
		t.Errorf("pushes to unknown kiosks must not publish sessions, got %v", got)
	}
}

func TestPushSignWriteFailureIsServerError(t *testing.T) {
	h, buf := newTestHub(nil)
	id := uuid.New()
	fc := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, fc)

	// Fail only the PushSign write, after the greeting succeeded.
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(fc.recordedWrites()) >= 1
	})
	underlying := errors.New("socket closed")
	fc.setWriteErr(underlying)

	err := h.PushSign(context.Background(), id, "https://example.com")
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

// TestPushSignBlockedPushCompletesBeforeReplacement pins the linearization of
// a Push ordered before a reconnect: PushSign holds the identity boundary
// across session selection and the write, so the replacement cannot publish
// until the push completes. The prior-generation push therefore succeeds and
// delivers to the old session, and only later pushes target the replacement.
func TestPushSignBlockedPushCompletesBeforeReplacement(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()

	// connA is the live session; its greeting succeeds before we arm the gate.
	connA := newFakeConn()
	doneA := startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	// Gate connA's Writes: PushSign selects connA and blocks inside Write,
	// holding the identity boundary for the whole write.
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
	url1 := "https://docusign.example.com/signing/abc123"
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- h.PushSign(context.Background(), id, url1)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not block inside Write")
	}

	// While the push is blocked, the kiosk reconnects. Publication of the
	// replacement waits on the identity boundary, so its greeting write — the
	// publication completion signal — cannot begin before the push releases
	// the boundary.
	connB := newFakeConn()
	gateB := make(chan struct{})
	defer func() {
		select {
		case <-gateB:
		default:
			close(gateB)
		}
	}()
	publishedB := make(chan struct{})
	connB.setWriteGate(gateB, publishedB)
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connB)
	assertBlockedWhileGated(t, "replacement publication", publishedB)

	// Release the write: the push, ordered before the replacement, succeeds
	// against the prior generation and delivers to connA.
	close(release)
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("push ordered before replacement must succeed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not return after write released")
	}
	assertPushedSign(t, connA, url1)

	// The queued replacement now publishes: its greeting write begins once
	// the boundary is released, then completes and owns the session.
	assertCompletesAfterRelease(t, "replacement publication", publishedB)
	close(gateB)
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })
	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("expected exactly session %v, got %v", id, got)
	}
	// connB never received the prior-generation push.
	if writes := connB.recordedWrites(); len(writes) != 1 {
		t.Fatalf("replacement conn got %d writes, want only the greeting", len(writes))
	}

	// The stale session's teardown must not remove its replacement. The done
	// channel closes only after CloseNow and the conditional teardown both
	// completed, so the assertion runs after A's runSession fully returned.
	connA.closeRead()
	assertDone(t, "stale session teardown", doneA)
	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("stale teardown removed live session: expected exactly %v, got %v", id, got)
	}

	// A push ordered after the replacement sees only the replacement.
	url2 := "https://docusign.example.com/signing/def456"
	if err := h.PushSign(context.Background(), id, url2); err != nil {
		t.Fatalf("push sign to replacement session: %v", err)
	}
	assertPushedSign(t, connB, url2)
	// The replaced session never receives a post-replacement delivery.
	if writes := connA.recordedWrites(); len(writes) != 2 {
		t.Fatalf("replaced conn got %d writes, want greeting + prior-generation push", len(writes))
	}
}

// TestPushSignBlockedPushWriteErrorWithPendingReplacement pins error
// classification when the write itself fails while a replacement waits on the
// identity boundary: the push reports ErrWriteFailed wrapping the underlying
// write error through the unchanged write-failure path (not a stale-generation
// failure, not ErrNotConnected), and the queued replacement publishes after.
func TestPushSignBlockedPushWriteErrorWithPendingReplacement(t *testing.T) {
	h, buf := newTestHub(nil)
	id := uuid.New()

	// connA is the live session; its greeting succeeds before we arm the gate.
	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	// The write itself will fail; the pending replacement must not mask the
	// underlying error or change its classification.
	underlying := errors.New("socket closed")
	connA.setWriteErr(underlying)

	// Gate connA's Writes: PushSign selects connA and blocks inside Write.
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
		sendDone <- h.PushSign(context.Background(), id, "https://example.com")
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not block inside Write")
	}

	// A reconnect queues behind the blocked push. The replacement's greeting
	// write is gated, so the greeting write beginning is the publication
	// completion signal.
	connB := newFakeConn()
	gateB := make(chan struct{})
	defer func() {
		select {
		case <-gateB:
		default:
			close(gateB)
		}
	}()
	publishedB := make(chan struct{})
	connB.setWriteGate(gateB, publishedB)
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connB)
	assertBlockedWhileGated(t, "replacement publication", publishedB)

	// Release the write: the push fails with the write error, wrapping both
	// ErrWriteFailed and the underlying error.
	close(release)
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrWriteFailed) {
			t.Errorf("expected ErrWriteFailed, got %v", err)
		}
		if errors.Is(err, ErrNotConnected) {
			t.Error("write failure must not be ErrNotConnected")
		}
		if !errors.Is(err, underlying) {
			t.Errorf("expected %v to wrap underlying write error %v", err, underlying)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not return after write released")
	}

	// The failure is logged through the unchanged write-failure path, with
	// the kiosk id, not as a lost-generation failure.
	logged := buf.String()
	if !strings.Contains(logged, "write to kiosk") {
		t.Errorf("expected log to mention write to kiosk, got: %s", logged)
	}
	if !strings.Contains(logged, id.String()) {
		t.Errorf("expected log to contain kiosk id %s, got: %s", id, logged)
	}
	if strings.Contains(logged, "push lost") {
		t.Errorf("expected no lost-push log, got: %s", logged)
	}

	// The queued replacement publishes and remains fully functional.
	assertCompletesAfterRelease(t, "replacement publication", publishedB)
	close(gateB)
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })
	if got := h.Connected(); len(got) != 1 || got[0] != id {
		t.Fatalf("expected exactly session %v, got %v", id, got)
	}
	url := "https://docusign.example.com/signing/def456"
	if err := h.PushSign(context.Background(), id, url); err != nil {
		t.Fatalf("push sign to replacement session: %v", err)
	}
	assertPushedSign(t, connB, url)
}

// TestPushSignBlockedPushCompletesBeforeRemoval pins the linearization of a
// Push ordered before a disconnect: teardown waits on the identity boundary,
// so the removal cannot complete while the push is in flight; the push
// succeeds against the still-published session, and only then does the
// session leave the hub. A push ordered after the removal gets ErrNotConnected.
func TestPushSignBlockedPushCompletesBeforeRemoval(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()

	// connA is the live session; its greeting succeeds before we arm the gate.
	connA := newFakeConn()
	doneA := startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	// Gate connA's Writes: PushSign selects connA and blocks inside Write,
	// holding the identity boundary for the whole write.
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
	url := "https://docusign.example.com/signing/abc123"
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- h.PushSign(context.Background(), id, url)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not block inside Write")
	}

	// While the push is blocked, the kiosk disconnects. Teardown waits on the
	// identity boundary: the session must stay published and its goroutine
	// alive until the push is released.
	connA.closeRead()
	assertBlockedWhileGated(t, "session removal", doneA)

	// Release the write: the push, ordered before the removal, succeeds and
	// delivers against the still-published session.
	close(release)
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("push ordered before removal must succeed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not return after write released")
	}
	assertPushedSign(t, connA, url)

	// The queued teardown now completes and removes the session: doneA closes
	// only after CloseNow and the conditional teardown both completed.
	assertCompletesAfterRelease(t, "session removal", doneA)
	waitFor(t, func() bool { return len(h.Connected()) == 0 })
}

// TestPushSignStalledWriteReleasedByCloseNow pins the session cleanup defer
// order: CloseNow runs before teardownSession so a Push stalled inside Write
// is interrupted, releases the identity boundary, and teardown completes. A
// teardown-first defer order deadlocks instead: teardown waits on the
// boundary the stalled write holds, CloseNow never executes, and neither the
// push nor the session ever completes.
func TestPushSignStalledWriteReleasedByCloseNow(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()

	connA := newFakeConn()
	done := startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	// Arm close-release mode: the push's Write signals entered, then blocks
	// until CloseNow interrupts it — the stalled write teardown must unstick.
	closeErr := errors.New("socket closed")
	connA.setWriteErr(closeErr)
	entered := make(chan struct{})
	connA.setWriteBlockedUntilClose(entered)
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- h.PushSign(context.Background(), id, "https://docusign.example.com/signing/abc123")
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not block inside Write")
	}

	// End the session: the read loop returns and the deferred cleanup runs.
	connA.closeRead()

	// CloseNow releases the stalled write; the push fails as a write failure
	// wrapping the configured close error, not as ErrNotConnected.
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrWriteFailed) {
			t.Errorf("expected ErrWriteFailed, got %v", err)
		}
		if errors.Is(err, ErrNotConnected) {
			t.Error("interrupted write must not be ErrNotConnected")
		}
		if !errors.Is(err, closeErr) {
			t.Errorf("expected %v to wrap close error %v", err, closeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push sign did not return after CloseNow released the write")
	}

	// The released boundary lets teardown finish and removes the session.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session teardown did not finish after the stalled write was released")
	}
	waitFor(t, func() bool { return len(h.Connected()) == 0 })

	// Cleanup is idempotent: a second CloseNow after teardown must not panic.
	connA.CloseNow()
}

// TestPushSignAfterReplacementWritesToCurrentGeneration pins the ordering of
// a Push at or after a reconnect: once the replacement has published, pushes
// select the replacement only, and the replaced session never receives a
// post-replacement delivery.
func TestPushSignAfterReplacementWritesToCurrentGeneration(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()

	// connA is replaced by connB; B's greeting proves B published.
	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})
	connB := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connB)
	waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })

	// Arm connA's write gate so any (forbidden) post-replacement delivery to
	// the old session would block and be observable as an in-flight write.
	gate := make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	entered := make(chan struct{})
	connA.setWriteGate(gate, entered)

	url := "https://docusign.example.com/signing/abc123"
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- h.PushSign(context.Background(), id, url)
	}()
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("push sign to replacement session: %v", err)
		}
	case <-entered:
		t.Fatal("push after replacement wrote to the replaced session")
	case <-time.After(2 * time.Second):
		t.Fatal("push after replacement did not complete")
	}
	assertPushedSign(t, connB, url)

	// The replaced session still has only its greeting.
	if writes := connA.recordedWrites(); len(writes) != 1 {
		t.Fatalf("replaced conn got %d writes, want only the greeting", len(writes))
	}
}

// TestPushSignAfterDisconnectIsNotConnected pins the ordering of a Push at or
// after a disconnect: once the removal has completed, the push gets
// ErrNotConnected.
func TestPushSignAfterDisconnectIsNotConnected(t *testing.T) {
	h, _ := newTestHub(nil)
	id := uuid.New()

	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
	waitFor(t, func() bool {
		return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
	})

	connA.closeRead()
	waitFor(t, func() bool { return len(h.Connected()) == 0 })

	err := h.PushSign(context.Background(), id, "https://example.com")
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
	if errors.Is(err, ErrWriteFailed) {
		t.Error("push after disconnect must not be ErrWriteFailed")
	}
}

// TestTeardownRacingReplacementLeavesReplacementLive pins the resolution of
// an old-generation teardown racing a replacement publication on the same
// identity. Both operations queue behind a Push holding the identity
// boundary; whichever wins the boundary after the push releases, the
// replacement must end up reachable: Connected reports it and Push delivers
// to it. The old teardown can neither remove the replacement (when
// publication wins) nor orphan it (when teardown wins and publication must
// install a fresh live identity instead of writing into the detached one).
func TestTeardownRacingReplacementLeavesReplacementLive(t *testing.T) {
	t.Run("old teardown wins the boundary", func(t *testing.T) {
		h, _ := newTestHub(nil)
		id := uuid.New()

		// connA is the old generation; its greeting succeeds before gating.
		connA := newFakeConn()
		doneA := startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
		waitFor(t, func() bool {
			return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
		})

		// Hold the identity boundary with a gated Push.
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
		url1 := "https://docusign.example.com/signing/abc123"
		sendDone := make(chan error, 1)
		go func() {
			sendDone <- h.PushSign(context.Background(), id, url1)
		}()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("push sign did not block inside Write")
		}

		// The old session disconnects first: its teardown queues behind the
		// push and cannot complete while the boundary is held.
		connA.closeRead()
		assertBlockedWhileGated(t, "session removal", doneA)

		// The replacement connection queues behind the old teardown. Its
		// greeting write is gated, so the greeting write beginning is the
		// publication completion signal.
		connB := newFakeConn()
		gateB := make(chan struct{})
		defer func() {
			select {
			case <-gateB:
			default:
				close(gateB)
			}
		}()
		publishedB := make(chan struct{})
		connB.setWriteGate(gateB, publishedB)
		startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connB)
		assertBlockedWhileGated(t, "replacement publication", publishedB)

		// Release the push: the old teardown, queued first, wins the boundary
		// and removes the old session; the replacement then publishes into a
		// fresh live identity, not the detached one.
		close(release)
		select {
		case err := <-sendDone:
			if err != nil {
				t.Fatalf("push ordered before teardown and replacement must succeed, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("push sign did not return after write released")
		}
		assertPushedSign(t, connA, url1)

		assertCompletesAfterRelease(t, "replacement publication", publishedB)
		close(gateB)
		waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })
		assertCompletesAfterRelease(t, "old teardown", doneA)

		// The replacement is the live session: Connected reports it and Push
		// delivers to it; the old teardown removed nothing.
		if got := h.Connected(); len(got) != 1 || got[0] != id {
			t.Fatalf("replacement must stay reachable: expected exactly session %v, got %v", id, got)
		}
		url2 := "https://docusign.example.com/signing/def456"
		if err := h.PushSign(context.Background(), id, url2); err != nil {
			t.Fatalf("push sign to replacement session: %v", err)
		}
		assertPushedSign(t, connB, url2)
		if writes := connA.recordedWrites(); len(writes) != 2 {
			t.Fatalf("old conn got %d writes, want greeting + pre-race push", len(writes))
		}
	})

	t.Run("replacement publication wins the boundary", func(t *testing.T) {
		h, _ := newTestHub(nil)
		id := uuid.New()

		connA := newFakeConn()
		doneA := startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connA)
		waitFor(t, func() bool {
			return slices.Contains(h.Connected(), id) && len(connA.recordedWrites()) >= 1
		})

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
		url1 := "https://docusign.example.com/signing/abc123"
		sendDone := make(chan error, 1)
		go func() {
			sendDone <- h.PushSign(context.Background(), id, url1)
		}()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("push sign did not block inside Write")
		}

		// The replacement queues first: its publication cannot complete while
		// the push holds the boundary.
		connB := newFakeConn()
		gateB := make(chan struct{})
		defer func() {
			select {
			case <-gateB:
			default:
				close(gateB)
			}
		}()
		publishedB := make(chan struct{})
		connB.setWriteGate(gateB, publishedB)
		startSession(t, h, kiosks.Kiosk{ID: id, Name: "lobby", IP: "10.0.0.5"}, connB)
		assertBlockedWhileGated(t, "replacement publication", publishedB)

		// The old session disconnects after the replacement queued: its
		// teardown waits behind the pending publication.
		connA.closeRead()
		assertBlockedWhileGated(t, "session removal", doneA)

		// Release the push: the queued publication wins the boundary and
		// replaces the session; the old teardown then finds the generation
		// already replaced and removes nothing.
		close(release)
		select {
		case err := <-sendDone:
			if err != nil {
				t.Fatalf("push ordered before teardown and replacement must succeed, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("push sign did not return after write released")
		}
		assertPushedSign(t, connA, url1)

		assertCompletesAfterRelease(t, "replacement publication", publishedB)
		close(gateB)
		waitFor(t, func() bool { return len(connB.recordedWrites()) >= 1 })
		assertCompletesAfterRelease(t, "old teardown", doneA)

		// The replacement is the live session: Connected reports it and Push
		// delivers to it; the old teardown removed nothing.
		if got := h.Connected(); len(got) != 1 || got[0] != id {
			t.Fatalf("replacement must stay reachable: expected exactly session %v, got %v", id, got)
		}
		url2 := "https://docusign.example.com/signing/def456"
		if err := h.PushSign(context.Background(), id, url2); err != nil {
			t.Fatalf("push sign to replacement session: %v", err)
		}
		assertPushedSign(t, connB, url2)
		if writes := connA.recordedWrites(); len(writes) != 2 {
			t.Fatalf("old conn got %d writes, want greeting + pre-race push", len(writes))
		}
	})
}

// TestPushBlockedOnOneKioskDoesNotBlockAnother pins per-identity
// independence: a Push blocked inside a write for one kiosk serializes only
// that identity's lifecycle. Publication (reconnect) and Push for a second
// kiosk proceed without waiting.
func TestPushBlockedOnOneKioskDoesNotBlockAnother(t *testing.T) {
	h, _ := newTestHub(nil)
	id1, id2 := uuid.New(), uuid.New()

	connA := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id1, Name: "lobby", IP: "10.0.0.5"}, connA)
	connB := newFakeConn()
	doneB := startSession(t, h, kiosks.Kiosk{ID: id2, Name: "lobby", IP: "10.0.0.5"}, connB)
	waitFor(t, func() bool {
		return len(h.Connected()) == 2 && len(connA.recordedWrites()) >= 1 && len(connB.recordedWrites()) >= 1
	})

	// Block a push for kiosk id1 inside its write.
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
	url1 := "https://docusign.example.com/signing/abc123"
	sendDone1 := make(chan error, 1)
	go func() {
		sendDone1 <- h.PushSign(context.Background(), id1, url1)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("push to first kiosk did not block inside Write")
	}

	// Publication for the other identity is unaffected: id2 reconnects and
	// its replacement greets while id1's push is still blocked.
	connC := newFakeConn()
	startSession(t, h, kiosks.Kiosk{ID: id2, Name: "lobby", IP: "10.0.0.5"}, connC)
	waitFor(t, func() bool { return len(connC.recordedWrites()) >= 1 })

	// Push for the other identity completes against the replacement.
	url2 := "https://docusign.example.com/signing/def456"
	sendDone2 := make(chan error, 1)
	go func() {
		sendDone2 <- h.PushSign(context.Background(), id2, url2)
	}()
	select {
	case err := <-sendDone2:
		if err != nil {
			t.Fatalf("push to second kiosk: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push to second kiosk blocked behind the first kiosk's push")
	}
	assertPushedSign(t, connC, url2)

	// The first kiosk's push is still in flight.
	select {
	case err := <-sendDone1:
		t.Fatalf("first kiosk's push completed while gated: %v", err)
	default:
	}

	// Releasing it completes it against the first kiosk's current session.
	close(release)
	select {
	case err := <-sendDone1:
		if err != nil {
			t.Fatalf("push to first kiosk after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push to first kiosk did not return after write released")
	}
	assertPushedSign(t, connA, url1)

	// id2's stale session teardown must not remove its replacement. The done
	// channel closes only after CloseNow and the conditional teardown both
	// completed, so the assertion runs after B's runSession fully returned.
	connB.closeRead()
	assertDone(t, "stale session teardown", doneB)
	got := h.Connected()
	if len(got) != 2 || !slices.Contains(got, id1) || !slices.Contains(got, id2) {
		t.Fatalf("expected sessions %v and %v, got %v", id1, id2, got)
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
			h.runSession(context.Background(), kiosks.Kiosk{ID: id, Name: "kiosk", IP: "10.0.0.5"}, fc)
		}()
	}

	var churnWG sync.WaitGroup
	for range churners {
		churnWG.Add(1)
		go func() {
			defer churnWG.Done()
			for range iterations {
				h.Connected()
				_ = h.PushSign(context.Background(), ids[rand.IntN(sessions)], "https://example.com")
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
