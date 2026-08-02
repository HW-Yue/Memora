package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/compose"
)

const iterations = 10_000

type memoryStore struct {
	mu    sync.Mutex
	value []byte
}

func (store *memoryStore) Get(_ context.Context, _ string) ([]byte, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.value) == 0 {
		return nil, false, nil
	}
	return append([]byte(nil), store.value...), true, nil
}

func (store *memoryStore) Set(_ context.Context, _ string, value []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.value = append(store.value[:0], value...)
	return nil
}

func checkedAppend(suffix string) *compose.Lambda {
	return compose.InvokableLambda(func(ctx context.Context, input string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return input + suffix, nil
	})
}

func compile(ctx context.Context, store *memoryStore) (compose.Runnable[string, string], error) {
	graph := compose.NewGraph[string, string]()
	for _, node := range []struct {
		name   string
		suffix string
	}{
		{"bootstrap", "|bootstrap"},
		{"provider_tool_call", "|tool_call:execute_msql"},
		{"tool", "|tool_result"},
		{"provider_final", "|final"},
	} {
		if err := graph.AddLambdaNode(node.name, checkedAppend(node.suffix)); err != nil {
			return nil, err
		}
	}
	for _, edge := range [][2]string{
		{compose.START, "bootstrap"},
		{"bootstrap", "provider_tool_call"},
		{"provider_tool_call", "tool"},
		{"tool", "provider_final"},
		{"provider_final", compose.END},
	} {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	return graph.Compile(ctx,
		compose.WithGraphName("query"),
		compose.WithCheckPointStore(store),
		compose.WithInterruptAfterNodes([]string{"tool"}),
	)
}

func run(ctx context.Context, runnable compose.Runnable[string, string]) (string, error) {
	var output string
	for index := 0; index < iterations; index++ {
		_, err := runnable.Invoke(ctx, "question", compose.WithCheckPointID("benchmark"), compose.WithForceNewRun())
		info, interrupted := compose.ExtractInterruptInfo(err)
		if !interrupted || len(info.InterruptContexts) != 1 {
			return "", fmt.Errorf("interrupt: %w", err)
		}
		resume := compose.Resume(ctx, info.InterruptContexts[0].ID)
		output, err = runnable.Invoke(resume, "question", compose.WithCheckPointID("benchmark"))
		if err != nil {
			return "", err
		}
	}
	return output, nil
}

func main() {
	runnable, err := compile(context.Background(), &memoryStore{})
	if err != nil {
		panic(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = runnable.Invoke(cancelled, "question", compose.WithForceNewRun()); !errors.Is(err, context.Canceled) {
		panic("cancellation was not propagated")
	}
	output, err := run(context.Background(), runnable)
	if err != nil {
		panic(err)
	}
	fmt.Println(output)
}
