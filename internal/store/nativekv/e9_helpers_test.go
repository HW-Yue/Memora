package nativekv

import "os"

// removeIndex deletes the Tree beside a log, which is the extreme form of the
// Tree being behind: everything the log holds has to be replayed.
func removeIndex(path string) error { return os.RemoveAll(path + ".index") }
