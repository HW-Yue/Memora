//go:build cgo

// Package vecext compiles SQLite's vector index (sqlite-vec) into this binary.
//
// The C source is vendored instead of depending on the upstream Go bindings for
// one concrete reason: upstream's translation unit includes <sqlite3.h> at
// compile time, so whether a build succeeds depends on the host having a C
// SQLite development package, and the header it finds is whatever version that
// package happens to be. This binary links the SQLite amalgamation bundled with
// the driver, so an older host header would describe a different library than
// the one being linked.
//
// Vendoring keeps the header and the library the same version, and keeps a bare
// `go build` working with no system prerequisites — which matters because the
// vector path is required, not optional: vector recall is one of the four
// retrieval paths, and every build that can open a database must be able to
// answer a vector query.
//
// include/sqlite3.h is generated from the driver, never hand-edited. See
// generate.go, and regenerate with `go generate ./internal/sqlstore/vecext/`.
package vecext

/*
#cgo CFLAGS: -I${SRCDIR}/include -DSQLITE_CORE
#cgo linux LDFLAGS: -lm
#include "sqlite-vec.h"
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Auto registers the extension for every connection this process opens from now
// on. It is an SQLite auto-extension, so it has to run before the first
// connection: one opened earlier will never see it.
func Auto() {
	C.sqlite3_auto_extension((*[0]byte)(C.sqlite3_vec_init))
}

// SerializeFloat32 packs a vector the way vec0 stores and compares it. The
// byte order is fixed by the extension, not chosen here.
func SerializeFloat32(vector []float32) ([]byte, error) {
	buffer := new(bytes.Buffer)
	if err := binary.Write(buffer, binary.LittleEndian, vector); err != nil {
		return nil, fmt.Errorf("serialize vector: %w", err)
	}
	return buffer.Bytes(), nil
}
