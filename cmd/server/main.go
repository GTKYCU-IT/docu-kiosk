package main

import (
	"database/sql"
	"log"
	"os"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/server"
	sentry "github.com/getsentry/sentry-go"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func main() {
	if err := sentry.Init(sentry.ClientOptions{
		Dsn: os.Getenv("SENTRY_DSN"),
	}); err != nil {
		log.Printf("sentry init: %s", err)
	}
	defer sentry.Flush(2 * time.Second)

	db, err := sql.Open("sqlite", "kiosks.db")
	if err != nil {
		log.Fatalf("open db: %s", err)
	}
	defer db.Close()

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		log.Fatalf("goose dialect: %s", err)
	}
	if err := goose.Up(db, "./sql/migrations"); err != nil {
		log.Fatalf("run migrations: %s", err)
	}

	srv, err := server.NewServer(8080, database.New(db))
	if err != nil {
		log.Fatalf("error creating server: %s", err)
	}

	if err := srv.Start(); err != nil {
		log.Fatalf("error stopping server: %s", err)
	}
}
