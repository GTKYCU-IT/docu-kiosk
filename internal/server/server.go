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
	authModule *auth.AuthModule
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

func (s *server) ensureAdminUser(username, password string) {
	if username == "" || password == "" {
		slog.Error("AUTH_USERNAME and AUTH_PASSWORD are required when the users table is empty")
		os.Exit(1)
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := s.db.CreateUser(context.Background(), database.CreateUserParams{
		ID:       uuid.New(),
		Username: username,
		Password: hash,
	}); err != nil {
		log.Fatal(err)
	}

	slog.Info("created admin user successfully", "username", username)
}

func NewServer(port int, db *database.Queries, authModule *auth.AuthModule, adminUsername, adminPassword string) (server, error) {
	s := server{
		db:         db,
		hub:        hub.New(),
		authModule: authModule,
		port:       port,
		logger:     newLogger(),
	}
	count, err := db.CountUsers(context.Background())
	if err != nil {
		return server{}, fmt.Errorf("count users: %w", err)
	}
	if count == 0 {
		slog.Info("users table is empty, ensuring admin user exists")
		s.ensureAdminUser(adminUsername, adminPassword)
	} else {
		slog.Info("users table has existing users, skipping admin creation")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /protected", s.ensureAuthMiddlware(s.handleProtected))
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /refresh", s.handleRefresh)

	mux.HandleFunc("POST /api/kiosks", s.handleRegister)
	mux.HandleFunc("GET /api/kiosks", s.handleListKiosks)
	mux.HandleFunc("POST /api/kiosks/{id}/sessions", s.handlePush)
	mux.HandleFunc("/ws", s.handleWS)

	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: corsMiddleware(s.loggingMiddleware(mux)),
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
