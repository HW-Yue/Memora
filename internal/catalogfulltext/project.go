package catalogfulltext

import (
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
)

var ErrInvalid = errors.New("Catalog fulltext projection is invalid")

func Project([]catalog.Database) ([]fulltext.Document, error) {
	return nil, fmt.Errorf("%w: projection is not implemented", ErrInvalid)
}

func Tombstone(fulltext.Object) (fulltext.Document, error) {
	return fulltext.Document{}, fmt.Errorf("%w: tombstone is not implemented", ErrInvalid)
}
