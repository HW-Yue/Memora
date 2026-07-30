package nativerouter

import (
	"encoding/binary"
	"errors"
	"fmt"
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
	if id == "" || databaseID == "" || tableID == "" || purpose == "" {
		return router.Node{}, fmt.Errorf("%w: root identity and purpose are required", ErrInvalid)
	}
	for _, root := range repository.Roots(tableID) {
		if !root.Deleted {
			return router.Node{}, fmt.Errorf("%w: Table already has a root", ErrInvalid)
		}
	}
	value := router.Node{Version: router.Version, ID: id, DatabaseID: databaseID, TableID: tableID, Name: "root", Aliases: []string{}, Path: "/", Kind: router.KindRoot, Purpose: purpose, Revision: 1}
	return value, repository.putNode(value)
}

func (repository *Repository) CreateChild(id, parentID, name string, kind router.Kind, purpose string) (router.Node, error) {
	parent, err := repository.Get(parentID)
	if err != nil {
		return router.Node{}, err
	}
	if id == "" || name == "" || purpose == "" || parent.Kind == router.KindLeaf || (kind != router.KindBranch && kind != router.KindLeaf) {
		return router.Node{}, fmt.Errorf("%w: invalid child definition", ErrInvalid)
	}
	for _, sibling := range repository.Children(parentID) {
		if strings.EqualFold(sibling.Name, name) {
			return router.Node{}, fmt.Errorf("%w: duplicate sibling name", ErrInvalid)
		}
	}
	path := strings.TrimSuffix(parent.Path, "/") + "/" + name
	value := router.Node{Version: router.Version, ID: id, DatabaseID: parent.DatabaseID, TableID: parent.TableID, ParentID: parent.ID, Name: name, Aliases: []string{}, Path: path, Kind: kind, Purpose: purpose, Revision: 1}
	return value, repository.putNode(value)
}

func (repository *Repository) Attach(leafID string, locator router.Locator, membershipRevision uint64) error {
	value := router.Membership{LeafID: leafID, MembershipRevision: membershipRevision, Locator: locator}
	if err := repository.validateMembership(value); err != nil {
		return err
	}
	payload, err := encodeMembership(value)
	if err != nil {
		return err
	}
	return repository.file.Put(nativestore.ObjectKindRouteMembership, schemaVersion, membershipRecordID(value), payload)
}

func (repository *Repository) StageMembership(transaction *nativestore.Transaction, value router.Membership) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	if err := repository.validateMembership(value); err != nil {
		return err
	}
	payload, err := encodeMembership(value)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRouteMembership, schemaVersion, membershipRecordID(value), payload)
}

func (repository *Repository) StageNode(transaction *nativestore.Transaction, value router.Node) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	latest, err := repository.Get(value.ID)
	if err != nil {
		return err
	}
	if latest.Deleted || value.Revision != latest.Revision+1 || value.DatabaseID != latest.DatabaseID ||
		value.TableID != latest.TableID || value.ParentID != latest.ParentID || value.Kind != latest.Kind {
		return fmt.Errorf("%w: route revision conflicts with latest", ErrInvalid)
	}
	payload, err := encodeNode(value)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRoute, schemaVersion, nodeRecordID(value.ID, value.Revision), payload)
}

func (repository *Repository) validateMembership(value router.Membership) error {
	leafID, locator, membershipRevision := value.LeafID, value.Locator, value.MembershipRevision
	leaf, err := repository.Get(leafID)
	if err != nil {
		return err
	}
	if leaf.Kind != router.KindLeaf || locator.DatabaseID != leaf.DatabaseID || locator.TableID != leaf.TableID || locator.RowID == "" || locator.Revision == 0 || membershipRevision == 0 {
		return fmt.Errorf("%w: invalid leaf membership", ErrInvalid)
	}
	return nil
}

func membershipRecordID(value router.Membership) string {
	id := value.LeafID + "@" + value.RowID
	if value.MembershipRevision > 1 {
		id += fmt.Sprintf("@%020d", value.MembershipRevision)
	}
	return id
}

func (repository *Repository) Get(id string) (router.Node, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRoute)
	if err != nil {
		return router.Node{}, err
	}
	var latest router.Node
	found := false
	for _, recordID := range ids {
		if recordID != id && !strings.HasPrefix(recordID, id+"@") {
			continue
		}
		payload, err := repository.file.Get(nativestore.ObjectKindRoute, recordID)
		if err != nil {
			return router.Node{}, err
		}
		value, err := decodeNode(payload)
		if err != nil || value.ID != id || nodeRecordID(value.ID, value.Revision) != recordID {
			return router.Node{}, fmt.Errorf("%w: route identity mismatch", ErrCorrupt)
		}
		if !found || value.Revision > latest.Revision {
			latest, found = value, true
		}
	}
	if !found {
		return router.Node{}, nativestore.ErrNotFound
	}
	return latest, nil
}

func (repository *Repository) Roots(tableID string) []router.Node {
	nodes, _ := repository.nodes()
	result := make([]router.Node, 0, 1)
	for _, node := range nodes {
		if node.TableID == tableID && node.Kind == router.KindRoot {
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
	if limit < 1 || limit > 1000 {
		return nil, "", fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalid)
	}
	children := repository.Children(parentID)
	start := 0
	if cursor != "" {
		for index := range children {
			if children[index].ID == cursor {
				start = index + 1
				break
			}
		}
	}
	end := start + limit
	if end >= len(children) {
		return children[start:], "", nil
	}
	return children[start:end], children[end-1].ID, nil
}

func (repository *Repository) Open(leafID string, limit int) ([]router.Locator, bool, error) {
	leaf, err := repository.Get(leafID)
	if err != nil {
		return nil, false, err
	}
	if leaf.Kind != router.KindLeaf || limit < 1 || limit > 1000 {
		return nil, false, fmt.Errorf("%w: OPEN requires a leaf and valid limit", ErrInvalid)
	}
	memberships, err := repository.memberships()
	if err != nil {
		return nil, false, err
	}
	locators := make([]router.Locator, 0)
	for _, membership := range memberships {
		if membership.LeafID == leafID {
			locators = append(locators, membership.Locator)
		}
	}
	sort.Slice(locators, func(left, right int) bool { return locators[left].RowID < locators[right].RowID })
	if len(locators) > limit {
		return locators[:limit], true, nil
	}
	return locators, false, nil
}

func (repository *Repository) Memberships(rowID string) ([]router.Membership, error) {
	values, err := repository.memberships()
	if err != nil {
		return nil, err
	}
	result := make([]router.Membership, 0)
	for _, value := range values {
		if value.RowID == rowID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].LeafID < result[right].LeafID })
	return result, nil
}

func (repository *Repository) putNode(value router.Node) error {
	payload, err := encodeNode(value)
	if err != nil {
		return err
	}
	return repository.file.Put(nativestore.ObjectKindRoute, schemaVersion, nodeRecordID(value.ID, value.Revision), payload)
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
	logicalIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindRoute, id)
		if err != nil {
			return nil, err
		}
		value, err := decodeNode(payload)
		if err != nil {
			return nil, err
		}
		logicalIDs[value.ID] = struct{}{}
	}
	result := make([]router.Node, 0, len(logicalIDs))
	for id := range logicalIDs {
		value, err := repository.Get(id)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (repository *Repository) memberships() ([]router.Membership, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRouteMembership)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]router.Membership)
	for _, id := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindRouteMembership, id)
		if err != nil {
			return nil, err
		}
		value, err := decodeMembership(payload)
		if err != nil {
			return nil, err
		}
		key := value.LeafID + "\x00" + value.RowID
		current, ok := latest[key]
		if !ok || value.MembershipRevision > current.MembershipRevision {
			latest[key] = value
		}
	}
	result := make([]router.Membership, 0, len(latest))
	for _, value := range latest {
		if !value.Deleted {
			result = append(result, value)
		}
	}
	return result, nil
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
	if err != nil || input.offset != len(payload) || revision == 0 || (kind != router.KindRoot && kind != router.KindBranch && kind != router.KindLeaf) {
		return router.Node{}, ErrCorrupt
	}
	return router.Node{Version: router.Version, ID: texts[0], DatabaseID: texts[1], TableID: texts[2], ParentID: texts[3], Name: texts[4], Aliases: aliases, Path: texts[5], Kind: kind, Purpose: texts[7], Revision: revision, Deleted: deleted == 1}, nil
}

func encodeMembership(value router.Membership) ([]byte, error) {
	encoded, err := encodeTexts([]string{value.LeafID, value.DatabaseID, value.TableID, value.RowID})
	if err != nil {
		return nil, err
	}
	encoded = binary.LittleEndian.AppendUint64(encoded, value.Revision)
	encoded = binary.LittleEndian.AppendUint64(encoded, value.MembershipRevision)
	if value.Deleted {
		encoded = append(encoded, 1)
	} else {
		encoded = append(encoded, 0)
	}
	return encoded, nil
}

func decodeMembership(payload []byte) (router.Membership, error) {
	input := decoder{bytes: payload}
	texts, err := input.texts(4)
	if err != nil {
		return router.Membership{}, err
	}
	rowRevision, err := input.u64()
	if err != nil {
		return router.Membership{}, err
	}
	membershipRevision, err := input.u64()
	if err != nil {
		return router.Membership{}, err
	}
	deleted, err := input.byte()
	if err != nil || deleted > 1 || input.offset != len(payload) || rowRevision == 0 || membershipRevision == 0 {
		return router.Membership{}, ErrCorrupt
	}
	return router.Membership{LeafID: texts[0], MembershipRevision: membershipRevision, Deleted: deleted == 1, Locator: router.Locator{DatabaseID: texts[1], TableID: texts[2], RowID: texts[3], Revision: rowRevision}}, nil
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
