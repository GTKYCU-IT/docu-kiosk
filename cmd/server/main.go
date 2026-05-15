package main

import (
	"database/sql"
	"log"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/server"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func main() {
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
