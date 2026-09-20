package executor_test

// code reports an executor error's stable machine code, which is what most of
// this package's tests assert on rather than the human-facing message.
func code(err error) string {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		return stable.StableCode()
	}
	return ""
}
