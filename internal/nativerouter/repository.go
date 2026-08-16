package nativerouter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
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

func (repository *Repository) CreateChild(id, parentID, name string, kind router.Kind, purpose string) (router.Node, error) {
	return repository.CreateChildWithSynopsis(id, parentID, name, kind, purpose, "")
}

func (repository *Repository) CreateChildWithSynopsis(id, parentID, name string, kind router.Kind, purpose, synopsis string) (router.Node, error) {
	value, err := repository.prepareChild(id, parentID, name, kind, purpose, synopsis)
	if err != nil {
		return router.Node{}, err
	}
	return value, repository.putNode(value)
}

func (repository *Repository) StageChild(
	transaction *nativestore.Transaction,
	id, parentID, name string,
	kind router.Kind,
	purpose, synopsis string,
) (router.Node, error) {
	if transaction == nil {
		return router.Node{}, fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	value, err := repository.prepareChild(id, parentID, name, kind, purpose, synopsis)
	if err != nil {
		return router.Node{}, err
	}
	return value, repository.stageInitialNode(transaction, value)
}

func (repository *Repository) prepareChild(id, parentID, name string, kind router.Kind, purpose, synopsis string) (router.Node, error) {
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
	for _, sibling := range repository.Children(parentID) {
		if strings.EqualFold(sibling.Name, name) {
			return router.Node{}, fmt.Errorf("%w: duplicate sibling name", ErrInvalid)
		}
	}
	path := strings.TrimSuffix(parent.Path, "/") + "/" + name
	value := router.Node{Version: router.Version, ID: id, DatabaseID: parent.DatabaseID, TableID: parent.TableID, ParentID: parent.ID, Name: name, Aliases: []string{}, Path: path, Kind: kind, Purpose: purpose, Synopsis: synopsis, Revision: 1}
	return value, nil
}

func (repository *Repository) Attach(leafID string, locator router.Locator, membershipRevision uint64) error {
	value := router.Membership{LeafID: leafID, MembershipRevision: membershipRevision, Locator: locator}
	if err := repository.validateMembership(value); err != nil {
		return err
	}
	if err := repository.ValidateMembershipChanges([]router.Membership{value}); err != nil {
		return err
	}
	payload, err := encodeMembership(value)
	if err != nil {
		return err
	}
	if err := repository.file.Put(nativestore.ObjectKindRouteMembership, schemaVersion, membershipRecordID(value), payload); err != nil {
		return err
	}
	return repository.file.Put(nativestore.ObjectKindRouteRowMembership, schemaVersion, rowMembershipRecordID(value), payload)
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
	if err := transaction.Put(nativestore.ObjectKindRouteMembership, schemaVersion, membershipRecordID(value), payload); err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRouteRowMembership, schemaVersion, rowMembershipRecordID(value), payload)
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

// StagePlannedMembership validates the immutable locator shape while allowing
// its target leaf to be a Route created in the same transaction.
func (repository *Repository) StagePlannedMembership(transaction *nativestore.Transaction, value router.Membership) error {
	if transaction == nil || value.LeafID == "" || value.DatabaseID == "" || value.TableID == "" ||
		value.RowID == "" || value.Revision == 0 || value.MembershipRevision == 0 {
		return fmt.Errorf("%w: invalid planned membership", ErrInvalid)
	}
	payload, err := encodeMembership(value)
	if err != nil {
		return err
	}
	if err := transaction.Put(nativestore.ObjectKindRouteMembership, schemaVersion, membershipRecordID(value), payload); err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRouteRowMembership, schemaVersion, rowMembershipRecordID(value), payload)
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

func rowMembershipRecordID(value router.Membership) string {
	id := value.RowID + "@" + value.LeafID
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

func (repository *Repository) Open(leafID string, limit int) ([]router.Locator, bool, error) {
	locators, page, err := repository.OpenPage(leafID, "", limit)
	return locators, page.NextCursor != "", err
}

func (repository *Repository) OpenPage(leafID, cursor string, limit int) ([]router.Locator, router.ReadPage, error) {
	return repository.openPage(leafID, cursor, limit, true)
}

// InspectLeafPage is a maintenance-only read used to diagnose and monotonically
// repair legacy multi-Row leaves. Normal Router reads must use OpenPage.
func (repository *Repository) InspectLeafPage(leafID, cursor string, limit int) ([]router.Locator, router.ReadPage, error) {
	return repository.openPage(leafID, cursor, limit, false)
}

func (repository *Repository) openPage(leafID, cursor string, limit int, enforceSingleRow bool) ([]router.Locator, router.ReadPage, error) {
	leaf, err := repository.Get(leafID)
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	if leaf.Kind != router.KindLeaf || limit < 1 || limit > 1000 {
		return nil, router.ReadPage{}, fmt.Errorf("%w: OPEN requires a leaf and valid limit", ErrInvalid)
	}
	memberships, err := repository.memberships()
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	locators := make([]router.Locator, 0)
	for _, membership := range memberships {
		if membership.LeafID == leafID {
			locators = append(locators, membership.Locator)
		}
	}
	if enforceSingleRow && len(locators) > 1 {
		return nil, router.ReadPage{}, &router.Error{
			Code:    result.CodeConstraint,
			Message: "Router leaf locates multiple Rows and requires semantic reshape",
		}
	}
	sort.Slice(locators, func(left, right int) bool { return locators[left].RowID < locators[right].RowID })
	return router.PaginateLocators("leaf:"+leafID, cursor, limit, locators)
}

// ValidateMembershipChanges checks the touched leaves in the final live state.
// A Row may remain in multiple leaves, but a healthy leaf locates at most one
// live Row. A legacy invalid leaf may only decrease its occupant count, which
// permits bounded repair without allowing new ambiguity. Callers must invoke it
// before staging an atomic write set.
func (repository *Repository) ValidateMembershipChanges(changes []router.Membership) error {
	current, err := repository.latestMemberships(false)
	if err != nil {
		return err
	}
	latestChanges := make(map[string]router.Membership, len(changes))
	touchedLeaves := make(map[string]bool, len(changes))
	for _, membership := range changes {
		key := membershipKey(membership)
		touchedLeaves[membership.LeafID] = true
		if existing, ok := latestChanges[key]; !ok || membership.MembershipRevision > existing.MembershipRevision {
			latestChanges[key] = membership
		}
	}
	currentByLeaf := make(map[string]map[string]router.Membership, len(touchedLeaves))
	for leafID := range touchedLeaves {
		currentByLeaf[leafID] = map[string]router.Membership{}
	}
	for _, membership := range current {
		if touchedLeaves[membership.LeafID] {
			currentByLeaf[membership.LeafID][membership.RowID] = membership
		}
	}
	finalByLeaf := make(map[string]map[string]router.Membership, len(currentByLeaf))
	for leafID, occupants := range currentByLeaf {
		finalByLeaf[leafID] = make(map[string]router.Membership, len(occupants))
		for rowID, membership := range occupants {
			finalByLeaf[leafID][rowID] = membership
		}
	}
	for _, membership := range latestChanges {
		if membership.Deleted {
			delete(finalByLeaf[membership.LeafID], membership.RowID)
		} else {
			finalByLeaf[membership.LeafID][membership.RowID] = membership
		}
	}
	for leafID, occupants := range finalByLeaf {
		currentCount, finalCount := len(currentByLeaf[leafID]), len(occupants)
		if finalCount > 1 && !(currentCount > 1 && finalCount < currentCount) {
			return &router.Error{
				Code:    result.CodeConstraint,
				Message: "Router leaf already locates another Row; create a distinct semantic leaf",
			}
		}
	}
	return nil
}

func membershipKey(value router.Membership) string {
	return value.LeafID + "\x00" + value.RowID
}

func (repository *Repository) Memberships(rowID string) ([]router.Membership, error) {
	return repository.membershipsForRow(rowID, false)
}

func (repository *Repository) MembershipsIncludingDeleted(rowID string) ([]router.Membership, error) {
	return repository.membershipsForRow(rowID, true)
}

func (repository *Repository) membershipsForRow(rowID string, includeDeleted bool) ([]router.Membership, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRouteRowMembership)
	if err != nil {
		return nil, err
	}
	prefix := rowID + "@"
	latest := make(map[string]router.Membership)
	for _, id := range ids {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		payload, err := repository.file.Get(nativestore.ObjectKindRouteRowMembership, id)
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
		if includeDeleted || !value.Deleted {
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
			if previous.Deleted || value.Version != previous.Version ||
				value.DatabaseID != previous.DatabaseID || value.TableID != previous.TableID ||
				value.Kind != previous.Kind {
				return nil, fmt.Errorf("%w: Route %q revision identity", ErrCorrupt, id)
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

func (repository *Repository) memberships() ([]router.Membership, error) {
	return repository.latestMemberships(false)
}

func (repository *Repository) latestMemberships(includeDeleted bool) ([]router.Membership, error) {
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
		if includeDeleted || !value.Deleted {
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
	encoded, err = appendText(encoded, value.Synopsis)
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
	if err != nil || input.offset != len(payload) {
		return router.Node{}, ErrCorrupt
	}
	return router.Node{Version: router.Version, ID: texts[0], DatabaseID: texts[1], TableID: texts[2], ParentID: texts[3], Name: texts[4], Aliases: aliases, Path: texts[5], Kind: kind, Purpose: texts[7], Synopsis: synopsis, Revision: revision, Deleted: deleted == 1}, nil
}

func validateSynopsis(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 1000 {
		return fmt.Errorf("%w: Route synopsis exceeds 1000 characters", ErrInvalid)
	}
	return nil
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
