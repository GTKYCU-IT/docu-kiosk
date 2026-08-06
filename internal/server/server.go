// Package server wires together the database, sessions, routes, and HTTP lifecycle.
package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/session"
	serverauth "github.com/calvertjadon/docu-kiosk/internal/server/auth"
	"github.com/calvertjadon/docu-kiosk/internal/server/kiosk"
	"github.com/felixge/httpsnoop"
	"github.com/google/uuid"
)

// Server holds the HTTP lifecycle, logger, and composed route groups.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
	port       int
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if raw := strings.ToUpper(os.Getenv("LOG_LEVEL")); raw != "" {
		_ = level.UnmarshalText([]byte(raw))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// realIP extracts the client IP from X-Forwarded-For or RemoteAddr.
func realIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if before, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(before)
		}
		return strings.TrimSpace(xff)
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func ensureAdminUser(db *database.Queries) {
	if _, err := db.GetUserByUsername(context.Background(), "admin"); err != nil {
		hash, err := auth.HashPassword("admin")
		if err != nil {
			log.Fatal(err)
		}

		if _, err = db.CreateUser(context.Background(), database.CreateUserParams{
			ID:       uuid.New(),
			Username: "admin",
			Password: hash,
		}); err != nil {
			log.Fatal(err)
		}

		slog.Info("created admin user successfully")
	}
}

// NewServer creates a Server with all route groups wired.
func NewServer(port int, db *database.Queries, authModule *auth.AuthModule) (*Server, error) {
	logger := newLogger()

	ensureAdminUser(db)

	kioskHandlers := kiosk.NewHandlers(db, session.NewManager(logger), logger)
	authHandlers := serverauth.NewHandlers(authModule)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", authHandlers.Login)
	mux.HandleFunc("POST /refresh", authHandlers.Refresh)

	// Example authenticated endpoint.
	mux.HandleFunc("GET /protected", authHandlers.AuthMiddleware(
		func(w http.ResponseWriter, r *http.Request, user database.User) {
			w.WriteHeader(http.StatusOK)
		},
	))

	mux.HandleFunc("POST /api/kiosks", kioskHandlers.Register)
	mux.HandleFunc("GET /api/kiosks", kioskHandlers.List)
	mux.HandleFunc("POST /api/kiosks/{id}/sessions", kioskHandlers.Push)
	mux.HandleFunc("/ws", kioskHandlers.WS)

	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))

	s := &Server{
		port:   port,
		logger: logger,
		httpServer: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: loggingMiddleware(logger, newCORSConfig(logger).middleware(mux)),
		},
	}

	return s, nil
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := httpsnoop.CaptureMetrics(next, w, r)
		logger.Info(
			"request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", m.Code,
			"duration_ms", m.Duration/time.Millisecond,
			"ip", realIP(r),
		)
	})
}

type corsConfig struct {
	allowedOrigins []string    // full origins matched exactly, except "chrome-extension://" which is a prefix
	logger         *slog.Logger
}

// newCORSConfig reads CORS_ORIGINS (comma-separated).  Entries without a
// scheme (bare hostnames) get https:// prepended automatically.  The only
// origin that matches by prefix is "chrome-extension://"; any other
// scheme-only entry is rejected with a warning.
func newCORSConfig(logger *slog.Logger) *corsConfig {
	if raw := strings.TrimSpace(os.Getenv("CORS_ORIGINS")); raw != "" {
		origins := strings.Split(raw, ",")
		trimmed := make([]string, 0, len(origins))
		for _, o := range origins {
			t := strings.TrimSpace(o)
			if t == "" {
				continue
			}
			if !strings.Contains(t, "://") {
				// Bare hostname — assume https.
				t = "https://" + t
			} else if strings.HasSuffix(t, "://") && t != "chrome-extension://" {
				logger.Warn("cors: ignoring scheme-only origin (only chrome-extension:// is supported for prefix matching)", "entry", o)
				continue
			}
			trimmed = append(trimmed, t)
		}
		if len(trimmed) > 0 {
			return &corsConfig{allowedOrigins: trimmed, logger: logger}
		}
		// All entries were blank or invalid — fall through to defaults.
	}

	// Defaults when CORS_ORIGINS is not set.
	var origins []string
	// Any Chrome extension — the extension ID is random per build, so we
	// match by scheme prefix.  Operators who need tighter control should
	// set CORS_ORIGINS to the exact chrome-extension://<id> of their CRX.
	origins = append(origins, "chrome-extension://")
	// Same-origin SPA served by the broker — exact match.
	if host := os.Getenv("BROKER_HOST"); host != "" {
		origins = append(origins, "https://"+host)
	}
	return &corsConfig{allowedOrigins: origins, logger: logger}
}

func (c *corsConfig) isAllowed(origin string) bool {
	for _, allowed := range c.allowedOrigins {
		if allowed == "chrome-extension://" {
			if strings.HasPrefix(origin, allowed) {
				return true
			}
		} else if origin == allowed {
			return true
		}
	}
	return false
}

func (c *corsConfig) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header — not a cross-origin request (same-origin
			// or non-browser client); pass through.
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Vary", "Origin")
		if !c.isAllowed(origin) {
			// Fail closed: no Access-Control-Allow-Origin, browser
			// blocks the response.  This also gates the /ws upgrade
			// path — browsers always send Origin on WebSocket
			// handshakes, so a kiosk SPA served from an unlisted
			// origin will be rejected here before the upgrade.
			if c.logger != nil {
				c.logger.Warn("cors: rejected origin", "origin", origin, "method", r.Method, "path", r.URL.Path)
			}
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Start begins listening and blocks until SIGINT/SIGTERM.
func (s *Server) Start() error {
	s.logger.Info("starting server", "port", s.port)
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

	if err := s.httpServer.Shutdown(context.Background()); err != nil {
		s.logger.Error("shutdown error", "error", err)
		return err
	}

	return nil
}
