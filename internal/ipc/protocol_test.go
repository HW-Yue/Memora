package ipc

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRemoteErrorMatchesStableErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		remote *RemoteError
		target error
		match  bool
	}{
		{"protocol version", &RemoteError{Code: "protocol_version"}, ErrProtocolVersion, true},
		{"cancelled", &RemoteError{Code: "cancelled"}, context.Canceled, true},
		{"deadline exceeded", &RemoteError{Code: "deadline_exceeded"}, context.DeadlineExceeded, true},
		{"unrelated", &RemoteError{Code: "handler_error"}, context.Canceled, false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := errors.Is(test.remote, test.target); got != test.match {
				t.Fatalf("errors.Is(%v, %v) = %v, want %v", test.remote, test.target, got, test.match)
			}
		})
	}
}

func TestDurationMillisecondsCeil(t *testing.T) {
	t.Parallel()

	tests := []struct {
		duration time.Duration
		want     int64
	}{
		{time.Nanosecond, 1},
		{time.Millisecond, 1},
		{time.Millisecond + time.Nanosecond, 2},
		{30*time.Millisecond - time.Nanosecond, 30},
	}
	for _, test := range tests {
		if got := durationMillisecondsCeil(test.duration); got != test.want {
			t.Fatalf("durationMillisecondsCeil(%v) = %d, want %d", test.duration, got, test.want)
		}
	}
}
