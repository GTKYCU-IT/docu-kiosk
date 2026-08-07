package server

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func realIPTestServer(trusted ...string) *server {
	s := &server{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, entry := range trusted {
		if p, err := netip.ParsePrefix(entry); err == nil {
			s.trustedProxies = append(s.trustedProxies, p)
			continue
		}
		if ip, err := netip.ParseAddr(entry); err == nil {
			s.trustedProxies = append(s.trustedProxies, netip.PrefixFrom(ip, ip.BitLen()))
		}
	}
	return s
}

func TestRealIPUsesRemoteAddrWhenNoXFF(t *testing.T) {
	s := realIPTestServer("127.0.0.1")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.50:54321"

	if got := s.realIP(req); got != "192.168.1.50" {
		t.Errorf("realIP = %q, want 192.168.1.50", got)
	}
}

func TestRealIPIgnoresXFFFromUntrustedPeer(t *testing.T) {
	s := realIPTestServer("10.0.0.1")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.50:54321"
	req.Header.Set("X-Forwarded-For", "10.9.9.9")

	if got := s.realIP(req); got != "192.168.1.50" {
		t.Errorf("realIP = %q, want 192.168.1.50 (XFF from untrusted peer must be ignored)", got)
	}
}

func TestRealIPIgnoresXFFWhenNoProxiesConfigured(t *testing.T) {
	s := realIPTestServer()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "172.16.0.9:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := s.realIP(req); got != "172.16.0.9" {
		t.Errorf("realIP = %q, want 172.16.0.9 (fail closed without TRUSTED_PROXIES)", got)
	}
}

func TestRealIPUsesRightmostXFFFromTrustedPeer(t *testing.T) {
	s := realIPTestServer("127.0.0.1")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	// Client-supplied XFF plus the proxy's appended entry: only the proxy's
	// addition (rightmost) may be trusted.
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.20.30.40")

	if got := s.realIP(req); got != "10.20.30.40" {
		t.Errorf("realIP = %q, want 10.20.30.40", got)
	}
}

func TestRealIPTrustsProxyByCIDR(t *testing.T) {
	s := realIPTestServer("10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:9999"
	req.Header.Set("X-Forwarded-For", "198.51.100.23")

	if got := s.realIP(req); got != "198.51.100.23" {
		t.Errorf("realIP = %q, want 198.51.100.23", got)
	}
}

func TestRealIPToleratesMissingPort(t *testing.T) {
	s := realIPTestServer("127.0.0.1")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.5"

	if got := s.realIP(req); got != "192.168.1.5" {
		t.Errorf("realIP = %q, want 192.168.1.5", got)
	}
}
