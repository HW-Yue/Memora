package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
)

// runInstance destroys an instance the only way that is actually safe: by
// deleting its directory, explicitly, after naming which directory that is.
//
// The product needs this because an Agent that mis-modelled something has to be
// able to clean up, and the alternative — an irreversible DROP of a database or
// a table — is a much larger and much more dangerous thing to build. Deleting a
// test instance's directory is the smallest operation that gives the same
// outcome, and it is outside the language on purpose: nobody should be able to
// drop a database from inside a query.
func runInstance(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "destroy" {
		return usageError(stderr, "instance requires destroy")
	}
	dataDir := ""
	authorized := false
	rest := args[1:]
	for index := 0; index < len(rest); index++ {
		switch rest[index] {
		case "--yes":
			authorized = true
		case "--data-dir":
			if index+1 >= len(rest) {
				return usageError(stderr, "--data-dir requires a path")
			}
			dataDir = rest[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown option for instance destroy: %q", rest[index]))
		}
	}
	if !authorized {
		return usageError(stderr, "removing an instance is irreversible; rerun with --yes after the user approves")
	}
	if dataDir == "" {
		return usageError(stderr, "instance destroy requires --data-dir <absolute path>")
	}
	if !filepath.IsAbs(dataDir) {
		return usageError(stderr, "--data-dir must be an absolute path")
	}
	// Only a directory that identifies itself as an instance: a typo must not be
	// able to delete something else.
	metadata, err := instance.Read(dataDir)
	if err != nil {
		return commandError(stderr, "read instance", err)
	}
	if state, inspectErr := daemon.Inspect(dataDir); inspectErr == nil && state.Running {
		if err := daemon.Stop(context.Background(), dataDir); err != nil {
			return commandError(stderr, "stop the instance's daemon before removing it", err)
		}
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return commandError(stderr, "remove instance", err)
	}
	return writeText(stdout, stderr,
		fmt.Sprintf("removed Memora instance %s at %s\n", metadata.InstanceID, dataDir))
}
