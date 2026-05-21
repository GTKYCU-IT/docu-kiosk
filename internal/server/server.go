// Package server wires together the database, hub, routes, and HTTP lifecycle.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	"github.com/felixge/httpsnoop"
	"github.com/google/uuid"
)

// kioskDB is the subset of *database.Queries the server needs.
type kioskDB interface {
	CreateKiosk(context.Context, database.CreateKioskParams) (database.Kiosk, error)
	GetKioskByIP(context.Context, string) (database.Kiosk, error)
	GetKioskByID(context.Context, uuid.UUID) (database.Kiosk, error)
}

type server struct {
	db         kioskDB
	hub        *hub.Hub
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

func NewServer(port int, db *database.Queries) (server, error) {
	s := server{
		db:     db,
		hub:    hub.New(),
		port:   port,
		logger: newLogger(),
	}

	mux := http.NewServeMux()
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
		s.logger.Info("request",
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

