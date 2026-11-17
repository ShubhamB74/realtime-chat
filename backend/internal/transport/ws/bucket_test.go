package ws

import (
	"testing"
	"time"
)

func TestBucketBurstThenRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newBucket(10, 3, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if !b.allow() {
			t.Fatalf("burst event %d should be allowed", i+1)
		}
	}
	if b.allow() {
		t.Fatal("4th immediate event must be denied")
	}
	now = now.Add(150 * time.Millisecond) // 1.5 tokens at 10/s
	if !b.allow() {
		t.Error("one token should have refilled")
	}
	if b.allow() {
		t.Error("only one token had refilled")
	}
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if !b.allow() {
			t.Error("tokens must refill up to the burst")
		}
	}
	if b.allow() {
		t.Error("tokens must not exceed the burst")
	}
}
