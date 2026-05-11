package main

import (
	"docu-kiosk/broker/internal/server"
	"docu-kiosk/broker/internal/store"
	"log"
)

func main() {
	store, err := store.NewInMemoryClientStore()
	if err != nil {
		log.Fatalf("error creating client store: %s", err)
	}

	server, err := server.NewServer(store, 8080)
	if err != nil {
		log.Fatalf("error creating server: %s", err)
	}

	if err := server.Start(); err != nil {
		log.Fatalf("error stopping server: %s", err)
	}
}
