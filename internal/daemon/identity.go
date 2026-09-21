package daemon

// Identity is what a running daemon is. A client asks the daemon for this — never
// a file on disk: an instance directory outlives the process that wrote it, so a
// version recorded there is stale after every restart and after every binary
// swapped in place. Whoever answers is the truth.
type Identity struct {
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuiltAt        string `json:"built_at"`
	EngineProtocol int    `json:"engine_protocol"`
}
