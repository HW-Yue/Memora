package rowfulltext

import (
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/row"
)

var (
	ErrInvalid     = errors.New("Row fulltext projection is invalid")
	errUnsupported = errors.New("Row fulltext projection is not implemented")
)

func Project(catalog.Table, row.Row) (fulltext.Document, error) {
	return fulltext.Document{}, fmt.Errorf("%w: %v", ErrInvalid, errUnsupported)
}
