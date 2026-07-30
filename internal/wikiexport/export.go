package wikiexport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/snapshot"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	Version        = "memora.wiki-export/v1"
	ProfileVersion = "memora.wiki-profile/v1"
	manifestName   = ".memora-wiki.json"
)

type Profile struct {
	Version string                  `json:"version"`
	Tables  map[string]TableProfile `json:"tables"`
}

type TableProfile struct {
	TitleFieldID     string   `json:"title_field_id,omitempty"`
	BodyFieldIDs     []string `json:"body_field_ids"`
	PropertyFieldIDs []string `json:"property_field_ids"`
	TagFieldIDs      []string `json:"tag_field_ids"`
	HiddenFieldIDs   []string `json:"hidden_field_ids"`
	FieldOrder       []string `json:"field_order"`
}

type Object struct {
	DatabaseID    string `json:"database_id"`
	TableID       string `json:"table_id"`
	RowID         string `json:"row_id"`
	Revision      uint64 `json:"revision"`
	Path          string `json:"path"`
	ContentSHA256 string `json:"content_sha256"`
}

type Manifest struct {
	Version        string   `json:"version"`
	SnapshotSHA256 string   `json:"snapshot_sha256"`
	ProfileSHA256  string   `json:"profile_sha256"`
	Objects        []Object `json:"objects"`
}

type Error struct {
	Code    result.Code
	Message string
}

type Service struct{ snapshots *snapshot.Service }

func New(database store.Store) *Service {
	return &Service{snapshots: snapshot.New(database)}
}

func (service *Service) Export(
	ctx context.Context,
	root string,
	profile Profile,
	databases []string,
) (Manifest, error) {
	if service == nil || service.snapshots == nil {
		return Manifest{}, exportError(result.CodeInternal, "Wiki exporter is unavailable")
	}
	encoded, err := service.snapshots.Export(ctx)
	if err != nil {
		return Manifest{}, err
	}
	return ExportSnapshotScoped(encoded, root, profile, databases)
}

func DecodeProfile(encoded []byte) (Profile, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, exportError(result.CodeValidation, "Wiki Export Profile JSON is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Profile{}, exportError(result.CodeValidation, "Wiki Export Profile JSON has trailing content")
	}
	if profile.Version != ProfileVersion || profile.Tables == nil {
		return Profile{}, exportError(result.CodeValidation, "Wiki Export Profile version and Tables are required")
	}
	return profile, nil
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

type snapshotDocument struct {
	Version   string            `json:"version"`
	Catalog   json.RawMessage   `json:"catalog"`
	Rows      []json.RawMessage `json:"rows"`
	Relations struct {
		Current []json.RawMessage `json:"current"`
	} `json:"relations"`
}

type layout struct {
	databases map[string]catalog.Database
	tables    map[string]catalog.Table
	rows      map[string]row.Row
	titles    map[string]string
	paths     map[string]string
}

func ExportSnapshot(encoded []byte, root string, profile Profile) (Manifest, error) {
	return exportSnapshot(encoded, root, profile, nil)
}

func ExportSnapshotScoped(
	encoded []byte,
	root string,
	profile Profile,
	databases []string,
) (Manifest, error) {
	if len(databases) == 0 {
		return Manifest{}, exportError(result.CodePermissionDenied, "Wiki export requires a non-empty Database scope")
	}
	return exportSnapshot(encoded, root, profile, databases)
}

func exportSnapshot(encoded []byte, root string, profile Profile, databases []string) (Manifest, error) {
	migrated, err := snapshot.Migrate(encoded)
	if err != nil {
		return Manifest{}, err
	}
	value, currentRelations, err := decodeSnapshot(migrated)
	if err != nil {
		return Manifest{}, err
	}
	if len(databases) > 0 {
		value, err = scopedLayout(value, profile, databases)
		if err != nil {
			return Manifest{}, err
		}
		currentRelations = scopedRelations(currentRelations, value.databases)
	}
	if err := validateProfile(profile, value.tables); err != nil {
		return Manifest{}, err
	}
	for key, stored := range value.rows {
		if tableProfile, configured := profile.Tables[stored.TableID]; configured && tableProfile.TitleFieldID != "" {
			if title, ok := stored.Values[tableProfile.TitleFieldID].(string); ok && strings.TrimSpace(title) != "" {
				value.titles[key] = strings.TrimSpace(title)
			}
		}
	}
	var snapshotHash string
	if len(databases) == 0 {
		snapshotHash, err = snapshot.CanonicalHash(migrated)
	} else {
		snapshotHash, err = hashJSON(struct {
			Databases map[string]catalog.Database `json:"databases"`
			Tables    map[string]catalog.Table    `json:"tables"`
			Rows      map[string]row.Row          `json:"rows"`
			Relations []relation.Relation         `json:"relations"`
		}{
			Databases: value.databases, Tables: value.tables,
			Rows: value.rows, Relations: currentRelations,
		})
	}
	if err != nil {
		return Manifest{}, exportError(result.CodeInternal, "Wiki scoped snapshot could not be hashed")
	}
	profileHash, err := hashJSON(profile)
	if err != nil {
		return Manifest{}, exportError(result.CodeInternal, "Wiki Export Profile could not be encoded")
	}
	manifest := Manifest{
		Version: Version, SnapshotSHA256: snapshotHash, ProfileSHA256: profileHash,
		Objects: []Object{},
	}
	contents := map[string][]byte{}
	rowIDs := make([]string, 0, len(value.rows))
	for id := range value.rows {
		rowIDs = append(rowIDs, id)
	}
	sort.Strings(rowIDs)
	for _, id := range rowIDs {
		stored := value.rows[id]
		if stored.State != row.StateLive {
			continue
		}
		table := value.tables[stored.TableID]
		database := value.databases[stored.DatabaseID]
		tableProfile, configured := profile.Tables[stored.TableID]
		page := renderPage(stored, database, table, tableProfile, configured, value, currentRelations)
		path := value.paths[id]
		hash := hashBytes(page)
		contents[path] = page
		manifest.Objects = append(manifest.Objects, Object{
			DatabaseID: stored.DatabaseID, TableID: stored.TableID, RowID: stored.ID,
			Revision: stored.Revision, Path: path, ContentSHA256: hash,
		})
	}
	sort.Slice(manifest.Objects, func(left, right int) bool {
		return manifest.Objects[left].Path < manifest.Objects[right].Path
	})
	if err := writeIncremental(root, manifest, contents); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func scopedLayout(value layout, profile Profile, selectors []string) (layout, error) {
	allowed := map[string]bool{}
	for _, database := range value.databases {
		for _, selector := range selectors {
			if strings.EqualFold(strings.TrimSpace(selector), database.ID) ||
				strings.EqualFold(strings.TrimSpace(selector), database.Name) {
				allowed[database.ID] = true
			}
		}
	}
	if len(allowed) == 0 {
		return layout{}, exportError(result.CodeNotFound, "Wiki export Database scope matched no Database")
	}
	for tableID := range profile.Tables {
		table, exists := value.tables[tableID]
		if exists && !allowed[table.DatabaseID] {
			return layout{}, exportError(result.CodePermissionDenied, "Wiki Export Profile references a Table outside the authorized scope")
		}
	}
	scoped := layout{
		databases: map[string]catalog.Database{}, tables: map[string]catalog.Table{},
		rows: map[string]row.Row{}, titles: map[string]string{}, paths: map[string]string{},
	}
	for databaseID, database := range value.databases {
		if allowed[databaseID] {
			scoped.databases[databaseID] = database
		}
	}
	for tableID, table := range value.tables {
		if allowed[table.DatabaseID] {
			scoped.tables[tableID] = table
		}
	}
	for key, stored := range value.rows {
		if !allowed[stored.DatabaseID] {
			continue
		}
		scoped.rows[key] = stored
		scoped.titles[key] = value.titles[key]
		scoped.paths[key] = value.paths[key]
	}
	return scoped, nil
}

func scopedRelations(values []relation.Relation, databases map[string]catalog.Database) []relation.Relation {
	scoped := make([]relation.Relation, 0, len(values))
	for _, value := range values {
		if _, sourceAllowed := databases[value.Source.DatabaseID]; !sourceAllowed {
			continue
		}
		if _, targetAllowed := databases[value.Target.DatabaseID]; !targetAllowed {
			continue
		}
		scoped = append(scoped, value)
	}
	sort.Slice(scoped, func(left, right int) bool {
		return scoped[left].ID < scoped[right].ID
	})
	return scoped
}

func HashDirectory(root string) (string, error) {
	if err := security.ValidateOutputRoot(root); err != nil {
		return "", err
	}
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return errors.New("Wiki export contains a non-regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func decodeSnapshot(encoded []byte) (layout, []relation.Relation, error) {
	var document snapshotDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&document) != nil || document.Version != snapshot.Version {
		return layout{}, nil, exportError(result.CodeValidation, "logical snapshot cannot be projected as a Wiki")
	}
	var dictionary struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}
	if json.Unmarshal(document.Catalog, &dictionary) != nil || dictionary.Version != catalog.Version {
		return layout{}, nil, exportError(result.CodeValidation, "Wiki Catalog is invalid")
	}
	value := layout{
		databases: map[string]catalog.Database{}, tables: map[string]catalog.Table{},
		rows: map[string]row.Row{}, titles: map[string]string{}, paths: map[string]string{},
	}
	for _, database := range dictionary.Databases {
		value.databases[database.ID] = database
		for _, table := range database.Tables {
			value.tables[table.ID] = table
		}
	}
	for _, raw := range document.Rows {
		var stored row.Row
		rowDecoder := json.NewDecoder(bytes.NewReader(raw))
		rowDecoder.UseNumber()
		if rowDecoder.Decode(&stored) != nil {
			return layout{}, nil, exportError(result.CodeValidation, "Wiki Row is invalid")
		}
		key := locatorKey(stored.DatabaseID, stored.TableID, stored.ID)
		value.rows[key] = stored
		value.paths[key] = objectPath(stored.DatabaseID, stored.TableID, stored.ID)
		value.titles[key] = fallbackTitle(stored, value.tables[stored.TableID])
	}
	relations := make([]relation.Relation, 0, len(document.Relations.Current))
	for _, raw := range document.Relations.Current {
		var edge relation.Relation
		if json.Unmarshal(raw, &edge) != nil {
			return layout{}, nil, exportError(result.CodeValidation, "Wiki Relation is invalid")
		}
		if edge.State == relation.StateLive {
			relations = append(relations, edge)
		}
	}
	sort.Slice(relations, func(left, right int) bool { return relations[left].ID < relations[right].ID })
	return value, relations, nil
}

func validateProfile(profile Profile, tables map[string]catalog.Table) error {
	if profile.Version != ProfileVersion || profile.Tables == nil {
		return exportError(result.CodeValidation, "Wiki Export Profile version and Tables are required")
	}
	for tableID, tableProfile := range profile.Tables {
		table, exists := tables[tableID]
		if !exists {
			return exportError(result.CodeValidation, "Wiki Export Profile references an unknown Table")
		}
		known := map[string]bool{}
		for _, column := range table.Columns {
			known[column.ID] = true
		}
		seen := map[string]bool{}
		fields := append([]string{}, tableProfile.BodyFieldIDs...)
		fields = append(fields, tableProfile.PropertyFieldIDs...)
		fields = append(fields, tableProfile.TagFieldIDs...)
		fields = append(fields, tableProfile.HiddenFieldIDs...)
		if tableProfile.TitleFieldID != "" {
			fields = append(fields, tableProfile.TitleFieldID)
		}
		for _, field := range fields {
			if !known[field] {
				return exportError(result.CodeValidation, "Wiki Export Profile references an unknown field")
			}
			if seen[field] {
				return exportError(result.CodeValidation, "Wiki Export Profile field roles are duplicated")
			}
			seen[field] = true
		}
		if duplicated(tableProfile.BodyFieldIDs) || duplicated(tableProfile.PropertyFieldIDs) ||
			duplicated(tableProfile.TagFieldIDs) || duplicated(tableProfile.HiddenFieldIDs) ||
			duplicated(tableProfile.FieldOrder) {
			return exportError(result.CodeValidation, "Wiki Export Profile field list is duplicated")
		}
		rendered := map[string]bool{}
		for _, field := range tableProfile.BodyFieldIDs {
			rendered[field] = true
		}
		for _, field := range tableProfile.PropertyFieldIDs {
			rendered[field] = true
		}
		for _, field := range tableProfile.FieldOrder {
			if !known[field] {
				return exportError(result.CodeValidation, "Wiki Export Profile references an unknown field")
			}
			if !rendered[field] {
				return exportError(result.CodeValidation, "Wiki Export Profile orders a field without a rendered role")
			}
		}
		if len(tableProfile.FieldOrder) > 0 && len(tableProfile.FieldOrder) != len(rendered) {
			return exportError(result.CodeValidation, "Wiki Export Profile field order is incomplete")
		}
	}
	return nil
}

func renderPage(
	stored row.Row,
	database catalog.Database,
	table catalog.Table,
	profile TableProfile,
	configured bool,
	value layout,
	relations []relation.Relation,
) []byte {
	title := value.titles[locatorKey(stored.DatabaseID, stored.TableID, stored.ID)]
	if configured && profile.TitleFieldID != "" {
		if text, ok := stored.Values[profile.TitleFieldID].(string); ok && strings.TrimSpace(text) != "" {
			title = strings.TrimSpace(text)
		}
	}
	var output strings.Builder
	output.WriteString("---\n")
	writeYAML(&output, "memora_version", Version)
	writeYAML(&output, "row_id", stored.ID)
	writeYAML(&output, "database_id", stored.DatabaseID)
	writeYAML(&output, "database", database.Name)
	writeYAML(&output, "table_id", stored.TableID)
	writeYAML(&output, "table", table.Name)
	fmt.Fprintf(&output, "revision: %d\n", stored.Revision)
	writeTags(&output, stored, profile.TagFieldIDs)
	output.WriteString("---\n\n# ")
	output.WriteString(markdownLine(title))
	output.WriteString("\n")

	bodyFields, propertyFields := selectedFields(table, profile, configured)
	for _, column := range bodyFields {
		output.WriteString("\n## ")
		output.WriteString(markdownLine(column.Name))
		output.WriteString("\n\n")
		output.WriteString(markdownValue(stored.Values[column.ID]))
		output.WriteString("\n")
	}
	if len(propertyFields) > 0 {
		output.WriteString("\n## Properties\n")
		for _, column := range propertyFields {
			output.WriteString("\n- **")
			output.WriteString(markdownLine(column.Name))
			output.WriteString("**: ")
			output.WriteString(markdownValue(stored.Values[column.ID]))
		}
		output.WriteString("\n")
	}
	relationLines := renderRelations(stored, value, relations)
	if len(relationLines) > 0 {
		output.WriteString("\n## Relations\n")
		for _, line := range relationLines {
			output.WriteString("\n- ")
			output.WriteString(line)
		}
		output.WriteString("\n")
	}
	return []byte(output.String())
}

func selectedFields(table catalog.Table, profile TableProfile, configured bool) ([]catalog.Column, []catalog.Column) {
	byID := map[string]catalog.Column{}
	for _, column := range table.Columns {
		byID[column.ID] = column
	}
	if !configured {
		body := []catalog.Column{}
		titleSkipped := false
		for _, column := range table.Columns {
			if !titleSkipped && column.Type == "TEXT" {
				titleSkipped = true
				continue
			}
			body = append(body, column)
		}
		return body, nil
	}
	order := profile.FieldOrder
	if len(order) == 0 {
		order = append(append([]string{}, profile.BodyFieldIDs...), profile.PropertyFieldIDs...)
	}
	body, properties := []catalog.Column{}, []catalog.Column{}
	for _, id := range order {
		if contains(profile.BodyFieldIDs, id) {
			body = append(body, byID[id])
		} else if contains(profile.PropertyFieldIDs, id) {
			properties = append(properties, byID[id])
		}
	}
	return body, properties
}

func renderRelations(stored row.Row, value layout, relations []relation.Relation) []string {
	current := locatorKey(stored.DatabaseID, stored.TableID, stored.ID)
	lines := []string{}
	for _, edge := range relations {
		source := locatorKey(edge.Source.DatabaseID, edge.Source.TableID, edge.Source.RowID)
		target := locatorKey(edge.Target.DatabaseID, edge.Target.TableID, edge.Target.RowID)
		other, direction := "", "outgoing"
		switch current {
		case source:
			other = target
		case target:
			other, direction = source, "incoming"
		default:
			continue
		}
		otherRow, exists := value.rows[other]
		if !exists || otherRow.State != row.StateLive {
			continue
		}
		line := "relation_type: " + markdownLine(edge.Type) + " · direction: " + direction +
			" · target: [[" + value.paths[other] + "|" + wikilinkAlias(value.titles[other]) + "]]"
		if strings.TrimSpace(edge.Description) != "" {
			line += " · description: " + markdownLine(edge.Description)
		}
		lines = append(lines, line)
	}
	return lines
}

func writeIncremental(root string, manifest Manifest, contents map[string][]byte) error {
	if strings.TrimSpace(root) == "" {
		return exportError(result.CodeValidation, "Wiki export root is required")
	}
	if err := security.ValidateOutputRoot(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return exportError(result.CodeInternal, "Wiki export root could not be created")
	}
	if err := security.ValidateOutputRoot(root); err != nil {
		return err
	}
	previous, err := readManifest(root)
	if err != nil {
		return err
	}
	currentPaths := map[string]bool{}
	for path, content := range contents {
		if !managedPath(path) {
			return exportError(result.CodeValidation, "Wiki object path is unsafe")
		}
		currentPaths[path] = true
		absolute, err := security.SecureJoin(root, filepath.FromSlash(path))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			return exportError(result.CodeInternal, "Wiki object directory could not be created")
		}
		if err := writeIfChanged(absolute, content); err != nil {
			return exportError(result.CodeInternal, "Wiki object could not be written")
		}
	}
	for _, object := range previous.Objects {
		if !managedPath(object.Path) {
			return exportError(result.CodeValidation, "previous Wiki manifest contains an unsafe path")
		}
		if !currentPaths[object.Path] {
			absolute, err := security.SecureJoin(root, filepath.FromSlash(object.Path))
			if err != nil {
				return err
			}
			if err := os.Remove(absolute); err != nil && !os.IsNotExist(err) {
				return exportError(result.CodeInternal, "stale Wiki object could not be removed")
			}
		}
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return exportError(result.CodeInternal, "Wiki manifest could not be encoded")
	}
	encoded = append(encoded, '\n')
	manifestPath, err := security.SecureJoin(root, manifestName)
	if err != nil {
		return err
	}
	if err := writeIfChanged(manifestPath, encoded); err != nil {
		return exportError(result.CodeInternal, "Wiki manifest could not be written")
	}
	return nil
}

func readManifest(root string) (Manifest, error) {
	path, err := security.SecureJoin(root, manifestName)
	if err != nil {
		return Manifest{}, err
	}
	encoded, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Manifest{Version: Version, Objects: []Object{}}, nil
	}
	if err != nil {
		return Manifest{}, exportError(result.CodeInternal, "previous Wiki manifest could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value Manifest
	if decoder.Decode(&value) != nil || value.Version != Version || value.Objects == nil {
		return Manifest{}, exportError(result.CodeValidation, "previous Wiki manifest is invalid")
	}
	return value, nil
}

func writeIfChanged(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".memora-wiki-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func objectPath(databaseID, tableID, rowID string) string {
	return safeSegment(databaseID) + "/" + safeSegment(tableID) + "/" + safeSegment(rowID) + ".md"
}

func safeSegment(value string) string {
	var output strings.Builder
	changed := false
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			output.WriteRune(character)
		} else {
			output.WriteByte('_')
			changed = true
		}
	}
	if output.Len() == 0 {
		changed = true
		output.WriteString("object")
	}
	if changed {
		sum := sha256.Sum256([]byte(value))
		output.WriteString("--")
		output.WriteString(hex.EncodeToString(sum[:4]))
	}
	return output.String()
}

func managedPath(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path {
		return false
	}
	parts := strings.Split(path, "/")
	return len(parts) == 3 && parts[0] != ".." && parts[1] != ".." &&
		parts[2] != ".." && strings.HasSuffix(parts[2], ".md")
}

func fallbackTitle(stored row.Row, table catalog.Table) string {
	for _, column := range table.Columns {
		if column.Type != "TEXT" {
			continue
		}
		if value, ok := stored.Values[column.ID].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return stored.ID
}

func markdownValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func markdownLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func wikilinkAlias(value string) string {
	return strings.ReplaceAll(markdownLine(value), "|", "¦")
}

func writeYAML(output *strings.Builder, name, value string) {
	encoded, _ := json.Marshal(value)
	output.WriteString(name)
	output.WriteString(": ")
	output.Write(encoded)
	output.WriteByte('\n')
}

func writeTags(output *strings.Builder, stored row.Row, fieldIDs []string) {
	tags := make([]string, 0, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		value, ok := stored.Values[fieldID].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		tags = append(tags, strings.TrimSpace(value))
	}
	if len(tags) == 0 {
		return
	}
	encoded, _ := json.Marshal(tags)
	output.WriteString("tags: ")
	output.Write(encoded)
	output.WriteByte('\n')
}

func locatorKey(databaseID, tableID, rowID string) string {
	return databaseID + "\x00" + tableID + "\x00" + rowID
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func duplicated(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func exportError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
