// Package config loads the broker's configuration from the environment.
// Load is the only place in the codebase that reads environment variables;
// everything downstream receives the parsed Config struct. Both comma-separated
// lists (CORS_ORIGINS and TRUSTED_PROXIES) are parsed and validated here:
// CORSOrigins holds the normalized allowlist, and the server applies its
// default allowlist when it is empty.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds every broker setting. CORSOrigins is the normalized CORS
// allowlist (nil/empty means unconfigured — the server applies its default);
// TrustedProxies is parsed here so malformed entries fail startup.
type Config struct {
	Port           int    // default 8080
	TokenSecret    []byte // DOCU_KIOSK_TOKEN_SECRET
	AdminUsername  string // AUTH_USERNAME
	AdminPassword  string // AUTH_PASSWORD
	LogLevel       slog.Level
	CORSOrigins    []string       // normalized allowlist from CORS_ORIGINS; nil/empty means the server applies its default allowlist
	TrustedProxies []netip.Prefix // parsed from TRUSTED_PROXIES; nil when unset
}

// Load reads every broker environment variable exactly once, failing fast
// on invalid values so the broker never starts in a misconfigured state.
func Load() (Config, error) {
	var cfg Config

	// The JWT signing key is the single secret the broker needs. Requiring it
	// up front means a missing key fails fast instead of silently producing
	// forgeable tokens.
	jwtKey := os.Getenv("DOCU_KIOSK_TOKEN_SECRET")
	if len(jwtKey) < 32 {
		return Config{}, errors.New("DOCU_KIOSK_TOKEN_SECRET must be set to a random string of at least 32 characters")
	}
	cfg.TokenSecret = []byte(jwtKey)

	cfg.AdminUsername = os.Getenv("AUTH_USERNAME")
	cfg.AdminPassword = os.Getenv("AUTH_PASSWORD")

	if raw := os.Getenv("PORT"); raw == "" {
		cfg.Port = 8080
	} else {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid PORT %q: must be an integer", raw)
		}
		if port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("invalid PORT %d: must be between 1 and 65535", port)
		}
		cfg.Port = port
	}

	if raw := os.Getenv("LOG_LEVEL"); raw == "" {
		cfg.LogLevel = slog.LevelInfo
	} else {
		// Accept only the documented names (case-insensitive). UnmarshalText
		// would also accept numeric levels, which the .env.example contract
		// does not promise — LOG_LEVEL=4 must fail loudly.
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "debug":
			cfg.LogLevel = slog.LevelDebug
		case "info":
			cfg.LogLevel = slog.LevelInfo
		case "warn":
			cfg.LogLevel = slog.LevelWarn
		case "error":
			cfg.LogLevel = slog.LevelError
		default:
			return Config{}, fmt.Errorf("invalid LOG_LEVEL %q: must be one of debug, info, warn, error", raw)
		}
	}

	var err error
	cfg.CORSOrigins, err = parseCORSOrigins(os.Getenv("CORS_ORIGINS"))
	if err != nil {
		return Config{}, err
	}

	cfg.TrustedProxies, err = ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// ParseTrustedProxies parses the TRUSTED_PROXIES value into a list of
// prefixes. Entries may be CIDR ranges or bare IPs, which become full-width
// prefixes (e.g. 192.168.1.7 -> 192.168.1.7/32). Empty raw returns (nil, nil).
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, entry := range splitCommaList(raw) {
		if p, err := netip.ParsePrefix(entry); err == nil {
			prefixes = append(prefixes, p)
			continue
		}
		if ip, err := netip.ParseAddr(entry); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(ip, ip.BitLen()))
			continue
		}
		return nil, fmt.Errorf("invalid TRUSTED_PROXIES entry %q: must be an IP or CIDR", entry)
	}
	return prefixes, nil
}

// parseCORSOrigins normalizes the CORS_ORIGINS value into an allowlist of
// full origins. Bare hostnames get https:// prepended; "chrome-extension://"
// is kept verbatim for prefix matching. Every other entry must parse as a URL
// with a host. Empty raw returns (nil, nil).
func parseCORSOrigins(raw string) ([]string, error) {
	var origins []string
	for _, entry := range splitCommaList(raw) {
		if !strings.Contains(entry, "://") {
			// Bare hostname — assume https.
			entry = "https://" + entry
		} else if strings.HasSuffix(entry, "://") {
			if entry != "chrome-extension://" {
				return nil, fmt.Errorf("invalid CORS_ORIGINS entry %q: scheme-only origin not supported (only chrome-extension://)", entry)
			}
			origins = append(origins, entry)
			continue
		}
		if u, err := url.Parse(entry); err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid CORS_ORIGINS entry %q", entry)
		}
		origins = append(origins, entry)
	}
	return origins, nil
}

// splitCommaList splits a comma-separated env value into trimmed, non-empty
// entries. A blank or whitespace-only value yields nil.
func splitCommaList(raw string) []string {
	var entries []string
	for _, entry := range strings.Split(strings.TrimSpace(raw), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}
