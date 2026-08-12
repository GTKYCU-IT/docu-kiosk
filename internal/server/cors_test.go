package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/config"
	"github.com/coder/websocket"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// corsTestServer builds the real handler chain (CORS -> logging -> mux) with
// CORS origins pinned via cfg so tests are independent of the developer's env.
func corsTestServer(t *testing.T, origins ...string) *httptest.Server {
	t.Helper()
	cfg := testConfig()
	cfg.CORSOrigins = origins
	s, err := NewServer(cfg, newTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	return ts
}

// TestNewCORSConfigPassthrough pins that newCORSConfig applies no default of
// its own: config.Load owns the policy and supplies the effective allowlist,
// so the server passes it through verbatim.
func TestNewCORSConfigPassthrough(t *testing.T) {
	cfg := newCORSConfig(nil, discardLogger())
	if len(cfg.allowedOrigins) != 0 {
		t.Fatalf("allowedOrigins = %v, want empty (the default lives in config.Load)", cfg.allowedOrigins)
	}

	origins := []string{"https://admin.example.com"}
	cfg = newCORSConfig(origins, discardLogger())
	if !slices.Equal(cfg.allowedOrigins, origins) {
		t.Fatalf("allowedOrigins = %v, want %v", cfg.allowedOrigins, origins)
	}
}

func TestCORSMiddleware(t *testing.T) {
	cases := []struct {
		name       string
		origins    []string
		origin     string
		host       string
		method     string
		wantStatus int
		wantACAO   string // empty means no Access-Control-Allow-Origin header
	}{
		{
			name:       "same-origin kiosk origin passes",
			origin:     "http://kiosk.local:8080",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
		},
		{
			name:       "same-origin https through TLS-terminating proxy",
			origin:     "https://kiosk.local:8080",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
		},
		{
			name:       "same-origin without explicit port",
			origin:     "http://kiosk.local",
			host:       "kiosk.local",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
		},
		{
			name:       "no origin header passes through",
			origin:     "",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
		},
		{
			// origins mirrors the default config.Load supplies; the
			// middleware itself no longer applies a default.
			name:       "chrome extension origin allowed by default allowlist",
			origins:    slices.Clone(config.DefaultCORSOrigins),
			origin:     "chrome-extension://ndmpfjhihnpgakamhhdcpjemakdgmkcp",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantACAO:   "chrome-extension://ndmpfjhihnpgakamhhdcpjemakdgmkcp",
		},
		{
			name:       "unknown cross-origin rejected",
			origin:     "https://evil.example.com",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "null origin rejected",
			origin:     "null",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "allowlisted cross-origin passes",
			origins:    []string{"https://admin.example.com"},
			origin:     "https://admin.example.com",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantACAO:   "https://admin.example.com",
		},
		{
			name:       "allowlist does not admit other cross-origins",
			origins:    []string{"https://admin.example.com"},
			origin:     "https://evil.example.com",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "allowed preflight returns 204 with headers",
			origins:    []string{"https://admin.example.com"},
			origin:     "https://admin.example.com",
			host:       "kiosk.local:8080",
			method:     http.MethodOptions,
			wantStatus: http.StatusNoContent,
			wantACAO:   "https://admin.example.com",
		},
		{
			name:       "rejected preflight returns 403",
			origin:     "https://evil.example.com",
			host:       "kiosk.local:8080",
			method:     http.MethodOptions,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "same-origin preflight returns 204 without CORS headers",
			origin:     "http://kiosk.local:8080",
			host:       "kiosk.local:8080",
			method:     http.MethodOptions,
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(newCORSConfig(tc.origins, discardLogger()).middleware(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			))
			defer ts.Close()
			req, err := http.NewRequest(tc.method, ts.URL+"/anything", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()

			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			acao := res.Header.Get("Access-Control-Allow-Origin")
			if acao != tc.wantACAO {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", acao, tc.wantACAO)
			}
			if tc.origin != "" && res.Header.Get("Vary") != "Origin" {
				t.Fatalf("Vary = %q, want %q", res.Header.Get("Vary"), "Origin")
			}
		})
	}
}

// TestWSBrowserSameOriginConnects is the regression test for the prod
// incident: a kiosk SPA served by the broker opens the WebSocket with its
// own origin as the Origin header, which the fail-closed CORS rewrite must
// not reject.
func TestWSBrowserSameOriginConnects(t *testing.T) {
	ts := corsTestServer(t)
	registerKiosk(t, ts, "kiosk-a")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL(ts), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{ts.URL}},
	})
	if err != nil {
		t.Fatalf("same-origin WebSocket handshake rejected: %v", err)
	}
	defer conn.CloseNow()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"connected"`; !strings.Contains(string(data), want) {
		t.Fatalf("first message = %s, want it to contain %s", data, want)
	}
}

// TestWSBrowserCrossOriginRejected proves cross-origin WebSocket handshakes
// fail closed (403) before the upgrade.
func TestWSBrowserCrossOriginRejected(t *testing.T) {
	ts := corsTestServer(t)
	registerKiosk(t, ts, "kiosk-a")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, wsURL(ts), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example.com"}},
	})
	if err == nil {
		t.Fatal("cross-origin WebSocket handshake succeeded, want rejection")
	}
}

// TestKioskRegistrationSameOrigin proves the same-origin POST registration
// flow (the staging send-to-kiosk path) passes the CORS gate.
func TestKioskRegistrationSameOrigin(t *testing.T) {
	ts := corsTestServer(t)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/kiosks", strings.NewReader(`{"name":"kiosk-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = strings.TrimPrefix(ts.URL, "http://")
	req.Header.Set("Origin", ts.URL)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("same-origin registration = %d, want %d", res.StatusCode, http.StatusNoContent)
	}
}

// TestWSHubGateRejectsCrossOrigin proves the origin policy is enforced inside
// the hub itself, not only by the CORS middleware: Serve is called directly
// (middleware bypassed) with a hostile Origin and the handshake is rejected
// before the socket is accepted.
func TestWSHubGateRejectsCrossOrigin(t *testing.T) {
	s, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	s.hub.Serve(rec, req, "10.0.0.99")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("hub gate status = %d, want 403", rec.Code)
	}
	if rec.Body.String() != "origin rejected\n" {
		t.Fatalf("hub gate body = %q, want %q", rec.Body.String(), "origin rejected\n")
	}
}

// TestWSHubGateAllowsSameOrigin proves the hub gate admits a same-origin
// request, which then proceeds to the normal Serve flow (auth rejects the
// unknown IP here — the gate itself passed).
func TestWSHubGateAllowsSameOrigin(t *testing.T) {
	s, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	s.hub.Serve(rec, req, "10.0.0.99")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("hub gate status = %d, want 401 from the auth step", rec.Code)
	}
}
