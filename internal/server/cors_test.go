package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
