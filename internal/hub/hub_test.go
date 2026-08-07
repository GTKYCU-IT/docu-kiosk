package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/calvertjadon/docu-kiosk/internal/protocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

func newConnPair(t *testing.T) (serverConn, clientConn *websocket.Conn) {
	t.Helper()
	ready := make(chan *websocket.Conn, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		ready <- conn
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	var err error
	clientConn, _, err = websocket.Dial(context.Background(), "ws://"+ts.Listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { clientConn.CloseNow() })

	serverConn = <-ready
	return serverConn, clientConn
}

func TestRegisterReturnsID(t *testing.T) {
	h := New()
	id := h.Register(uuid.New(), nil)
	if id == (uuid.UUID{}) {
		t.Error("expected non-zero id")
	}
}

func TestRegisterTwiceCreatesTwoSessions(t *testing.T) {
	h := New()
	id1 := h.Register(uuid.New(), nil)
	id2 := h.Register(uuid.New(), nil)

	if id1 == id2 {
		t.Error("each register call should produce a distinct ID")
	}
	if got := h.Connected(); len(got) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(got))
	}
}

func TestUnregister(t *testing.T) {
	h := New()
	id := h.Register(uuid.New(), nil)
	h.Unregister(id)
	if got := h.Connected(); len(got) != 0 {
		t.Errorf("expected 0 connected after unregister, got %d", len(got))
	}
}

func TestUnregisterNonexistentIsNoop(t *testing.T) {
	h := New()
	h.Unregister(uuid.New()) // must not panic
}

func TestConnectedEmpty(t *testing.T) {
	h := New()
	if got := h.Connected(); len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestConnected(t *testing.T) {
	h := New()
	a := uuid.New()
	b := uuid.New()
	h.Register(a, nil)
	h.Register(b, nil)

	connected := h.Connected()
	if len(connected) != 2 {
		t.Fatalf("expected 2 kiosks, got %d", len(connected))
	}
	if !slices.Contains(connected, a) || !slices.Contains(connected, b) {
		t.Errorf("unexpected IDs in connected list: %v", connected)
	}
}

func TestConnectedIncludesID(t *testing.T) {
	h := New()
	id := h.Register(uuid.New(), nil)

	connected := h.Connected()
	if len(connected) != 1 || connected[0] != id {
		t.Errorf("expected ID %v, got %v", id, connected)
	}
}

func TestSend(t *testing.T) {
	serverConn, clientConn := newConnPair(t)

	h := New()
	id := h.Register(uuid.New(), serverConn)

	want := protocol.NewSign("https://docusign.example.com/signing/abc123")

	if err := h.Send(context.Background(), id, want); err != nil {
		t.Fatalf("send: %v", err)
	}

	_, data, err := clientConn.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var got protocol.Sign
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestSendToUnconnected(t *testing.T) {
	h := New()
	err := h.Send(context.Background(), uuid.New(), protocol.NewSign("https://example.com"))
	if err == nil {
		t.Error("expected error sending to unconnected kiosk")
	}
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestConcurrent(t *testing.T) {
	h := New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := h.Register(uuid.New(), nil)
			h.Connected()
			h.Unregister(id)
		}()
	}
	wg.Wait()
	if got := h.Connected(); len(got) != 0 {
		t.Errorf("expected 0 connected after cleanup, got %d", len(got))
	}
}
