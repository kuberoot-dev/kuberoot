package nodeapi

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The boot assessment gets one chance per boot: if it cannot read the entries
// or record a healthy slot, the slot stays on probation and that boot spends
// one of the attempts standing between the node and a rollback. A moment's
// failure used to cost the whole chance, so both steps are worth retrying.
func TestRetryKeepsTryingWithinTheAttempts(t *testing.T) {
	calls := 0
	err := retry(context.Background(), 5, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("device or resource busy")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a call that succeeds on the third try must succeed: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected to stop at the first success, made %d calls", calls)
	}
}

// Trying forever is not an option either: the assessment has a deadline of its
// own, and the caller has to learn that the step did not happen.
func TestRetryGivesUpAndReportsTheLastFailure(t *testing.T) {
	calls := 0
	want := errors.New("mount EFI partition: device or resource busy")
	err := retry(context.Background(), 4, time.Millisecond, func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Errorf("expected the last failure to be returned, got: %v", err)
	}
	if calls != 4 {
		t.Errorf("expected 4 attempts, made %d", calls)
	}
}

// A node shutting down must not be held by the retries.
func TestRetryStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- retry(ctx, 100, time.Hour, func() error {
			calls++
			cancel()
			return errors.New("busy")
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected the context's error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not return after the context ended")
	}
	if calls != 1 {
		t.Errorf("expected one attempt before the context ended, made %d", calls)
	}
}

// One attempt is the old behaviour, and the test says so: a single failure
// must not be what decides the slot.
func TestSingleAttemptIsNotTheDefault(t *testing.T) {
	if assessTries < 2 {
		t.Fatalf("the assessment must survive one failure: assessTries is %d", assessTries)
	}
}
