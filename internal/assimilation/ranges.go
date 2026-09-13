package assimilation

import "sort"

type interval struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

func mergeCoverage(values []interval, added interval) ([]interval, bool) {
	before := coveredLength(values)
	merged := append(append([]interval{}, values...), added)
	sort.Slice(merged, func(left, right int) bool {
		if merged[left].Start != merged[right].Start {
			return merged[left].Start < merged[right].Start
		}
		return merged[left].End < merged[right].End
	})
	compacted := make([]interval, 0, len(merged))
	for _, value := range merged {
		if len(compacted) == 0 || value.Start > compacted[len(compacted)-1].End {
			compacted = append(compacted, value)
			continue
		}
		if value.End > compacted[len(compacted)-1].End {
			compacted[len(compacted)-1].End = value.End
		}
	}
	return compacted, coveredLength(compacted) > before
}

func coveredLength(values []interval) uint64 {
	var total uint64
	for _, value := range values {
		total += value.End - value.Start
	}
	return total
}

func missingRanges(unit Unit, values []interval) []UnreadRange {
	if unit.Optional || unit.Extent == 0 {
		return nil
	}
	missing := make([]UnreadRange, 0, len(values)+1)
	cursor := uint64(0)
	for _, value := range values {
		if cursor < value.Start {
			missing = append(missing, UnreadRange{UnitID: unit.ID, Start: cursor, End: value.Start})
		}
		if value.End > cursor {
			cursor = value.End
		}
	}
	if cursor < unit.Extent {
		missing = append(missing, UnreadRange{UnitID: unit.ID, Start: cursor, End: unit.Extent})
	}
	return missing
}
