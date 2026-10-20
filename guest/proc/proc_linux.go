// Package proc starts child processes and reaps every child of the agent
// (including orphans re-parented to PID 1) from a single goroutine, so
// exit statuses are never lost to a competing wait call.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Status is the result of a reaped process.
type Status struct {
	WaitStatus unix.WaitStatus
	Rusage     unix.Rusage
}

// ExitCode follows shell conventions: the exit status, or 128+signal.
func (s Status) ExitCode() int {
	if s.WaitStatus.Signaled() {
		return 128 + int(s.WaitStatus.Signal())
	}
	return s.WaitStatus.ExitStatus()
}

type reaper struct {
	mu      sync.Mutex
	waiters map[int]chan Status
	early   map[int]Status // exited before Track registered them
}

var r = &reaper{waiters: map[int]chan Status{}, early: map[int]Status{}}
var startOnce sync.Once

// StartReaper begins handling SIGCHLD. It is safe to call more than once.
func StartReaper() {
	startOnce.Do(func() {
		ch := make(chan os.Signal, 64)
		signal.Notify(ch, syscall.SIGCHLD)
		go func() {
			// The ticker is a safety net: SIGCHLDs coalesce, and wait4 can
			// be interrupted.
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ch:
				case <-t.C:
				}
				r.reapAll()
			}
		}()
	})
}

func (r *reaper) reapAll() {
	for {
		var ws unix.WaitStatus
		var ru unix.Rusage
		r.mu.Lock()
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG|unix.WALL, &ru)
		if err == unix.EINTR {
			r.mu.Unlock()
			continue
		}
		if os.Getenv("SANDBOX_AGENT_DEBUG_REAPER") != "" {
			fmt.Fprintf(os.Stderr, "reaper: wait4 pid=%d err=%v\n", pid, err)
		}
		if pid <= 0 || err != nil {
			r.mu.Unlock()
			return
		}
		st := Status{WaitStatus: ws, Rusage: ru}
		if w, ok := r.waiters[pid]; ok {
			delete(r.waiters, pid)
			w <- st
		} else {
			// Either an orphan (discard) or a child whose Start has not yet
			// registered; keep a bounded record for the latter.
			if len(r.early) < 4096 {
				r.early[pid] = st
			}
		}
		r.mu.Unlock()
	}
}

// Proc is a started child process.
type Proc struct {
	Pid  int
	done chan Status
	st   Status
	once sync.Once
}

// Wait blocks until the process exits.
func (p *Proc) Wait() Status {
	p.once.Do(func() { p.st = <-p.done })
	return p.st
}

// Done returns a channel closed-over by Wait semantics: it delivers the
// status exactly once; prefer Wait unless selecting.
func (p *Proc) Done() <-chan Status { return p.done }

// Start launches argv with the given attributes and registers it with the
// reaper. The reaper lock is held across fork so the exit can't be missed.
func Start(argv []string, attr *os.ProcAttr) (*Proc, error) {
	StartReaper()
	if len(argv) == 0 {
		return nil, errors.New("proc: empty argv")
	}
	path, err := lookPath(argv[0], attr.Env)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := os.StartProcess(path, argv, attr)
	if err != nil {
		return nil, err
	}
	pr := &Proc{Pid: p.Pid, done: make(chan Status, 1)}
	_ = p.Release() // sets p.Pid to -1; use pr.Pid from here on
	if st, ok := r.early[pr.Pid]; ok {
		delete(r.early, pr.Pid)
		pr.done <- st
	} else {
		r.waiters[pr.Pid] = pr.done
	}
	return pr, nil
}

func lookPath(name string, env []string) (string, error) {
	if bytes.ContainsRune([]byte(name), '/') {
		return name, nil
	}
	pathEnv := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	for _, kv := range env {
		if len(kv) > 5 && kv[:5] == "PATH=" {
			pathEnv = kv[5:]
		}
	}
	for _, dir := range bytes.Split([]byte(pathEnv), []byte(":")) {
		p := string(dir) + "/" + name
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("proc: %s: executable not found in PATH", name)
}

// Cmd describes a short helper command whose output is captured.
type Cmd struct {
	Argv  []string
	Dir   string
	Env   []string
	Cred  *syscall.Credential
	Stdin []byte
}

// Output runs c to completion and returns stdout, stderr and status.
func Output(ctx context.Context, c Cmd) (stdout, stderr []byte, st Status, err error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, st, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, nil, st, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, nil, st, err
	}
	env := c.Env
	if env == nil {
		env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	}
	p, err := Start(c.Argv, &os.ProcAttr{
		Dir:   c.Dir,
		Env:   env,
		Files: []*os.File{inR, outW, errW},
		Sys:   &syscall.SysProcAttr{Setpgid: true, Credential: c.Cred},
	})
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, nil, st, err
	}
	go func() {
		if len(c.Stdin) > 0 {
			_, _ = inW.Write(c.Stdin)
		}
		inW.Close()
	}()
	var ob, eb bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&ob, outR); outR.Close() }()
	go func() { defer wg.Done(); _, _ = io.Copy(&eb, errR); errR.Close() }()
	select {
	case st = <-p.done:
	case <-ctx.Done():
		_ = unix.Kill(-p.Pid, unix.SIGKILL)
		st = <-p.done
		err = ctx.Err()
	}
	wg.Wait()
	return ob.Bytes(), eb.Bytes(), st, err
}
