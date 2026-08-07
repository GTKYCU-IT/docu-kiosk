package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/config"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/calvertjadon/docu-kiosk/internal/protocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// testConfig returns the baseline Config used by every server test: the
// broker is built exactly like production (NewServer) with dev credentials.
func testConfig() config.Config {
	return config.Config{
		Port:          0,
		TokenSecret:   []byte("0123456789abcdef0123456789abcdef"),
		AdminUsername: "admin",
		AdminPassword: "admin1234",
		LogLevel:      slog.LevelInfo,
	}
}

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
	db := newTestDB(t)
	s, err := NewServer(testConfig(), db)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	return s, ts
}

// login obtains a JWT and refresh token for the bootstrap admin user.
func login(t *testing.T, ts *httptest.Server, username, password string) (int, string) {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"username": %q, "password": %q}`, username, password))
	res, err := http.Post(ts.URL+"/login", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(data)
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

// stubHub is a kioskHub fake for handler tests.
type stubHub struct {
	sendErr error
	served  bool
}

func (sh *stubHub) Serve(w http.ResponseWriter, r *http.Request, kioskIP string) {
	sh.served = true
}

func (sh *stubHub) Send(ctx context.Context, id uuid.UUID, msg protocol.Message) error {
	return sh.sendErr
}

func (sh *stubHub) Connected() []uuid.UUID {
	return nil
}

func TestPushErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "not connected", err: hub.ErrNotConnected, want: http.StatusNotFound},
		{name: "write failed", err: hub.ErrWriteFailed, want: http.StatusInternalServerError},
		{name: "arbitrary error", err: errors.New("boom"), want: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{
				hub:    &stubHub{sendErr: tc.err},
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			req := httptest.NewRequest("POST", "/api/kiosks/"+uuid.NewString()+"/sessions", strings.NewReader(`{"url":"https://example.com"}`))
			req.SetPathValue("id", uuid.NewString())
			rec := httptest.NewRecorder()
			s.handlePush(rec, req)
			if rec.Code != tc.want {
				t.Errorf("expected %d, got %d", tc.want, rec.Code)
			}
		})
	}
}

func TestHandleWSDelegatesToServe(t *testing.T) {
	sh := &stubHub{}
	s := &server{
		hub:    sh,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	req := httptest.NewRequest("GET", "/ws", nil)
	rec := httptest.NewRecorder()
	s.handleWS(rec, req)
	if !sh.served {
		t.Error("expected handleWS to delegate to hub.Serve")
	}
}
