package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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

// --- CORS ---

func TestCORSAllowedOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		cfg    *corsConfig
	}{
		{
			name:   "chrome-extension scheme prefix",
			origin: "chrome-extension://abcdefghijklmnop",
			cfg:    &corsConfig{allowedOrigins: []string{"chrome-extension://"}},
		},
		{
			name:   "exact host match",
			origin: "https://broker.example.com",
			cfg:    &corsConfig{allowedOrigins: []string{"https://broker.example.com"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := tt.cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest("GET", "/api/kiosks", nil)
			req.Header.Set("Origin", tt.origin)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rr.Code)
			}
			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != tt.origin {
				t.Errorf("expected ACAO %q, got %q", tt.origin, got)
			}
			if got := rr.Header().Get("Vary"); got != "Origin" {
				t.Errorf("expected Vary: Origin, got %q", got)
			}
		})
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	cfg := &corsConfig{allowedOrigins: []string{"https://trusted.example.com"}}
	handler := cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for disallowed origin")
	}))

	req := httptest.NewRequest("GET", "/api/kiosks", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no ACAO header on rejection, got %q", got)
	}
	if got := rr.Header().Get("Vary"); got != "Origin" {
		t.Errorf("expected Vary: Origin on rejection, got %q", got)
	}
}

func TestCORSPrefixBypassRejected(t *testing.T) {
	// An origin that starts with a trusted origin must NOT be allowed.
	cfg := &corsConfig{allowedOrigins: []string{"https://broker.example.com"}}
	handler := cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for bypass origin")
	}))

	for _, origin := range []string{
		"https://broker.example.com.evil.com",
		"https://broker.example.com:8443",
	} {
		req := httptest.NewRequest("GET", "/api/kiosks", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("origin %q should be 403, got %d", origin, rr.Code)
		}
	}
}

func TestCORSSchemeEntryIgnored(t *testing.T) {
	// Scheme-only entries other than chrome-extension:// must not match.
	cfg := &corsConfig{allowedOrigins: []string{"chrome-extension://", "https://broker.example.com"}}
	if cfg.isAllowed("https://evil.example.com") {
		t.Error("random scheme-only entry should NOT match")
	}
	// chrome-extension:// still works.
	if !cfg.isAllowed("chrome-extension://anyid") {
		t.Error("chrome-extension:// should still match")
	}
	// Exact match still works.
	if !cfg.isAllowed("https://broker.example.com") {
		t.Error("exact host match should still work")
	}
}

func TestCORSNoOriginPassesThrough(t *testing.T) {
	cfg := &corsConfig{allowedOrigins: []string{"https://trusted.example.com"}}
	called := false
	handler := cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/kiosks", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should be called when Origin is missing")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestCORSOptionsPreflight(t *testing.T) {
	cfg := &corsConfig{allowedOrigins: []string{"chrome-extension://"}}
	handler := cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for OPTIONS")
	}))

	req := httptest.NewRequest("OPTIONS", "/api/kiosks", nil)
	req.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "chrome-extension://abcdefghijklmnop" {
		t.Errorf("expected ACAO chrome-extension://..., got %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Errorf("expected Allow-Headers Content-Type, got %q", got)
	}
}

func TestCORSOptionsPreflightDisallowed(t *testing.T) {
	cfg := &corsConfig{allowedOrigins: []string{"https://trusted.example.com"}}
	handler := cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for disallowed preflight")
	}))

	req := httptest.NewRequest("OPTIONS", "/api/kiosks", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for disallowed preflight, got %d", rr.Code)
	}
}

func TestCORSOptionsWithoutOrigin(t *testing.T) {
	// OPTIONS without an Origin header should pass through to the handler.
	cfg := &corsConfig{allowedOrigins: []string{"https://trusted.example.com"}}
	called := false
	handler := cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/api/kiosks", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("OPTIONS without Origin should pass through")
	}
}

func TestCORSDefaultConfig(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("BROKER_HOST", "")
	cfg := newCORSConfig(slog.Default())

	if !cfg.isAllowed("chrome-extension://anyid") {
		t.Error("default config should allow chrome-extension:// via scheme prefix")
	}
	if cfg.isAllowed("https://evil.example.com") {
		t.Error("default config should NOT allow arbitrary origins")
	}
}

func TestCORSDefaultConfigWithBrokerHost(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("BROKER_HOST", "broker.example.com")
	cfg := newCORSConfig(slog.Default())

	if !cfg.isAllowed("https://broker.example.com") {
		t.Error("default config with BROKER_HOST should allow https://broker.example.com")
	}
	if cfg.isAllowed("https://broker.example.com.evil.com") {
		t.Error("default config must NOT allow prefix-bypass origin")
	}
}

func TestCORSCustomOrigins(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://mybroker.example.com, https://admin.example.com")
	cfg := newCORSConfig(slog.Default())

	if !cfg.isAllowed("https://mybroker.example.com") {
		t.Error("custom config should allow listed origins")
	}
	if !cfg.isAllowed("https://admin.example.com") {
		t.Error("custom config should allow listed origins")
	}
	if cfg.isAllowed("https://evil.example.com") {
		t.Error("custom config should NOT allow unlisted origins")
	}
	if cfg.isAllowed("https://mybroker.example.com.evil.com") {
		t.Error("custom config should NOT allow prefix-bypass origin")
	}
}

func TestCORSBareHostname(t *testing.T) {
	// Bare hostnames get https:// prepended.
	t.Setenv("CORS_ORIGINS", "broker.example.com")
	cfg := newCORSConfig(slog.Default())

	if !cfg.isAllowed("https://broker.example.com") {
		t.Error("bare hostname should be treated as https://broker.example.com")
	}
	if cfg.isAllowed("http://broker.example.com") {
		t.Error("bare hostname should be https:// only, not http://")
	}
}

func TestCORSWhitespaceOnly(t *testing.T) {
	// Whitespace-only CORS_ORIGINS should fall back to defaults.
	t.Setenv("CORS_ORIGINS", " , ")
	t.Setenv("BROKER_HOST", "")
	cfg := newCORSConfig(slog.Default())

	if !cfg.isAllowed("chrome-extension://anyid") {
		t.Error("whitespace-only CORS_ORIGINS should fall back to default chrome-extension://")
	}
}

func TestCORSDuplicateEntries(t *testing.T) {
	// Duplicates should not break anything.
	t.Setenv("CORS_ORIGINS", "https://a.example.com, https://a.example.com, https://b.example.com")
	cfg := newCORSConfig(slog.Default())

	if !cfg.isAllowed("https://a.example.com") {
		t.Error("duplicate entry should still match")
	}
	if !cfg.isAllowed("https://b.example.com") {
		t.Error("non-duplicate entry should still match")
	}
	if cfg.isAllowed("https://evil.example.com") {
		t.Error("unlisted origin should be rejected")
	}
}

func TestCORSMixedPrefixExact(t *testing.T) {
	// Mixing chrome-extension:// prefix with exact origins works.
	cfg := &corsConfig{allowedOrigins: []string{"chrome-extension://", "https://admin.example.com"}}

	if !cfg.isAllowed("chrome-extension://abcdefghijklmnop") {
		t.Error("chrome-extension:// prefix should match any extension ID")
	}
	if !cfg.isAllowed("https://admin.example.com") {
		t.Error("exact host should match")
	}
	if cfg.isAllowed("https://evil.example.com") {
		t.Error("unlisted origin should be rejected")
	}
}
