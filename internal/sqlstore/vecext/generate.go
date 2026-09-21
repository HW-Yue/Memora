package vecext

// The header sqlite-vec includes must be the one that matches the SQLite
// library this binary links, so it is copied from the driver rather than taken
// from whatever the host has installed. Regenerate after bumping
// github.com/mattn/go-sqlite3; CI runs this and fails if the copy is stale,
// which is the only thing keeping the two in step.
//
//go:generate sh -c "cp \"$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)/sqlite3-binding.h\" include/sqlite3.h"
