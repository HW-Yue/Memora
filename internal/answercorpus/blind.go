package answercorpus

func (manifest Manifest) BlindTasks() ([]BlindTask, error) {
	return nil, corpusError("public blind task projection is not implemented")
}

func (bundle Bundle) BlindTask(caseID string) (BlindTask, error) {
	if err := bundle.Validate(); err != nil {
		return BlindTask{}, err
	}
	for _, testCase := range bundle.Manifest.Cases {
		if testCase.ID != caseID {
			continue
		}
		return BlindTask{
			Version: BlindTaskVersion, CorpusID: bundle.Manifest.CorpusID,
			CorpusRevision: bundle.Manifest.Revision, CaseID: testCase.ID,
			SnapshotID:          bundle.Manifest.Fixture.SnapshotID,
			SnapshotSHA256:      bundle.Manifest.Fixture.SnapshotSHA256,
			Question:            testCase.Question,
			AuthorizedDatabases: append([]string(nil), testCase.AuthorizedDatabases...),
			Budget:              testCase.Budget,
		}, nil
	}
	return BlindTask{}, corpusError("case %q was not found", caseID)
}
