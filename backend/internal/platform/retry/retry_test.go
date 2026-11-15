package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

func TestBackoffBounds(t *testing.T) {
	base, maxD := 100*time.Millisecond, 1*time.Second
	if got := Backoff(0, base, maxD, func() float64 { return 0.999999 }); got > base {
		t.Errorf("attempt 0 ceiling exceeded: %v", got)
	}
	if got := Backoff(10, base, maxD, func() float64 { return 0.999999 }); got > maxD {
		t.Errorf("max exceeded: %v", got)
	}
	if got := Backoff(3, base, maxD, func() float64 { return 0 }); got != 0 {
		t.Errorf("zero jitter should give 0, got %v", got)
	}
	if got := Backoff(2, base, maxD, func() float64 { return 0.5 }); got != 200*time.Millisecond {
		t.Errorf("attempt 2 half jitter = %v, want 200ms", got)
	}
}

func TestDoSucceedsAfterFailures(t *testing.T) {
	calls, retries := 0, 0
	p := Policy{Attempts: 5, Base: time.Millisecond, Max: time.Millisecond, Sleep: noSleep, Rand: func() float64 { return 0.5 }}
	err := Do(context.Background(), p, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	}, func(int, error, time.Duration) { retries++ })
	if err != nil || calls != 3 || retries != 2 {
		t.Fatalf("err=%v calls=%d retries=%d", err, calls, retries)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	sentinel := errors.New("down")
	calls := 0
	p := Policy{Attempts: 3, Base: time.Millisecond, Max: time.Millisecond, Sleep: noSleep, Rand: func() float64 { return 0 }}
	err := Do(context.Background(), p, func(context.Context) error { calls++; return sentinel }, nil)
	if !errors.Is(err, sentinel) || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	p := Policy{Attempts: 10, Base: time.Millisecond, Max: time.Millisecond, Sleep: noSleep, Rand: func() float64 { return 0 }}
	err := Do(ctx, p, func(context.Context) error {
		calls++
		cancel()
		return errors.New("fail")
	}, nil)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
