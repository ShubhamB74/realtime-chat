package gwbus

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestMessageJSONRoundTripKeepsFrameBytes(t *testing.T) {
	in := Message{Kind: KindDeliver, Users: []string{"u1", "u2"}, Except: "c1", Frame: []byte(`{"type":"x","bin":"\u0000"}`)}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if string(out.Frame) != string(in.Frame) || out.Kind != KindDeliver || len(out.Users) != 2 || out.Except != "c1" {
		t.Fatalf("round trip lost data: %+v", out)
	}
}

func TestGatewayOf(t *testing.T) {
	if gw, ok := GatewayOf("gw-1/conn-9"); !ok || gw != "gw-1" {
		t.Errorf("got %q %v", gw, ok)
	}
	for _, bad := range []string{"", "no-slash", "/conn"} {
		if _, ok := GatewayOf(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func collect(t *testing.T, m *Mem, gw string) (recv func() []Message, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var got []Message
	done := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		defer close(done)
		go func() { // Subscribe registers before blocking; poll until it is visible
			defer close(ready)
			for i := 0; i < 200; i++ {
				m.mu.Lock()
				n := len(m.subs[gw])
				m.mu.Unlock()
				if n > 0 {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
		_ = m.Subscribe(ctx, gw, func(msg Message) { mu.Lock(); got = append(got, msg); mu.Unlock() })
	}()
	<-ready
	return func() []Message {
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			return append([]Message(nil), got...)
		}, func() {
			cancel()
			<-done
		}
}

func TestMemBusTargetsOneGatewayAndBroadcastReachesAll(t *testing.T) {
	m := NewMem()
	r1, stop1 := collect(t, m, "gw1")
	defer stop1()
	r2, stop2 := collect(t, m, "gw2")
	defer stop2()

	ctx := context.Background()
	_ = m.Publish(ctx, "gw2", Message{Kind: KindDeliver, Users: []string{"u"}})
	_ = m.Broadcast(ctx, Message{Kind: KindPresence, User: "u", Online: true})

	if got := r1(); len(got) != 1 || got[0].Kind != KindPresence {
		t.Errorf("gw1 should only see the broadcast: %+v", got)
	}
	if got := r2(); len(got) != 2 {
		t.Errorf("gw2 should see the targeted message and the broadcast: %+v", got)
	}
}

func TestMemRegistryGatewaysOnlineAndExpiry(t *testing.T) {
	m := NewMem()
	ctx := context.Background()
	_ = m.Touch(ctx, "alice", "gw1/c1", time.Minute)
	_ = m.Touch(ctx, "alice", "gw2/c2", time.Minute)
	_ = m.Touch(ctx, "alice", "gw2/c3", time.Minute) // second connection on the same gateway
	_ = m.Touch(ctx, "bob", "gw1/c4", 20*time.Millisecond)

	gws, err := m.Gateways(ctx, []string{"alice", "bob", "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for gw := range gws {
		names = append(names, gw)
	}
	sort.Strings(names)
	if len(names) != 2 || len(gws["gw2"]) != 1 || gws["gw2"][0] != "alice" || len(gws["gw1"]) != 2 {
		t.Fatalf("gateways = %v", gws)
	}
	time.Sleep(40 * time.Millisecond)
	on, _ := m.Online(ctx, []string{"alice", "bob"})
	if !on["alice"] || on["bob"] {
		t.Errorf("online = %v (bob's entry expired)", on)
	}
	_ = m.Remove(ctx, "alice", "gw1/c1")
	_ = m.Remove(ctx, "alice", "gw2/c2")
	_ = m.Remove(ctx, "alice", "gw2/c3")
	if on, _ := m.Online(ctx, []string{"alice"}); on["alice"] {
		t.Error("alice should be offline after all connections were removed")
	}
}

func TestMemFailureInjection(t *testing.T) {
	m := NewMem()
	m.SetFailing(true)
	ctx := context.Background()
	if err := m.Publish(ctx, "g", Message{}); !errors.Is(err, ErrInjected) {
		t.Errorf("publish: %v", err)
	}
	if _, err := m.Gateways(ctx, []string{"u"}); !errors.Is(err, ErrInjected) {
		t.Errorf("gateways: %v", err)
	}
	if err := m.Subscribe(ctx, "g", func(Message) {}); !errors.Is(err, ErrInjected) {
		t.Errorf("subscribe: %v", err)
	}
	m.SetFailing(false)
	if err := m.Publish(ctx, "g", Message{}); err != nil {
		t.Errorf("recovered publish: %v", err)
	}
}
