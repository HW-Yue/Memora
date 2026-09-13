package change

import (
	"context"
	"errors"
)

// ErrNotFound is what a committed-change reader reports when a change does not
// exist in the requested scope.
var ErrNotFound = errors.New("committed change was not found")

type metadataKey struct{}

// WithMetadata attaches the attribution of the statement being executed, so a
// backend can seal its change envelope with who did it and why.
func WithMetadata(ctx context.Context, metadata Metadata) context.Context {
	return context.WithValue(ctx, metadataKey{}, metadata)
}

// MetadataFrom returns the attribution attached by WithMetadata.
func MetadataFrom(ctx context.Context) (Metadata, bool) {
	if ctx == nil {
		return Metadata{}, false
	}
	metadata, ok := ctx.Value(metadataKey{}).(Metadata)
	return metadata, ok && metadata.Actor != ""
}
