package executor_test

// code reports an executor error's stable machine code, which is what most of
// this package's tests assert on rather than the human-facing message. It used
// to live in wiki_export_test.go; that file went away with the Wiki export
// implementation on 2026-09-02, so the helper moved here.
func code(err error) string {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		return stable.StableCode()
	}
	return ""
}
