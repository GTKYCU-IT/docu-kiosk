// Package server
package server

import (
	"context"
	"docu-kiosk/broker/internal/api"
	"docu-kiosk/broker/internal/domain"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

type server struct {
	api        *api.API
	port       int
	httpServer *http.Server
}

func NewServer(store domain.ClientStore, port int) (server, error) {
	if store == nil {
		return server{}, errors.New("ClientStore is required")
	}

	api := api.NewAPI(store)

	return server{
		api:  api,
		port: port,
		httpServer: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: NewRouter(api),
		},
	}, nil
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
