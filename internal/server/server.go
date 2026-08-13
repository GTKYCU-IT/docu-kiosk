// Package server wires together the database, hub, routes, and HTTP lifecycle.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/config"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/calvertjadon/docu-kiosk/internal/kiosks"
	"github.com/calvertjadon/docu-kiosk/internal/version"
	"github.com/felixge/httpsnoop"
	"github.com/google/uuid"
)

// kioskHub is the session-module surface the HTTP handlers consume.
type kioskHub interface {
	Serve(w http.ResponseWriter, r *http.Request, kioskIP string)
	PushSign(ctx context.Context, id uuid.UUID, url string) error
	Statuses() map[uuid.UUID]hub.Status
}

type server struct {
	db             *database.Queries
	kiosks         *kiosks.Module
	hub            kioskHub
	authModule     *auth.AuthModule
	httpServer     *http.Server
	logger         *slog.Logger
	port           int
	trustedProxies []netip.Prefix
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func (s *server) trustedProxy(peer string) bool {
	ip, err := netip.ParseAddr(peer)
	if err != nil {
		return false
	}
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// realIP returns the client IP for IP-based kiosk auth. When the direct peer
// is a trusted proxy, the rightmost X-Forwarded-For entry (the one the proxy
// appended) is used; otherwise X-Forwarded-For is ignored so clients cannot
// spoof their identity.
func (s *server) realIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if s.trustedProxy(peer) {
			entries := strings.Split(xff, ",")
			return strings.TrimSpace(entries[len(entries)-1])
		}
		s.logger.Debug("ignoring X-Forwarded-For from untrusted peer", "peer", peer)
	}

	return peer
}

// NewServer builds the broker: routes, middleware chain, and the admin-user
// bootstrap. The AuthModule owns the first-boot policy (creating the admin
// user only when the users table is empty), so existing credentials are never
// silently reset.
func NewServer(cfg config.Config, db *database.Queries) (*server, error) {
	logger := newLogger(cfg.LogLevel)
	if len(cfg.TrustedProxies) == 0 {
		logger.Info("TRUSTED_PROXIES not set — X-Forwarded-For will be ignored, kiosk IPs resolve to the direct peer")
	}

	authModule, err := auth.NewAuthModule(db, cfg.TokenSecret, auth.TokenLifetimes{JWTTTL: cfg.JWTTTL, RefreshTTL: cfg.RefreshTTL})
	if err != nil {
		return nil, fmt.Errorf("init auth: %w", err)
	}

	kioskModule := kiosks.New(db, logger)
	corsCfg := newCORSConfig(cfg.CORSOrigins, logger)
	s := &server{
		db:     db,
		kiosks: kioskModule,
		// The CORS policy is enforced inside the hub as well as by the
		// middleware, via the same allowsRequest predicate, so the origin
		// gate holds even if the hub is ever used without the middleware.
		hub:            hub.New(kioskModule, logger, hub.WithOriginPolicy(corsCfg.allowsRequest)),
		authModule:     authModule,
		port:           cfg.Port,
		logger:         logger,
		trustedProxies: cfg.TrustedProxies,
	}

	if err := authModule.EnsureAdminUser(cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return nil, fmt.Errorf("ensure admin user: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /protected", s.ensureAuthMiddleware(s.handleProtected))
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /refresh", s.handleRefresh)

	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("POST /api/kiosks", s.handleRegister)
	mux.HandleFunc("GET /api/kiosks", s.handleListKiosks)
	mux.HandleFunc("POST /api/kiosks/{id}/sessions", s.handlePush)
	mux.HandleFunc("/ws", s.handleWS)

	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: corsCfg.middleware(s.loggingMiddleware(mux)),
	}

	return s, nil
}

func (s *server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := httpsnoop.CaptureMetrics(next, w, r)
		s.logger.Info(
			"request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", m.Code,
			"duration_ms", m.Duration/time.Millisecond,
			"ip", s.realIP(r),
		)
	})
}

type corsConfig struct {
	allowedOrigins []string // full origins matched exactly; scheme-only entries (the config default) are prefix matches
	logger         *slog.Logger
}

// newCORSConfig builds the CORS allowlist from the origins parsed by
// config.Load, which owns the policy and applies its default when CORS_ORIGINS
// is unset. The server never substitutes a default of its own.
func newCORSConfig(origins []string, logger *slog.Logger) *corsConfig {
	return &corsConfig{allowedOrigins: origins, logger: logger}
}

// isSameOrigin reports whether the Origin header belongs to the same origin
// as the request itself. The kiosk SPA is served by the broker, so its
// same-origin WebSocket handshake and API calls must pass even though the
// exact http://host:port origin cannot be enumerated in the allowlist. The
// scheme is ignored on purpose: behind a TLS-terminating proxy the browser's
// Origin is https while the server sees plain http.
func (c *corsConfig) isSameOrigin(origin string, r *http.Request) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host)
}

func (c *corsConfig) isAllowed(origin string, r *http.Request) bool {
	if c.isSameOrigin(origin, r) {
		return true
	}
	for _, allowed := range c.allowedOrigins {
		if strings.HasSuffix(allowed, "://") {
			// Scheme-only entry (the config default): any origin with that
			// scheme is allowed.
			if strings.HasPrefix(origin, allowed) {
				return true
			}
		} else if origin == allowed {
			return true
		}
	}
	return false
}

// allowsRequest is the single origin-admission predicate shared by the CORS
// middleware and the hub's injected origin policy. A request passes when it
// has no Origin header (same-origin navigation or a non-browser client —
// there is nothing for CORS to gate), or when the Origin is same-origin or
// listed in the allowlist.
func (c *corsConfig) allowsRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || c.isAllowed(origin, r)
}

func (c *corsConfig) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Vary", "Origin")

		if !c.allowsRequest(r) {
			// Fail closed: no Access-Control-Allow-Origin, so the browser
			// blocks the response. Browsers always send Origin on WebSocket
			// handshakes, so an unlisted origin is rejected here before the
			// upgrade.
			if c.logger != nil {
				c.logger.Warn("cors: rejected origin", "origin", origin, "method", r.Method, "path", r.URL.Path)
			}
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}

		if !c.isSameOrigin(origin, r) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) Start() error {
	s.logger.Info("starting server", "port", s.port, "version", version.Version, "commit", version.Commit)
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-stopChan
	s.logger.Info("shutting down server")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.logger.Error("shutdown error", "error", err)
		return err
	}

	return nil
}
