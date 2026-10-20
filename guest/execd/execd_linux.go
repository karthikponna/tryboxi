// Package execd runs commands for the host and records every output frame
// in a spool until the host acknowledges it.
package execd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/guest/proc"
	"github.com/karthikponna/tryboxi/guest/spool"
)

const (
	maxChunk   = 32 * 1024
	killGrace  = 10 * time.Second
	drainGrace = 2 * time.Second
	timeoutRC  = 124
)

// Exec is one running or finished command.
type Exec struct {
	ID      string
	Spool   *spool.Spool
	pgid    int
	stdin   io.WriteCloser
	stdinMu sync.Mutex
	wmu     sync.Mutex
	done    chan struct{}
	exitSeq uint64
}

// Done is closed once the exit frame has been spooled.
func (e *Exec) Done() <-chan struct{} { return e.done }

// ExitSeq returns the sequence number of the exit frame (0 if running).
func (e *Exec) ExitSeq() uint64 {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	return e.exitSeq
}

func (e *Exec) emit(f *guestv1.ExecFrame) uint64 {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	f.ExecId = e.ID
	f.Seq = e.Spool.NextSeq()
	f.TsMs = time.Now().UnixMilli()
	b, err := proto.Marshal(f)
	if err != nil {
		return 0
	}
	seq, _ := e.Spool.Append(b)
	if f.GetExit() != nil {
		e.exitSeq = seq
	}
	return seq
}

// Manager owns all executions in the guest.
type Manager struct {
	SpoolDir string

	mu         sync.Mutex
	execs      map[string]*Exec
	defaultEnv map[string]string
	running    int
}

func NewManager(spoolDir string) *Manager {
	return &Manager{SpoolDir: spoolDir, execs: map[string]*Exec{}, defaultEnv: map[string]string{}}
}

// SetDefaultEnv sets env vars applied to every exec.
func (m *Manager) SetDefaultEnv(env map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultEnv = map[string]string{}
	for k, v := range env {
		m.defaultEnv[k] = v
	}
}

// Running returns the number of executions still running.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Get returns an execution by id.
func (m *Manager) Get(id string) (*Exec, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.execs[id]
	return e, ok
}

// Ack records host acknowledgement and frees the spool once the exit frame
// has been acknowledged.
func (m *Manager) Ack(id string, seq uint64) {
	e, ok := m.Get(id)
	if !ok {
		return
	}
	e.Spool.Ack(seq)
	if es := e.ExitSeq(); es > 0 && seq >= es {
		_ = e.Spool.Remove()
		m.mu.Lock()
		delete(m.execs, id)
		m.mu.Unlock()
	}
}

type creds struct {
	uid, gid int
	groups   []uint32
	home     string
	name     string
}

func lookupUser(name string) (creds, error) {
	if name == "" {
		name = "agent"
	}
	u, err := user.Lookup(name)
	if err != nil {
		if id, perr := strconv.Atoi(name); perr == nil {
			u, err = user.LookupId(strconv.Itoa(id))
		}
		if err != nil {
			return creds{}, err
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	c := creds{uid: uid, gid: gid, home: u.HomeDir, name: u.Username}
	if gids, err := u.GroupIds(); err == nil {
		for _, g := range gids {
			if n, err := strconv.Atoi(g); err == nil {
				c.groups = append(c.groups, uint32(n))
			}
		}
	}
	return c, nil
}

func readOOMKills() uint64 {
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "oom_kill "); ok {
			n, _ := strconv.ParseUint(v, 10, 64)
			return n
		}
	}
	return 0
}

// OOMKills exposes the kernel's cumulative OOM kill counter.
func OOMKills() uint64 { return readOOMKills() }

// Start launches the command described by req and returns immediately.
func (m *Manager) Start(req *guestv1.ExecRequest) (*Exec, error) {
	if req.ExecId == "" {
		return nil, errors.New("exec_id required")
	}
	m.mu.Lock()
	if e, ok := m.execs[req.ExecId]; ok {
		m.mu.Unlock()
		return e, nil // idempotent: re-attach to the existing execution
	}
	defEnv := m.defaultEnv
	m.mu.Unlock()

	sp, err := spool.Open(m.SpoolDir, req.ExecId)
	if err != nil {
		return nil, err
	}
	e := &Exec{ID: req.ExecId, Spool: sp, done: make(chan struct{})}
	m.mu.Lock()
	m.execs[req.ExecId] = e
	m.running++
	m.mu.Unlock()

	fail := func(err error) (*Exec, error) {
		e.emit(&guestv1.ExecFrame{Kind: &guestv1.ExecFrame_Exit{Exit: &guestv1.Exit{ExitCode: 127, Error: err.Error()}}})
		e.Spool.Seal()
		close(e.done)
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
		return e, nil
	}

	c, err := lookupUser(req.User)
	if err != nil {
		return fail(fmt.Errorf("user %q: %w", req.User, err))
	}
	argv := req.Argv
	if len(argv) == 0 {
		argv = []string{"/bin/bash", "-lc", req.Command}
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		cwd = c.home
	}
	term := "dumb"
	if req.Tty {
		term = "xterm-256color"
	}
	envMap := map[string]string{
		"PATH":    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":    c.home,
		"USER":    c.name,
		"LOGNAME": c.name,
		"SHELL":   "/bin/bash",
		"LANG":    "C.UTF-8",
		"TERM":    term,
		"CI":      "1",
	}
	for k, v := range defEnv {
		envMap[k] = v
	}
	for k, v := range req.Env {
		envMap[k] = v
	}
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}

	sys := &syscall.SysProcAttr{}
	if c.uid != 0 || c.gid != 0 {
		sys.Credential = &syscall.Credential{Uid: uint32(c.uid), Gid: uint32(c.gid), Groups: c.groups}
	}
	var files []*os.File
	var readers []struct {
		r      *os.File
		stream guestv1.Stream
	}
	var toClose []*os.File
	var stdinW io.WriteCloser

	if req.Tty {
		ptmx, tty, err := pty.Open()
		if err != nil {
			return fail(err)
		}
		rows, cols := req.TtyRows, req.TtyCols
		if rows == 0 {
			rows, cols = 40, 120
		}
		_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
		files = []*os.File{tty, tty, tty}
		toClose = append(toClose, tty)
		sys.Setsid = true
		sys.Setctty = true
		sys.Ctty = 0
		readers = append(readers, struct {
			r      *os.File
			stream guestv1.Stream
		}{ptmx, guestv1.Stream_STREAM_STDOUT})
		stdinW = ptmx
	} else {
		sys.Setpgid = true
		var stdinR *os.File
		if len(req.Stdin) > 0 || req.KeepStdinOpen {
			r, w, err := os.Pipe()
			if err != nil {
				return fail(err)
			}
			stdinR, stdinW = r, w
			toClose = append(toClose, r)
		} else {
			stdinR, err = os.Open(os.DevNull)
			if err != nil {
				return fail(err)
			}
			toClose = append(toClose, stdinR)
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		toClose = append(toClose, outW, errW)
		files = []*os.File{stdinR, outW, errW}
		readers = append(readers,
			struct {
				r      *os.File
				stream guestv1.Stream
			}{outR, guestv1.Stream_STREAM_STDOUT},
			struct {
				r      *os.File
				stream guestv1.Stream
			}{errR, guestv1.Stream_STREAM_STDERR})
	}

	oomBefore := readOOMKills()
	start := time.Now()
	p, err := proc.Start(argv, &os.ProcAttr{Dir: cwd, Env: env, Files: files, Sys: sys})
	for _, f := range toClose {
		f.Close()
	}
	if err != nil {
		for _, rd := range readers {
			rd.r.Close()
		}
		if stdinW != nil {
			stdinW.Close()
		}
		return fail(err)
	}
	e.pgid = p.Pid
	e.stdin = stdinW
	e.emit(&guestv1.ExecFrame{Kind: &guestv1.ExecFrame_Started{Started: &guestv1.Started{Pid: int32(p.Pid)}}})

	if stdinW != nil && len(req.Stdin) > 0 {
		go func() {
			e.stdinMu.Lock()
			defer e.stdinMu.Unlock()
			_, _ = stdinW.Write(req.Stdin)
			if !req.KeepStdinOpen && !req.Tty {
				stdinW.Close()
				e.stdin = nil
			}
		}()
	}

	var rwg sync.WaitGroup
	for _, rd := range readers {
		rwg.Add(1)
		go func(r *os.File, stream guestv1.Stream) {
			defer rwg.Done()
			buf := make([]byte, maxChunk)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					data := make([]byte, n)
					copy(data, buf[:n])
					e.emit(&guestv1.ExecFrame{Kind: &guestv1.ExecFrame_Output{Output: &guestv1.Output{Stream: stream, Data: data}}})
				}
				if err != nil {
					return
				}
			}
		}(rd.r, rd.stream)
	}

	var timedOut bool
	var tmu sync.Mutex
	var timer *time.Timer
	if req.TimeoutS > 0 {
		timer = time.AfterFunc(time.Duration(req.TimeoutS)*time.Second, func() {
			tmu.Lock()
			timedOut = true
			tmu.Unlock()
			_ = unix.Kill(-p.Pid, unix.SIGTERM)
			time.AfterFunc(killGrace, func() { _ = unix.Kill(-p.Pid, unix.SIGKILL) })
		})
	}

	go func() {
		st := p.Wait()
		if timer != nil {
			timer.Stop()
		}
		// Background children may keep the pipes open; give readers a short
		// window to drain, then force them closed.
		drained := make(chan struct{})
		go func() { rwg.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(drainGrace):
			for _, rd := range readers {
				rd.r.Close()
			}
			<-drained
		}
		for _, rd := range readers {
			rd.r.Close()
		}
		e.stdinMu.Lock()
		if e.stdin != nil {
			e.stdin.Close()
			e.stdin = nil
		}
		e.stdinMu.Unlock()

		tmu.Lock()
		to := timedOut
		tmu.Unlock()
		exit := &guestv1.Exit{
			ExitCode:   int32(st.ExitCode()),
			DurationMs: time.Since(start).Milliseconds(),
			UserCpuMs:  st.Rusage.Utime.Nano() / 1e6,
			SysCpuMs:   st.Rusage.Stime.Nano() / 1e6,
			MaxRssKb:   st.Rusage.Maxrss,
			TimedOut:   to,
		}
		if st.WaitStatus.Signaled() {
			exit.Signal = int32(st.WaitStatus.Signal())
			if st.WaitStatus.Signal() == unix.SIGKILL && readOOMKills() > oomBefore {
				exit.OomKilled = true
			}
		}
		if to {
			exit.ExitCode = timeoutRC
		}
		e.emit(&guestv1.ExecFrame{Kind: &guestv1.ExecFrame_Exit{Exit: exit}})
		e.Spool.Seal()
		close(e.done)
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
	}()
	return e, nil
}

// Signal sends sig to the execution's process group (or leader).
func (m *Manager) Signal(id string, sig int, group bool) error {
	e, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("exec %s not found", id)
	}
	if e.pgid == 0 {
		return errors.New("process not started")
	}
	pid := e.pgid
	if group {
		pid = -pid
	}
	return unix.Kill(pid, unix.Signal(sig))
}

// WriteStdin writes to (and optionally closes) the execution's stdin.
func (m *Manager) WriteStdin(id string, data []byte, closeIt bool) error {
	e, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("exec %s not found", id)
	}
	e.stdinMu.Lock()
	defer e.stdinMu.Unlock()
	if e.stdin == nil {
		return errors.New("stdin is not open")
	}
	if len(data) > 0 {
		if _, err := e.stdin.Write(data); err != nil {
			return err
		}
	}
	if closeIt {
		err := e.stdin.Close()
		e.stdin = nil
		return err
	}
	return nil
}

// Frames streams spooled frames with seq > after to send until the exit
// frame has been sent or stop is closed.
func (e *Exec) Frames(after uint64, stop <-chan struct{}, send func([]byte) error) error {
	stopper := make(chan struct{})
	defer close(stopper)
	go func() {
		select {
		case <-stop:
			e.Spool.Wake()
		case <-stopper:
		}
	}()
	for seq := after + 1; ; seq++ {
		b, err := e.Spool.Read(seq, stop)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := send(b); err != nil {
			return err
		}
		if es := e.ExitSeq(); es > 0 && seq >= es {
			return nil
		}
	}
}
