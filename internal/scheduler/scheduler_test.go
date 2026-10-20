package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestAdmitAndQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, Budget{VCPU: 4, MemMB: 4096, MaxSandboxes: 8}, nil)
	a, err := s.Reserve(ctx, Request{ClientID: "a", VCPU: 2, MemMB: 2048}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Reserve(ctx, Request{ClientID: "a", VCPU: 2, MemMB: 2048}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Full: a third request waits, then times out.
	if _, err := s.Reserve(ctx, Request{ClientID: "b", VCPU: 1, MemMB: 512}, 50*time.Millisecond); err == nil {
		t.Fatal("expected capacity error")
	} else if _, ok := IsCapacity(err); !ok {
		t.Fatalf("wrong error %v", err)
	}
	// Queued request is granted when capacity frees up.
	got := make(chan *Reservation, 1)
	go func() {
		r, _ := s.Reserve(ctx, Request{ClientID: "b", VCPU: 2, MemMB: 2048}, 2*time.Second)
		got <- r
	}()
	time.Sleep(30 * time.Millisecond)
	a.Release()
	a.Release() // idempotent
	select {
	case r := <-got:
		if r == nil {
			t.Fatal("queued request not granted")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	c := s.Capacity()
	if c.Sandboxes != 2 || c.MemFreeMB != 0 {
		t.Fatalf("capacity %+v", c)
	}
	b.Release()
}

func TestFairness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, Budget{VCPU: 2, MemMB: 2048, MaxSandboxes: 8}, nil)
	hold, _ := s.Reserve(ctx, Request{ClientID: "greedy", VCPU: 1, MemMB: 1024}, time.Second)
	hold2, _ := s.Reserve(ctx, Request{ClientID: "greedy", VCPU: 1, MemMB: 1024}, time.Second)
	order := make(chan string, 2)
	go func() {
		if r, err := s.Reserve(ctx, Request{ClientID: "greedy", VCPU: 1, MemMB: 1024}, 2*time.Second); err == nil {
			order <- "greedy"
			_ = r
		}
	}()
	time.Sleep(20 * time.Millisecond)
	go func() {
		if r, err := s.Reserve(ctx, Request{ClientID: "polite", VCPU: 1, MemMB: 1024}, 2*time.Second); err == nil {
			order <- "polite"
			_ = r
		}
	}()
	time.Sleep(20 * time.Millisecond)
	hold.Release()
	// "polite" has no active reservations, so it goes first despite arriving later.
	if first := <-order; first != "polite" {
		t.Fatalf("first=%s", first)
	}
	hold2.Release()
}

func TestPoolReclaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, Budget{VCPU: 2, MemMB: 2048, MaxSandboxes: 8}, nil)
	pool, err := s.Reserve(ctx, Request{ClientID: "pool", VCPU: 2, MemMB: 2048, Pool: true}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s.SetReclaimer(func(int) int { pool.Release(); return 1 })
	if _, err := s.Reserve(ctx, Request{ClientID: "a", VCPU: 2, MemMB: 2048}, time.Second); err != nil {
		t.Fatalf("pool VM should be evicted for a real request: %v", err)
	}
}

func TestCriticalPressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, Budget{VCPU: 8, MemMB: 8192, MaxSandboxes: 8}, func() Pressure { return PressureCritical })
	if _, err := s.Reserve(ctx, Request{ClientID: "a", VCPU: 1, MemMB: 512}, time.Second); err == nil {
		t.Fatal("expected refusal under critical pressure")
	}
}
