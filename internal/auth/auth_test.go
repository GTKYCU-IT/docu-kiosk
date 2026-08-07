package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) (*sql.DB, *database.Queries) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, file, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(file), "..", "..", "sql", "migrations")

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, migrationsDir); err != nil {
		t.Fatal(err)
	}

	return db, database.New(db)
}

func newTestModule(t *testing.T) (*AuthModule, *sql.DB) {
	t.Helper()
	db, queries := newTestDB(t)
	module, err := NewAuthModule(queries, []byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return module, db
}

func createTestUser(t *testing.T, queries *database.Queries, username, password string) {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateUser(context.Background(), database.CreateUserParams{
		ID:       uuid.New(),
		Username: username,
		Password: hash,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNewAuthModuleRejectsShortKey(t *testing.T) {
	_, queries := newTestDB(t)
	if _, err := NewAuthModule(queries, []byte("short")); err == nil {
		t.Error("expected error for short jwt key")
	}
}

func TestLoginSuccess(t *testing.T) {
	module, _ := newTestModule(t)
	createTestUser(t, module.db, "admin", "correct horse")

	jwt, refresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if jwt == "" || refresh == "" {
		t.Fatalf("expected jwt and refresh token, got %q and %q", jwt, refresh)
	}

	userID, err := validateJWT(jwt, []byte(testSecret))
	if err != nil {
		t.Fatalf("issued jwt does not validate: %v", err)
	}
	if userID == uuid.Nil {
		t.Error("issued jwt has empty subject")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	module, _ := newTestModule(t)
	createTestUser(t, module.db, "admin", "correct horse")

	if _, _, err := module.Login(context.Background(), "admin", "wrong horse"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLoginUnknownUser(t *testing.T) {
	module, _ := newTestModule(t)

	if _, _, err := module.Login(context.Background(), "nobody", "whatever"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestRotateRefresh(t *testing.T) {
	module, _ := newTestModule(t)
	createTestUser(t, module.db, "admin", "correct horse")

	_, oldRefresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	jwt, newRefresh, err := module.RotateRefresh(context.Background(), oldRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if jwt == "" || newRefresh == "" {
		t.Fatalf("expected new jwt and refresh token, got %q and %q", jwt, newRefresh)
	}
	if newRefresh == oldRefresh {
		t.Error("rotated refresh token should differ from the old one")
	}
}

func TestRotateRefreshRejectsRevoked(t *testing.T) {
	module, _ := newTestModule(t)
	createTestUser(t, module.db, "admin", "correct horse")

	_, oldRefresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := module.RotateRefresh(context.Background(), oldRefresh); err != nil {
		t.Fatal(err)
	}
	// The old token was revoked by the first rotation; replaying it must fail.
	if _, _, err := module.RotateRefresh(context.Background(), oldRefresh); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken for replayed token, got %v", err)
	}
}

func TestRotateRefreshRejectsUnknownToken(t *testing.T) {
	module, _ := newTestModule(t)

	if _, _, err := module.RotateRefresh(context.Background(), "not-a-real-token"); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken, got %v", err)
	}
}

func TestRotateRefreshRejectsExpired(t *testing.T) {
	module, db := newTestModule(t)
	createTestUser(t, module.db, "admin", "correct horse")

	_, oldRefresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	// Backdate the token so it looks expired to RotateRefresh.
	if _, err := db.Exec("UPDATE refresh_tokens SET expires_at = datetime('now', '-1 day') WHERE token = ?", oldRefresh); err != nil {
		t.Fatal(err)
	}

	if _, _, err := module.RotateRefresh(context.Background(), oldRefresh); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken for expired token, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	module, _ := newTestModule(t)
	createTestUser(t, module.db, "admin", "correct horse")

	jwt, _, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	user, err := module.Validate(context.Background(), jwt)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "admin" {
		t.Errorf("expected admin user, got %q", user.Username)
	}
}

func TestValidateRejectsForgedToken(t *testing.T) {
	module, _ := newTestModule(t)
	createTestUser(t, module.db, "admin", "correct horse")

	// A token signed with a different key (e.g. an empty one) must not validate.
	forged, err := generateJWT(uuid.New(), []byte("another-key-0123456789abcdef"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := module.Validate(context.Background(), forged); err == nil {
		t.Error("token signed with a different key should fail validation")
	}
}
