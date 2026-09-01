package nativechange

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/HW-Yue/Memora/internal/change"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const (
	recordSchemaVersion = 1
	maxEnvelopeBytes    = 1 << 20
)

var (
	ErrInvalid = errors.New("native committed change request is invalid")
	ErrCorrupt = errors.New("native committed change record is corrupt")
)

type Repository struct{ file *nativestore.File }

func New(file *nativestore.File) *Repository { return &Repository{file: file} }

func Stage(transaction *nativestore.Transaction, value change.Envelope) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction", ErrInvalid)
	}
	if err := value.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > maxEnvelopeBytes {
		return fmt.Errorf("%w: envelope exceeds byte budget", ErrInvalid)
	}
	return transaction.Put(
		nativestore.ObjectKindCommittedChange, recordSchemaVersion,
		recordID(value.CommitSequence), encoded,
	)
}

func (repository *Repository) Get(sequence uint64) (change.Envelope, error) {
	if repository == nil || repository.file == nil || sequence == 0 {
		return change.Envelope{}, fmt.Errorf("%w: sequence", ErrInvalid)
	}
	payload, err := repository.file.Get(nativestore.ObjectKindCommittedChange, recordID(sequence))
	if err != nil {
		return change.Envelope{}, err
	}
	return decode(payload, sequence)
}

func (repository *Repository) ListAfter(after uint64, limit int) ([]change.Envelope, bool, error) {
	if repository == nil || repository.file == nil || limit < 1 || limit > 1000 {
		return nil, false, fmt.Errorf("%w: change cursor", ErrInvalid)
	}
	ids, err := repository.file.IDs(nativestore.ObjectKindCommittedChange)
	if err != nil {
		return nil, false, err
	}
	values := make([]change.Envelope, 0, min(limit+1, len(ids)))
	for _, id := range ids {
		sequence, err := parseRecordID(id)
		if err != nil {
			return nil, false, err
		}
		if sequence <= after {
			continue
		}
		value, err := repository.Get(sequence)
		if err != nil {
			return nil, false, err
		}
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left].CommitSequence < values[right].CommitSequence
	})
	if len(values) > limit {
		return values[:limit], true, nil
	}
	return values, false, nil
}

// Exists reports whether the log holds this commit sequence. One point read.
func (repository *Repository) Exists(sequence uint64) (bool, error) {
	if repository == nil || repository.file == nil || sequence == 0 {
		return false, fmt.Errorf("%w: sequence", ErrInvalid)
	}
	_, err := repository.file.Get(nativestore.ObjectKindCommittedChange, recordID(sequence))
	if errors.Is(err, nativestore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// NextSequence reports the first unused commit sequence above floor.
//
// Sequences are handed out one after another with no gaps, so the end of the
// log is found by probing forward from a floor the caller already knows is
// taken — the change index Tree's high-water, which is durable and can never
// lead the log. That is a bounded number of point reads, normally one.
//
// It used to list every committed change record and decode each one. Every
// write takes a sequence, so that made the cost of one write a pass over every
// change the Database had ever committed — the failure that gets worse the
// longer the Database is used. Passing floor 0 still works and still costs
// that, which is why callers with a Tree pass its high-water instead.
func (repository *Repository) NextSequence(floor uint64) (uint64, error) {
	if repository == nil || repository.file == nil {
		return 0, fmt.Errorf("%w: repository", ErrInvalid)
	}
	for sequence := floor + 1; sequence != 0; sequence++ {
		found, err := repository.Exists(sequence)
		if err != nil {
			return 0, err
		}
		if !found {
			return sequence, nil
		}
		if sequence == math.MaxUint64 {
			return 0, fmt.Errorf("%w: commit sequence exhausted", ErrCorrupt)
		}
	}
	return 0, fmt.Errorf("%w: commit sequence exhausted", ErrCorrupt)
}

func decode(payload []byte, sequence uint64) (change.Envelope, error) {
	if len(payload) == 0 || len(payload) > maxEnvelopeBytes {
		return change.Envelope{}, ErrCorrupt
	}
	var value change.Envelope
	if err := json.Unmarshal(payload, &value); err != nil || value.CommitSequence != sequence || value.Validate() != nil {
		return change.Envelope{}, ErrCorrupt
	}
	canonical, err := json.Marshal(value)
	if err != nil || string(canonical) != string(payload) {
		return change.Envelope{}, ErrCorrupt
	}
	return value, nil
}

func recordID(sequence uint64) string { return fmt.Sprintf("change_%020d", sequence) }

func parseRecordID(id string) (uint64, error) {
	var sequence uint64
	if _, err := fmt.Sscanf(id, "change_%020d", &sequence); err != nil || sequence == 0 || recordID(sequence) != id {
		return 0, ErrCorrupt
	}
	return sequence, nil
}
