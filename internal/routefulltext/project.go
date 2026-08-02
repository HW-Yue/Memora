package routefulltext

import (
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/router"
)

var ErrInvalid = errors.New("Route fulltext projection is invalid")

func Project([]router.Node) ([]fulltext.Document, error) {
	return nil, fmt.Errorf("%w: projection is not implemented", ErrInvalid)
}

func Tombstone(fulltext.Object) (fulltext.Document, error) {
	return fulltext.Document{}, fmt.Errorf("%w: tombstone is not implemented", ErrInvalid)
}
