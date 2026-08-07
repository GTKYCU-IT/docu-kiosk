package main

import (
	"database/sql"
	"log/slog"
	"os"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/server"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func main() {
	godotenv.Load()

	if err := os.MkdirAll("./data", 0o755); err != nil {
		slog.Error("create data dir", "error", err)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", "./data/kiosks.db?_texttotime=1")
	if err != nil {
		slog.Error("open db", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		slog.Error("goose dialect", "error", err)
		os.Exit(1)
	}
	if err := goose.Up(db, "./sql/migrations"); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}

	srv, err := server.NewServer(8080, database.New(db))
	if err != nil {
		slog.Error("create server", "error", err)
		os.Exit(1)
	}

	if err := srv.Start(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
