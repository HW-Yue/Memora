package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const iterations = 10_000

var errInterrupted = errors.New("interrupted")

type checkpoint struct {
	Step  uint8  `json:"step"`
	Value string `json:"value"`
}

type memoryStore struct {
	value []byte
}

func (store *memoryStore) set(value checkpoint) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	store.value = encoded
	return nil
}

func (store *memoryStore) get() (checkpoint, error) {
	var value checkpoint
	if len(store.value) == 0 {
		return value, errors.New("checkpoint not found")
	}
	return value, json.Unmarshal(store.value, &value)
}

func run(ctx context.Context, store *memoryStore, input string, resume bool) (string, error) {
	state := checkpoint{Value: input}
	if resume {
		var err error
		state, err = store.get()
		if err != nil {
			return "", err
		}
	}
	for ; state.Step < 4; state.Step++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		switch state.Step {
		case 0:
			state.Value += "|bootstrap"
		case 1:
			state.Value += "|tool_call:execute_msql"
		case 2:
			state.Value += "|tool_result"
			state.Step++
			if err := store.set(state); err != nil {
				return "", err
			}
			return "", errInterrupted
		case 3:
			state.Value += "|final"
		}
	}
	return state.Value, nil
}

func workload(ctx context.Context) (string, error) {
	store := &memoryStore{}
	var output string
	for index := 0; index < iterations; index++ {
		if _, err := run(ctx, store, "question", false); !errors.Is(err, errInterrupted) {
			return "", fmt.Errorf("interrupt: %w", err)
		}
		var err error
		output, err = run(ctx, store, "", true)
		if err != nil {
			return "", err
		}
	}
	return output, nil
}

func main() {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workload(cancelled); !errors.Is(err, context.Canceled) {
		panic("cancellation was not propagated")
	}
	output, err := workload(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(output)
}
