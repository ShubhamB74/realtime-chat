//go:build integration

// Needs a running Redis (`make up`) and REDIS_URL set.
package redis_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"realtime-chat/backend/internal/platform/gwbus"
	rstore "realtime-chat/backend/internal/store/redis"
)

func TestRedisLimiter(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Fatal("REDIS_URL must be set for integration tests")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal("invalid REDIS_URL")
	}
	client := goredis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	l := rstore.NewLimiter(client, fmt.Sprintf("test:%d:", time.Now().UnixNano()))
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		ok, _, err := l.Allow(ctx, "k", 3, 2*time.Second)
		if err != nil || !ok {
			t.Fatalf("hit %d: ok=%v err=%v", i, ok, err)
		}
	}
	ok, retry, err := l.Allow(ctx, "k", 3, 2*time.Second)
	if err != nil || ok || retry <= 0 || retry > 2*time.Second {
		t.Fatalf("4th hit: ok=%v retry=%v err=%v", ok, retry, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ok, _, err := l.Allow(ctx, "k", 3, 2*time.Second)
		if err != nil {
			t.Fatalf("after window: %v", err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rate limit did not reset within 3 seconds")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestPresenceTouchOnlineRemoveAndExpiry(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Fatal("REDIS_URL must be set for integration tests")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal("invalid REDIS_URL")
	}
	client := goredis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	p := rstore.NewPresence(client, fmt.Sprintf("test-presence:%d:", time.Now().UnixNano()))
	ctx := context.Background()

	if on, err := p.Online(ctx, []string{"u1", "u2"}); err != nil || on["u1"] || on["u2"] {
		t.Fatalf("nobody should be online: %v %v", on, err)
	}
	if err := p.Touch(ctx, "u1", "c1", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := p.Touch(ctx, "u1", "c2", 5*time.Second); err != nil { // second device
		t.Fatal(err)
	}
	on, err := p.Online(ctx, []string{"u1", "u2"})
	if err != nil || !on["u1"] || on["u2"] {
		t.Fatalf("online = %v err=%v", on, err)
	}
	_ = p.Remove(ctx, "u1", "c1")
	if on, _ := p.Online(ctx, []string{"u1"}); !on["u1"] {
		t.Error("u1 still has a second connection")
	}
	_ = p.Remove(ctx, "u1", "c2")
	if on, _ := p.Online(ctx, []string{"u1"}); on["u1"] {
		t.Error("u1 should be offline after both connections left")
	}
	// Entries expire on their own if a gateway dies without removing them.
	if err := p.Touch(ctx, "u3", "c9", 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if on, _ := p.Online(ctx, []string{"u3"}); on["u3"] {
		t.Error("expired connection must not count as online")
	}
}

func redisClient(t *testing.T) *goredis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Fatal("REDIS_URL must be set for integration tests")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal("invalid REDIS_URL")
	}
	c := goredis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRegistryGatewaysAcrossMultipleGateways(t *testing.T) {
	p := rstore.NewPresence(redisClient(t), fmt.Sprintf("test-registry:%d:", time.Now().UnixNano()))
	ctx := context.Background()
	_ = p.Touch(ctx, "alice", "gw1/c1", 5*time.Second)
	_ = p.Touch(ctx, "alice", "gw2/c2", 5*time.Second)
	_ = p.Touch(ctx, "bob", "gw2/c3", 5*time.Second)
	_ = p.Touch(ctx, "dead", "gw3/c4", 100*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	got, err := p.Gateways(ctx, []string{"alice", "bob", "dead", "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["gw1"]) != 1 || got["gw1"][0] != "alice" || len(got["gw2"]) != 2 {
		t.Fatalf("gateways = %v", got)
	}
	if _, present := got["gw3"]; present {
		t.Errorf("an expired connection (crashed gateway) must not be routable: %v", got)
	}
}

func TestBusDeliversTargetedAndBroadcastMessages(t *testing.T) {
	client := redisClient(t)
	prefix := fmt.Sprintf("test-bus:%d:", time.Now().UnixNano())
	bus := rstore.NewBus(client, prefix)

	type sink struct {
		mu   sync.Mutex
		msgs []gwbus.Message
	}
	subscribe := func(gw string) (*sink, context.CancelFunc) {
		s := &sink{}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			_ = bus.Subscribe(ctx, gw, func(m gwbus.Message) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() })
		}()
		return s, cancel
	}
	s1, stop1 := subscribe("gw1")
	defer stop1()
	s2, stop2 := subscribe("gw2")
	defer stop2()
	time.Sleep(300 * time.Millisecond) // let both subscriptions register

	ctx := context.Background()
	frame := []byte(`{"type":"message.received"}`)
	if err := bus.Publish(ctx, "gw2", gwbus.Message{Kind: gwbus.KindDeliver, Users: []string{"bob"}, Except: "c9", Frame: frame}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Broadcast(ctx, gwbus.Message{Kind: gwbus.KindPresence, User: "bob", Online: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	s1.mu.Lock()
	defer s1.mu.Unlock()
	s2.mu.Lock()
	defer s2.mu.Unlock()
	if len(s1.msgs) != 1 || s1.msgs[0].Kind != gwbus.KindPresence {
		t.Errorf("gw1 should only receive the broadcast: %+v", s1.msgs)
	}
	if len(s2.msgs) != 2 {
		t.Fatalf("gw2 should receive the targeted message and the broadcast: %+v", s2.msgs)
	}
	var targeted gwbus.Message
	for _, m := range s2.msgs {
		if m.Kind == gwbus.KindDeliver {
			targeted = m
		}
	}
	if string(targeted.Frame) != string(frame) || targeted.Except != "c9" || len(targeted.Users) != 1 {
		t.Errorf("targeted message corrupted in transit: %+v", targeted)
	}
}
