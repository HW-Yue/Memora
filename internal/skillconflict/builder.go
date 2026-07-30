package skillconflict

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

func Build(request Request) (View, error) {
	if blank(request.ID) || blank(request.Database) || blank(request.Table) {
		return View{}, conflictError(result.CodeValidation, "conflict identity and scope are required")
	}
	allowed, err := validateAuthorization(request.AuthorizedDatabases)
	if err != nil {
		return View{}, err
	}
	if !allowed[canonical(request.Database)] {
		return View{}, conflictError(result.CodePermissionDenied, "database %q is outside the authorized scope", request.Database)
	}
	proposal, err := cloneProposal(request.Proposal)
	if err != nil {
		return View{}, err
	}
	if len(request.Existing) == 0 || len(request.Existing) > 10 {
		return View{}, conflictError(result.CodeValidation, "conflict view requires 1 to 10 existing Rows")
	}
	existing := make([]RowSnapshot, 0, len(request.Existing))
	differences := make([]RowDifference, 0, len(request.Existing))
	seen := map[string]bool{}
	for index, snapshot := range request.Existing {
		if blank(snapshot.RowID) || snapshot.Revision == 0 || seen[snapshot.RowID] {
			return View{}, conflictError(result.CodeValidation, "existing Row %d has invalid identity or revision", index)
		}
		seen[snapshot.RowID] = true
		cloned, err := cloneSnapshot(snapshot)
		if err != nil {
			return View{}, err
		}
		fields := compareValues(proposal.Values, cloned.Values)
		if len(fields) == 0 {
			return View{}, conflictError(result.CodeValidation, "existing Row %q has no visible difference from the proposal", snapshot.RowID)
		}
		existing = append(existing, cloned)
		differences = append(differences, RowDifference{
			RowID: cloned.RowID, Revision: cloned.Revision, Fields: fields,
		})
	}
	return View{
		Version: ViewVersion, ID: request.ID, Database: request.Database, Table: request.Table,
		AuthorizedDatabases: append([]string{}, request.AuthorizedDatabases...),
		Proposal:            proposal, Existing: existing, Differences: differences,
	}, nil
}

func cloneProposal(proposal Proposal) (Proposal, error) {
	if err := validateProvenance(proposal.Source, "proposal"); err != nil {
		return Proposal{}, err
	}
	values, err := cloneValues(proposal.Values, "proposal")
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Values: values, Source: proposal.Source}, nil
}

func cloneSnapshot(snapshot RowSnapshot) (RowSnapshot, error) {
	if err := validateProvenance(snapshot.Source, "existing Row "+snapshot.RowID); err != nil {
		return RowSnapshot{}, err
	}
	values, err := cloneValues(snapshot.Values, "existing Row "+snapshot.RowID)
	if err != nil {
		return RowSnapshot{}, err
	}
	snapshot.Values = values
	return snapshot, nil
}

func cloneValues(values map[string]any, label string) (map[string]any, error) {
	if len(values) == 0 {
		return nil, conflictError(result.CodeValidation, "%s requires complete values", label)
	}
	for field := range values {
		if blank(field) {
			return nil, conflictError(result.CodeValidation, "%s contains an empty field name", label)
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, conflictError(result.CodeValidation, "%s values are not valid JSON: %v", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var cloned map[string]any
	if err := decoder.Decode(&cloned); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, conflictError(result.CodeInternal, "clone %s values", label)
	}
	return cloned, nil
}

func compareValues(proposed, existing map[string]any) []FieldDifference {
	fieldSet := make(map[string]bool, len(proposed)+len(existing))
	for field := range proposed {
		fieldSet[field] = true
	}
	for field := range existing {
		fieldSet[field] = true
	}
	fields := make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	differences := make([]FieldDifference, 0, len(fields))
	for _, field := range fields {
		proposedValue, proposedPresent := proposed[field]
		existingValue, existingPresent := existing[field]
		if proposedPresent == existingPresent && equalJSON(proposedValue, existingValue) {
			continue
		}
		differences = append(differences, FieldDifference{
			Field:    field,
			Proposed: ComparedValue{Present: proposedPresent, Value: proposedValue},
			Existing: ComparedValue{Present: existingPresent, Value: existingValue},
		})
	}
	return differences
}

func equalJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func validateProvenance(provenance Provenance, label string) error {
	if blank(provenance.Actor) || blank(provenance.SourceEventID) || blank(provenance.Reason) {
		return conflictError(result.CodeValidation, "%s provenance is required", label)
	}
	return nil
}

func validateAuthorization(databases []string) (map[string]bool, error) {
	if len(databases) == 0 {
		return nil, conflictError(result.CodeValidation, "authorized databases are required")
	}
	allowed := make(map[string]bool, len(databases))
	for _, database := range databases {
		normalized := canonical(database)
		if normalized == "" || allowed[normalized] {
			return nil, conflictError(result.CodeValidation, "authorized databases must be non-empty and deduplicated")
		}
		allowed[normalized] = true
	}
	return allowed, nil
}

func blank(value string) bool       { return strings.TrimSpace(value) == "" }
func canonical(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
