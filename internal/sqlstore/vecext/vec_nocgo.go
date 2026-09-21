//go:build !cgo

package vecext

import "errors"

// Without cgo there is no usable SQLite driver either, so a build in this shape
// only exists for vet and lint. It reports the missing module rather than
// pretending the vector path is there.
func Auto() {}

func SerializeFloat32([]float32) ([]byte, error) {
	return nil, errors.New("sqlite-vec is a C extension and needs cgo")
}
