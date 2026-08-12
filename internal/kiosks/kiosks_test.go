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
	_ "modernc.org/sqlite"
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

// --- Registration ---

func TestRegisterNewKiosk(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	k, err := m.ResolveIdentity(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if k.ID == uuid.Nil {
		t.Error("ResolveIdentity returned a zero ID")
	}
	if k.IP != "10.0.0.1" || k.Name != "Lobby" {
		t.Errorf("ResolveIdentity = {%s %s}, want IP 10.0.0.1 Name Lobby", k.IP, k.Name)
	}
}

func TestRegisterIdempotentKeepsIdentity(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	first, err := m.ResolveIdentity(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("ResolveIdentity after first register: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("second Register: %v", err)
	}
	second, err := m.ResolveIdentity(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("ResolveIdentity after second register: %v", err)
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
	before, err := m.ResolveIdentity(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("ResolveIdentity before rename: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.1", "B"); err != nil {
		t.Fatalf("Register B: %v", err)
	}
	after, err := m.ResolveIdentity(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("ResolveIdentity after rename: %v", err)
	}
	if after.Name != "B" {
		t.Errorf("ResolveIdentity.Name = %q, want B", after.Name)
	}
	if after.ID != before.ID {
		t.Errorf("rename changed identity: %s -> %s", before.ID, after.ID)
	}
}

func TestRegisterNameTakenByOtherKiosk(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	original, err := m.ResolveIdentity(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("ResolveIdentity original: %v", err)
	}
	if original.Name != "Lobby" {
		t.Errorf("original kiosk name changed to %q", original.Name)
	}
	if _, err := m.ResolveIdentity(ctx, "10.0.0.2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ResolveIdentity(10.0.0.2) = %v, want ErrNotFound (second kiosk must not be registered)", err)
	}
}

// --- Identity ---

func TestResolveIdentityUnknownIP(t *testing.T) {
	db := newTestDB(t)
	m := testModule(db)

	if _, err := m.ResolveIdentity(context.Background(), "10.0.0.99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveIdentity = %v, want ErrNotFound", err)
	}
}

// --- Listing ---

func TestListLiveOrdersByNameAndFilters(t *testing.T) {
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
		k, err := m.ResolveIdentity(ctx, r.ip)
		if err != nil {
			t.Fatalf("ResolveIdentity(%s): %v", r.ip, err)
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
	s := &stubStore{upsertErr: errors.New("db down"), nameErr: sql.ErrNoRows}
	m, buf := newTestModule(s)

	err := m.Register(context.Background(), "10.0.0.1", "Lobby")
	if err == nil {
		t.Fatal("Register returned nil error, want wrapped error")
	}
	if !strings.Contains(buf.String(), "register kiosk") {
		t.Errorf("log buffer does not contain %q: %s", "register kiosk", buf.String())
	}
}

func TestRegisterNameTakenNotLoggedAsError(t *testing.T) {
	s := &stubStore{
		upsertErr: errors.New("unique constraint violation"),
		byName: map[string]database.Kiosk{
			"Lobby": {ID: uuid.New(), IP: "10.0.0.1", Name: "Lobby"},
		},
	}
	m, buf := newTestModule(s)

	err := m.Register(context.Background(), "10.0.0.2", "Lobby")
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("name conflict was logged as an error: %s", buf.String())
	}
}
