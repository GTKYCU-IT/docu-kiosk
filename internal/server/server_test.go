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
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/config"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/calvertjadon/docu-kiosk/internal/kiosks"
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
		JWTTTL:        config.DefaultJWTTTL,
		RefreshTTL:    config.DefaultRefreshTTL,
	}
}

func newTestDB(t *testing.T) *database.Queries {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTestDB(t, db)
	return database.New(db)
}

// migrateTestDB applies the goose migrations and then the kiosk directory's
// application backfill, mirroring the broker's production startup order.
func migrateTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(file), "..", "..", "sql", "migrations")

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, migrationsDir); err != nil {
		t.Fatal(err)
	}
	if err := kiosks.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
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

// RFC 9457 problem type URNs for registration failures. The browser
// classifier keys on the type member, so these strings are a stable contract.
const (
	problemAlreadyRegistered = "urn:docu-kiosk:problem:kiosk-already-registered"
	problemNameConflict      = "urn:docu-kiosk:problem:kiosk-name-conflict"
	problemInvalidName       = "urn:docu-kiosk:problem:invalid-kiosk-name"
	problemMalformedRequest  = "urn:docu-kiosk:problem:malformed-request"
	problemInternalError     = "urn:docu-kiosk:problem:internal-error"
)

// kioskSummary mirrors the GET /api/kiosks wire shape.
type kioskSummary struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// postKiosks POSTs a raw body to /api/kiosks from the given client IP and
// returns the status and parsed problem envelope. A non-empty ip is sent as
// X-Forwarded-For and requires the server to trust the direct peer. Every
// failure response is verified to be an opaque application/problem+json
// document.
func postKiosks(t *testing.T, ts *httptest.Server, ip, body string) (int, problem) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/kiosks", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode == http.StatusNoContent {
		return res.StatusCode, problem{}
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	for _, leak := range []string{"sqlite", "constraint", "no rows"} {
		if strings.Contains(strings.ToLower(string(data)), leak) {
			t.Errorf("response leaks internal detail %q: %s", leak, data)
		}
	}
	var p problem
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("decode problem body %q: %v", data, err)
	}
	return res.StatusCode, p
}

// registerFromIP registers a kiosk by display name; see postKiosks for the
// IP semantics.
func registerFromIP(t *testing.T, ts *httptest.Server, ip, name string) (int, problem) {
	t.Helper()
	return postKiosks(t, ts, ip, fmt.Sprintf(`{"name":%q}`, name))
}

// assertProblem verifies a complete RFC 9457 problem response: the exact
// machine-readable type URN, the matching HTTP status, and a non-empty
// title.
func assertProblem(t *testing.T, got problem, wantType string, wantStatus int) {
	t.Helper()
	if got.Type != wantType {
		t.Errorf("problem type = %q, want %q", got.Type, wantType)
	}
	if got.Status != wantStatus {
		t.Errorf("problem status = %d, want %d", got.Status, wantStatus)
	}
	if strings.TrimSpace(got.Title) == "" {
		t.Errorf("problem title is empty for %s", wantType)
	}
	if got.Detail != "" && (strings.Contains(strings.ToLower(got.Detail), "sqlite") ||
		strings.Contains(strings.ToLower(got.Detail), "constraint")) {
		t.Errorf("problem detail leaks internals: %q", got.Detail)
	}
}

// listKiosks GETs the live kiosk list.
func listKiosks(t *testing.T, ts *httptest.Server) []kioskSummary {
	t.Helper()
	res, err := http.Get(ts.URL + "/api/kiosks")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	var kiosks []kioskSummary
	if err := json.NewDecoder(res.Body).Decode(&kiosks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return kiosks
}

// setupTrustedProxyServer builds a test server that trusts its direct peer
// (127.0.0.1), so X-Forwarded-For can impersonate additional client IPs.
func setupTrustedProxyServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	db := newTestDB(t)
	cfg := testConfig()
	cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	return s, ts
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

// TestRegisterInvalidName covers the name boundary failures: every rejected
// name must produce 422 with the invalid-kiosk-name problem.
func TestRegisterInvalidName(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: `{"name":""}`},
		{name: "whitespace only", body: `{"name":"   "}`},
		{name: "unicode whitespace only", body: `{"name":"\u00a0\u2003"}`},
		{name: "missing name field", body: `{}`},
		{name: "embedded control char", body: `{"name":"lob\u001fby"}`},
		{name: "embedded newline", body: `{"name":"lob\nby"}`},
		{name: "65 code points", body: fmt.Sprintf(`{"name":%q}`, strings.Repeat("a", 65))},
		{name: "65 multibyte code points", body: fmt.Sprintf(`{"name":%q}`, strings.Repeat("🧮", 65))},
		{name: "over 64 after trim", body: fmt.Sprintf(`{"name":%q}`, " "+strings.Repeat("a", 65)+" ")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := setupTestServer(t)
			status, p := postKiosks(t, ts, "", tc.body)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d", status)
			}
			assertProblem(t, p, problemInvalidName, http.StatusUnprocessableEntity)
		})
	}
}

// TestRegisterBadJSON verifies malformed request bodies produce the 400
// malformed-request problem, not an internal-detail dump.
func TestRegisterBadJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "not json", body: "not json"},
		{name: "empty body", body: ""},
		{name: "truncated json", body: `{"name":"lobby"`},
		{name: "wrong field type", body: `{"name":123}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := setupTestServer(t)
			status, p := postKiosks(t, ts, "", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", status)
			}
			assertProblem(t, p, problemMalformedRequest, http.StatusBadRequest)
		})
	}
}

// TestRegisterAlreadyRegisteredSameName covers lost-registration-response
// recovery: a kiosk that never saw its 204 retries the same name from the
// same IP, receives kiosk-already-registered, and recovers by opening a
// session whose greeting carries the authoritative name and identity.
func TestRegisterAlreadyRegisteredSameName(t *testing.T) {
	_, ts := setupTestServer(t)
	registerKiosk(t, ts, "Lobby")

	status, p := registerFromIP(t, ts, "", "Lobby")
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}
	assertProblem(t, p, problemAlreadyRegistered, http.StatusConflict)

	// The retry created no second identity and changed nothing: the greeting
	// still names the single original kiosk.
	_, name := connectWS(t, ts)
	if name != "Lobby" {
		t.Errorf("expected greeting name Lobby, got %s", name)
	}
	if kiosks := listKiosks(t, ts); len(kiosks) != 1 || kiosks[0].Name != "Lobby" {
		t.Errorf("expected exactly one kiosk named Lobby, got %+v", kiosks)
	}
}

// TestRegisterSameIPCannotRename verifies that a fixed IP is bound to its
// first identity: a different name from the same IP is rejected with
// kiosk-already-registered and the stored identity — id, display name, and
// greeting — is untouched.
func TestRegisterSameIPCannotRename(t *testing.T) {
	_, ts := setupTestServer(t)
	registerKiosk(t, ts, "A")

	_, name := connectWS(t, ts)
	if name != "A" {
		t.Errorf("expected greeting name A, got %s", name)
	}
	before := listKiosks(t, ts)
	if len(before) != 1 || before[0].Name != "A" {
		t.Fatalf("expected one kiosk named A, got %+v", before)
	}

	status, p := registerFromIP(t, ts, "", "B")
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}
	assertProblem(t, p, problemAlreadyRegistered, http.StatusConflict)

	after := listKiosks(t, ts)
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].Name != "A" {
		t.Errorf("identity changed after rejected rename: before %+v, after %+v", before, after)
	}
	_, name = connectWS(t, ts)
	if name != "A" {
		t.Errorf("expected greeting name A after rejected rename, got %s", name)
	}
}

// TestRegisterNameConflictDifferentIP verifies global uniqueness under full
// Unicode case folding: a folded-equivalent name from a different IP is
// rejected with kiosk-name-conflict, and the failed attempt registers
// nothing.
func TestRegisterNameConflictDifferentIP(t *testing.T) {
	tests := []struct {
		name        string
		first, fold string
	}{
		{name: "ascii case fold", first: "Lobby", fold: "lobby"},
		{name: "unicode case fold", first: "Straße", fold: "STRASSE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := setupTrustedProxyServer(t)

			if status, p := registerFromIP(t, ts, "", tc.first); status != http.StatusNoContent {
				t.Fatalf("register %q: expected 204, got %d (%+v)", tc.first, status, p)
			}

			status, p := registerFromIP(t, ts, "10.0.0.2", tc.fold)
			if status != http.StatusConflict {
				t.Fatalf("register %q from another IP: expected 409, got %d", tc.fold, status)
			}
			assertProblem(t, p, problemNameConflict, http.StatusConflict)

			// The foreign IP stayed unregistered: it can still claim a fresh
			// identity.
			if status, p := registerFromIP(t, ts, "10.0.0.2", "Fresh"); status != http.StatusNoContent {
				t.Fatalf("expected 204 for fresh identity, got %d (%+v)", status, p)
			}
		})
	}
}

// TestRegisterIPConflictTakesPrecedence: when a registered IP submits a name
// held by another identity, the IP conflict wins and neither row changes.
func TestRegisterIPConflictTakesPrecedence(t *testing.T) {
	_, ts := setupTrustedProxyServer(t)
	if status, p := registerFromIP(t, ts, "", "Lobby"); status != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%+v)", status, p)
	}
	if status, p := registerFromIP(t, ts, "10.0.0.2", "Front"); status != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%+v)", status, p)
	}

	status, p := registerFromIP(t, ts, "", "Front")
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}
	assertProblem(t, p, problemAlreadyRegistered, http.StatusConflict)

	// The first identity is untouched: greeting still Lobby.
	_, name := connectWS(t, ts)
	if name != "Lobby" {
		t.Errorf("expected greeting name Lobby, got %s", name)
	}
	// The second identity is untouched: its name is still held by another
	// identity, and its IP stays bound — any further registration from it is
	// rejected as already registered rather than creating a new row.
	if status, p := registerFromIP(t, ts, "10.0.0.3", "Front"); status != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%+v)", status, p)
	} else {
		assertProblem(t, p, problemNameConflict, http.StatusConflict)
	}
	if status, p := registerFromIP(t, ts, "10.0.0.2", "Back"); status != http.StatusConflict {
		t.Fatalf("expected 409 for a bound IP, got %d (%+v)", status, p)
	} else {
		assertProblem(t, p, problemAlreadyRegistered, http.StatusConflict)
	}
}

// TestRegisterNameNormalization covers the shared Unicode name boundary as
// observed over HTTP: surrounding Unicode whitespace is trimmed, the stored
// display form is NFC, and display casing is preserved.
func TestRegisterNameNormalization(t *testing.T) {
	t.Run("trims surrounding unicode whitespace", func(t *testing.T) {
		_, ts := setupTestServer(t)
		status, p := registerFromIP(t, ts, "", "\u00a0Lobby\u2003")
		if status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%+v)", status, p)
		}
		_, name := connectWS(t, ts)
		if name != "Lobby" {
			t.Errorf("stored display name = %q, want trimmed %q", name, "Lobby")
		}
	})

	t.Run("stores NFC display form", func(t *testing.T) {
		_, ts := setupTrustedProxyServer(t)
		// NFD input is stored as NFC.
		if status, p := registerFromIP(t, ts, "", "cafe\u0301"); status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%+v)", status, p)
		}
		_, name := connectWS(t, ts)
		if want := "caf\u00e9"; name != want {
			t.Errorf("stored display name = %q, want NFC %q", name, want)
		}
		// The NFC spelling from another IP collides on the folded key.
		status, p := registerFromIP(t, ts, "10.0.0.2", "caf\u00e9")
		if status != http.StatusConflict {
			t.Fatalf("expected 409, got %d", status)
		}
		assertProblem(t, p, problemNameConflict, http.StatusConflict)
	})

	t.Run("preserves display casing", func(t *testing.T) {
		_, ts := setupTestServer(t)
		if status, p := registerFromIP(t, ts, "", "LoBBy"); status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%+v)", status, p)
		}
		_, name := connectWS(t, ts)
		if name != "LoBBy" {
			t.Errorf("stored display name = %q, want submitted casing %q", name, "LoBBy")
		}
	})
}

// TestRegisterNameBoundaries pins the valid side of the 1–64 Unicode code
// point boundary; each case uses a fresh server so every registration is a
// genuinely new identity.
func TestRegisterNameBoundaries(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "single code point", body: `{"name":"a"}`},
		{name: "single multibyte code point", body: `{"name":"🧮"}`},
		{name: "64 code points", body: fmt.Sprintf(`{"name":%q}`, strings.Repeat("a", 64))},
		{name: "64 multibyte code points", body: fmt.Sprintf(`{"name":%q}`, strings.Repeat("🧮", 64))},
		{name: "64 after trim", body: fmt.Sprintf(`{"name":%q}`, " "+strings.Repeat("a", 64)+" ")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := setupTestServer(t)
			status, p := postKiosks(t, ts, "", tc.body)
			if status != http.StatusNoContent {
				t.Fatalf("expected 204, got %d (%+v)", status, p)
			}
		})
	}
}

// TestRegisterInternalErrorIsOpaque verifies the internal-error problem is
// still a complete, opaque RFC 9457 document when persistence fails.
func TestRegisterInternalErrorIsOpaque(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTestDB(t, db)

	s, err := NewServer(testConfig(), database.New(db))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)

	// Break persistence beneath the handler so Register hits a real error.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	status, p := postKiosks(t, ts, "", `{"name":"lobby"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", status)
	}
	assertProblem(t, p, problemInternalError, http.StatusInternalServerError)
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

func (sh *stubHub) PushSign(ctx context.Context, id uuid.UUID, url string) error {
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

func TestRespondWithErrorOpaque(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rr := httptest.NewRecorder()

	s.respondWithError(rr, "login failed", http.StatusInternalServerError,
		errors.New("sqlite: UNIQUE constraint failed: kiosks.name"))

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "login failed" {
		t.Errorf("expected error %q, got %q", "login failed", body.Error)
	}
	if strings.Contains(rr.Body.String(), "sqlite") {
		t.Error("response body must not contain raw error text")
	}
}
