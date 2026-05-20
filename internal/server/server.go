// Package server wires together the database, hub, routes, and HTTP lifecycle.
package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
	sentryhttp "github.com/getsentry/sentry-go/http"
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
	port       int
}

func NewServer(port int, db *database.Queries) (server, error) {
	s := server{
		db:   db,
		hub:  hub.New(),
		port: port,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/kiosks", s.handleRegister)
	mux.HandleFunc("GET /api/kiosks", s.handleListKiosks)
	mux.HandleFunc("POST /api/kiosks/{id}/sessions", s.handlePush)
	mux.HandleFunc("/ws", s.handleWS)
	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))

	sentryHandler := sentryhttp.New(sentryhttp.Options{Repanic: true})
	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: sentryHandler.Handle(corsMiddleware(mux)),
	}

	return s, nil
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
	log.Printf("starting server on port %d", s.port)
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("error when running server: %s", err)
		}
	}()

	<-stopChan

	if err := s.httpServer.Shutdown(context.Background()); err != nil {
		log.Fatalf("error when shutting down server: %s", err)
		return err
	}

	return nil
}

