package config

import (
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// validSecret satisfies Load's DOCU_KIOSK_TOKEN_SECRET minimum (32 bytes);
// set it in every test that isn't about the secret so leaked env can't trip
// the secret check.
const validSecret = "0123456789abcdef0123456789abcdef"

// setEnv pins every broker env var to a neutral value, then applies
// overrides, so ambient developer env cannot leak into tests.
func setEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	for k, v := range map[string]string{
		"PORT": "", "DOCU_KIOSK_TOKEN_SECRET": validSecret,
		"AUTH_USERNAME": "", "AUTH_PASSWORD": "",
		"LOG_LEVEL": "", "CORS_ORIGINS": "", "TRUSTED_PROXIES": "",
		"DOCU_KIOSK_JWT_TTL": "", "DOCU_KIOSK_REFRESH_TTL": "",
	} {
		t.Setenv(k, v)
	}
	for k, v := range overrides {
		t.Setenv(k, v)
	}
}

// wantErrMentions fails the test unless err is non-nil and its message
// contains substr — every validation error must name the offending variable.
func wantErrMentions(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want error mentioning %q", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Errorf("error = %q, want it to mention %q", err, substr)
	}
}

func TestLoadDefaultPort(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

func TestLoadCustomPort(t *testing.T) {
	setEnv(t, map[string]string{"PORT": "9090"})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
}

func TestLoadInvalidPort(t *testing.T) {
	setEnv(t, map[string]string{"PORT": "abc"})

	_, err := Load()
	wantErrMentions(t, err, "PORT")
}

func TestLoadSecretTooShort(t *testing.T) {
	setEnv(t, map[string]string{"DOCU_KIOSK_TOKEN_SECRET": "short"})

	_, err := Load()
	wantErrMentions(t, err, "DOCU_KIOSK_TOKEN_SECRET")
}

func TestLoadSecretEmpty(t *testing.T) {
	setEnv(t, map[string]string{"DOCU_KIOSK_TOKEN_SECRET": ""})

	_, err := Load()
	wantErrMentions(t, err, "DOCU_KIOSK_TOKEN_SECRET")
}

func TestLoadSecretPasses(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(cfg.TokenSecret) != validSecret {
		t.Errorf("TokenSecret = %q, want %q", cfg.TokenSecret, validSecret)
	}
}

func TestLoadDefaultTokenTTLs(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JWTTTL != 15*time.Second {
		t.Errorf("JWTTTL = %v, want 15s", cfg.JWTTTL)
	}
	if cfg.RefreshTTL != 60*24*time.Hour {
		t.Errorf("RefreshTTL = %v, want 60 days", cfg.RefreshTTL)
	}
}

func TestLoadCustomTokenTTLs(t *testing.T) {
	setEnv(t, map[string]string{"DOCU_KIOSK_JWT_TTL": "1m", "DOCU_KIOSK_REFRESH_TTL": "24h"})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JWTTTL != time.Minute {
		t.Errorf("JWTTTL = %v, want 1m", cfg.JWTTTL)
	}
	if cfg.RefreshTTL != 24*time.Hour {
		t.Errorf("RefreshTTL = %v, want 24h", cfg.RefreshTTL)
	}
}

func TestLoadInvalidTokenTTLs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		varName string
		raw     string
	}{
		{"jwt not a duration", "DOCU_KIOSK_JWT_TTL", "banana"},
		{"jwt negative", "DOCU_KIOSK_JWT_TTL", "-15s"},
		{"jwt zero", "DOCU_KIOSK_JWT_TTL", "0s"},
		{"refresh not a duration", "DOCU_KIOSK_REFRESH_TTL", "banana"},
		{"refresh negative", "DOCU_KIOSK_REFRESH_TTL", "-24h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, map[string]string{tc.varName: tc.raw})

			_, err := Load()
			wantErrMentions(t, err, tc.varName)
		})
	}
}

func TestLoadDefaultLogLevel(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want Info", cfg.LogLevel)
	}
}

func TestLoadLogLevelParsing(t *testing.T) {
	setEnv(t, map[string]string{})

	cases := []struct {
		raw  string
		want slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"debug", slog.LevelDebug},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			setEnv(t, map[string]string{"LOG_LEVEL": tc.raw})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LogLevel != tc.want {
				t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, tc.want)
			}
		})
	}
}

func TestLoadInvalidLogLevel(t *testing.T) {
	setEnv(t, map[string]string{"LOG_LEVEL": "banana"})

	_, err := Load()
	wantErrMentions(t, err, "LOG_LEVEL")
}

// Numeric levels pass slog.Level.UnmarshalText but are not part of the
// documented LOG_LEVEL contract (DEBUG, INFO, WARN, ERROR) — they must fail.
func TestLoadNumericLogLevelRejected(t *testing.T) {
	for _, raw := range []string{"4", "-4"} {
		t.Run(raw, func(t *testing.T) {
			setEnv(t, map[string]string{"LOG_LEVEL": raw})

			_, err := Load()
			wantErrMentions(t, err, "LOG_LEVEL")
		})
	}
}

func TestLoadAllVars(t *testing.T) {
	setEnv(t, map[string]string{
		"PORT":                   "8443",
		"AUTH_USERNAME":          "admin",
		"AUTH_PASSWORD":          "admin1234",
		"LOG_LEVEL":              "warn",
		"CORS_ORIGINS":           "https://admin.example.com, chrome-extension://abc",
		"TRUSTED_PROXIES":        "10.0.0.0/8, 127.0.0.1",
		"DOCU_KIOSK_JWT_TTL":     "2m",
		"DOCU_KIOSK_REFRESH_TTL": "48h",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 8443 {
		t.Errorf("Port = %d, want 8443", cfg.Port)
	}
	if string(cfg.TokenSecret) != validSecret {
		t.Errorf("TokenSecret = %q, want %q", cfg.TokenSecret, validSecret)
	}
	if cfg.AdminUsername != "admin" {
		t.Errorf("AdminUsername = %q, want admin", cfg.AdminUsername)
	}
	if cfg.AdminPassword != "admin1234" {
		t.Errorf("AdminPassword = %q, want admin1234", cfg.AdminPassword)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v, want Warn", cfg.LogLevel)
	}
	wantOrigins := []string{"https://admin.example.com", "chrome-extension://abc"}
	if !slices.Equal(cfg.CORSOrigins, wantOrigins) {
		t.Errorf("CORSOrigins = %v, want %v", cfg.CORSOrigins, wantOrigins)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.PrefixFrom(netip.MustParseAddr("127.0.0.1"), 32),
	}
	if !slices.Equal(cfg.TrustedProxies, want) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
	if cfg.JWTTTL != 2*time.Minute {
		t.Errorf("JWTTTL = %v, want 2m", cfg.JWTTTL)
	}
	if cfg.RefreshTTL != 48*time.Hour {
		t.Errorf("RefreshTTL = %v, want 48h", cfg.RefreshTTL)
	}
}

func TestLoadCORSParsing(t *testing.T) {
	setEnv(t, map[string]string{"CORS_ORIGINS": "admin.example.com, https://ops.example.com, chrome-extension://abc"})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"https://admin.example.com", "https://ops.example.com", "chrome-extension://abc"}
	if !slices.Equal(cfg.CORSOrigins, want) {
		t.Errorf("CORSOrigins = %v, want %v", cfg.CORSOrigins, want)
	}
}

func TestLoadCORSSchemeOnlyInvalid(t *testing.T) {
	setEnv(t, map[string]string{"CORS_ORIGINS": "http://"})

	_, err := Load()
	wantErrMentions(t, err, "CORS_ORIGINS")
}

func TestLoadCORSGarbageInvalid(t *testing.T) {
	setEnv(t, map[string]string{"CORS_ORIGINS": "foo bar"})

	_, err := Load()
	wantErrMentions(t, err, "CORS_ORIGINS")
}

func TestLoadCORSEmpty(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.CORSOrigins) != 0 {
		t.Errorf("CORSOrigins = %v, want empty", cfg.CORSOrigins)
	}
}

func TestLoadPortOutOfRange(t *testing.T) {
	setEnv(t, map[string]string{})

	for _, port := range []string{"0", "-1", "65536"} {
		t.Run(port, func(t *testing.T) {
			setEnv(t, map[string]string{"PORT": port})

			_, err := Load()
			wantErrMentions(t, err, "PORT")
		})
	}
}

func TestLoadTrustedProxiesParsing(t *testing.T) {
	setEnv(t, map[string]string{"TRUSTED_PROXIES": "127.0.0.1, 10.0.0.0/8"})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []netip.Prefix{
		netip.PrefixFrom(netip.MustParseAddr("127.0.0.1"), 32),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	if !slices.Equal(cfg.TrustedProxies, want) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}

	// A trailing comma leaves an empty entry, which must be skipped.
	setEnv(t, map[string]string{"TRUSTED_PROXIES": "1.2.3.4, "})
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	wantSingle := []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr("1.2.3.4"), 32)}
	if !slices.Equal(cfg.TrustedProxies, wantSingle) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.TrustedProxies, wantSingle)
	}
}

func TestLoadInvalidTrustedProxies(t *testing.T) {
	setEnv(t, map[string]string{"TRUSTED_PROXIES": "banana"})

	_, err := Load()
	wantErrMentions(t, err, "TRUSTED_PROXIES")
}

func TestLoadTrustedProxiesEmpty(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want empty", cfg.TrustedProxies)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	prefixes, err := ParseTrustedProxies("10.0.0.0/8, 127.0.0.1")
	if err != nil {
		t.Fatalf("ParseTrustedProxies() error = %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.PrefixFrom(netip.MustParseAddr("127.0.0.1"), 32),
	}
	if !slices.Equal(prefixes, want) {
		t.Errorf("ParseTrustedProxies() = %v, want %v", prefixes, want)
	}

	prefixes, err = ParseTrustedProxies("")
	if err != nil {
		t.Fatalf("ParseTrustedProxies(\"\") error = %v", err)
	}
	if prefixes != nil {
		t.Errorf("ParseTrustedProxies(\"\") = %v, want nil", prefixes)
	}

	_, err = ParseTrustedProxies("banana")
	wantErrMentions(t, err, "TRUSTED_PROXIES")
}
