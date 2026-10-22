package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackendRepairThenNormalRetriesBeforeStartingControllers(t *testing.T) {
	calls := 0
	normalStarted := 0
	runBackendRepair(context.Background(), true, true, func(_ context.Context, apply bool) error {
		if !apply {
			t.Fatal("repair apply was disabled")
		}
		calls++
		if calls == 1 {
			return errors.New("OCI rate limited")
		}
		return nil
	}, func() {
		normalStarted++
	}, nil, time.Millisecond)
	if calls != 2 || normalStarted != 1 {
		t.Fatalf("got %d repair calls and %d normal starts, want 2 and 1", calls, normalStarted)
	}
}

func TestBackendRepairOnePassReportsFailureWithoutStartingControllers(t *testing.T) {
	want := errors.New("repair failed")
	done := make(chan error, 1)
	runBackendRepair(context.Background(), true, false, func(context.Context, bool) error {
		return want
	}, func() {
		t.Fatal("normal controllers started after failed repair")
	}, done, time.Minute)
	if got := <-done; !errors.Is(got, want) {
		t.Fatalf("got repair result %v, want %v", got, want)
	}
}
