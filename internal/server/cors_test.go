package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/coder/websocket"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// corsTestServer builds the real handler chain (CORS -> logging -> mux) with
// CORS_ORIGINS pinned to env so tests are independent of the developer's env.
func corsTestServer(t *testing.T, env string) *httptest.Server {
	t.Helper()
	t.Setenv("CORS_ORIGINS", env)
	db := newTestDB(t)
	authModule, err := auth.NewAuthModule(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(0, db, authModule, "admin", "admin1234")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestNewCORSConfigParsing(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "admin.example.com, https://ops.example.com, chrome-extension://abc, http://")
	cfg := newCORSConfig(discardLogger())
	want := []string{"https://admin.example.com", "https://ops.example.com", "chrome-extension://abc"}
	if len(cfg.allowedOrigins) != len(want) {
		t.Fatalf("allowedOrigins = %v, want %v", cfg.allowedOrigins, want)
	}
	for i := range want {
		if cfg.allowedOrigins[i] != want[i] {
			t.Fatalf("allowedOrigins = %v, want %v", cfg.allowedOrigins, want)
		}
	}

	t.Setenv("CORS_ORIGINS", "")
	cfg = newCORSConfig(discardLogger())
	if len(cfg.allowedOrigins) != 1 || cfg.allowedOrigins[0] != "chrome-extension://" {
		t.Fatalf("default allowedOrigins = %v, want [chrome-extension://]", cfg.allowedOrigins)
	}
}

func TestCORSMiddleware(t *testing.T) {
	cases := []struct {
		name       string
		env        string
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
			name:       "chrome extension origin allowed by default",
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
			env:        "https://admin.example.com",
			origin:     "https://admin.example.com",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantACAO:   "https://admin.example.com",
		},
		{
			name:       "allowlist does not admit other cross-origins",
			env:        "https://admin.example.com",
			origin:     "https://evil.example.com",
			host:       "kiosk.local:8080",
			method:     http.MethodGet,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "allowed preflight returns 204 with headers",
			env:        "https://admin.example.com",
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
			t.Setenv("CORS_ORIGINS", tc.env)
			ts := httptest.NewServer(newCORSConfig(discardLogger()).middleware(
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
	ts := corsTestServer(t, "")
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
	ts := corsTestServer(t, "")
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
	ts := corsTestServer(t, "")
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
