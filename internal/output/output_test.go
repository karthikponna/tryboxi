package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanAndCollapse(t *testing.T) {
	in := []byte("\x1b[32mPASS\x1b[0m test\nprogress 10%\rprogress 50%\rprogress 100%\ndone\r\n")
	got := string(Clean(in, true))
	want := "PASS test\nprogress 100%\ndone\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTruncate(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	out, tr := Truncate([]byte(b.String()), Limits{HeadLines: 50, HeadBytes: 8192, TailLines: 200, TailBytes: 24576}, "exe_x stdout")
	if !tr {
		t.Fatal("expected truncation")
	}
	if !strings.HasPrefix(out, "line 1\n") || !strings.HasSuffix(out, "line 1000\n") || !strings.Contains(out, "... 750 lines omitted (exe_x stdout) ...") {
		t.Fatalf("bad output:\n%s", out[:200])
	}
	short, tr := Truncate([]byte("a\nb\n"), Limits{50, 8192, 200, 24576}, "")
	if tr || short != "a\nb\n" {
		t.Fatal("short output must be unchanged")
	}
}

func TestRing(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("abc"))
	if string(r.Bytes()) != "abc" {
		t.Fatal(string(r.Bytes()))
	}
	r.Write([]byte("defghij"))
	if string(r.Bytes()) != "cdefghij" {
		t.Fatalf("got %q", r.Bytes())
	}
	r.Write([]byte("0123456789"))
	if string(r.Bytes()) != "23456789" {
		t.Fatalf("got %q", r.Bytes())
	}
}

func TestLogReplayAndSubscribe(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(dir, 1<<20, 1024)
	if err != nil {
		t.Fatal(err)
	}
	l.Append(1, Stdout, []byte("hello\n"))
	l.Append(2, Stderr, []byte("oops\n"))
	l.Append(2, Stderr, []byte("dup\n")) // ignored
	replay, live, cancel := l.Subscribe(1)
	if len(replay) != 1 || string(replay[0].Data) != "oops\n" {
		t.Fatalf("replay=%v", replay)
	}
	l.Append(3, Stdout, []byte("world\n"))
	f := <-live
	if f.Seq != 3 || string(f.Data) != "world\n" {
		t.Fatalf("live=%+v", f)
	}
	cancel()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	so, _ := Paths(dir)
	b, _ := os.ReadFile(so)
	if string(b) != "hello\nworld\n" {
		t.Fatalf("stdout=%q", b)
	}
	frames, err := ReplayFinished(dir, 0)
	if err != nil || len(frames) != 3 {
		t.Fatalf("frames=%v err=%v", frames, err)
	}
	m, _, err := Grep(so, "wor", 10)
	if err != nil || len(m) != 1 || m[0].Line != 2 {
		t.Fatalf("grep=%v %v", m, err)
	}
	_ = filepath.Join
}

func TestMaxStored(t *testing.T) {
	dir := t.TempDir()
	l, _ := Create(dir, 10, 1024)
	l.Append(1, Stdout, []byte("0123456789abcdef"))
	l.Close()
	so, _ := Paths(dir)
	b, _ := os.ReadFile(so)
	if string(b) != "0123456789" {
		t.Fatalf("got %q", b)
	}
	if s, _ := l.Sizes(); s != 16 {
		t.Fatalf("size=%d", s)
	}
}
