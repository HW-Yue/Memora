package main

import (
	"context"
	"errors"
	"testing"
)

func TestLoopDispatchesToolAndResumesCheckpoint(t *testing.T) {
	store := &memoryStore{}
	if _, err := run(context.Background(), store, "question", false); !errors.Is(err, errInterrupted) {
		t.Fatalf("first run error = %v", err)
	}
	got, err := run(context.Background(), store, "ignored", true)
	if err != nil {
		t.Fatal(err)
	}
	want := "question|bootstrap|tool_call:execute_msql|tool_result|final"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLoopPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, &memoryStore{}, "question", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v", err)
	}
}
