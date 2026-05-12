package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// newConnPair returns a connected server/client websocket pair. Both are
// cleaned up automatically at the end of the test.
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

func TestRegisterReturnsUUID(t *testing.T) {
	h := New()
	id := h.Register("lobby", nil)
	if id.String() == "" {
		t.Error("expected non-empty UUID")
	}
}

func TestRegisterTwiceCreatesTwoSessions(t *testing.T) {
	h := New()
	id1 := h.Register("lobby", nil)
	id2 := h.Register("lobby", nil)

	if id1 == id2 {
		t.Error("each register call should produce a distinct UUID")
	}
	if got := h.Connected(); len(got) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(got))
	}
}

func TestUnregister(t *testing.T) {
	h := New()
	id := h.Register("lobby", nil)
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
	h.Register("lobby", nil)
	h.Register("teller-1", nil)

	connected := h.Connected()
	if len(connected) != 2 {
		t.Fatalf("expected 2 kiosks, got %d", len(connected))
	}

	names := map[string]bool{}
	for _, k := range connected {
		names[k.Name] = true
	}
	if !names["lobby"] || !names["teller-1"] {
		t.Errorf("unexpected names in connected list: %v", connected)
	}
}

func TestConnectedIncludesID(t *testing.T) {
	h := New()
	id := h.Register("lobby", nil)

	connected := h.Connected()
	if len(connected) != 1 || connected[0].ID != id {
		t.Errorf("expected ID %v, got %v", id, connected)
	}
}

func TestSend(t *testing.T) {
	serverConn, clientConn := newConnPair(t)

	h := New()
	id := h.Register("lobby", serverConn)

	type Msg struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	want := Msg{Type: "sign", URL: "https://docusign.example.com/signing/abc123"}

	if err := h.Send(context.Background(), id, want); err != nil {
		t.Fatalf("send: %v", err)
	}

	_, data, err := clientConn.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var got Msg
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestSendToUnconnected(t *testing.T) {
	h := New()
	if err := h.Send(context.Background(), uuid.New(), "hello"); err == nil {
		t.Error("expected error sending to unconnected kiosk")
	}
}

func TestConcurrent(t *testing.T) {
	h := New()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("kiosk-%d", i)
			id := h.Register(name, nil)
			h.Connected()
			h.Unregister(id)
		}(i)
	}
	wg.Wait()
	if got := h.Connected(); len(got) != 0 {
		t.Errorf("expected 0 connected after cleanup, got %d", len(got))
	}
}
