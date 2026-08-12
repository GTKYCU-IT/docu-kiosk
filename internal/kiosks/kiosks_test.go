package kiosks

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	sqlite "modernc.org/sqlite"
)

// newTestDB returns a goose-migrated in-memory SQLite database, closed
// automatically when the test finishes.
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

// testModule builds the production module (New) over a real database,
// discarding log output.
func testModule(db *database.Queries) *Module {
	return New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// newTestModule builds a Module over any store with a debug logger that
// writes into a buffer the test can inspect.
func newTestModule(s store) (*Module, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return newModule(s, logger), buf
}

// stubStore is a scriptable store for kiosk tests. It embeds the store
// interface so unimplemented methods are promoted, and overrides the methods
// each test exercises.
type stubStore struct {
	store
	upsertErr  error
	nameErr    error
	listErr    error
	listCalled bool
	byName     map[string]database.Kiosk
}

func (s *stubStore) UpsertKiosk(_ context.Context, arg database.UpsertKioskParams) (database.Kiosk, error) {
	if s.upsertErr != nil {
		return database.Kiosk{}, s.upsertErr
	}
	return database.Kiosk{ID: arg.ID, IP: arg.IP, Name: arg.Name}, nil
}

func (s *stubStore) GetKioskByName(_ context.Context, name string) (database.Kiosk, error) {
	if s.nameErr != nil {
		return database.Kiosk{}, s.nameErr
	}
	k, ok := s.byName[name]
	if !ok {
		return database.Kiosk{}, sql.ErrNoRows
	}
	return k, nil
}

func (s *stubStore) ListKiosksByIDs(_ context.Context, _ []uuid.UUID) ([]database.Kiosk, error) {
	s.listCalled = true
	if s.listErr != nil {
		return nil, s.listErr
	}
	return nil, nil
}

// uniqueConstraintError returns a real driver-level UNIQUE constraint error
// (modernc *sqlite.Error, extended code 2067) produced by violating a scratch
// table, so fake-store tests exercise the real classification gate.
func uniqueConstraintError(t *testing.T) error {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (v text UNIQUE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t VALUES ('x')"); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO t VALUES ('x')")
	if err == nil {
		t.Fatal("second insert succeeded, want UNIQUE constraint error")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("error %T is not *sqlite.Error: %v", err, err)
	}
	return err
}

// --- Registration ---

func TestRegisterNewKiosk(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	k, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP: %v", err)
	}
	if k.ID == uuid.Nil {
		t.Error("GetKioskByIP returned a zero ID")
	}
	if k.IP != "10.0.0.1" || k.Name != "Lobby" {
		t.Errorf("GetKioskByIP = {%s %s}, want IP 10.0.0.1 Name Lobby", k.IP, k.Name)
	}
}

func TestRegisterIdempotentKeepsIdentity(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	first, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP after first register: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("second Register: %v", err)
	}
	second, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP after second register: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("identity changed across re-registration: %s -> %s", first.ID, second.ID)
	}
}

func TestRegisterSameIPRenames(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "A"); err != nil {
		t.Fatalf("Register A: %v", err)
	}
	before, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP before rename: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.1", "B"); err != nil {
		t.Fatalf("Register B: %v", err)
	}
	after, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP after rename: %v", err)
	}
	if after.Name != "B" {
		t.Errorf("GetKioskByIP.Name = %q, want B", after.Name)
	}
	if after.ID != before.ID {
		t.Errorf("rename changed identity: %s -> %s", before.ID, after.ID)
	}
}

func TestRegisterNameTakenByOtherKiosk(t *testing.T) {
	db := newTestDB(t)
	m, buf := newTestModule(db)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("name conflict was logged as an error: %s", buf.String())
	}
	original, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP original: %v", err)
	}
	if original.Name != "Lobby" {
		t.Errorf("original kiosk name changed to %q", original.Name)
	}
	if _, err := m.GetKioskByIP(ctx, "10.0.0.2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetKioskByIP(10.0.0.2) = %v, want ErrNotFound (second kiosk must not be registered)", err)
	}

	// DO UPDATE-phase conflict: a registered IP re-registering under a name
	// held by another kiosk hits the upsert's ON CONFLICT(ip) DO UPDATE path
	// and must still yield ErrNameTaken, not a silent rename.
	if err := m.Register(ctx, "10.0.0.2", "Branch2"); err != nil {
		t.Fatalf("Register Branch2: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register(10.0.0.2, Lobby) = %v, want ErrNameTaken", err)
	}
	kept, err := m.GetKioskByIP(ctx, "10.0.0.2")
	if err != nil {
		t.Fatalf("GetKioskByIP(10.0.0.2) after conflict: %v", err)
	}
	if kept.Name != "Branch2" {
		t.Errorf("10.0.0.2 name changed to %q, want Branch2", kept.Name)
	}
}

// --- Identity ---

func TestGetKioskByIPUnknownIP(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)

	if _, err := m.GetKioskByIP(context.Background(), "10.0.0.99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetKioskByIP = %v, want ErrNotFound", err)
	}
}

// --- Listing ---

func TestListLiveOrdersByName(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)
	ctx := context.Background()

	registered := []struct {
		ip   string
		name string
	}{
		{ip: "10.0.0.1", name: "Zebra"},
		{ip: "10.0.0.2", name: "Alpha"},
		{ip: "10.0.0.3", name: "Mango"},
	}
	ids := make(map[string]uuid.UUID, len(registered))
	for _, r := range registered {
		if err := m.Register(ctx, r.ip, r.name); err != nil {
			t.Fatalf("Register(%s): %v", r.name, err)
		}
		k, err := m.GetKioskByIP(ctx, r.ip)
		if err != nil {
			t.Fatalf("GetKioskByIP(%s): %v", r.ip, err)
		}
		ids[r.name] = k.ID
	}

	got, err := m.ListLive(ctx, []uuid.UUID{ids["Zebra"], ids["Alpha"]})
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListLive returned %d kiosks, want 2", len(got))
	}
	if got[0].Name != "Alpha" || got[1].Name != "Zebra" {
		t.Errorf("ListLive order = [%s %s], want [Alpha Zebra]", got[0].Name, got[1].Name)
	}
}

// --- Failure paths with a fake store ---

func TestListLiveDBFailureLogged(t *testing.T) {
	s := &stubStore{listErr: errors.New("disk I/O error")}
	m, buf := newTestModule(s)

	_, err := m.ListLive(context.Background(), []uuid.UUID{uuid.New()})
	if err == nil {
		t.Fatal("ListLive returned nil error, want wrapped error")
	}
	if !strings.Contains(buf.String(), "list live kiosks") {
		t.Errorf("log buffer does not contain %q: %s", "list live kiosks", buf.String())
	}
}

func TestListLiveEmptySkipsQuery(t *testing.T) {
	s := &stubStore{}
	m, _ := newTestModule(s)

	got, err := m.ListLive(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if got == nil {
		t.Fatal("ListLive returned a nil slice, want empty non-nil")
	}
	if len(got) != 0 {
		t.Errorf("ListLive returned %d kiosks, want 0", len(got))
	}
	if s.listCalled {
		t.Error("ListKiosksByIDs was called for an empty connected set")
	}
}

func TestRegisterDBFailureLogged(t *testing.T) {
	// A genuine constraint error followed by a failing name lookup falls
	// through to the logged wrapped-error path (not ErrNameTaken).
	s := &stubStore{upsertErr: uniqueConstraintError(t), nameErr: sql.ErrNoRows}
	m, buf := newTestModule(s)

	err := m.Register(context.Background(), "10.0.0.1", "Lobby")
	if err == nil {
		t.Fatal("Register returned nil error, want wrapped error")
	}
	if errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want wrapped non-conflict error", err)
	}
	if !strings.Contains(buf.String(), "register kiosk") {
		t.Errorf("log buffer does not contain %q: %s", "register kiosk", buf.String())
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("upsert failure was not logged at ERROR level: %s", buf.String())
	}
}

func TestRegisterNameTakenWithConstraintError(t *testing.T) {
	// A genuine UNIQUE constraint error whose name lookup finds a
	// different-IP holder is ErrNameTaken — the seam-level twin of
	// TestRegisterNameTakenByOtherKiosk.
	s := &stubStore{
		upsertErr: uniqueConstraintError(t),
		byName: map[string]database.Kiosk{
			"Lobby": {ID: uuid.New(), IP: "10.0.0.1", Name: "Lobby"},
		},
	}
	m, buf := newTestModule(s)

	if err := m.Register(context.Background(), "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("name conflict was logged as an error: %s", buf.String())
	}
}

func TestRegisterUpsertFailureIsNotNameConflict(t *testing.T) {
	// A non-constraint failure must never be reported as a name conflict.
	s := &stubStore{upsertErr: errors.New("disk I/O error")}
	m, buf := newTestModule(s)

	err := m.Register(context.Background(), "10.0.0.2", "Lobby")
	if err == nil {
		t.Fatal("Register returned nil error, want wrapped error")
	}
	if errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want wrapped non-conflict error", err)
	}
	if !strings.Contains(buf.String(), "register kiosk") {
		t.Errorf("log buffer does not contain %q: %s", "register kiosk", buf.String())
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("upsert failure was not logged at ERROR level: %s", buf.String())
	}
}
