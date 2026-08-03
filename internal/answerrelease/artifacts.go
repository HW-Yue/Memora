package answerrelease

import "errors"

var ErrInvalidArtifacts = errors.New("query release artifacts are invalid")

func LoadEvidence([]string, []string) ([]Evidence, error) {
	return nil, ErrInvalidArtifacts
}

func LoadReport(string) (Report, error) {
	return Report{}, ErrInvalidArtifacts
}
