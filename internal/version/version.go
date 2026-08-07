// Package version carries the broker build identity. Version and Commit are
// set at build time via -ldflags; local dev builds fall back to "dev".
package version

var (
	// Version is the human-readable build version (e.g. "v2.2.8").
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "dev"
)
