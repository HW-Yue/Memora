package answercorpus

func (manifest Manifest) BlindTasks() ([]BlindTask, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	tasks := make([]BlindTask, 0, len(manifest.Cases))
	for _, testCase := range manifest.Cases {
		tasks = append(tasks, blindTask(manifest, testCase))
	}
	return tasks, nil
}

func (bundle Bundle) BlindTask(caseID string) (BlindTask, error) {
	if err := bundle.Validate(); err != nil {
		return BlindTask{}, err
	}
	for _, testCase := range bundle.Manifest.Cases {
		if testCase.ID != caseID {
			continue
		}
		return blindTask(bundle.Manifest, testCase), nil
	}
	return BlindTask{}, corpusError("case %q was not found", caseID)
}

func blindTask(manifest Manifest, testCase Case) BlindTask {
	return BlindTask{
		Version: BlindTaskVersion, CorpusID: manifest.CorpusID,
		CorpusRevision: manifest.Revision, CaseID: testCase.ID,
		SnapshotID:          manifest.Fixture.SnapshotID,
		SnapshotSHA256:      manifest.Fixture.SnapshotSHA256,
		Question:            testCase.Question,
		AuthorizedDatabases: append([]string(nil), testCase.AuthorizedDatabases...),
		Budget:              testCase.Budget,
	}
}
