package kiosks

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Migrate normalizes existing kiosk rows onto the shared name boundary and
// backfills their full-Unicode case-folded name keys, then creates the
// unique name-key index. It runs in a single transaction: every key
// collision is detected and reported before any row is written, and a
// collision (or any other failure) leaves all rows untouched. Rows that
// already hold their normalized display name and key are skipped, so Migrate
// is idempotent. Call it after goose migrations, before serving.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin kiosk name migration: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id, ip, name, name_key FROM kiosks`)
	if err != nil {
		return fmt.Errorf("read kiosk rows: %w", err)
	}
	defer rows.Close()

	type kioskRow struct {
		id      string
		ip      string
		name    string
		nameKey string
	}
	var kiosks []kioskRow
	for rows.Next() {
		var r kioskRow
		if err := rows.Scan(&r.id, &r.ip, &r.name, &r.nameKey); err != nil {
			return fmt.Errorf("read kiosk row: %w", err)
		}
		kiosks = append(kiosks, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read kiosk rows: %w", err)
	}

	// Compute the target display name and name key for every row before
	// writing anything, and group rows by their target key so collisions
	// surface as a complete report instead of a partial backfill.
	type target struct {
		display string
		nameKey string
	}
	targets := make([]target, len(kiosks))
	keyHolders := make(map[string][]int)
	for i, k := range kiosks {
		display := mechanicalDisplay(k.name)
		nameKey := NameKey(display)
		targets[i] = target{display: display, nameKey: nameKey}
		keyHolders[nameKey] = append(keyHolders[nameKey], i)
	}

	keys := make([]string, 0, len(keyHolders))
	for key := range keyHolders {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var collisions []string
	for _, key := range keys {
		holders := keyHolders[key]
		if len(holders) < 2 {
			continue
		}
		parts := make([]string, 0, len(holders))
		for _, i := range holders {
			k := kiosks[i]
			parts = append(parts, fmt.Sprintf("%q (id %s, ip %s)", k.name, k.id, k.ip))
		}
		collisions = append(collisions, fmt.Sprintf("%d kiosks share name key %q: %s", len(holders), key, strings.Join(parts, ", ")))
	}
	if len(collisions) > 0 {
		return fmt.Errorf("kiosk name migration aborted: %s", strings.Join(collisions, "; "))
	}

	for i, k := range kiosks {
		t := targets[i]
		if k.name == t.display && k.nameKey == t.nameKey {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kiosks SET name = ?, name_key = ? WHERE id = ?`, t.display, t.nameKey, k.id); err != nil {
			return fmt.Errorf("backfill kiosk %s: %w", k.id, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS kiosks_name_key_idx ON kiosks (name_key)`); err != nil {
		return fmt.Errorf("create kiosk name-key index: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit kiosk name migration: %w", err)
	}
	return nil
}
