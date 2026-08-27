package rowid

import "testing"

func TestNumberReadsCountedIDsAndIgnoresLegacyOnes(t *testing.T) {
	t.Parallel()

	for id, want := range map[string]uint64{
		"row_9c8090b3777a9449_1":       1,
		"row_9c8090b3777a9449_42":      42,
		"row_0000000000000001_1000000": 1000000,
	} {
		if number, ok := Number(id); !ok || number != want {
			t.Fatalf("Number(%q) = %d, %v; want %d", id, number, ok, want)
		}
	}
	// A UUID ID from before Row IDs counted, and shapes that would let two
	// different strings claim the same number.
	for _, id := range []string{
		"row_0e01c78e-2e51-4873-9c93-a0e0ecf80a00",
		"row_", "row_1", "row_9c8090b3777a9449_0", "row_9c8090b3777a9449_007",
		"row_9c8090b3777a9449_-1", "row_zzzzzzzzzzzzzzzz_1", "row_9c8090b3777a944_1",
		"note_9c8090b3777a9449_1", "1",
	} {
		if number, ok := Number(id); ok {
			t.Fatalf("Number(%q) = %d, want not counted", id, number)
		}
	}
}

func TestHighWaterIgnoresWhatItCannotCount(t *testing.T) {
	t.Parallel()

	got := HighWater([]string{
		"row_9c8090b3777a9449_3", "row_0e01c78e-2e51-4873-9c93-a0e0ecf80a00",
		"row_9c8090b3777a9449_11", "row_9c8090b3777a9449_7",
	})
	if got != 11 {
		t.Fatalf("HighWater() = %d, want 11", got)
	}
	if got := HighWater(nil); got != 0 {
		t.Fatalf("HighWater(nil) = %d, want 0", got)
	}
}

func TestFormatRoundTrips(t *testing.T) {
	t.Parallel()

	for _, number := range []uint64{1, 2, 99, 1 << 40} {
		id, err := Format(0x9c8090b3777a9449, number)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := Number(id); !ok || got != number {
			t.Fatalf("round trip %d -> %q -> %d, %v", number, id, got, ok)
		}
	}
	if _, err := Format(0x9c8090b3777a9449, 0); err == nil {
		t.Fatal("Format(number 0) must fail: a Table's first Row is 1")
	}
	if _, err := Format(0, 1); err == nil {
		t.Fatal("Format without a Table space must fail")
	}
}
