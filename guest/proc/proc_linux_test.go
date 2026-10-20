package proc

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestStartWait(t *testing.T) {
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	p, err := Start([]string{"/bin/sh", "-c", "exit 7"}, &os.ProcAttr{Files: []*os.File{devnull, os.Stdout, os.Stderr}, Sys: &syscall.SysProcAttr{Setpgid: true}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-p.Done():
		if st.ExitCode() != 7 {
			t.Fatalf("exit=%d", st.ExitCode())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit never observed")
	}
}

func TestOutput(t *testing.T) {
	out, _, st, err := Output(context.Background(), Cmd{Argv: []string{"/bin/sh", "-c", "echo hi; exit 2"}})
	if err != nil || string(out) != "hi\n" || st.ExitCode() != 2 {
		t.Fatalf("out=%q st=%d err=%v", out, st.ExitCode(), err)
	}
}
