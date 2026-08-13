// Package kiosks owns the kiosk directory: registration, identity lookup by
// IP, and live-kiosk listing.
package kiosks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
)

// ErrAlreadyRegistered is returned by Register when the source IP already
// holds a kiosk identity; the stored identity and name are never changed.
var ErrAlreadyRegistered = errors.New("kiosk already registered")

// ErrNameTaken is returned by Register when the normalized name key is held
// by a different kiosk.
var ErrNameTaken = errors.New("kiosk name already in use")

// ErrNotFound is returned by GetKioskByIP when the IP is not registered.
var ErrNotFound = errors.New("kiosk not found")

// Kiosk identifies a registered kiosk in the directory.
type Kiosk struct {
	ID   uuid.UUID
	IP   string
	Name string
}

// store is the persistence seam for the kiosk directory. dbStore adapts
// *database.Queries to it in production; tests inject fakes.
type store interface {
	CreateKiosk(ctx context.Context, arg database.CreateKioskParams) (database.Kiosk, error)
	GetKioskByIP(ctx context.Context, ip string) (database.Kiosk, error)
	NameKeyHeldByOther(ctx context.Context, ip, nameKey string) (bool, error)
	ListKiosksByIDs(ctx context.Context, ids []uuid.UUID) ([]database.Kiosk, error)
}

// dbStore adapts *database.Queries to the domain-shaped store seam. sqlc
// generates a params struct for the two-argument NameKeyHeldByOther query,
// so the adapter bridges it into the seam's flat signature; the remaining
// seam methods are promoted from the embedded *database.Queries.
type dbStore struct {
	*database.Queries
}

func (d dbStore) NameKeyHeldByOther(ctx context.Context, ip, nameKey string) (bool, error) {
	return d.Queries.NameKeyHeldByOther(ctx, database.NameKeyHeldByOtherParams{IP: ip, NameKey: nameKey})
}

// Module is the kiosk directory. It owns registration and identity
// resolution so HTTP handlers stay thin.
type Module struct {
	store  store
	logger *slog.Logger
}

// New returns a Module that persists kiosks through db and logs through
// logger.
func New(db *database.Queries, logger *slog.Logger) *Module {
	return newModule(dbStore{Queries: db}, logger)
}

// newModule builds a Module around any store; tests use it with a fake.
func newModule(s store, logger *slog.Logger) *Module {
	return &Module{store: s, logger: logger}
}

// Register creates a kiosk identity for ip from the normalized form of
// rawName. It returns ErrInvalidName when rawName falls outside the shared
// name boundary, ErrAlreadyRegistered when ip already holds an identity
// (checked before any name conflict; the stored identity and name are never
// mutated), and ErrNameTaken when the normalized name key is held by a
// different kiosk. On success the stored identity is logged under "kiosk
// registered".
func (m *Module) Register(ctx context.Context, ip, rawName string) error {
	display, err := NormalizeName(rawName)
	if err != nil {
		return err
	}
	nameKey := NameKey(display)

	// An existing IP takes precedence over any submitted-name conflict and
	// must never mutate the stored row.
	if _, err := m.store.GetKioskByIP(ctx, ip); err == nil {
		return ErrAlreadyRegistered
	} else if !errors.Is(err, sql.ErrNoRows) {
		return m.logRegisterError(err, ip, display)
	}

	held, err := m.store.NameKeyHeldByOther(ctx, ip, nameKey)
	if err != nil {
		return m.logRegisterError(err, ip, display)
	}
	if held {
		return ErrNameTaken
	}

	id := uuid.New()
	row, err := m.store.CreateKiosk(ctx, database.CreateKioskParams{ID: id, IP: ip, Name: display, NameKey: nameKey})
	if err != nil {
		// A racing register may have claimed the IP or the name key between
		// the pre-checks and the write; classify the failure through the
		// store's domain answers, IP first, instead of driver error codes.
		if _, lookupErr := m.store.GetKioskByIP(ctx, ip); lookupErr == nil {
			return ErrAlreadyRegistered
		}
		if held, lookupErr := m.store.NameKeyHeldByOther(ctx, ip, nameKey); lookupErr == nil && held {
			return ErrNameTaken
		}
		return m.logRegisterError(err, ip, display)
	}
	m.logger.Info("kiosk registered", "kiosk_id", row.ID, "name", row.Name, "ip", row.IP)
	return nil
}

// logRegisterError records a register failure with the kiosk context and
// wraps the underlying error for the caller.
func (m *Module) logRegisterError(err error, ip, name string) error {
	m.logger.Error("register kiosk", "error", err, "name", name, "ip", ip)
	return fmt.Errorf("register kiosk: %w", err)
}

// GetKioskByIP looks up the kiosk registered under ip. An unregistered IP
// yields ErrNotFound.
func (m *Module) GetKioskByIP(ctx context.Context, ip string) (Kiosk, error) {
	row, err := m.store.GetKioskByIP(ctx, ip)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Kiosk{}, ErrNotFound
		}
		m.logger.Error("resolve kiosk identity", "error", err, "ip", ip)
		return Kiosk{}, fmt.Errorf("resolve kiosk identity: %w", err)
	}
	return Kiosk{ID: row.ID, IP: row.IP, Name: row.Name}, nil
}

// ListLive returns the registered kiosks whose IDs appear in connected,
// ordered by name. An empty connected slice returns an empty, non-nil slice
// without querying the store.
func (m *Module) ListLive(ctx context.Context, connected []uuid.UUID) ([]Kiosk, error) {
	if len(connected) == 0 {
		return []Kiosk{}, nil
	}
	rows, err := m.store.ListKiosksByIDs(ctx, connected)
	if err != nil {
		m.logger.Error("list live kiosks", "error", err, "count", len(connected))
		return nil, fmt.Errorf("list live kiosks: %w", err)
	}
	kiosks := make([]Kiosk, 0, len(rows))
	for _, row := range rows {
		kiosks = append(kiosks, Kiosk{ID: row.ID, IP: row.IP, Name: row.Name})
	}
	return kiosks, nil
}
