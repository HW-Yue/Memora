package nativerow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

const maxReadViewOverlay = 1000

type SnapshotLocatorPage struct {
	Locators       []rowversionindex.Locator
	HasMore        bool
	NextAfterRowID string
}

type ReadView struct {
	mu       sync.RWMutex
	reader   *IndexedReader
	sequence uint64
	overlay  map[string]row.Row
	closed   bool
}

func (reader *IndexedReader) BeginReadView(ctx context.Context) (*ReadView, error) {
	sequence, err := reader.Capture(ctx)
	if err != nil {
		return nil, err
	}
	return &ReadView{
		reader: reader, sequence: sequence, overlay: make(map[string]row.Row),
	}, nil
}

func (view *ReadView) Sequence() uint64 {
	if view == nil {
		return 0
	}
	view.mu.RLock()
	defer view.mu.RUnlock()
	return view.sequence
}

func (view *ReadView) Stage(value row.Row) error {
	if view == nil {
		return fmt.Errorf("%w: Read View", ErrInvalid)
	}
	if err := validateOverlayRow(value); err != nil {
		return err
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.closed {
		return fmt.Errorf("%w: Read View is closed", ErrInvalid)
	}
	if _, exists := view.overlay[value.ID]; !exists && len(view.overlay) == maxReadViewOverlay {
		return fmt.Errorf("%w: Read View overlay exceeds %d Rows", ErrInvalid, maxReadViewOverlay)
	}
	view.overlay[value.ID] = cloneOverlayRow(value)
	return nil
}

func (view *ReadView) Get(ctx context.Context, table catalog.Table, rowID string) (row.Row, error) {
	if err := validateIndexedTable(ctx, table); err != nil {
		return row.Row{}, err
	}
	value, found, sequence, err := view.overlayRow(rowID)
	if err != nil {
		return row.Row{}, err
	}
	if found {
		if value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
			return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), nil)
		}
		if value.State == row.StateDeleted || value.State == row.StateSuperseded {
			return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), nil)
		}
		return project(table, value), nil
	}
	return view.reader.Get(ctx, table, rowID, sequence)
}

func (view *ReadView) PageLocators(
	ctx context.Context,
	table catalog.Table,
	afterRowID string,
	limit uint64,
) (SnapshotLocatorPage, error) {
	if err := validateIndexedTable(ctx, table); err != nil {
		return SnapshotLocatorPage{}, err
	}
	if view == nil || limit == 0 || limit > 1000 {
		return SnapshotLocatorPage{}, fmt.Errorf("%w: snapshot Page request", ErrInvalid)
	}
	overlay, sequence, err := view.overlayRows(table.DatabaseID, table.ID, afterRowID)
	if err != nil {
		return SnapshotLocatorPage{}, err
	}
	base, err := view.reader.current.Page(table.ID, afterRowID, limit)
	if err != nil {
		return SnapshotLocatorPage{}, err
	}
	baseByID := make(map[string]currentrowindex.Locator, len(base.Locators))
	candidates := make(map[string]struct{}, len(base.Locators)+len(overlay))
	for _, locator := range base.Locators {
		baseByID[locator.RowID] = locator
		candidates[locator.RowID] = struct{}{}
	}
	for rowID := range overlay {
		candidates[rowID] = struct{}{}
	}
	ids := make([]string, 0, len(candidates))
	orderKeys := make(map[string][]byte, len(candidates))
	for rowID := range candidates {
		key, err := currentrowindex.RowOrderKey(rowID)
		if err != nil {
			return SnapshotLocatorPage{}, err
		}
		orderKeys[rowID] = key
		ids = append(ids, rowID)
	}
	sort.Slice(ids, func(left, right int) bool {
		return bytes.Compare(orderKeys[ids[left]], orderKeys[ids[right]]) < 0
	})
	scanned := min(len(ids), int(limit))
	page := SnapshotLocatorPage{Locators: make([]rowversionindex.Locator, 0, scanned)}
	for _, rowID := range ids[:scanned] {
		if value, own := overlay[rowID]; own {
			page.Locators = append(page.Locators, overlayLocator(value))
			continue
		}
		current := baseByID[rowID]
		visible, err := view.reader.visibleLocator(table, current, sequence)
		if errors.Is(err, rowversionindex.ErrNotFound) {
			continue
		}
		if err != nil {
			return SnapshotLocatorPage{}, err
		}
		page.Locators = append(page.Locators, visible)
	}
	page.HasMore = scanned < len(ids) || base.HasMore
	if page.HasMore && scanned != 0 {
		page.NextAfterRowID = ids[scanned-1]
	}
	return page, nil
}

// Discard closes the view and drops every private Row without publishing it.
func (view *ReadView) Discard() error {
	if view == nil {
		return nil
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.closed {
		return nil
	}
	view.closed = true
	view.overlay = nil
	return nil
}

func (view *ReadView) overlayRow(rowID string) (row.Row, bool, uint64, error) {
	if view == nil || view.reader == nil {
		return row.Row{}, false, 0, fmt.Errorf("%w: Read View", ErrInvalid)
	}
	view.mu.RLock()
	defer view.mu.RUnlock()
	if view.closed {
		return row.Row{}, false, 0, fmt.Errorf("%w: Read View is closed", ErrInvalid)
	}
	value, found := view.overlay[rowID]
	return cloneOverlayRow(value), found, view.sequence, nil
}

func (view *ReadView) overlayRows(databaseID, tableID, afterRowID string) (map[string]row.Row, uint64, error) {
	if view == nil || view.reader == nil {
		return nil, 0, fmt.Errorf("%w: Read View", ErrInvalid)
	}
	view.mu.RLock()
	defer view.mu.RUnlock()
	if view.closed {
		return nil, 0, fmt.Errorf("%w: Read View is closed", ErrInvalid)
	}
	var afterKey []byte
	var err error
	if afterRowID != "" {
		afterKey, err = currentrowindex.RowOrderKey(afterRowID)
		if err != nil {
			return nil, 0, err
		}
	}
	result := make(map[string]row.Row)
	for rowID, value := range view.overlay {
		rowKey, keyErr := currentrowindex.RowOrderKey(rowID)
		if keyErr != nil {
			return nil, 0, keyErr
		}
		if value.DatabaseID == databaseID && value.TableID == tableID &&
			(afterRowID == "" || bytes.Compare(rowKey, afterKey) > 0) {
			result[rowID] = cloneOverlayRow(value)
		}
	}
	return result, view.sequence, nil
}

func validateOverlayRow(value row.Row) error {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.ID) != value.ID ||
		strings.TrimSpace(value.DatabaseID) == "" || strings.TrimSpace(value.TableID) == "" ||
		value.SchemaVersion == 0 || value.Revision == 0 ||
		(value.State != row.StateLive && value.State != row.StateDeleted && value.State != row.StateSuperseded) {
		return fmt.Errorf("%w: staged Row identity, revision, or state", ErrInvalid)
	}
	return nil
}

func cloneOverlayRow(value row.Row) row.Row {
	result := value
	result.Values = make(map[string]any, len(value.Values))
	for key, item := range value.Values {
		switch typed := item.(type) {
		case []byte:
			result.Values[key] = bytes.Clone(typed)
		default:
			result.Values[key] = item
		}
	}
	return result
}

func overlayLocator(value row.Row) rowversionindex.Locator {
	return rowversionindex.Locator{
		DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
		SchemaRevision: value.SchemaVersion, Revision: value.Revision,
		CommitSequence: value.CommitSequence, State: value.State,
	}
}
