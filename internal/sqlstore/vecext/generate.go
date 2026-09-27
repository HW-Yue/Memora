package vecext

// The header sqlite-vec includes must be the one that matches the SQLite
// library this binary links, so it is copied from the driver rather than taken
// from whatever the host has installed. Regenerate after bumping
// github.com/mattn/go-sqlite3; CI runs this and fails if the copy is stale,
// which is the only thing keeping the two in step.
//
// `go mod download` is not optional. That module is imported behind a build
// tag, so nothing in this package's own build needs its files; `go list -m`
// then answers with an empty {{.Dir}} for a module the local cache has not
// extracted yet, and the copy below would silently aim at "/sqlite3-binding.h".
// A developer machine rarely notices — its cache is warm from earlier builds —
// but the format stage runs before anything else on a fresh runner, which is
// how it failed on 2026-09-26. Downloading first is what makes {{.Dir}} real.
//
//go:generate sh -c "go mod download github.com/mattn/go-sqlite3 && cp \"$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)/sqlite3-binding.h\" include/sqlite3.h"
