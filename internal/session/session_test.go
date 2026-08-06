package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// testStore is a KioskStore backed by an in-memory map.
type testStore struct {
	byIP map[string]database.Kiosk
}

func (s *testStore) GetKioskByIP(_ context.Context, ip string) (database.Kiosk, error) {
	k, ok := s.byIP[ip]
	if !ok {
		return database.Kiosk{}, fmt.Errorf("not found")
	}
	return k, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// acceptPair starts an httptest server, dials a client WebSocket, and calls
// Accept on the server side.  Returns the manager, the client connection,
// and the kiosk info returned by Accept.
func acceptPair(t *testing.T, store KioskStore, ip string) (*Manager, *websocket.Conn, KioskInfo) {
	t.Helper()
	m := NewManager(testLogger())

	type result struct {
		info KioskInfo
		err  error
	}
	done := make(chan result, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, err := m.Accept(w, r, ip, store)
		done <- result{info, err}
	}))
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.CloseNow() })

	// Read the "connected" message so Accept completes its write.
	_, _, err = client.Read(context.Background())
	if err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	return m, client, r.info
}

// --- Connected ---

func TestConnectedEmpty(t *testing.T) {
	m := NewManager(testLogger())
	if got := m.Connected(); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestConnectedAfterAccept(t *testing.T) {
	id := uuid.New()
	store := &testStore{byIP: map[string]database.Kiosk{
		"10.0.0.1": {ID: id, IP: "10.0.0.1", Name: "lobby"},
	}}
	m, _, info := acceptPair(t, store, "10.0.0.1")

	if info.ID != id || info.Name != "lobby" {
		t.Errorf("expected lobby/%s, got %s/%s", id, info.Name, info.ID)
	}

	connected := m.Connected()
	if len(connected) != 1 {
		t.Fatalf("expected 1 connected, got %d", len(connected))
	}
	if connected[0].ID != id || connected[0].Name != "lobby" {
		t.Errorf("expected lobby/%s, got %s/%s", id, connected[0].Name, connected[0].ID)
	}
}

// TODO: test Accept path where conn.Write for the "connected" message fails
// after successful websocket.Accept — Accept should unregister + return error.
// Hard to trigger without a conn mock; low-priority since post-handshake write
// failures are rare.

func TestAcceptRejectsUnregisteredIP(t *testing.T) {
	store := &testStore{byIP: map[string]database.Kiosk{}}
	m := NewManager(testLogger())

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := m.Accept(w, r, "10.0.0.99", store)
		if err == nil {
			t.Error("expected error for unregistered IP")
		}
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	_, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err == nil {
		t.Error("expected dial to fail for unregistered IP")
	}

	if got := m.Connected(); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// --- Unregister ---

func TestUnregister(t *testing.T) {
	id := uuid.New()
	store := &testStore{byIP: map[string]database.Kiosk{
		"10.0.0.1": {ID: id, IP: "10.0.0.1", Name: "lobby"},
	}}
	m, client, _ := acceptPair(t, store, "10.0.0.1")

	client.CloseNow()
	// The read loop auto-unregisters on disconnect.  Poll until it fires.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(m.Connected()) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("kiosk still connected after close")
}

func TestUnregisterNonexistentIsNoop(t *testing.T) {
	m := NewManager(testLogger())
	m.Unregister(uuid.New()) // must not panic
}

// --- Send ---

func TestSend(t *testing.T) {
	id := uuid.New()
	store := &testStore{byIP: map[string]database.Kiosk{
		"10.0.0.1": {ID: id, IP: "10.0.0.1", Name: "lobby"},
	}}
	m, client, _ := acceptPair(t, store, "10.0.0.1")

	type Msg struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	want := Msg{Type: "sign", URL: "https://docusign.example.com/signing/abc123"}

	if err := m.Send(context.Background(), id, want); err != nil {
		t.Fatalf("send: %v", err)
	}

	_, data, err := client.Read(context.Background())
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
	m := NewManager(testLogger())
	if err := m.Send(context.Background(), uuid.New(), "hello"); err == nil {
		t.Error("expected error sending to unconnected kiosk")
	}
}

// --- Concurrent ---

func TestConcurrent(t *testing.T) {
	m := NewManager(testLogger())

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := uuid.New()
			ip := fmt.Sprintf("10.0.0.%d", n+1)
			store := &testStore{byIP: map[string]database.Kiosk{
				ip: {ID: id, IP: ip, Name: fmt.Sprintf("kiosk-%d", n)},
			}}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				m.Accept(w, r, ip, store)
			}))
			wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
			client, _, err := websocket.Dial(context.Background(), wsURL, nil)
			if err != nil {
				t.Errorf("dial: %v", err)
				ts.Close()
				return
			}
			client.Read(context.Background())
			m.Connected()
			m.Unregister(id)
			client.CloseNow()
			ts.Close()
		}(i)
	}
	wg.Wait()
	if got := m.Connected(); len(got) != 0 {
		t.Errorf("expected 0 connected after cleanup, got %d", len(got))
	}
}
