package spool

import (
	"fmt"
	"io"
	"testing"
	"time"
)

func TestAppendReadAndBlock(t *testing.T) {
	s, err := Open(t.TempDir(), "exe_1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		seq, err := s.Append([]byte(fmt.Sprintf("frame-%d", i)))
		if err != nil || seq != uint64(i) {
			t.Fatalf("append %d: seq=%d err=%v", i, seq, err)
		}
	}
	b, err := s.Read(2, nil)
	if err != nil || string(b) != "frame-2" {
		t.Fatalf("read 2: %q %v", b, err)
	}
	got := make(chan string, 1)
	go func() {
		b, _ := s.Read(4, nil)
		got <- string(b)
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := s.Append([]byte("frame-4")); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != "frame-4" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked reader never woke")
	}
	s.Seal()
	if _, err := s.Read(5, nil); err != io.EOF {
		t.Fatalf("want EOF after seal, got %v", err)
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
}
