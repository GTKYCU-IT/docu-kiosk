// Package server wires together the database, hub, routes, and HTTP lifecycle.
package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/felixge/httpsnoop"
	"github.com/google/uuid"
)

type server struct {
	db         *database.Queries
	hub        *hub.Hub
	httpServer *http.Server
	logger     *slog.Logger
	port       int
	jwtKey     []byte
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if raw := strings.ToUpper(os.Getenv("LOG_LEVEL")); raw != "" {
		_ = level.UnmarshalText([]byte(raw))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func (s *server) ensureAdminUser() {
	if _, err := s.db.GetUserByUsername(context.Background(), "admin"); err != nil {
		hash, err := auth.HashPassword("admin")
		if err != nil {
			log.Fatal(err)
		}

		if _, err = s.db.CreateUser(context.Background(), database.CreateUserParams{
			ID:       uuid.New(),
			Username: "admin",
			Password: hash,
		}); err != nil {
			log.Fatal(err)
		}

		slog.Info("created admin user successfully")
	}
}

func NewServer(port int, db *database.Queries) (server, error) {
	s := server{
		db:     db,
		hub:    hub.New(),
		port:   port,
		logger: newLogger(),
	}

	s.ensureAdminUser()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /protected", s.ensureAuthMiddlware(s.handleProtected))
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /refresh", s.handleRefresh)

	mux.HandleFunc("POST /api/kiosks", s.handleRegister)
	mux.HandleFunc("GET /api/kiosks", s.handleListKiosks)
	mux.HandleFunc("POST /api/kiosks/{id}/sessions", s.handlePush)
	mux.HandleFunc("/ws", s.handleWS)

	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))

	cors := newCORSConfig()
	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: s.loggingMiddleware(cors.middleware(mux)),
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
			"ip", realIP(r),
		)
	})
}

type corsConfig struct {
	allowedOrigins []string // exact origins or scheme-only ("chrome-extension://") for prefix match
}

func newCORSConfig() *corsConfig {
	if raw := os.Getenv("CORS_ORIGINS"); raw != "" {
		origins := strings.Split(raw, ",")
		trimmed := make([]string, 0, len(origins))
		for _, o := range origins {
			if t := strings.TrimSpace(o); t != "" {
				trimmed = append(trimmed, t)
			}
		}
		return &corsConfig{allowedOrigins: trimmed}
	}

	// Sensible defaults when CORS_ORIGINS is not set.
	var origins []string
	// Any Chrome extension — the extension ID is random per build, so we
	// match by scheme prefix.  Operators who need tighter control should
	// set CORS_ORIGINS to the exact chrome-extension://<id> of their CRX.
	origins = append(origins, "chrome-extension://")
	// Same-origin SPA served by the broker — exact match.
	if host := os.Getenv("BROKER_HOST"); host != "" {
		origins = append(origins, "https://"+host)
	}
	return &corsConfig{allowedOrigins: origins}
}

func (c *corsConfig) isAllowed(origin string) bool {
	for _, allowed := range c.allowedOrigins {
		if strings.HasSuffix(allowed, "://") {
			// Scheme-only entries use prefix matching (e.g. "chrome-extension://").
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
			// No Origin header — not a cross-origin request; pass through.
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Vary", "Origin")
		if !c.isAllowed(origin) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) Start() error {
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
