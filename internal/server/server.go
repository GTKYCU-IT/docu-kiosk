// Package server wires together the database, hub, routes, and HTTP lifecycle.
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
	"github.com/calvertjadon/docu-kiosk/internal/hub"
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

	kioskHandlers := kiosk.NewHandlers(db, hub.New(), logger)
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
			Handler: corsMiddleware(loggingMiddleware(logger, mux)),
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

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
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
