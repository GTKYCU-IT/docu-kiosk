package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *database.Queries {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, file, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(file), "..", "..", "sql", "migrations")

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, migrationsDir); err != nil {
		t.Fatal(err)
	}

	return database.New(db)
}

func setupTestServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	s := &server{
		db:     newTestDB(t),
		hub:    hub.New(),
		logger: newLogger(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/kiosks", s.handleRegister)
	mux.HandleFunc("GET /api/kiosks", s.handleListKiosks)
	mux.HandleFunc("POST /api/kiosks/{id}/sessions", s.handlePush)
	mux.HandleFunc("/ws", s.handleWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 1 second")
}

func wsURL(ts *httptest.Server) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
}

// registerKiosk POSTs to register a kiosk by name and expects 204.
func registerKiosk(t *testing.T, ts *httptest.Server, name string) {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/kiosks", "application/json",
		strings.NewReader(fmt.Sprintf(`{"name":%q}`, name)))
	if err != nil {
		t.Fatalf("register kiosk: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("register: expected 204, got %d", res.StatusCode)
	}
}

// connectWS dials the WebSocket endpoint and reads the initial "connected" message.
// The connecting IP must already be registered via registerKiosk.
func connectWS(t *testing.T, ts *httptest.Server) (*websocket.Conn, string) {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL(ts), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })

	_, data, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("read initial message: %v", err)
	}

	var msg struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal initial message: %v", err)
	}
	if msg.Type != "connected" {
		t.Fatalf("expected connected message, got type %q", msg.Type)
	}

	return conn, msg.Name
}

// --- Registration ---

func TestRegisterSuccess(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks", "application/json",
		strings.NewReader(`{"name":"lobby"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks", "application/json",
		strings.NewReader(`{"name":""}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", res.StatusCode)
	}
}

func TestRegisterBadJSON(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks", "application/json",
		strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", res.StatusCode)
	}
}

// --- List kiosks ---

func TestListKiosksEmpty(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var kiosks []map[string]string
	if err := json.NewDecoder(res.Body).Decode(&kiosks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(kiosks) != 0 {
		t.Errorf("expected empty list, got %v", kiosks)
	}
}

func TestListKiosksShowsConnected(t *testing.T) {
	_, ts := setupTestServer(t)
	registerKiosk(t, ts, "lobby")
	connectWS(t, ts)

	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	var kiosks []struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&kiosks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(kiosks) != 1 {
		t.Fatalf("expected 1 kiosk, got %d", len(kiosks))
	}
	if kiosks[0].Name != "lobby" {
		t.Errorf("expected name lobby, got %s", kiosks[0].Name)
	}
	if kiosks[0].ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}
}

// --- WebSocket ---

func TestWSUnregisteredIPRejected(t *testing.T) {
	_, ts := setupTestServer(t)

	_, _, err := websocket.Dial(context.Background(), wsURL(ts), nil)
	if err == nil {
		t.Error("expected dial to fail for unregistered IP")
	}
}

func TestWSConnectedMessage(t *testing.T) {
	_, ts := setupTestServer(t)
	registerKiosk(t, ts, "lobby")

	_, name := connectWS(t, ts)
	if name != "lobby" {
		t.Errorf("expected name %q, got %q", "lobby", name)
	}
}

func TestWSConnectsWhenRegistered(t *testing.T) {
	s, ts := setupTestServer(t)
	registerKiosk(t, ts, "lobby")
	connectWS(t, ts)

	if len(s.hub.Connected()) != 1 {
		t.Errorf("expected 1 connected kiosk, got %v", s.hub.Connected())
	}
}

func TestWSDisconnectRemovesFromHub(t *testing.T) {
	s, ts := setupTestServer(t)
	registerKiosk(t, ts, "lobby")

	conn, _ := connectWS(t, ts)
	conn.CloseNow()

	waitFor(t, func() bool { return len(s.hub.Connected()) == 0 })
}

func TestWSReconnect(t *testing.T) {
	s, ts := setupTestServer(t)
	registerKiosk(t, ts, "lobby")

	conn, _ := connectWS(t, ts)
	conn.CloseNow()
	waitFor(t, func() bool { return len(s.hub.Connected()) == 0 })

	connectWS(t, ts)
	waitFor(t, func() bool { return len(s.hub.Connected()) == 1 })
}

// --- Push ---

func TestPushSuccess(t *testing.T) {
	_, ts := setupTestServer(t)
	registerKiosk(t, ts, "lobby")
	conn, _ := connectWS(t, ts)

	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("list kiosks: %v", err)
	}
	var kiosks []struct {
		ID uuid.UUID `json:"id"`
	}
	json.NewDecoder(res.Body).Decode(&kiosks)
	res.Body.Close()

	res, err = http.Post(ts.URL+"/api/kiosks/"+kiosks[0].ID.String()+"/sessions", "application/json",
		strings.NewReader(`{"url":"https://example.docusign.net/sign/abc123"}`))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}

	_, data, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("read ws message: %v", err)
	}
	var msg struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "sign" {
		t.Errorf("expected type sign, got %q", msg.Type)
	}
	if msg.URL != "https://example.docusign.net/sign/abc123" {
		t.Errorf("unexpected url: %s", msg.URL)
	}
}

func TestPushKioskNotConnected(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks/"+uuid.New().String()+"/sessions",
		"application/json", strings.NewReader(`{"url":"https://example.docusign.net/sign/abc"}`))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", res.StatusCode)
	}
}

func TestPushInvalidID(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks/not-a-uuid/sessions",
		"application/json", strings.NewReader(`{"url":"https://example.docusign.net/sign/abc"}`))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", res.StatusCode)
	}
}

func TestPushMissingURL(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks/"+uuid.New().String()+"/sessions",
		"application/json", strings.NewReader(`{"url":""}`))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", res.StatusCode)
	}
}

func TestPushBadJSON(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks/"+uuid.New().String()+"/sessions",
		"application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", res.StatusCode)
	}
}

// --- End-to-end ---

func TestRegisterThenConnect(t *testing.T) {
	s, ts := setupTestServer(t)
	registerKiosk(t, ts, "lobby")

	_, name := connectWS(t, ts)
	if name != "lobby" {
		t.Errorf("expected name lobby from connected message, got %q", name)
	}

	if len(s.hub.Connected()) != 1 {
		t.Errorf("expected 1 kiosk in hub, got %v", s.hub.Connected())
	}
}

