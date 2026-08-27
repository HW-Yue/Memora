// Package rowid owns the shape of a Row ID.
//
// A Row ID counts up within its Table: the Table's Tree holds the counter, and
// an insert takes the next number in the same commit that writes the Row.
// See docs/storage/per-table-tree-v1.md §3 and the write model §2.4.
//
// The ID stays globally unique even though the sequence is per-Table, by
// carrying the Table's space in front of the number. That is deliberate: the
// native store keys a Row record by the Row ID alone, so two Tables both
// starting at 1 would collide there. Making that record identity Table-scoped
// instead is a migration of the source-of-truth file, which has no
// copy-on-write rebuild path — it needs its own design, not a side effect of
// this one. The space is derived from the Table, so putting it in the ID
// records nothing that was not already known.
package rowid

import (
	"fmt"
	"strconv"
	"strings"
)

// Prefix marks a Row ID, so an ID is self-describing in a log line or an error
// message where a bare number would not be.
const Prefix = "row_"

const spaceDigits = 16

// Format renders the nth Row of the Table occupying spaceID.
func Format(spaceID, number uint64) (string, error) {
	if number == 0 {
		return "", fmt.Errorf("Row ID number must be positive")
	}
	if spaceID == 0 {
		return "", fmt.Errorf("Row ID needs the Table's space")
	}
	return fmt.Sprintf("%s%0*x_%d", Prefix, spaceDigits, spaceID, number), nil
}

// Number reads the counter value out of a Row ID, and reports whether the ID is
// a counted one at all.
//
// Databases written before Row IDs counted carry a UUID after the prefix. Those
// are still valid IDs and still readable; they have no number, so they never
// constrain a counter.
func Number(id string) (uint64, bool) {
	if !strings.HasPrefix(id, Prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(id, Prefix)
	separator := strings.IndexByte(rest, '_')
	if separator != spaceDigits {
		return 0, false
	}
	if _, err := strconv.ParseUint(rest[:spaceDigits], 16, 64); err != nil {
		return 0, false
	}
	digits := rest[separator+1:]
	if digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	number, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || number == 0 {
		return 0, false
	}
	return number, true
}

// HighWater is the counter a Table needs so that no future ID collides with the
// IDs it already holds.
func HighWater(ids []string) uint64 {
	highest := uint64(0)
	for _, id := range ids {
		if number, ok := Number(id); ok && number > highest {
			highest = number
		}
	}
	return highest
}
