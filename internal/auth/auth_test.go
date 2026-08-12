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

// testJWTTTL and testRefreshTTL mirror the config defaults; the seam tests
// pass them to newAuthModule so token behavior matches production.
const (
	testJWTTTL     = 15 * time.Second
	testRefreshTTL = 60 * 24 * time.Hour
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

func newTestModule(t *testing.T) (*AuthModule, *database.Queries, *sql.DB) {
	t.Helper()
	db, queries := newTestDB(t)
	module, err := NewAuthModule(queries, []byte(testSecret), testJWTTTL, testRefreshTTL)
	if err != nil {
		t.Fatal(err)
	}
	return module, queries, db
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
	if _, err := NewAuthModule(queries, []byte("short"), testJWTTTL, testRefreshTTL); err == nil {
		t.Error("expected error for short jwt key")
	}
}

func TestLoginSuccess(t *testing.T) {
	module, queries, _ := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

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
	module, queries, _ := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

	if _, _, err := module.Login(context.Background(), "admin", "wrong horse"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLoginUnknownUser(t *testing.T) {
	module, _, _ := newTestModule(t)

	if _, _, err := module.Login(context.Background(), "nobody", "whatever"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestRotateRefresh(t *testing.T) {
	module, queries, _ := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

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
	module, queries, _ := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

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
	module, _, _ := newTestModule(t)

	if _, _, err := module.RotateRefresh(context.Background(), "not-a-real-token"); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken, got %v", err)
	}
}

func TestRotateRefreshRejectsExpired(t *testing.T) {
	module, queries, db := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

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
	module, queries, _ := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

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
	module, queries, _ := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

	// A token signed with a different key (e.g. an empty one) must not validate.
	forged, err := generateJWT(uuid.New(), []byte("another-key-0123456789abcdef"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := module.Validate(context.Background(), forged); err == nil {
		t.Error("token signed with a different key should fail validation")
	}
}

// fakeStore is a scriptable store for AuthModule tests. It embeds the store
// interface so methods a test does not exercise are promoted (and would panic
// on a nil embedded interface), and overrides exactly the methods each
// scenario drives.
type fakeStore struct {
	store
	userByUsername      database.User
	userByUsernameErr   error
	refreshToken        database.RefreshToken
	refreshTokenErr     error
	makeRefreshToken    database.RefreshToken
	makeRefreshTokenErr error
	revokeErr           error
}

func (f *fakeStore) GetUserByUsername(_ context.Context, _ string) (database.User, error) {
	return f.userByUsername, f.userByUsernameErr
}

func (f *fakeStore) GetRefreshToken(_ context.Context, _ string) (database.RefreshToken, error) {
	return f.refreshToken, f.refreshTokenErr
}

func (f *fakeStore) MakeRefreshToken(_ context.Context, _ database.MakeRefreshTokenParams) (database.RefreshToken, error) {
	return f.makeRefreshToken, f.makeRefreshTokenErr
}

func (f *fakeStore) RevokeRefreshToken(_ context.Context, _ string) error {
	return f.revokeErr
}

// newFakeModule builds an AuthModule around a fake store, proving the seam
// (not the concrete *database.Queries) carries the operations.
func newFakeModule(s store) *AuthModule {
	return newAuthModule(s, []byte(testSecret), testJWTTTL, testRefreshTTL)
}

// A store failure while looking up the user must map to
// ErrInvalidCredentials — identical to an unknown user, so the login
// response cannot be used to probe the database.
func TestLoginStoreLookupFailureMapsToInvalidCredentials(t *testing.T) {
	storeErr := errors.New("database unavailable")
	module := newFakeModule(&fakeStore{userByUsernameErr: storeErr})

	if _, _, err := module.Login(context.Background(), "admin", "whatever"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// A store failure while persisting the refresh token must surface as a
// wrapped non-credentials error (the handler maps it to 500), not a panic.
func TestLoginRefreshTokenStoreFailureIsWrapped(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	storeErr := errors.New("disk full")
	module := newFakeModule(&fakeStore{
		userByUsername:      database.User{ID: uuid.New(), Username: "admin", Password: hash},
		makeRefreshTokenErr: storeErr,
	})

	if _, _, err := module.Login(context.Background(), "admin", "correct horse"); !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped store error, got %v", err)
	}
}

// A store failure while reading the refresh token must map to
// ErrInvalidRefreshToken, the same response a revoked or unknown token gets.
func TestRotateRefreshStoreLookupFailureMapsToInvalidToken(t *testing.T) {
	storeErr := errors.New("database unavailable")
	module := newFakeModule(&fakeStore{refreshTokenErr: storeErr})

	if _, _, err := module.RotateRefresh(context.Background(), "some-token"); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken, got %v", err)
	}
}
