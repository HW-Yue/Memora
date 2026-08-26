package semantichealth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	requestBucket       = "semantic_health.requests.v1"
	maximumRowsPerTable = 1000
	maximumIssues       = 512
	staleDescriptionGap = 30 * 24 * time.Hour
)

type Service struct {
	source Source
	store  store.Store
}

type requestRecord struct {
	Digest  string  `json:"digest"`
	Receipt Receipt `json:"receipt"`
}

func New(source Source, database store.Store) *Service {
	return &Service{source: source, store: database}
}

func (service *Service) Report(ctx context.Context) (Report, error) {
	if service == nil || service.source == nil {
		return Report{}, healthError(result.CodeInternal, "health source is not configured")
	}
	databases, err := service.source.ShowDatabases(ctx)
	if err != nil {
		return Report{}, healthError(result.CodeInternal, "read catalog: %v", err)
	}
	sort.Slice(databases, func(left, right int) bool { return databases[left].ID < databases[right].ID })
	issues := []Issue{}
	truncated := false
	tablesByID := map[string]*healthTableState{}
	for _, database := range databases {
		tables := append([]catalog.Table{}, database.Tables...)
		sort.Slice(tables, func(left, right int) bool { return tables[left].ID < tables[right].ID })
		for _, table := range tables {
			issues = append(issues, synonymousColumnIssues(database, table)...)
			rows, more, listErr := service.source.ListPage(ctx, database.Name, table.Name, maximumRowsPerTable)
			if listErr != nil {
				return Report{}, healthError(result.CodeInternal, "list %s.%s Rows: %v", database.Name, table.Name, listErr)
			}
			truncated = truncated || more
			state := &healthTableState{database: database, table: table, rows: map[string]row.Row{}, rowsComplete: !more}
			for _, value := range rows {
				if value.State == row.StateLive {
					state.rows[value.ID] = value
				}
			}
			tablesByID[table.ID] = state
			issues = append(issues, duplicateRowIssues(database, table, rows)...)
			if issue, found := staleDescriptionIssue(database, table, rows); found {
				issues = append(issues, issue)
			}
		}
	}
	routeIssues, err := scanRoutes(ctx, service.source, tablesByID)
	if err != nil {
		return Report{}, err
	}
	issues = append(issues, routeIssues...)
	sortIssues(issues)
	if len(issues) > maximumIssues {
		issues, truncated = issues[:maximumIssues], true
	}
	report := Report{
		Version: ReportVersion, Status: "healthy", Issues: issues,
		IssueCount: len(issues), Truncated: truncated,
	}
	for _, issue := range issues {
		if issue.AutoFix {
			report.AutoFixCount++
		}
	}
	if len(issues) > 0 || truncated {
		report.Status = "attention"
	}
	report.Hash = reportDigest(report)
	return report, nil
}

func (service *Service) Maintain(ctx context.Context, request Request) (Receipt, error) {
	if service == nil || service.source == nil || service.store == nil {
		return Receipt{}, healthError(result.CodeInternal, "maintenance service is not configured")
	}
	if err := validateRequest(request); err != nil {
		return Receipt{}, err
	}
	digest := valueDigest(request)
	if receipt, found, err := service.loadRequest(ctx, request.RequestID, digest); err != nil {
		return Receipt{}, err
	} else if found {
		receipt.Replayed = true
		return receipt, nil
	}
	report, err := service.Report(ctx)
	if err != nil {
		return Receipt{}, err
	}
	if request.ExpectedReportHash != report.Hash {
		return Receipt{}, healthError(result.CodeRevisionConflict, "semantic health report changed")
	}
	byID := make(map[string]Issue, len(report.Issues))
	for _, issue := range report.Issues {
		byID[issue.ID] = issue
	}
	selected := make([]Issue, len(request.IssueIDs))
	seen := map[string]bool{}
	for index, id := range request.IssueIDs {
		issue, found := byID[id]
		if !found || seen[id] {
			return Receipt{}, healthError(result.CodeValidation, "maintenance issue IDs must exist and be deduplicated")
		}
		seen[id] = true
		if !issue.AutoFix {
			return Receipt{}, healthError(result.CodePermissionDenied, "issue %q requires review and cannot be auto-fixed", id)
		}
		selected[index] = issue
	}
	receipt := Receipt{
		Version: ReceiptVersion, RequestID: request.RequestID, Status: "noop",
		ReportHash: report.Hash, Actions: []Action{},
	}
	if len(receipt.Actions) > 0 {
		receipt.Status = "completed"
	}
	if err := service.saveRequest(ctx, request.RequestID, digest, receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func synonymousColumnIssues(database catalog.Database, table catalog.Table) []Issue {
	groups := map[string][]string{}
	// An archived Column is invisible, so proposing to merge it with a live one
	// would be advice the user cannot act on.
	for _, column := range catalog.LiveColumns(table.Columns) {
		key := string(column.Type) + "\x00" + stringBoolean(column.Nullable) + "\x00" + normalizeWords(column.Purpose)
		groups[key] = append(groups[key], column.ID)
	}
	issues := []Issue{}
	for _, ids := range groups {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		issues = append(issues, newIssue(Issue{
			Kind: KindSynonymousColumns, Severity: SeverityReviewRequired, Action: "review_schema",
			DatabaseID: database.ID, Database: database.Name, TableID: table.ID, Table: table.Name,
			ObjectIDs: ids, Count: len(ids),
		}))
	}
	return issues
}

func duplicateRowIssues(database catalog.Database, table catalog.Table, rows []row.Row) []Issue {
	groups := map[string][]string{}
	for _, value := range rows {
		if value.State != row.StateLive {
			continue
		}
		groups[valueDigest(value.Values)] = append(groups[valueDigest(value.Values)], value.ID)
	}
	issues := []Issue{}
	for _, ids := range groups {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		issues = append(issues, newIssue(Issue{
			Kind: KindDuplicateRow, Severity: SeverityReviewRequired, Action: "review_merge",
			DatabaseID: database.ID, Database: database.Name, TableID: table.ID, Table: table.Name,
			ObjectIDs: ids, Count: len(ids),
		}))
	}
	return issues
}

func staleDescriptionIssue(database catalog.Database, table catalog.Table, rows []row.Row) (Issue, bool) {
	latest := time.Time{}
	for _, value := range rows {
		if value.State == row.StateLive && value.UpdatedAt.After(latest) {
			latest = value.UpdatedAt
		}
	}
	if latest.IsZero() || !latest.After(table.UpdatedAt.Add(staleDescriptionGap)) {
		return Issue{}, false
	}
	return newIssue(Issue{
		Kind: KindStaleDescription, Severity: SeverityReviewRequired, Action: "review_description",
		DatabaseID: database.ID, Database: database.Name, TableID: table.ID, Table: table.Name,
		ObjectIDs: []string{table.ID}, Count: 1,
	}), true
}

func validateRequest(request Request) error {
	if request.Version != RequestVersion || !bounded(request.RequestID, 200) ||
		!bounded(request.Actor, 200) || !validDigest(request.ExpectedReportHash) {
		return healthError(result.CodeValidation, "maintenance version, identity, actor, and report hash are required")
	}
	if request.Trigger != TriggerCheckpoint && request.Trigger != TriggerUserRequest {
		return healthError(result.CodeValidation, "maintenance trigger is unsupported")
	}
	if len(request.IssueIDs) > 128 {
		return healthError(result.CodeValidation, "maintenance issue selection exceeds 128")
	}
	return nil
}

func (service *Service) loadRequest(ctx context.Context, id, digest string) (Receipt, bool, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Receipt{}, false, healthError(result.CodeInternal, "begin maintenance lookup: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, requestBucket, id)
	if errors.Is(err, store.ErrNotFound) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, healthError(result.CodeInternal, "read maintenance request: %v", err)
	}
	var record requestRecord
	if json.Unmarshal(encoded, &record) != nil {
		return Receipt{}, false, healthError(result.CodeInternal, "maintenance request record is corrupt")
	}
	if record.Digest != digest {
		return Receipt{}, false, healthError(result.CodeRevisionConflict, "request ID was reused with different content")
	}
	return record.Receipt, true, nil
}

func (service *Service) saveRequest(ctx context.Context, id, digest string, receipt Receipt) error {
	receipt.Replayed = false
	encoded, _ := json.Marshal(requestRecord{Digest: digest, Receipt: receipt})
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return healthError(result.CodeInternal, "begin maintenance receipt: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Put(ctx, requestBucket, id, encoded); err != nil {
		return healthError(result.CodeInternal, "write maintenance receipt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return healthError(result.CodeInternal, "commit maintenance receipt: %v", err)
	}
	return nil
}

func newIssue(issue Issue) Issue {
	if issue.ObjectIDs == nil {
		issue.ObjectIDs = []string{}
	}
	sort.Strings(issue.ObjectIDs)
	identity := strings.Join([]string{
		string(issue.Kind), issue.DatabaseID, issue.TableID, issue.RowID,
		strings.Join(issue.ObjectIDs, ","),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	issue.ID = "health_" + hex.EncodeToString(sum[:8])
	return issue
}

func sortIssues(issues []Issue) {
	sort.Slice(issues, func(left, right int) bool {
		l, r := issues[left], issues[right]
		lk := string(l.Kind) + "\x00" + l.DatabaseID + "\x00" + l.TableID + "\x00" + strings.Join(l.ObjectIDs, "\x00") + "\x00" + l.ID
		rk := string(r.Kind) + "\x00" + r.DatabaseID + "\x00" + r.TableID + "\x00" + strings.Join(r.ObjectIDs, "\x00") + "\x00" + r.ID
		return lk < rk
	})
}

func reportDigest(report Report) string {
	report.Hash = ""
	return valueDigest(report)
}

func valueDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeWords(value string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	}), " ")
}

func stringBoolean(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func choose(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func bounded(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]rune(value)) <= maximum
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func hasCode(err error, code result.Code) bool {
	stable, ok := err.(interface{ StableCode() string })
	return ok && result.Code(stable.StableCode()) == code
}
