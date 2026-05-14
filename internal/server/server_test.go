package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/coder/websocket"
)

const (
	testSecret = "test-token-secret"
	testKey    = "test-registration-key"
)

// setupTestServer builds a server and an httptest.Server wired to its handlers.
// The file server is excluded so tests run without web/dist.
func setupTestServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	s := &server{
		auth:            auth.New(testSecret),
		hub:             hub.New(),
		registrationKey: testKey,
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

func testToken(t *testing.T, name string) string {
	t.Helper()
	tok, err := auth.New(testSecret).GenerateToken(name)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return tok
}

// waitFor polls condition until it returns true or 1 second elapses.
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

// connectWS dials the WebSocket endpoint and reads the initial "connected"
// message. By the time it returns, hub registration is complete and the
// returned name is the one the server decoded from the token.
func connectWS(t *testing.T, ts *httptest.Server, token string) (*websocket.Conn, string) {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL(ts), &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{"kiosk-token=" + token}},
	})
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
		strings.NewReader(`{"name":"lobby","key":"test-registration-key"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}

	var kioskCookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "kiosk-token" {
			kioskCookie = c
			break
		}
	}
	if kioskCookie == nil {
		t.Fatal("expected kiosk-token cookie in response")
	}

	name, err := auth.New(testSecret).ValidateToken(kioskCookie.Value)
	if err != nil {
		t.Errorf("returned token failed validation: %v", err)
	}
	if name != "lobby" {
		t.Errorf("token encodes wrong name: got %q, want %q", name, "lobby")
	}
}

func TestRegisterWrongKey(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks", "application/json",
		strings.NewReader(`{"name":"lobby","key":"wrong"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", res.StatusCode)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks", "application/json",
		strings.NewReader(`{"name":"","key":"test-registration-key"}`))
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
	connectWS(t, ts, testToken(t, "teller-1"))

	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	var kiosks []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&kiosks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(kiosks) != 1 {
		t.Fatalf("expected 1 kiosk, got %d", len(kiosks))
	}
	if kiosks[0].Name != "teller-1" {
		t.Errorf("expected name teller-1, got %s", kiosks[0].Name)
	}
	if kiosks[0].ID == "" {
		t.Error("expected non-empty ID")
	}
}

// --- WebSocket ---

func TestWSInvalidToken(t *testing.T) {
	_, ts := setupTestServer(t)

	_, _, err := websocket.Dial(context.Background(), wsURL(ts), &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{"kiosk-token=badtoken"}},
	})
	if err == nil {
		t.Error("expected dial to fail with invalid token")
	}
}

func TestWSMissingToken(t *testing.T) {
	_, ts := setupTestServer(t)

	_, _, err := websocket.Dial(context.Background(), wsURL(ts), nil)
	if err == nil {
		t.Error("expected dial to fail with missing token")
	}
}

func TestWSConnectedMessage(t *testing.T) {
	_, ts := setupTestServer(t)

	_, name := connectWS(t, ts, testToken(t, "lobby"))
	if name != "lobby" {
		t.Errorf("expected name %q, got %q", "lobby", name)
	}
}

func TestWSValidTokenConnects(t *testing.T) {
	s, ts := setupTestServer(t)
	connectWS(t, ts, testToken(t, "lobby"))

	connected := s.hub.Connected()
	if len(connected) != 1 || connected[0].Name != "lobby" {
		t.Errorf("expected lobby in hub, got %v", connected)
	}
}

func TestWSDisconnectRemovesFromHub(t *testing.T) {
	s, ts := setupTestServer(t)

	conn, _ := connectWS(t, ts, testToken(t, "lobby"))
	conn.CloseNow()

	waitFor(t, func() bool {
		for _, k := range s.hub.Connected() {
			if k.Name == "lobby" {
				return false
			}
		}
		return true
	})
}

func TestWSReconnectCreatesFreshSession(t *testing.T) {
	s, ts := setupTestServer(t)
	token := testToken(t, "lobby")

	conn1, _ := connectWS(t, ts, token)
	id1 := s.hub.Connected()[0].ID

	conn1.CloseNow()
	waitFor(t, func() bool { return len(s.hub.Connected()) == 0 })

	connectWS(t, ts, token)
	connected := s.hub.Connected()
	if len(connected) != 1 {
		t.Fatalf("expected 1 session after reconnect, got %d", len(connected))
	}
	if connected[0].ID == id1 {
		t.Error("expected a new session ID after reconnect")
	}
}

// --- Push ---

func TestPushSuccess(t *testing.T) {
	_, ts := setupTestServer(t)
	conn, _ := connectWS(t, ts, testToken(t, "lobby"))

	id := ""
	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("list kiosks: %v", err)
	}
	var kiosks []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(res.Body).Decode(&kiosks)
	res.Body.Close()
	id = kiosks[0].ID

	res, err = http.Post(ts.URL+"/api/kiosks/"+id+"/sessions", "application/json",
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

func TestPushKioskNotFound(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/api/kiosks/00000000-0000-0000-0000-000000000000/sessions",
		"application/json", strings.NewReader(`{"url":"https://example.docusign.net/sign/abc"}`))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", res.StatusCode)
	}
}

func TestPushInvalidUUID(t *testing.T) {
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
	connectWS(t, ts, testToken(t, "lobby"))

	id := ""
	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("list kiosks: %v", err)
	}
	var kiosks []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(res.Body).Decode(&kiosks)
	res.Body.Close()
	id = kiosks[0].ID

	res, err = http.Post(ts.URL+"/api/kiosks/"+id+"/sessions",
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
	connectWS(t, ts, testToken(t, "lobby"))

	id := ""
	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("list kiosks: %v", err)
	}
	var kiosks []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(res.Body).Decode(&kiosks)
	res.Body.Close()
	id = kiosks[0].ID

	res, err = http.Post(ts.URL+"/api/kiosks/"+id+"/sessions",
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

	res, err := http.Post(ts.URL+"/api/kiosks", "application/json",
		strings.NewReader(`{"name":"lobby","key":"test-registration-key"}`))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}

	var kioskToken string
	for _, c := range res.Cookies() {
		if c.Name == "kiosk-token" {
			kioskToken = c.Value
			break
		}
	}
	if kioskToken == "" {
		t.Fatal("expected kiosk-token cookie")
	}

	_, name := connectWS(t, ts, kioskToken)
	if name != "lobby" {
		t.Errorf("expected name lobby from connected message, got %q", name)
	}

	connected := s.hub.Connected()
	if len(connected) != 1 || connected[0].Name != "lobby" {
		t.Errorf("expected lobby in hub, got %v", connected)
	}
}
