// Package retry implements bounded retries with exponential backoff and full jitter.
package retry

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// Policy configures Do. Nil Sleep/Rand fall back to real implementations.
type Policy struct {
	Attempts int
	Base     time.Duration
	Max      time.Duration
	Sleep    func(ctx context.Context, d time.Duration) error
	Rand     func() float64 // returns [0,1)
}

// DefaultPolicy suits startup dependency connections.
func DefaultPolicy() Policy {
	return Policy{Attempts: 8, Base: 250 * time.Millisecond, Max: 5 * time.Second}
}

// Backoff returns a full-jitter delay in [0, min(max, base*2^attempt)].
func Backoff(attempt int, base, maxDelay time.Duration, rnd func() float64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	ceiling := base
	for i := 0; i < attempt && ceiling < maxDelay; i++ {
		ceiling *= 2
	}
	if ceiling > maxDelay {
		ceiling = maxDelay
	}
	return time.Duration(rnd() * float64(ceiling))
}

// Do calls fn until it succeeds, attempts are exhausted, or ctx is done.
// onRetry (optional) is called before each wait.
func Do(ctx context.Context, p Policy, fn func(context.Context) error, onRetry func(attempt int, err error, wait time.Duration)) error {
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	if p.Sleep == nil {
		p.Sleep = sleep
	}
	if p.Rand == nil {
		p.Rand = rand.Float64 //nolint:gosec // jitter does not need a cryptographic source
	}
	var err error
	for attempt := 0; attempt < p.Attempts; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("aborted: %w", ctx.Err())
		}
		if attempt == p.Attempts-1 {
			break
		}
		wait := Backoff(attempt, p.Base, p.Max, p.Rand)
		if onRetry != nil {
			onRetry(attempt+1, err, wait)
		}
		if serr := p.Sleep(ctx, wait); serr != nil {
			return fmt.Errorf("aborted: %w", serr)
		}
	}
	return fmt.Errorf("failed after %d attempts: %w", p.Attempts, err)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
