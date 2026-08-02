package main

import (
	"context"
	"os"

	"github.com/HW-Yue/Memora/internal/cli"
	"github.com/cloudwego/eino/compose"
)

var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

func main() {
	// Keep an actually invokable Compose graph reachable so the linker includes
	// the runtime code measured by this integration-size candidate.
	if os.Getenv("MEMORA_F179_RUN_EINO_GRAPH") == "1" {
		graph := compose.NewGraph[string, string]()
		_ = graph.AddLambdaNode("identity", compose.InvokableLambda(
			func(_ context.Context, input string) (string, error) { return input, nil },
		))
		_ = graph.AddEdge(compose.START, "identity")
		_ = graph.AddEdge("identity", compose.END)
		runnable, err := graph.Compile(context.Background())
		if err != nil {
			panic(err)
		}
		if _, err = runnable.Invoke(context.Background(), "probe"); err != nil {
			panic(err)
		}
	}
	code := cli.Run(os.Args[1:], os.Stdout, os.Stderr, cli.BuildInfo{
		Version: version,
		Commit:  commit,
		BuiltAt: builtAt,
	})
	os.Exit(code)
}
