// Command broker runs a hermetic Broker for the Playwright administrator
// session acceptance suite (e2e/admin-session.spec.ts). It bootstraps exactly
// like production — goose migrations, the kiosk-name backfill, and the
// first-boot admin user — against a throwaway SQLite database in a temporary
// directory, and serves the built SPA from web/dist on a fixed loopback port.
// The suite therefore exercises the real Broker endpoints and never touches
// repository or production data.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/calvertjadon/docu-kiosk/internal/config"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/calvertjadon/docu-kiosk/internal/kiosks"
	"github.com/calvertjadon/docu-kiosk/internal/server"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// testPort is the fixed loopback port the broker listens on. The Playwright
// config and the spec file hardcode the same port so the helper and the
// suite can never drift apart silently.
const testPort = 4187

// testAdminUsername and testAdminPassword are the credentials the broker
// forces into its throwaway database; the spec signs in with the same pair.
const (
	testAdminUsername = "admin"
	testAdminPassword = "admin1234"
)

func main() {
	if err := run(); err != nil {
		slog.Error("e2e broker failed", "error", err)
		os.Exit(1)
	}
}

// run locates the repository root from this source file, serves from it, and
// blocks until the broker is signalled to stop. Production paths (the SPA
// static root and sql/migrations) resolve from the repository root, so the
// helper works no matter where it is launched from.
func run() error {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("locate broker source")
	}
	repoRoot := filepath.Join(filepath.Dir(sourceFile), "..", "..")
	if err := os.Chdir(repoRoot); err != nil {
		return fmt.Errorf("chdir to repository root %s: %w", repoRoot, err)
	}

	if _, err := os.Stat(filepath.Join(repoRoot, server.WebDistDir)); err != nil {
		return fmt.Errorf("%s missing — build the SPA first (npm run build in web/): %w", server.WebDistDir, err)
	}

	// The suite needs deterministic credentials and a fixed port regardless of
	// the invoking environment; the signing key is fresh per boot so the
	// broker never reuses a secret from anywhere else.
	secret, err := randomSecret()
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{
		{"DOCU_KIOSK_TOKEN_SECRET", secret},
		{"AUTH_USERNAME", testAdminUsername},
		{"AUTH_PASSWORD", testAdminPassword},
		{"PORT", strconv.Itoa(testPort)},
		{"LOG_LEVEL", "warn"},
	} {
		if err := os.Setenv(pair[0], pair[1]); err != nil {
			return fmt.Errorf("setenv %s: %w", pair[0], err)
		}
	}

	dbDir, err := os.MkdirTemp("", "docu-kiosk-e2e-*")
	if err != nil {
		return fmt.Errorf("create temporary database directory: %w", err)
	}
	defer os.RemoveAll(dbDir)

	db, err := sql.Open("sqlite", filepath.Join(dbDir, "kiosks.db")+"?_texttotime=1&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open temporary database: %w", err)
	}
	defer db.Close()

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "./sql/migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// Application-level backfill, mirroring cmd/server's startup order.
	if err := kiosks.Migrate(context.Background(), db); err != nil {
		return fmt.Errorf("migrate kiosk names: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	srv, err := server.NewServer(cfg, database.New(db))
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	fmt.Fprintf(os.Stderr, "e2e broker: listening on http://127.0.0.1:%d (temporary database %s)\n", testPort, dbDir)
	return srv.Start()
}

// randomSecret returns 32 random bytes hex-encoded, satisfying the broker's
// minimum signing-key length without reusing any ambient secret.
func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate signing key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
