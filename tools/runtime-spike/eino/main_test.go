package main

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/compose"
)

func TestGraphDispatchesToolAndResumesCheckpoint(t *testing.T) {
	runnable, err := compile(context.Background(), &memoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runnable.Invoke(context.Background(), "question", compose.WithCheckPointID("test"))
	info, interrupted := compose.ExtractInterruptInfo(err)
	if !interrupted || len(info.InterruptContexts) != 1 {
		t.Fatalf("Invoke() error = %v, info = %#v", err, info)
	}
	ctx := compose.Resume(context.Background(), info.InterruptContexts[0].ID)
	got, err := runnable.Invoke(ctx, "question", compose.WithCheckPointID("test"))
	if err != nil {
		t.Fatal(err)
	}
	want := "question|bootstrap|tool_call:execute_msql|tool_result|final"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestGraphPropagatesCancellation(t *testing.T) {
	runnable, err := compile(context.Background(), &memoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runnable.Invoke(ctx, "question", compose.WithForceNewRun()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Invoke() error = %v", err)
	}
}
