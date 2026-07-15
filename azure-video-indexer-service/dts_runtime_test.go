package main

import (
	"errors"
	"testing"

	"github.com/microsoft/durabletask-go/api"
)

func TestScheduleThenRaiseCancellationRaisesAfterIgnoredInstance(t *testing.T) {
	raised := 0
	err := scheduleThenRaiseCancellation(func() error { return api.ErrIgnoreInstance }, func() error {
		raised++
		return nil
	}, "schedule", "raise")
	if err != nil || raised != 1 {
		t.Fatalf("scheduleThenRaiseCancellation() = %v, raised = %d", err, raised)
	}
}

func TestScheduleThenRaiseCancellationStopsOnRealScheduleError(t *testing.T) {
	raised := 0
	want := errors.New("scheduler unavailable")
	err := scheduleThenRaiseCancellation(func() error { return want }, func() error { raised++; return nil }, "schedule", "raise")
	if !errors.Is(err, want) || raised != 0 {
		t.Fatalf("scheduleThenRaiseCancellation() = %v, raised = %d", err, raised)
	}
}
