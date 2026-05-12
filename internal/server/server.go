// Package server wires together auth, hub, routes, and HTTP lifecycle.
package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/hub"
)

type server struct {
	auth            *auth.Auth
	hub             *hub.Hub
	registrationKey string
	httpServer      *http.Server
	port            int
	certFile        string
	keyFile         string
}

func NewServer(port int, tokenSecret, registrationKey, certFile, keyFile string) (server, error) {
	s := server{
		auth:            auth.New(tokenSecret),
		hub:             hub.New(),
		registrationKey: registrationKey,
		port:            port,
		certFile:        certFile,
		keyFile:         keyFile,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/kiosks", s.handleRegister)
	mux.HandleFunc("GET /api/kiosks", s.handleListKiosks)
	mux.HandleFunc("/ws", s.handleWS)
	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return s, nil
}

func (s *server) Start() error {
	log.Printf("starting server on port %d (TLS)", s.port)
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		err := s.httpServer.ListenAndServeTLS(s.certFile, s.keyFile)
		if err != nil && err != http.ErrServerClosed {
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
