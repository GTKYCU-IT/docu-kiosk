package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/config"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// testLifetimes bundles the config token-lifetime defaults for constructor
// call sites, so tests exercise the same policy as production.
var testLifetimes = TokenLifetimes{JWTTTL: config.DefaultJWTTTL, RefreshTTL: config.DefaultRefreshTTL}

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
	module, err := NewAuthModule(queries, []byte(testSecret), testLifetimes)
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
	if _, err := NewAuthModule(queries, []byte("short"), testLifetimes); err == nil {
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

	// The rotation replaced the old value in place: the old token no longer
	// resolves, and the successor is the one live token that rotates again.
	if _, err := queries.GetRefreshToken(context.Background(), oldRefresh); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected the old token to be gone after rotation, got %v", err)
	}
	if _, successor2, err := module.RotateRefresh(context.Background(), newRefresh); err != nil {
		t.Fatalf("successor must rotate again: %v", err)
	} else if successor2 == newRefresh {
		t.Error("second rotation must issue yet another token")
	}
}

func TestRotateRefreshRejectsRevoked(t *testing.T) {
	module, queries, db := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

	_, oldRefresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := module.RotateRefresh(context.Background(), oldRefresh); err != nil {
		t.Fatal(err)
	}
	// The old token was replaced by the first rotation; replaying it must fail.
	if _, _, err := module.RotateRefresh(context.Background(), oldRefresh); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken for replayed token, got %v", err)
	}
	// The lost replay must not create a successor: exactly the one rotated
	// row remains, still carrying the first successor.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM refresh_tokens").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected exactly one token row after a lost replay, got %d", count)
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

// fakeStore is a scriptable store for AuthModule tests. Every store method is
// implemented explicitly — no nil interface promotion — so a test reaches a
// method only by stubbing it. The seam methods (CountUsers, CreateUser,
// GetUserByUsername, GetRefreshToken, MakeRefreshToken, RotateRefreshToken,
// RevokeCurrentRefreshToken) carry scripted results per scenario; GetUser
// fails loudly and intentionally, so any test that drives it must replace the
// stub. CreateUser records its calls so tests can prove no downstream creation
// happens on skipped, invalid, or error paths.
type fakeStore struct {
	countUsers                   int64
	countUsersErr                error
	createdUser                  database.User
	createUserErr                error
	createCalls                  int
	createdParams                database.CreateUserParams
	userByUsername               database.User
	userByUsernameErr            error
	refreshToken                 database.RefreshToken
	refreshTokenErr              error
	makeRefreshToken             database.RefreshToken
	makeRefreshTokenErr          error
	rotateRefreshToken           database.RefreshToken
	rotateRefreshTokenErr        error
	revokeCurrentRefreshToken    database.RefreshToken
	revokeCurrentRefreshTokenErr error
}

func (f *fakeStore) CountUsers(_ context.Context) (int64, error) {
	return f.countUsers, f.countUsersErr
}

func (f *fakeStore) CreateUser(_ context.Context, arg database.CreateUserParams) (database.User, error) {
	f.createCalls++
	f.createdParams = arg
	return f.createdUser, f.createUserErr
}

func (f *fakeStore) GetUser(_ context.Context, _ uuid.UUID) (database.User, error) {
	return database.User{}, errors.New("get user: not stubbed")
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

func (f *fakeStore) RotateRefreshToken(_ context.Context, _ database.RotateRefreshTokenParams) (database.RefreshToken, error) {
	return f.rotateRefreshToken, f.rotateRefreshTokenErr
}

func (f *fakeStore) RevokeCurrentRefreshToken(_ context.Context, _ string) (database.RefreshToken, error) {
	return f.revokeCurrentRefreshToken, f.revokeCurrentRefreshTokenErr
}

// newFakeModule builds an AuthModule around a fake store, proving the seam
// (not the concrete *database.Queries) carries the operations.
func newFakeModule(s store) *AuthModule {
	return newAuthModule(s, []byte(testSecret), testLifetimes)
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

// Concurrent rotations of the same token must produce at most one successor:
// exactly one racer wins the atomic exchange, every loser gets
// ErrInvalidRefreshToken, and a single live token row remains. The pool is
// pinned to one connection so the in-memory database is shared; the rotation
// property itself is enforced by the conditional UPDATE.
func TestRotateRefreshConcurrentReplayProducesSingleSuccessor(t *testing.T) {
	module, queries, db := newTestModule(t)
	db.SetMaxOpenConns(1)
	createTestUser(t, queries, "admin", "correct horse")

	_, refresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var wg sync.WaitGroup
	results := make(chan error, racers)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := module.RotateRefresh(context.Background(), refresh)
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInvalidRefreshToken) {
			t.Errorf("expected ErrInvalidRefreshToken for losing racers, got %v", err)
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly one successful rotation, got %d", successes)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM refresh_tokens").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected exactly one token row after concurrent rotation, got %d", count)
	}
}

// When the atomic rotation matches no row — a concurrent replay won the
// exchange — the loser must get ErrInvalidRefreshToken and no successor.
func TestRotateRefreshAtomicLossMapsToInvalidToken(t *testing.T) {
	module := newFakeModule(&fakeStore{
		refreshToken: database.RefreshToken{
			Token:     "old",
			UserID:    uuid.New(),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
		rotateRefreshTokenErr: sql.ErrNoRows,
	})

	if _, _, err := module.RotateRefresh(context.Background(), "old"); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken when the rotation matches no row, got %v", err)
	}
}

// A store failure inside the atomic rotation must surface as a wrapped
// non-credentials error (the handler maps it to 500), never as an invalid
// token.
func TestRotateRefreshStoreFailureIsWrapped(t *testing.T) {
	storeErr := errors.New("disk full")
	module := newFakeModule(&fakeStore{
		refreshToken: database.RefreshToken{
			Token:     "old",
			UserID:    uuid.New(),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
		rotateRefreshTokenErr: storeErr,
	})

	_, _, err := module.RotateRefresh(context.Background(), "old")
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected wrapped store error, got %v", err)
	}
	if !strings.Contains(err.Error(), "rotate refresh token") {
		t.Errorf("error %q does not carry rotation context", err)
	}
	if errors.Is(err, ErrInvalidRefreshToken) {
		t.Error("store failure must not be reported as an invalid token")
	}
}

// Logout on a live token succeeds, marks the row revoked, and makes the token
// unusable for both rotation and a second logout.
func TestLogoutRevokesToken(t *testing.T) {
	module, queries, _ := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

	_, refresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	if err := module.Logout(context.Background(), refresh); err != nil {
		t.Fatal(err)
	}

	rt, err := queries.GetRefreshToken(context.Background(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	if rt.RevokedAt == nil {
		t.Error("expected the token to be revoked after logout")
	}

	if _, _, err := module.RotateRefresh(context.Background(), refresh); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken for a logged-out token, got %v", err)
	}
	if err := module.Logout(context.Background(), refresh); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken for an already-revoked token, got %v", err)
	}
}

// Logout on a token that exists but has expired must reject it like an
// unknown one, not revoke it.
func TestLogoutRejectsExpiredToken(t *testing.T) {
	module, queries, db := newTestModule(t)
	createTestUser(t, queries, "admin", "correct horse")

	_, refresh, err := module.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("UPDATE refresh_tokens SET expires_at = datetime('now', '-1 day') WHERE token = ?", refresh); err != nil {
		t.Fatal(err)
	}

	if err := module.Logout(context.Background(), refresh); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken for expired token, got %v", err)
	}
}

// A token that matches no row (unknown or already revoked) maps to
// ErrInvalidRefreshToken.
func TestLogoutUnknownTokenMapsToInvalidToken(t *testing.T) {
	module := newFakeModule(&fakeStore{revokeCurrentRefreshTokenErr: sql.ErrNoRows})

	if err := module.Logout(context.Background(), "not-a-real-token"); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("expected ErrInvalidRefreshToken, got %v", err)
	}
}

// A persistence failure while revoking must surface as a wrapped error, not
// as an invalid token, so the handler can respond 500 without clearing the
// cookie.
func TestLogoutStoreFailureIsWrapped(t *testing.T) {
	storeErr := errors.New("database unavailable")
	module := newFakeModule(&fakeStore{revokeCurrentRefreshTokenErr: storeErr})

	err := module.Logout(context.Background(), "some-token")
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected wrapped store error, got %v", err)
	}
	if !strings.Contains(err.Error(), "revoke refresh token") {
		t.Errorf("error %q does not carry revoke context", err)
	}
	if errors.Is(err, ErrInvalidRefreshToken) {
		t.Error("store failure must not be reported as an invalid token")
	}
}

// When users already exist, EnsureAdminUser must be a no-op: even missing
// credentials are ignored (the count is consulted before any validation), and
// nothing is created.
func TestEnsureAdminUserSkipsWhenUsersExist(t *testing.T) {
	fs := &fakeStore{countUsers: 1}
	module := newFakeModule(fs)

	if err := module.EnsureAdminUser("", ""); err != nil {
		t.Fatalf("expected no-op for existing users, got %v", err)
	}
	if fs.createCalls != 0 {
		t.Errorf("expected no creation when users exist, got %d CreateUser calls", fs.createCalls)
	}
}

// On an empty table, EnsureAdminUser must create a UUID-backed user whose
// stored password is a verifiable hash of the supplied one.
func TestEnsureAdminUserCreatesUserWithHash(t *testing.T) {
	fs := &fakeStore{}
	module := newFakeModule(fs)

	if err := module.EnsureAdminUser("admin", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if fs.createCalls != 1 {
		t.Fatalf("expected exactly one CreateUser call, got %d", fs.createCalls)
	}
	if fs.createdParams.Username != "admin" {
		t.Errorf("created username = %q, want admin", fs.createdParams.Username)
	}
	if fs.createdParams.ID == uuid.Nil {
		t.Error("created user has empty UUID")
	}
	if fs.createdParams.Password == "correct horse" {
		t.Error("stored password must be hashed, not plaintext")
	}
	if !CheckPasswordHash("correct horse", fs.createdParams.Password) {
		t.Error("stored password does not verify against the supplied password")
	}
}

// An empty table with missing credentials must fail before any creation.
func TestEnsureAdminUserRejectsMissingCredentials(t *testing.T) {
	for _, tc := range []struct {
		name     string
		username string
		password string
	}{
		{name: "empty username", username: "", password: "correct horse"},
		{name: "empty password", username: "admin", password: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			module := newFakeModule(fs)

			err := module.EnsureAdminUser(tc.username, tc.password)
			if err == nil {
				t.Fatal("expected error for missing credentials")
			}
			if !strings.Contains(err.Error(), "AUTH_USERNAME") || !strings.Contains(err.Error(), "AUTH_PASSWORD") {
				t.Errorf("error %q does not name the required AUTH_USERNAME/AUTH_PASSWORD variables", err)
			}
			if fs.createCalls != 0 {
				t.Errorf("expected no creation on missing credentials, got %d CreateUser calls", fs.createCalls)
			}
		})
	}
}

// An empty table with a password shorter than 8 characters must fail before
// any creation.
func TestEnsureAdminUserRejectsShortPassword(t *testing.T) {
	fs := &fakeStore{}
	module := newFakeModule(fs)

	err := module.EnsureAdminUser("admin", "short7")
	if err == nil {
		t.Fatal("expected error for short password")
	}
	if !strings.Contains(err.Error(), "at least 8 characters") {
		t.Errorf("error %q does not state the 8-character minimum", err)
	}
	if fs.createCalls != 0 {
		t.Errorf("expected no creation for short password, got %d CreateUser calls", fs.createCalls)
	}
}

// A failure while counting users must surface as a wrapped error carrying the
// operation context, and must prevent any creation.
func TestEnsureAdminUserCountFailureIsWrapped(t *testing.T) {
	storeErr := errors.New("database unavailable")
	fs := &fakeStore{countUsersErr: storeErr}
	module := newFakeModule(fs)

	err := module.EnsureAdminUser("admin", "correct horse")
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected wrapped count error, got %v", err)
	}
	if !strings.Contains(err.Error(), "count users") {
		t.Errorf("error %q does not carry count context", err)
	}
	if fs.createCalls != 0 {
		t.Errorf("expected no creation on count failure, got %d CreateUser calls", fs.createCalls)
	}
}

// A failure while persisting the admin user must surface as a wrapped error
// carrying the operation context.
func TestEnsureAdminUserCreateFailureIsWrapped(t *testing.T) {
	storeErr := errors.New("disk full")
	fs := &fakeStore{createUserErr: storeErr}
	module := newFakeModule(fs)

	err := module.EnsureAdminUser("admin", "correct horse")
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected wrapped create error, got %v", err)
	}
	if !strings.Contains(err.Error(), "create admin user") {
		t.Errorf("error %q does not carry create context", err)
	}
}
