package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const testKey = "test-jwt-secret-key"

func newTestDB(t *testing.T) *database.Queries {
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

	return database.New(db)
}

func newTestModule(t *testing.T) *AuthModule {
	t.Helper()
	return NewAuthModule(newTestDB(t), []byte(testKey))
}

func createTestUser(t *testing.T, mod *AuthModule, username, password string) database.User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	user, err := mod.db.CreateUser(context.Background(), database.CreateUserParams{
		ID:       uuid.New(),
		Username: username,
		Password: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestLoginSuccess(t *testing.T) {
	mod := newTestModule(t)
	ctx := context.Background()

	createTestUser(t, mod, "testuser", "secret")

	jwt, rt, err := mod.Login(ctx, "testuser", "secret")
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if jwt == "" {
		t.Error("expected non-empty jwt")
	}
	if rt == "" {
		t.Error("expected non-empty refresh token")
	}
}

func TestLoginBadPassword(t *testing.T) {
	mod := newTestModule(t)
	ctx := context.Background()

	createTestUser(t, mod, "testuser", "secret")

	_, _, err := mod.Login(ctx, "testuser", "wrong")
	if err == nil {
		t.Error("expected error for bad password")
	}
}

func TestLoginNonexistentUser(t *testing.T) {
	mod := newTestModule(t)

	_, _, err := mod.Login(context.Background(), "nobody", "secret")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}
}

func TestRotateRefresh(t *testing.T) {
	mod := newTestModule(t)
	ctx := context.Background()

	createTestUser(t, mod, "testuser", "secret")

	// Issue a refresh token via Login.
	_, oldRT, err := mod.Login(ctx, "testuser", "secret")
	if err != nil {
		t.Fatal(err)
	}

	// Rotate it.
	jwt, newRT, err := mod.RotateRefresh(ctx, oldRT)
	if err != nil {
		t.Fatalf("RotateRefresh failed: %v", err)
	}
	if jwt == "" {
		t.Error("expected non-empty jwt")
	}
	if newRT == "" {
		t.Error("expected non-empty new refresh token")
	}
	if newRT == oldRT {
		t.Error("expected new refresh token to differ from old")
	}

	// The old token should be revoked — replay must fail.
	_, _, err = mod.RotateRefresh(ctx, oldRT)
	if err == nil {
		t.Error("expected error when replaying revoked refresh token")
	}
}

func TestRotateRefreshBadToken(t *testing.T) {
	mod := newTestModule(t)

	_, _, err := mod.RotateRefresh(context.Background(), "nonexistent-token")
	if err == nil {
		t.Error("expected error for bad refresh token")
	}
}

func TestValidate(t *testing.T) {
	mod := newTestModule(t)
	ctx := context.Background()

	user := createTestUser(t, mod, "testuser", "secret")

	jwt, _, err := mod.Login(ctx, "testuser", "secret")
	if err != nil {
		t.Fatal(err)
	}

	validatedUser, err := mod.Validate(ctx, jwt)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if validatedUser.ID != user.ID {
		t.Errorf("expected user ID %v, got %v", user.ID, validatedUser.ID)
	}
}

func TestValidateBadToken(t *testing.T) {
	mod := newTestModule(t)

	_, err := mod.Validate(context.Background(), "not-a-valid-jwt")
	if err == nil {
		t.Error("expected error for bad token")
	}
}
