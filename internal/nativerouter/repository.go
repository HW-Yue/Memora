package nativerouter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const schemaVersion = 1

var (
	ErrCorrupt = errors.New("native Table Router record is corrupt")
	ErrInvalid = errors.New("native Table Router value is invalid")
)

type Repository struct{ file *nativestore.File }

func New(file *nativestore.File) *Repository { return &Repository{file: file} }

func (repository *Repository) CreateRoot(id, databaseID, tableID, purpose string) (router.Node, error) {
	return repository.CreateRootWithSynopsis(id, databaseID, tableID, purpose, "")
}

func (repository *Repository) CreateRootWithSynopsis(id, databaseID, tableID, purpose, synopsis string) (router.Node, error) {
	value, err := repository.prepareRoot(id, databaseID, tableID, purpose, synopsis)
	if err != nil {
		return router.Node{}, err
	}
	return value, repository.putNode(value)
}

func (repository *Repository) StageRoot(
	transaction *nativestore.Transaction,
	id, databaseID, tableID, purpose, synopsis string,
) (router.Node, error) {
	if transaction == nil {
		return router.Node{}, fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	value, err := repository.prepareRoot(id, databaseID, tableID, purpose, synopsis)
	if err != nil {
		return router.Node{}, err
	}
	return value, repository.stageInitialNode(transaction, value)
}

func (repository *Repository) prepareRoot(id, databaseID, tableID, purpose, synopsis string) (router.Node, error) {
	if id == "" || databaseID == "" || tableID == "" || purpose == "" {
		return router.Node{}, fmt.Errorf("%w: root identity and purpose are required", ErrInvalid)
	}
	if err := validateSynopsis(synopsis); err != nil {
		return router.Node{}, err
	}
	for _, root := range repository.Roots(tableID) {
		if !root.Deleted {
			return router.Node{}, fmt.Errorf("%w: Table already has a root", ErrInvalid)
		}
	}
	value := router.Node{Version: router.Version, ID: id, DatabaseID: databaseID, TableID: tableID, Name: "root", Aliases: []string{}, Path: "/", Kind: router.KindRoot, Purpose: purpose, Synopsis: synopsis, Revision: 1}
	return value, nil
}

// CheckBranchFanout refuses a write that would push one live parent past the
// Database's structural fan-out limit. Callers resolve the limit from the
// Database's route_policy configuration.
func (repository *Repository) CheckBranchFanout(parentID string, adding, limit int) error {
	return router.CheckBranchFanout(parentID, len(repository.Children(parentID)), adding, limit)
}

func (repository *Repository) CreateChild(id, parentID, name string, kind router.Kind, purpose string) (router.Node, error) {
	return repository.CreateChildWithSynopsis(id, parentID, name, kind, purpose, "")
}

func (repository *Repository) CreateChildWithSynopsis(id, parentID, name string, kind router.Kind, purpose, synopsis string) (router.Node, error) {
	value, err := repository.prepareChild(id, parentID, name, kind, purpose, synopsis, router.DefaultBranchFanout)
	if err != nil {
		return router.Node{}, err
	}
	return value, repository.putNode(value)
}

// StageChild refuses to grow a parent past fanout, the Database's structural
// Route branch fan-out limit.
func (repository *Repository) StageChild(
	transaction *nativestore.Transaction,
	id, parentID, name string,
	kind router.Kind,
	purpose, synopsis string,
	fanout int,
) (router.Node, error) {
	if transaction == nil {
		return router.Node{}, fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	value, err := repository.prepareChild(id, parentID, name, kind, purpose, synopsis, fanout)
	if err != nil {
		return router.Node{}, err
	}
	return value, repository.stageInitialNode(transaction, value)
}

func (repository *Repository) prepareChild(
	id, parentID, name string, kind router.Kind, purpose, synopsis string, fanout int,
) (router.Node, error) {
	parent, err := repository.Get(parentID)
	if err != nil {
		return router.Node{}, err
	}
	if id == "" || name == "" || purpose == "" || parent.Kind == router.KindLeaf || (kind != router.KindBranch && kind != router.KindLeaf) {
		return router.Node{}, fmt.Errorf("%w: invalid child definition", ErrInvalid)
	}
	if err := validateSynopsis(synopsis); err != nil {
		return router.Node{}, err
	}
	if err := repository.CheckBranchFanout(parentID, 1, fanout); err != nil {
		return router.Node{}, err
	}
	for _, sibling := range repository.Children(parentID) {
		if strings.EqualFold(sibling.Name, name) {
			return router.Node{}, fmt.Errorf("%w: duplicate sibling name", ErrInvalid)
		}
	}
	path := strings.TrimSuffix(parent.Path, "/") + "/" + name
	value := router.Node{Version: router.Version, ID: id, DatabaseID: parent.DatabaseID, TableID: parent.TableID, ParentID: parent.ID, Name: name, Aliases: []string{}, Path: path, Kind: kind, Purpose: purpose, Synopsis: synopsis, Revision: 1}
	return value, nil
}

func (repository *Repository) StageNode(transaction *nativestore.Transaction, value router.Node) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	latest, err := repository.Get(value.ID)
	if err != nil {
		return err
	}
	if value.Revision != latest.Revision+1 || value.DatabaseID != latest.DatabaseID ||
		value.TableID != latest.TableID || value.ParentID != latest.ParentID || value.Kind != latest.Kind {
		return fmt.Errorf("%w: route revision conflicts with latest", ErrInvalid)
	}
	// Deleting a Route node is final: an index node is cheap to rebuild, so no
	// revision may follow its tombstone.
	if latest.Deleted {
		return fmt.Errorf("%w: route revision conflicts with latest", ErrInvalid)
	}
	if err := validateSynopsis(value.Synopsis); err != nil {
		return err
	}
	payload, err := encodeNode(value)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRoute, schemaVersion, nodeRecordID(value.ID, value.Revision), payload)
}

// StagePlannedCreate stages a fully validated revision-one node whose parent
// may also be staged in the same transaction. It is intentionally reserved for
// a guarded Route mutation plan.
func (repository *Repository) StagePlannedCreate(transaction *nativestore.Transaction, value router.Node) error {
	if transaction == nil || value.Version != router.Version || value.ID == "" || value.DatabaseID == "" ||
		value.TableID == "" || value.ParentID == "" || value.Name == "" || value.Path == "" ||
		value.Purpose == "" || value.Revision != 1 || value.Deleted ||
		(value.Kind != router.KindBranch && value.Kind != router.KindLeaf) {
		return fmt.Errorf("%w: invalid planned Route create", ErrInvalid)
	}
	if _, err := repository.Get(value.ID); err == nil || !errors.Is(err, nativestore.ErrNotFound) {
		if err == nil {
			return fmt.Errorf("%w: planned Route already exists", ErrInvalid)
		}
		return err
	}
	if err := validateSynopsis(value.Synopsis); err != nil {
		return err
	}
	return repository.stageInitialNode(transaction, value)
}

// StagePlannedRevision permits only reparent/path/deleted changes. Identity,
// semantic surfaces and kind remain immutable during structural execution.
func (repository *Repository) StagePlannedRevision(transaction *nativestore.Transaction, value router.Node) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	latest, err := repository.Get(value.ID)
	if err != nil {
		return err
	}
	if latest.Deleted || value.Version != latest.Version || value.Revision != latest.Revision+1 ||
		value.DatabaseID != latest.DatabaseID || value.TableID != latest.TableID || value.Kind != latest.Kind ||
		value.Name != latest.Name || value.Purpose != latest.Purpose || value.Synopsis != latest.Synopsis ||
		!slices.Equal(value.Aliases, latest.Aliases) || value.Path == "" {
		return fmt.Errorf("%w: planned Route revision conflicts with latest", ErrInvalid)
	}
	payload, err := encodeNode(value)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRoute, schemaVersion, nodeRecordID(value.ID, value.Revision), payload)
}

// Get resolves a Route node to its latest revision.
//
// Revisions are numbered from 1 with no gaps — StageNode only accepts
// latest+1, and nodes() verifies the sequence — so the latest one is found by
// probing forward from revision 1 until a record is missing. That is a bounded
// number of point reads. Enumerating every Route record instead, which is what
// this used to do, made one node read cost a sweep of the whole database: every
// SELECT resolves one node per leaf per Row, so a result page turned into as
// many full scans as it had rows. See internal/nativerouter/no_scan_test.go.
func (repository *Repository) Get(id string) (router.Node, error) {
	if id == "" {
		return router.Node{}, nativestore.ErrNotFound
	}
	var latest router.Node
	found := false
	for revision := uint64(1); ; revision++ {
		recordID := nodeRecordID(id, revision)
		payload, err := repository.file.Get(nativestore.ObjectKindRoute, recordID)
		if errors.Is(err, nativestore.ErrNotFound) {
			break
		}
		if err != nil {
			return router.Node{}, err
		}
		value, decodeErr := decodeNode(payload)
		if decodeErr != nil || value.ID != id || value.Revision != revision {
			return router.Node{}, fmt.Errorf("%w: route identity mismatch", ErrCorrupt)
		}
		latest, found = value, true
	}
	if !found {
		return router.Node{}, nativestore.ErrNotFound
	}
	return latest, nil
}

func sameNodeContent(left, right router.Node) bool {
	if left.Version != right.Version || left.ID != right.ID || left.DatabaseID != right.DatabaseID ||
		left.TableID != right.TableID || left.ParentID != right.ParentID || left.Name != right.Name ||
		left.Path != right.Path || left.Kind != right.Kind || left.Purpose != right.Purpose ||
		left.Synopsis != right.Synopsis || left.Revision != right.Revision || left.Deleted != right.Deleted ||
		len(left.Aliases) != len(right.Aliases) {
		return false
	}
	for index := range left.Aliases {
		if left.Aliases[index] != right.Aliases[index] {
			return false
		}
	}
	return true
}

func (repository *Repository) Roots(tableID string) []router.Node {
	nodes, _ := repository.nodes()
	result := make([]router.Node, 0, 1)
	for _, node := range nodes {
		if node.TableID == tableID && node.Kind == router.KindRoot && !node.Deleted {
			result = append(result, node)
		}
	}
	return result
}

func (repository *Repository) Children(parentID string) []router.Node {
	nodes, _ := repository.nodes()
	result := make([]router.Node, 0)
	for _, node := range nodes {
		if node.ParentID == parentID && !node.Deleted {
			result = append(result, node)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Name == result[right].Name {
			return result[left].ID < result[right].ID
		}
		return result[left].Name < result[right].Name
	})
	return result
}

func (repository *Repository) ShowUnder(parentID, cursor string, limit int) ([]router.Node, string, error) {
	nodes, page, err := repository.ShowUnderPage(parentID, cursor, limit)
	return nodes, page.NextCursor, err
}

func (repository *Repository) ShowUnderPage(parentID, cursor string, limit int) ([]router.Node, router.ReadPage, error) {
	if limit < 1 || limit > 1000 {
		return nil, router.ReadPage{}, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalid)
	}
	parent, err := repository.Get(parentID)
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	if parent.Deleted || parent.Kind == router.KindLeaf {
		return nil, router.ReadPage{}, fmt.Errorf("%w: children require a live root or branch", ErrInvalid)
	}
	children := repository.Children(parentID)
	return router.PaginateNodes("parent:"+parentID, cursor, limit, children)
}

func (repository *Repository) LeafForOpen(leafID string, limit int) (router.Node, error) {
	leaf, err := repository.Get(leafID)
	if err != nil {
		return router.Node{}, err
	}
	if leaf.Kind != router.KindLeaf || limit < 1 || limit > 1000 {
		return router.Node{}, fmt.Errorf("%w: OPEN requires a leaf and valid limit", ErrInvalid)
	}
	if leaf.Deleted {
		return router.Node{}, fmt.Errorf("%w: Route leaf is deleted", ErrInvalid)
	}
	return leaf, nil
}

func (repository *Repository) putNode(value router.Node) error {
	payload, err := encodeNode(value)
	if err != nil {
		return err
	}
	return repository.file.Put(nativestore.ObjectKindRoute, schemaVersion, nodeRecordID(value.ID, value.Revision), payload)
}

func (repository *Repository) stageInitialNode(
	transaction *nativestore.Transaction, value router.Node,
) error {
	payload, err := encodeNode(value)
	if err != nil {
		return err
	}
	return transaction.Put(
		nativestore.ObjectKindRoute, schemaVersion, nodeRecordID(value.ID, value.Revision), payload,
	)
}

func nodeRecordID(id string, revision uint64) string {
	if revision == 1 {
		return id
	}
	return fmt.Sprintf("%s@%020d", id, revision)
}

func (repository *Repository) nodes() ([]router.Node, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRoute)
	if err != nil {
		return nil, err
	}
	histories := make(map[string][]router.Node, len(ids))
	for _, id := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindRoute, id)
		if err != nil {
			return nil, err
		}
		value, err := decodeNode(payload)
		if err != nil {
			return nil, err
		}
		if nodeRecordID(value.ID, value.Revision) != id {
			return nil, fmt.Errorf("%w: route Record identity mismatch", ErrCorrupt)
		}
		histories[value.ID] = append(histories[value.ID], value)
	}
	result := make([]router.Node, 0, len(histories))
	for id, history := range histories {
		sort.Slice(history, func(left, right int) bool { return history[left].Revision < history[right].Revision })
		for index, value := range history {
			if value.Revision != uint64(index+1) || value.ID != id {
				return nil, fmt.Errorf("%w: Route %q revision sequence", ErrCorrupt, id)
			}
			if index == 0 {
				continue
			}
			previous := history[index-1]
			if value.Version != previous.Version ||
				value.DatabaseID != previous.DatabaseID || value.TableID != previous.TableID ||
				value.Kind != previous.Kind {
				return nil, fmt.Errorf("%w: Route %q revision identity", ErrCorrupt, id)
			}
			if previous.Deleted {
				return nil, fmt.Errorf("%w: Route %q revision after delete", ErrCorrupt, id)
			}
		}
		result = append(result, history[len(history)-1])
	}
	return result, nil
}

// Nodes returns current live semantic nodes in stable ID order. Memberships
// are deliberately outside this read surface.
func (repository *Repository) Nodes() ([]router.Node, error) {
	nodes, err := repository.nodes()
	if err != nil {
		return nil, err
	}
	live := nodes[:0]
	for _, node := range nodes {
		if !node.Deleted {
			node.Aliases = append([]string{}, node.Aliases...)
			live = append(live, node)
		}
	}
	sort.Slice(live, func(left, right int) bool { return live[left].ID < live[right].ID })
	return live, nil
}

func encodeNode(value router.Node) ([]byte, error) {
	texts := []string{value.ID, value.DatabaseID, value.TableID, value.ParentID, value.Name, value.Path, string(value.Kind), value.Purpose}
	encoded, err := encodeTexts(texts)
	if err != nil {
		return nil, err
	}
	encoded = binary.LittleEndian.AppendUint64(encoded, value.Revision)
	if value.Deleted {
		encoded = append(encoded, 1)
	} else {
		encoded = append(encoded, 0)
	}
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(value.Aliases)))
	for _, alias := range value.Aliases {
		encoded, err = appendText(encoded, alias)
		if err != nil {
			return nil, err
		}
	}
	encoded, err = appendText(encoded, value.Synopsis)
	if err != nil {
		return nil, err
	}
	// Appended after Synopsis rather than added to the fixed text array, for
	// the same reason Synopsis itself was: widening the array would make every
	// Route written before today fail to decode. The reader guards on offset.
	encoded, err = appendText(encoded, value.RowID)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func decodeNode(payload []byte) (router.Node, error) {
	input := decoder{bytes: payload}
	texts, err := input.texts(8)
	if err != nil {
		return router.Node{}, err
	}
	revision, err := input.u64()
	if err != nil {
		return router.Node{}, err
	}
	deleted, err := input.byte()
	if err != nil || deleted > 1 {
		return router.Node{}, ErrCorrupt
	}
	count, err := input.u32()
	if err != nil || count > 1000 {
		return router.Node{}, ErrCorrupt
	}
	aliases, err := input.moreTexts(int(count))
	kind := router.Kind(texts[6])
	if err != nil || revision == 0 || (kind != router.KindRoot && kind != router.KindBranch && kind != router.KindLeaf) {
		return router.Node{}, ErrCorrupt
	}
	synopsis := ""
	if input.offset < len(payload) {
		synopsis, err = input.text()
	}
	// A record written before leaves carried a RowID simply ends here.
	rowID := ""
	if err == nil && input.offset < len(payload) {
		rowID, err = input.text()
	}
	if err != nil || input.offset != len(payload) {
		return router.Node{}, ErrCorrupt
	}
	if rowID != "" && kind != router.KindLeaf {
		return router.Node{}, ErrCorrupt
	}
	return router.Node{Version: router.Version, ID: texts[0], DatabaseID: texts[1], TableID: texts[2], ParentID: texts[3], Name: texts[4], Aliases: aliases, Path: texts[5], Kind: kind, Purpose: texts[7], Synopsis: synopsis, RowID: rowID, Revision: revision, Deleted: deleted == 1}, nil
}

func validateSynopsis(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 1000 {
		return fmt.Errorf("%w: Route synopsis exceeds 1000 characters", ErrInvalid)
	}
	return nil
}

func encodeTexts(values []string) ([]byte, error) {
	encoded := binary.LittleEndian.AppendUint16(nil, schemaVersion)
	var err error
	for _, value := range values {
		encoded, err = appendText(encoded, value)
		if err != nil {
			return nil, err
		}
	}
	return encoded, nil
}
func appendText(encoded []byte, value string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return nil, ErrInvalid
	}
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(value)))
	return append(encoded, value...), nil
}

type decoder struct {
	bytes  []byte
	offset int
}

func (input *decoder) texts(count int) ([]string, error) {
	version, err := input.u16()
	if err != nil || version != schemaVersion {
		return nil, ErrCorrupt
	}
	return input.moreTexts(count)
}
func (input *decoder) moreTexts(count int) ([]string, error) {
	var err error
	result := make([]string, count)
	for index := range result {
		result[index], err = input.text()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (input *decoder) text() (string, error) {
	length, err := input.u32()
	if err != nil || int(length) > len(input.bytes)-input.offset {
		return "", ErrCorrupt
	}
	value := input.bytes[input.offset : input.offset+int(length)]
	input.offset += int(length)
	if !utf8.Valid(value) {
		return "", ErrCorrupt
	}
	return string(value), nil
}
func (input *decoder) take(length int) ([]byte, error) {
	if length > len(input.bytes)-input.offset {
		return nil, ErrCorrupt
	}
	value := input.bytes[input.offset : input.offset+length]
	input.offset += length
	return value, nil
}
func (input *decoder) byte() (byte, error) {
	value, err := input.take(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}
func (input *decoder) u16() (uint16, error) {
	value, err := input.take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(value), nil
}
func (input *decoder) u32() (uint32, error) {
	value, err := input.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(value), nil
}
func (input *decoder) u64() (uint64, error) {
	value, err := input.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(value), nil
}

// LeavesHoldingRow returns the live leaves that currently name this Row.
//
// It scans the Route nodes, which are metadata-sized — the semantic tree is
// bounded by how a person organises their knowledge, not by how much of it they
// have. The unbounded direction, Row to leaves, is answered by a field on the
// Row itself and never scans.
func (repository *Repository) LeavesHoldingRow(rowID string) ([]string, error) {
	if rowID == "" {
		return nil, nil
	}
	nodes, err := repository.nodes()
	if err != nil {
		return nil, err
	}
	var result []string
	for _, node := range nodes {
		if !node.Deleted && node.RowID == rowID {
			result = append(result, node.ID)
		}
	}
	sort.Strings(result)
	return result, nil
}
