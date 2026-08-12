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
	_ "modernc.org/sqlite" // register the sqlite driver for newTestDB
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
	upsertErr    error
	listErr      error
	listCalled   bool
	upsertCalled bool
	byName       map[string]database.Kiosk
	held         func(ip, name string) (bool, error) // optional scripted NameHeldByOther
	nameChecks   int
}

func (s *stubStore) UpsertKiosk(_ context.Context, arg database.UpsertKioskParams) (database.Kiosk, error) {
	s.upsertCalled = true
	if s.upsertErr != nil {
		return database.Kiosk{}, s.upsertErr
	}
	return database.Kiosk{ID: arg.ID, IP: arg.IP, Name: arg.Name}, nil
}

func (s *stubStore) NameHeldByOther(_ context.Context, ip, name string) (bool, error) {
	s.nameChecks++
	if s.held != nil {
		return s.held(ip, name)
	}
	k, ok := s.byName[name]
	if !ok {
		return false, nil
	}
	return k.IP != ip, nil
}

func (s *stubStore) ListKiosksByIDs(_ context.Context, _ []uuid.UUID) ([]database.Kiosk, error) {
	s.listCalled = true
	if s.listErr != nil {
		return nil, s.listErr
	}
	return nil, nil
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
	m, buf := newTestModule(dbStore{Queries: db})
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

	// Pre-check short-circuit: a registered IP re-registering under a name
	// held by another kiosk is rejected by the NameHeldByOther pre-check
	// before any upsert is attempted. The upsert's ON CONFLICT(ip) DO UPDATE
	// path is only reachable via a race between the pre-check and the write,
	// which TestRegisterNameTakenByRacingRegister exercises with a fake
	// store; either way the register must yield ErrNameTaken, not a silent
	// rename.
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

func TestRegisterEmptyNameRejectedBeforeStore(t *testing.T) {
	// The module owns the input contract: an empty or whitespace-only name
	// is rejected before any store access.
	s := &stubStore{}
	m, _ := newTestModule(s)

	for _, name := range []string{"", "   ", "\t\n"} {
		if err := m.Register(context.Background(), "10.0.0.1", name); !errors.Is(err, ErrNameRequired) {
			t.Fatalf("Register(name=%q) = %v, want ErrNameRequired", name, err)
		}
	}
	if s.upsertCalled || s.nameChecks != 0 {
		t.Error("store was accessed before the name check")
	}
}

func TestRegisterNameHeldByOtherKiosk(t *testing.T) {
	// A name held by a different IP is rejected by the pre-check alone; the
	// upsert is never attempted. The seam-level twin of
	// TestRegisterNameTakenByOtherKiosk.
	s := &stubStore{
		byName: map[string]database.Kiosk{
			"Lobby": {ID: uuid.New(), IP: "10.0.0.1", Name: "Lobby"},
		},
	}
	m, buf := newTestModule(s)

	if err := m.Register(context.Background(), "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	if s.upsertCalled {
		t.Error("UpsertKiosk was called for a name already held by another kiosk")
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("name conflict was logged as an error: %s", buf.String())
	}
}

func TestRegisterNameTakenByRacingRegister(t *testing.T) {
	// The name is free at pre-check time, but a racing register claims it
	// before this upsert lands; the failed upsert is classified as
	// ErrNameTaken via a second store answer, not a driver error code.
	s := &stubStore{
		upsertErr: errors.New("write failed"),
		held: func(ip, name string) (bool, error) {
			return s.nameChecks > 1, nil
		},
	}
	m, buf := newTestModule(s)

	if err := m.Register(context.Background(), "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	if s.nameChecks != 2 {
		t.Errorf("NameHeldByOther called %d times, want 2 (pre-check + race re-check)", s.nameChecks)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("name conflict was logged as an error: %s", buf.String())
	}
}

func TestRegisterDBFailureLogged(t *testing.T) {
	// The upsert fails and the follow-up conflict check also errors; the
	// failure falls through to the logged wrapped-error path (not ErrNameTaken).
	s := &stubStore{
		upsertErr: errors.New("disk I/O error"),
		held: func(ip, name string) (bool, error) {
			if s.nameChecks > 1 {
				return false, errors.New("name lookup failed")
			}
			return false, nil
		},
	}
	m, buf := newTestModule(s)

	err := m.Register(context.Background(), "10.0.0.1", "Lobby")
	if err == nil {
		t.Fatal("Register returned nil error, want wrapped error")
	}
	if errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want wrapped non-conflict error", err)
	}
	if s.nameChecks != 2 {
		t.Errorf("NameHeldByOther called %d times, want 2 (pre-check + post-upsert re-check)", s.nameChecks)
	}
	if !strings.Contains(buf.String(), "register kiosk") {
		t.Errorf("log buffer does not contain %q: %s", "register kiosk", buf.String())
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("upsert failure was not logged at ERROR level: %s", buf.String())
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
