package result

func (envelope Envelope) Validate() error {
	if envelope.Version != Version {
		return invalid("version is %q", envelope.Version)
	}
	if envelope.RequestID == "" {
		return invalid("request_id is empty")
	}
	if envelope.Results == nil || envelope.Warnings == nil {
		return invalid("results and warnings must be arrays")
	}
	if err := validateError(envelope.Error, nil); err != nil {
		return err
	}
	if envelope.Error != nil && len(envelope.Results) != 0 {
		return invalid("request error cannot contain statement results")
	}
	for index, result := range envelope.Results {
		if err := validateStatement(index, result); err != nil {
			return err
		}
		if result.Truncated && !envelope.Truncated {
			return invalid("result %d is truncated but envelope is not", index)
		}
	}
	if err := validateNotices(envelope.Warnings); err != nil {
		return err
	}
	if envelope.OK != envelope.computedOK() {
		return invalid("ok is inconsistent with contained errors")
	}
	return nil
}

func validateStatement(position int, result StatementResult) error {
	if result.Index != position {
		return invalid("result index %d is at position %d", result.Index, position)
	}
	if result.Statement == "" || result.Source == "" {
		return invalid("result %d has empty statement metadata", position)
	}
	if result.Columns == nil || result.Rows == nil || result.Warnings == nil {
		return invalid("result %d columns, rows, and warnings must be arrays", position)
	}
	if err := validateNotices(result.Warnings); err != nil {
		return err
	}
	if result.Page != nil {
		if result.Status != StatusSucceeded {
			return invalid("result %d failed with list page metadata", position)
		}
		if result.Page.Version != ListPageVersion || result.Page.Limit == 0 || result.Page.Snapshot == "" {
			return invalid("result %d has invalid list page metadata", position)
		}
		if result.Page.Truncated != result.Truncated || result.Page.NextCursor != result.NextCursor {
			return invalid("result %d list page continuation is inconsistent", position)
		}
		if result.Page.Truncated != (result.Page.NextCursor != "") {
			return invalid("result %d list page truncation has no valid continuation", position)
		}
	}
	switch result.Status {
	case StatusSucceeded:
		if result.Error != nil {
			return invalid("successful result %d contains an error", position)
		}
	case StatusFailed, StatusSkipped, StatusRolledBack:
		if result.Error == nil {
			return invalid("%s result %d has no error", result.Status, position)
		}
	default:
		return invalid("result %d has unknown status %q", position, result.Status)
	}
	return validateError(result.Error, &position)
}

func validateError(resultError *ResultError, statementIndex *int) error {
	if resultError == nil {
		return nil
	}
	if !IsRegisteredCode(resultError.Code) || resultError.Message == "" {
		return invalid("error has unregistered code or empty message")
	}
	if statementIndex == nil {
		if resultError.StatementIndex != nil {
			return invalid("request error has a statement index")
		}
	} else if resultError.StatementIndex == nil || *resultError.StatementIndex != *statementIndex {
		return invalid("statement error index is missing or inconsistent")
	}
	return nil
}

func validateNotices(notices []Notice) error {
	for _, notice := range notices {
		if !IsRegisteredCode(notice.Code) || notice.Message == "" {
			return invalid("warning has unregistered code or empty message")
		}
	}
	return nil
}

func (envelope Envelope) computedOK() bool {
	if envelope.Error != nil {
		return false
	}
	for _, result := range envelope.Results {
		if result.Status != StatusSucceeded {
			return false
		}
	}
	return true
}

func (envelope Envelope) normalized() Envelope {
	if envelope.Results == nil {
		envelope.Results = []StatementResult{}
	}
	if envelope.Warnings == nil {
		envelope.Warnings = []Notice{}
	}
	for index := range envelope.Results {
		if envelope.Results[index].Columns == nil {
			envelope.Results[index].Columns = []Column{}
		}
		if envelope.Results[index].Rows == nil {
			envelope.Results[index].Rows = []Row{}
		}
		if envelope.Results[index].Warnings == nil {
			envelope.Results[index].Warnings = []Notice{}
		}
	}
	return envelope
}
