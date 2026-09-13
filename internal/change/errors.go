package change

import "errors"

// ErrNotFound is what a committed-change reader reports when a change does not
// exist in the requested scope.
var ErrNotFound = errors.New("committed change was not found")
