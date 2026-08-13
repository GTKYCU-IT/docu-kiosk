// Name normalization is the shared value boundary for the kiosk directory:
// registration and future directory mutations (issue #46) both route names
// through NormalizeName and NameKey so display and uniqueness semantics can
// never drift.
package kiosks

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// ErrInvalidName is returned when a name is empty after trimming, exceeds
// the 1-64 Unicode code point limit, or contains a control character.
var ErrInvalidName = errors.New("kiosk name is invalid")

// maxNameRunes is the inclusive upper bound on display-name length in
// Unicode code points.
const maxNameRunes = 64

// mechanicalDisplay trims surrounding Unicode whitespace and NFC-normalizes
// raw without applying the value boundary. NormalizeName validates the
// result; Migrate backfills legacy rows that predate the boundary.
func mechanicalDisplay(raw string) string {
	display := strings.TrimFunc(raw, unicode.IsSpace)
	return norm.NFC.String(display)
}

// NormalizeName returns the display form of raw: surrounding Unicode
// whitespace trimmed, NFC-normalized, display casing preserved. It returns
// ErrInvalidName when the result is empty, longer than maxNameRunes code
// points, or contains a control character.
func NormalizeName(raw string) (string, error) {
	display := mechanicalDisplay(raw)
	if display == "" || utf8.RuneCountInString(display) > maxNameRunes {
		return "", ErrInvalidName
	}
	for _, r := range display {
		if unicode.IsControl(r) {
			return "", ErrInvalidName
		}
	}
	return display, nil
}

// NameKey derives the global-uniqueness key for a normalized display name:
// the full Unicode case fold (cases.Fold) of the display form. Names that
// differ only in case share a key and cannot coexist in the directory.
func NameKey(display string) string {
	return cases.Fold().String(display)
}
