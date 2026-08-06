package main

import (
	"database/sql"
	"log/slog"
	"os"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/server"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func main() {
	godotenv.Load()

	jwtKey := []byte(os.Getenv("DOCU_KIOSK_TOKEN_SECRET"))
	if len(jwtKey) == 0 {
		slog.Error("DOCU_KIOSK_TOKEN_SECRET is required")
		os.Exit(1)
	}

	adminUsername := os.Getenv("AUTH_USERNAME")
	adminPassword := os.Getenv("AUTH_PASSWORD")

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

	queries := database.New(db)
	authModule := auth.NewAuthModule(queries, jwtKey)

	srv, err := server.NewServer(8080, queries, authModule, adminUsername, adminPassword)
	if err != nil {
		slog.Error("create server", "error", err)
		os.Exit(1)
	}

	if err := srv.Start(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
