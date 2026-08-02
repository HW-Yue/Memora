package fulltextindex

import (
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

var (
	ErrInvalid     = errors.New("Fulltext Index input is invalid")
	ErrConflict    = errors.New("Fulltext Index revision conflicts")
	ErrCorrupt     = errors.New("Fulltext Index is corrupt")
	errUnsupported = errors.New("persistent Fulltext Index is not implemented")
)

type Receipt struct {
	Changed bool
	Replay  bool
	Digest  string
	Added   int
	Removed int
	State   treecontrol.State
	WAL     wal.Receipt
}

type Index struct {
	runtime *treecommit.Runtime
}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

func (index *Index) Bootstrap(uint64, []fulltext.Document) (Receipt, error) {
	return Receipt{}, errUnsupported
}

func (index *Index) Replace(uint64, fulltext.Document) (Receipt, error) {
	return Receipt{}, errUnsupported
}

func (index *Index) Postings(string) ([]fulltext.Posting, error) {
	return nil, errUnsupported
}

func (index *Index) AllPostings() ([]fulltext.Posting, error) {
	return nil, errUnsupported
}
