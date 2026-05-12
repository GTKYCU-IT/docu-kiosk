package main

import (
	"log"
	"os"

	"github.com/calvertjadon/docu-kiosk/internal/server"
)

func main() {
	tokenSecret := mustEnv("DOCU_KIOSK_TOKEN_SECRET")
	registrationKey := mustEnv("DOCU_KIOSK_REGISTRATION_KEY")
	certFile := envOr("DOCU_KIOSK_TLS_CERT", "server.crt")
	keyFile := envOr("DOCU_KIOSK_TLS_KEY", "server.key")

	srv, err := server.NewServer(8080, tokenSecret, registrationKey, certFile, keyFile)
	if err != nil {
		log.Fatalf("error creating server: %s", err)
	}

	if err := srv.Start(); err != nil {
		log.Fatalf("error stopping server: %s", err)
	}
}

func mustEnv(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
