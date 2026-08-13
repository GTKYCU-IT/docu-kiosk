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
	_ "modernc.org/sqlite" // register the sqlite driver for test DBs
)

// migrationsDir resolves the goose migration directory relative to this
// file, wherever the module is checked out.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "sql", "migrations")
}

// openTestDB opens an in-memory SQLite database closed at test teardown.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	return db
}

// testSQLDB returns a fully goose-migrated in-memory SQLite database plus
// the underlying *sql.DB for tests that observe durable rows (name_key and
// version) or seed data directly.
func testSQLDB(t *testing.T) (*database.Queries, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := goose.Up(db, migrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	return database.New(db), db
}

// newTestDB returns a goose-migrated in-memory SQLite database, closed
// automatically when the test finishes.
func newTestDB(t *testing.T) *database.Queries {
	t.Helper()
	q, _ := testSQLDB(t)
	return q
}

// newLegacyKioskDB returns an in-memory SQLite database with only the
// original kiosks migration applied (id/ip/name, before name_key and version
// existed), for seeding rows in the pre-migration shape. Call goose.Up with
// migrationsDir to apply the remaining migrations before exercising
// kiosks.Migrate.
func newLegacyKioskDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	// The kiosks table is the first migration in the directory
	// (20260515141553_create_kiosks.sql); stop right after it.
	if err := goose.UpTo(db, migrationsDir(t), 20260515141553); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedKiosk inserts a row in the pre-migration table shape and returns its
// id.
func seedKiosk(t *testing.T, db *sql.DB, ip, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(`INSERT INTO kiosks (id, ip, name) VALUES (?, ?, ?)`, id.String(), ip, name); err != nil {
		t.Fatalf("seed kiosk %s (%q): %v", ip, name, err)
	}
	return id
}

// kioskDurable reads the persisted name_key and version for ip directly
// from the database; the domain surface does not expose these fields.
func kioskDurable(t *testing.T, db *sql.DB, ip string) (nameKey string, version int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT name_key, version FROM kiosks WHERE ip = ?`, ip).Scan(&nameKey, &version); err != nil {
		t.Fatalf("read durable kiosk row for %s: %v", ip, err)
	}
	return nameKey, version
}

// kioskRow reads the full durable row for ip.
func kioskRow(t *testing.T, db *sql.DB, ip string) (id, name, nameKey string, version int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT id, name, name_key, version FROM kiosks WHERE ip = ?`, ip).Scan(&id, &name, &nameKey, &version); err != nil {
		t.Fatalf("read kiosk row for %s: %v", ip, err)
	}
	return id, name, nameKey, version
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
	createErr    error
	getErr       error
	listErr      error
	createCalled bool
	listCalled   bool
	ipChecks     int
	keyChecks    int
	createParams database.CreateKioskParams
	getKiosk     func(ip string) (database.Kiosk, error) // optional scripted IP lookup
	heldKey      func(ip, key string) (bool, error)      // optional scripted key check
}

func (s *stubStore) CreateKiosk(_ context.Context, arg database.CreateKioskParams) (database.Kiosk, error) {
	s.createCalled = true
	s.createParams = arg
	if s.createErr != nil {
		return database.Kiosk{}, s.createErr
	}
	return database.Kiosk{ID: arg.ID, IP: arg.IP, Name: arg.Name, NameKey: arg.NameKey, Version: 1}, nil
}

func (s *stubStore) GetKioskByIP(_ context.Context, ip string) (database.Kiosk, error) {
	s.ipChecks++
	if s.getKiosk != nil {
		return s.getKiosk(ip)
	}
	if s.getErr != nil {
		return database.Kiosk{}, s.getErr
	}
	return database.Kiosk{}, sql.ErrNoRows
}

func (s *stubStore) NameKeyHeldByOther(_ context.Context, ip, nameKey string) (bool, error) {
	s.keyChecks++
	if s.heldKey != nil {
		return s.heldKey(ip, nameKey)
	}
	return false, nil
}

func (s *stubStore) ListKiosksByIDs(_ context.Context, _ []uuid.UUID) ([]database.Kiosk, error) {
	s.listCalled = true
	if s.listErr != nil {
		return nil, s.listErr
	}
	return nil, nil
}

// --- Shared name boundary ---

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "surrounding ascii whitespace trimmed", raw: "  Lobby  ", want: "Lobby"},
		{name: "surrounding unicode whitespace trimmed", raw: "\u2003Lobby\u2003", want: "Lobby"},
		{name: "surrounding nbsp trimmed", raw: "\u00a0Lobby\u00a0", want: "Lobby"},
		{name: "nfc composes", raw: "Lobe\u0301", want: "Lob\u00e9"},
		{name: "nfc keeps composed form", raw: "Lob\u00e9", want: "Lob\u00e9"},
		{name: "trim before nfc", raw: "\u2003Lobe\u0301\u2003", want: "Lob\u00e9"},
		{name: "display casing preserved", raw: "lOBBY", want: "lOBBY"},
		{name: "width preserved", raw: "\uff41", want: "\uff41"},
		{name: "non-latin preserved", raw: "日本", want: "日本"},
		{name: "single code point", raw: "ß", want: "ß"},
		{name: "sixty four code points", raw: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
		{name: "sixty four non-ascii code points", raw: strings.Repeat("界", 64), want: strings.Repeat("界", 64)},
		{name: "decomposed pairs compose under the limit", raw: strings.Repeat("e\u0301", 32), want: strings.Repeat("\u00e9", 32)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeName(tt.raw)
			if err != nil {
				t.Fatalf("NormalizeName(%q) error = %v, want %q", tt.raw, err, tt.want)
			}
			if got != tt.want {
				t.Errorf("NormalizeName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeNameRejectsInvalid(t *testing.T) {
	invalid := []string{
		"",
		"   ",
		"\t\n\u2003",
		strings.Repeat("a", 65),
		strings.Repeat("界", 65),
		"Lob\x00by",
		"Lo\x1fbby",
		"Lob\x7fby",
		"Lob\u0085by",
		"Lob\u009fby",
		"Lo\tbby",
		"Lo\nbby",
		"\x01Lobby",
		"  Lob\x00by  ",
	}
	for _, raw := range invalid {
		if _, err := NormalizeName(raw); !errors.Is(err, ErrInvalidName) {
			t.Errorf("NormalizeName(%q) error = %v, want ErrInvalidName", raw, err)
		}
	}
}

func TestNameKey(t *testing.T) {
	tests := []struct {
		display string
		want    string
	}{
		{display: "Straße", want: "strasse"},
		{display: "STRASSE", want: "strasse"},
		{display: "\u1e9e", want: "ss"}, // capital sharp s
		{display: "Lobby", want: "lobby"},
		{display: "lOBBY", want: "lobby"},
		{display: "Lob\u00e9", want: "lob\u00e9"},
		{display: "LOB\u00c9", want: "lob\u00e9"},
		{display: "\u0130stanbul", want: "i\u0307stanbul"},
		{display: "i\u0307stanbul", want: "i\u0307stanbul"},
		{display: "Kiosk #1", want: "kiosk #1"},
		{display: "日本", want: "日本"},
		{display: "\uff41", want: "\uff41"},
	}
	for _, tt := range tests {
		if got := NameKey(tt.display); got != tt.want {
			t.Errorf("NameKey(%q) = %q, want %q", tt.display, got, tt.want)
		}
	}
}

// --- Registration: observable behavior over a real database ---

func TestRegisterNewKiosk(t *testing.T) {
	q, raw := testSQLDB(t)
	m := testModule(q)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "  Lobby  "); err != nil {
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
	nameKey, version := kioskDurable(t, raw, "10.0.0.1")
	if nameKey != "lobby" {
		t.Errorf("persisted name_key = %q, want %q", nameKey, "lobby")
	}
	if version != 1 {
		t.Errorf("persisted version = %d, want 1", version)
	}
}

func TestRegisterNormalizesName(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		raw     string
		want    string
		wantKey string
	}{
		{name: "surrounding ascii whitespace", ip: "10.0.0.1", raw: "  Lobby  ", want: "Lobby", wantKey: "lobby"},
		{name: "surrounding unicode whitespace", ip: "10.0.0.2", raw: "\u2003Lobby\u2003", want: "Lobby", wantKey: "lobby"},
		{name: "surrounding nbsp", ip: "10.0.0.3", raw: "\u00a0Lobby\u00a0", want: "Lobby", wantKey: "lobby"},
		{name: "nfc composes", ip: "10.0.0.4", raw: "Lobe\u0301", want: "Lob\u00e9", wantKey: "lob\u00e9"},
		{name: "nfc keeps composed form", ip: "10.0.0.5", raw: "Lob\u00e9", want: "Lob\u00e9", wantKey: "lob\u00e9"},
		{name: "trim before nfc", ip: "10.0.0.6", raw: "\u2003Lobe\u0301\u2003", want: "Lob\u00e9", wantKey: "lob\u00e9"},
		{name: "display casing preserved", ip: "10.0.0.7", raw: "lOBBY", want: "lOBBY", wantKey: "lobby"},
		{name: "width preserved", ip: "10.0.0.8", raw: "\uff41", want: "\uff41", wantKey: "\uff41"},
		{name: "non-latin preserved", ip: "10.0.0.9", raw: "日本", want: "日本", wantKey: "日本"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, raw := testSQLDB(t)
			m := testModule(q)
			ctx := context.Background()

			if err := m.Register(ctx, tt.ip, tt.raw); err != nil {
				t.Fatalf("Register(%q): %v", tt.raw, err)
			}
			k, err := m.GetKioskByIP(ctx, tt.ip)
			if err != nil {
				t.Fatalf("GetKioskByIP: %v", err)
			}
			if k.Name != tt.want {
				t.Errorf("stored name = %q, want %q", k.Name, tt.want)
			}
			nameKey, version := kioskDurable(t, raw, tt.ip)
			if nameKey != tt.wantKey {
				t.Errorf("persisted name_key = %q, want %q", nameKey, tt.wantKey)
			}
			if version != 1 {
				t.Errorf("persisted version = %d, want 1", version)
			}
		})
	}
}

func TestRegisterNameLengthBoundary(t *testing.T) {
	q, raw := testSQLDB(t)
	m := testModule(q)
	ctx := context.Background()

	accepted := []struct {
		name string
		ip   string
		raw  string
		want string
	}{
		{name: "single ascii code point", ip: "10.0.0.1", raw: "a", want: "a"},
		{name: "single non-ascii code point", ip: "10.0.0.2", raw: "ß", want: "ß"},
		{name: "sixty four ascii code points", ip: "10.0.0.3", raw: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
		{name: "sixty four non-ascii code points", ip: "10.0.0.4", raw: strings.Repeat("界", 64), want: strings.Repeat("界", 64)},
		{name: "sixty four code points after trim", ip: "10.0.0.5", raw: "  " + strings.Repeat("b", 64) + "\u2003", want: strings.Repeat("b", 64)},
		{name: "decomposed pairs compose under the limit", ip: "10.0.0.6", raw: strings.Repeat("e\u0301", 32), want: strings.Repeat("\u00e9", 32)},
	}
	for _, tt := range accepted {
		if err := m.Register(ctx, tt.ip, tt.raw); err != nil {
			t.Errorf("Register(%q) = %v, want success", tt.raw, err)
			continue
		}
		k, err := m.GetKioskByIP(ctx, tt.ip)
		if err != nil {
			t.Errorf("GetKioskByIP(%s): %v", tt.ip, err)
			continue
		}
		if k.Name != tt.want {
			t.Errorf("stored name = %q, want %q", k.Name, tt.want)
		}
		if _, version := kioskDurable(t, raw, tt.ip); version != 1 {
			t.Errorf("persisted version = %d, want 1", version)
		}
	}

	rejected := []struct {
		name string
		ip   string
		raw  string
	}{
		{name: "empty", ip: "10.0.1.1", raw: ""},
		{name: "whitespace only", ip: "10.0.1.2", raw: " \u2003\t "},
		{name: "sixty five ascii code points", ip: "10.0.1.3", raw: strings.Repeat("a", 65)},
		{name: "sixty five non-ascii code points", ip: "10.0.1.4", raw: strings.Repeat("界", 65)},
		{name: "sixty five code points after trim", ip: "10.0.1.5", raw: " " + strings.Repeat("a", 65) + " "},
	}
	for _, tt := range rejected {
		if err := m.Register(ctx, tt.ip, tt.raw); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Register(%q) error = %v, want ErrInvalidName", tt.raw, err)
		}
		if _, err := m.GetKioskByIP(ctx, tt.ip); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetKioskByIP(%s) after rejected register = %v, want ErrNotFound", tt.ip, err)
		}
	}
}

func TestRegisterRejectsControlCharacters(t *testing.T) {
	q, _ := testSQLDB(t)
	m := testModule(q)
	ctx := context.Background()

	controls := []struct {
		ip  string
		raw string
	}{
		{ip: "10.0.0.1", raw: "Lob\x00by"},
		{ip: "10.0.0.2", raw: "Lo\x1fbby"},
		{ip: "10.0.0.3", raw: "Lob\x7fby"},
		{ip: "10.0.0.4", raw: "Lob\u0085by"},
		{ip: "10.0.0.5", raw: "Lob\u009fby"},
		{ip: "10.0.0.6", raw: "Lo\tbby"},
		{ip: "10.0.0.7", raw: "Lo\nbby"},
		{ip: "10.0.0.8", raw: "\x01Lobby"},
		{ip: "10.0.0.9", raw: "  Lob\x00by  "},
	}
	for _, tt := range controls {
		if err := m.Register(ctx, tt.ip, tt.raw); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Register(%q) error = %v, want ErrInvalidName", tt.raw, err)
		}
		if _, err := m.GetKioskByIP(ctx, tt.ip); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetKioskByIP(%s) after rejected register = %v, want ErrNotFound", tt.ip, err)
		}
	}
}

func TestRegisterNameTakenUnderCaseFold(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		second string
	}{
		{name: "sharp s expands", first: "Straße", second: "STRASSE"},
		{name: "mixed casing", first: "Lobby", second: "lOBBY"},
		{name: "accented casing", first: "Lob\u00e9", second: "LOB\u00c9"},
		{name: "capital sharp s", first: "\u1e9e", second: "\u00df"},
		{name: "dotted capital i", first: "\u0130stanbul", second: "i\u0307stanbul"},
		{name: "normalization before keying", first: "  Lobby ", second: "lOBBY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, _ := testSQLDB(t)
			m := testModule(q)
			ctx := context.Background()

			if err := m.Register(ctx, "10.0.0.1", tt.first); err != nil {
				t.Fatalf("Register first %q: %v", tt.first, err)
			}
			held, err := m.GetKioskByIP(ctx, "10.0.0.1")
			if err != nil {
				t.Fatalf("GetKioskByIP after first register: %v", err)
			}
			if err := m.Register(ctx, "10.0.0.2", tt.second); !errors.Is(err, ErrNameTaken) {
				t.Fatalf("Register second %q = %v, want ErrNameTaken", tt.second, err)
			}
			if _, err := m.GetKioskByIP(ctx, "10.0.0.2"); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetKioskByIP(10.0.0.2) = %v, want ErrNotFound (second kiosk must not be registered)", err)
			}
			kept, err := m.GetKioskByIP(ctx, "10.0.0.1")
			if err != nil {
				t.Fatalf("GetKioskByIP after conflict: %v", err)
			}
			if kept.ID != held.ID || kept.Name != held.Name {
				t.Errorf("first kiosk changed across conflict: was {%s %s}, now {%s %s}", held.ID, held.Name, kept.ID, kept.Name)
			}
		})
	}
}

func TestRegisterNameTakenByOtherKiosk(t *testing.T) {
	q, _ := testSQLDB(t)
	m, buf := newTestModule(dbStore{Queries: q})
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
}

func TestRegisterAlreadyRegisteredNeverMutates(t *testing.T) {
	q, raw := testSQLDB(t)
	m := testModule(q)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Lobby"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	first, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP after first register: %v", err)
	}

	// Repeating the same registration is not an idempotent success: the
	// member kiosk must learn that its identity already exists so it can
	// recover by reconnecting and adopting the authoritative name.
	if err := m.Register(ctx, "10.0.0.1", "Lobby"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("repeat Register = %v, want ErrAlreadyRegistered", err)
	}
	// A different submitted name from the same IP must not rename the row.
	if err := m.Register(ctx, "10.0.0.1", "Renamed"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("Register under new name = %v, want ErrAlreadyRegistered", err)
	}

	second, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP after repeats: %v", err)
	}
	if second.ID != first.ID || second.Name != first.Name {
		t.Errorf("identity changed across repeat registration: was {%s %s}, now {%s %s}", first.ID, first.Name, second.ID, second.Name)
	}
	nameKey, version := kioskDurable(t, raw, "10.0.0.1")
	if nameKey != "lobby" {
		t.Errorf("persisted name_key = %q, want %q", nameKey, "lobby")
	}
	if version != 1 {
		t.Errorf("persisted version = %d, want 1 (row must not be mutated)", version)
	}
}

func TestRegisterExistingIPPrecedesNameConflict(t *testing.T) {
	q, _ := testSQLDB(t)
	m := testModule(q)
	ctx := context.Background()

	if err := m.Register(ctx, "10.0.0.1", "Branch2"); err != nil {
		t.Fatalf("Register Branch2: %v", err)
	}
	if err := m.Register(ctx, "10.0.0.2", "Lobby"); err != nil {
		t.Fatalf("Register Lobby: %v", err)
	}

	// 10.0.0.1 is already registered and submits a name held by 10.0.0.2:
	// the existing-IP conflict wins and neither row may change.
	if err := m.Register(ctx, "10.0.0.1", "Lobby"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("Register(10.0.0.1, Lobby) = %v, want ErrAlreadyRegistered", err)
	}
	first, err := m.GetKioskByIP(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("GetKioskByIP(10.0.0.1): %v", err)
	}
	if first.Name != "Branch2" {
		t.Errorf("10.0.0.1 name changed to %q, want Branch2", first.Name)
	}
	second, err := m.GetKioskByIP(ctx, "10.0.0.2")
	if err != nil {
		t.Fatalf("GetKioskByIP(10.0.0.2): %v", err)
	}
	if second.Name != "Lobby" {
		t.Errorf("10.0.0.2 name changed to %q, want Lobby", second.Name)
	}
}

// --- Registration: failure paths with a fake store ---

func TestRegisterInvalidNameRejectedBeforeStore(t *testing.T) {
	// The module owns the input contract: names outside the shared boundary
	// are rejected before any store access.
	s := &stubStore{}
	m, _ := newTestModule(s)

	invalid := []string{"", "   ", "\t\n", strings.Repeat("a", 65), "Lob\x00by"}
	for _, name := range invalid {
		if err := m.Register(context.Background(), "10.0.0.1", name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("Register(name=%q) = %v, want ErrInvalidName", name, err)
		}
	}
	if s.createCalled || s.ipChecks != 0 || s.keyChecks != 0 {
		t.Error("store was accessed before the name check")
	}
}

func TestRegisterNameKeyHeldSkipsCreate(t *testing.T) {
	// A name key held by a different IP is rejected by the pre-check alone;
	// the create is never attempted. The seam-level twin of
	// TestRegisterNameTakenByOtherKiosk.
	s := &stubStore{}
	s.heldKey = func(ip, key string) (bool, error) { return true, nil }
	m, buf := newTestModule(s)

	if err := m.Register(context.Background(), "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	if s.createCalled {
		t.Error("CreateKiosk was called for a name key already held by another kiosk")
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("name conflict was logged as an error: %s", buf.String())
	}
}

func TestRegisterNameTakenByRacingRegister(t *testing.T) {
	// The IP and name key are free at pre-check time, but a racing register
	// claims the name before this create lands; the failed insert is
	// classified as ErrNameTaken through the follow-up store answers, not a
	// driver error code.
	s := &stubStore{createErr: errors.New("write failed")}
	s.heldKey = func(ip, key string) (bool, error) {
		return s.keyChecks > 1, nil
	}
	m, buf := newTestModule(s)

	if err := m.Register(context.Background(), "10.0.0.2", "Lobby"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register = %v, want ErrNameTaken", err)
	}
	if !s.createCalled {
		t.Error("CreateKiosk was never attempted")
	}
	if s.createParams.Name != "Lobby" || s.createParams.NameKey != "lobby" {
		t.Errorf("CreateKiosk got name %q key %q, want normalized %q %q", s.createParams.Name, s.createParams.NameKey, "Lobby", "lobby")
	}
	if s.keyChecks != 2 {
		t.Errorf("NameKeyHeldByOther called %d times, want 2 (pre-check + race re-check)", s.keyChecks)
	}
	if s.ipChecks != 2 {
		t.Errorf("GetKioskByIP called %d times, want 2 (pre-check + race re-check)", s.ipChecks)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("name conflict was logged as an error: %s", buf.String())
	}
}

func TestRegisterAlreadyRegisteredByRacingRegister(t *testing.T) {
	// A racing register claims the IP between the pre-check and the create;
	// the failed insert is classified as ErrAlreadyRegistered (the existing
	// IP takes precedence over the submitted name).
	s := &stubStore{createErr: errors.New("write failed")}
	s.getKiosk = func(ip string) (database.Kiosk, error) {
		if s.createCalled {
			return database.Kiosk{ID: uuid.New(), IP: ip, Name: "Original", NameKey: "original", Version: 1}, nil
		}
		return database.Kiosk{}, sql.ErrNoRows
	}
	m, buf := newTestModule(s)

	if err := m.Register(context.Background(), "10.0.0.1", "Lobby"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("Register = %v, want ErrAlreadyRegistered", err)
	}
	if s.ipChecks != 2 {
		t.Errorf("GetKioskByIP called %d times, want 2 (pre-check + race re-check)", s.ipChecks)
	}
	if s.keyChecks != 1 {
		t.Errorf("NameKeyHeldByOther called %d times, want 1 (pre-check only; IP re-check short-circuits)", s.keyChecks)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("already-registered was logged as an error: %s", buf.String())
	}
}

func TestRegisterDBFailureLogged(t *testing.T) {
	// The create fails and the follow-up conflict checks also error; the
	// failure falls through to the logged wrapped-error path (not a
	// conflict classification).
	s := &stubStore{createErr: errors.New("disk I/O error")}
	s.heldKey = func(ip, key string) (bool, error) {
		if s.keyChecks > 1 {
			return false, errors.New("name lookup failed")
		}
		return false, nil
	}
	m, buf := newTestModule(s)

	err := m.Register(context.Background(), "10.0.0.1", "Lobby")
	if err == nil {
		t.Fatal("Register returned nil error, want wrapped error")
	}
	if errors.Is(err, ErrNameTaken) || errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("Register = %v, want wrapped non-conflict error", err)
	}
	if !strings.Contains(buf.String(), "register kiosk") {
		t.Errorf("log buffer does not contain %q: %s", "register kiosk", buf.String())
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("create failure was not logged at ERROR level: %s", buf.String())
	}
}

func TestRegisterCreateFailureIsNotNameConflict(t *testing.T) {
	// A non-constraint failure must never be reported as a name conflict or
	// an already-registered response: the re-checks come back negative and
	// the original error is wrapped.
	s := &stubStore{createErr: errors.New("disk I/O error")}
	m, buf := newTestModule(s)

	err := m.Register(context.Background(), "10.0.0.2", "Lobby")
	if err == nil {
		t.Fatal("Register returned nil error, want wrapped error")
	}
	if errors.Is(err, ErrNameTaken) || errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("Register = %v, want wrapped non-conflict error", err)
	}
	if !s.createCalled {
		t.Error("CreateKiosk was never attempted")
	}
	if !strings.Contains(buf.String(), "register kiosk") {
		t.Errorf("log buffer does not contain %q: %s", "register kiosk", buf.String())
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("create failure was not logged at ERROR level: %s", buf.String())
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

// --- Migration ---

func TestMigrateBackfillsExistingRows(t *testing.T) {
	db := newLegacyKioskDB(t)
	dir := migrationsDir(t)

	seeded := []struct {
		ip       string
		name     string
		wantName string
		wantKey  string
	}{
		{ip: "10.0.0.1", name: "  Lobby  ", wantName: "Lobby", wantKey: "lobby"},
		{ip: "10.0.0.2", name: "Lobe\u0301", wantName: "Lob\u00e9", wantKey: "lob\u00e9"},
		{ip: "10.0.0.3", name: "Straße", wantName: "Straße", wantKey: "strasse"},
		{ip: "10.0.0.4", name: "Kiosk #1", wantName: "Kiosk #1", wantKey: "kiosk #1"},
	}
	for _, s := range seeded {
		seedKiosk(t, db, s.ip, s.name)
	}
	if err := goose.Up(db, dir); err != nil {
		t.Fatalf("apply remaining migrations: %v", err)
	}

	// Before Migrate the structural rebuild leaves every key at the column
	// default: the backfill is Migrate's job.
	for _, s := range seeded {
		nameKey, version := kioskDurable(t, db, s.ip)
		if nameKey != "" || version != 1 {
			t.Errorf("pre-migrate row %s = key %q version %d, want '' and 1", s.ip, nameKey, version)
		}
	}

	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, s := range seeded {
		_, name, nameKey, version := kioskRow(t, db, s.ip)
		if name != s.wantName {
			t.Errorf("row %s name = %q, want %q", s.ip, name, s.wantName)
		}
		if nameKey != s.wantKey {
			t.Errorf("row %s name_key = %q, want %q", s.ip, nameKey, s.wantKey)
		}
		if version != 1 {
			t.Errorf("row %s version = %d, want 1", s.ip, version)
		}
	}

	// Idempotent: a second run changes nothing and reports no collision.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	for _, s := range seeded {
		_, name, nameKey, version := kioskRow(t, db, s.ip)
		if name != s.wantName || nameKey != s.wantKey || version != 1 {
			t.Errorf("row %s changed across second Migrate: {%q %q %d}", s.ip, name, nameKey, version)
		}
	}

	// The unique name-key index created by Migrate is enforced: a row that
	// repeats an existing key cannot be inserted.
	if _, err := db.Exec(`INSERT INTO kiosks (id, ip, name, name_key) VALUES (?, ?, ?, ?)`, uuid.NewString(), "10.0.0.99", "Other", "lobby"); err == nil {
		t.Error("insert with an existing name_key succeeded; unique name-key index missing")
	}
}

func TestMigrateCollisionAbortsPreservingRows(t *testing.T) {
	db := newLegacyKioskDB(t)
	dir := migrationsDir(t)

	seeded := []struct {
		ip   string
		name string
	}{
		{ip: "10.0.0.1", name: "Straße"},
		{ip: "10.0.0.2", name: "STRASSE"},
		{ip: "10.0.0.3", name: "Lobby"},
		{ip: "10.0.0.4", name: "lOBBY"},
	}
	ids := make(map[string]uuid.UUID, len(seeded))
	for _, s := range seeded {
		ids[s.ip] = seedKiosk(t, db, s.ip, s.name)
	}
	if err := goose.Up(db, dir); err != nil {
		t.Fatalf("apply remaining migrations: %v", err)
	}

	ctx := context.Background()
	err := Migrate(ctx, db)
	if err == nil {
		t.Fatal("Migrate returned nil, want an explicit collision report")
	}
	report := err.Error()
	// The report must surface every colliding display name, key, and row.
	for _, want := range []string{"Straße", "STRASSE", "Lobby", "lOBBY", "strasse", "lobby"} {
		if !strings.Contains(report, want) {
			t.Errorf("collision report does not mention %q: %s", want, report)
		}
	}
	for _, s := range seeded {
		if !strings.Contains(report, s.ip) {
			t.Errorf("collision report does not identify row ip %s: %s", s.ip, report)
		}
		if !strings.Contains(report, ids[s.ip].String()) {
			t.Errorf("collision report does not identify row id %s: %s", ids[s.ip], report)
		}
	}

	// No data loss and no partial backfill: every original row survives
	// with its pre-migration values and no key writes.
	for _, s := range seeded {
		id, name, nameKey, version := kioskRow(t, db, s.ip)
		if id != ids[s.ip].String() {
			t.Errorf("row %s id changed to %s", s.ip, id)
		}
		if name != s.name {
			t.Errorf("row %s name changed to %q, want %q", s.ip, name, s.name)
		}
		if nameKey != "" {
			t.Errorf("row %s name_key = %q, want '' (backfill must not run on collision)", s.ip, nameKey)
		}
		if version != 1 {
			t.Errorf("row %s version = %d, want 1", s.ip, version)
		}
	}
}
