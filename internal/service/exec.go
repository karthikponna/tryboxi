package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/ids"
	"github.com/karthikponna/tryboxi/internal/output"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/pkg/client"
)

// runExec tracks a live execution on the host.
type runExec struct {
	id         string
	box        *Box
	log        *output.Log
	dir        string
	background bool
	startMs    int64
	timeout    time.Duration
	done       chan struct{}

	mu         sync.Mutex
	guestStart int64 // guest clock at process start (ms)
	lastSeq    uint64
	lastOutput time.Time
	stalled    bool
	exit       *guestv1.Exit
	status     string
	reason     string
	changed    []string
}

func (b *Box) failRunning(reason string) {
	b.mu.Lock()
	list := make([]*runExec, 0, len(b.running))
	for _, re := range b.running {
		list = append(list, re)
	}
	b.mu.Unlock()
	for _, re := range list {
		re.finish(context.Background(), nil, "lost", reason)
	}
}

// Exec runs a command. With wait (default) it returns the finished result;
// otherwise it returns immediately with status "running".
func (m *Manager) Exec(ctx context.Context, sandboxID string, req client.ExecRequest, idemKey string) (client.Execution, error) {
	if req.Cmd == "" && len(req.Argv) == 0 {
		return client.Execution{}, errInvalid("cmd or argv is required")
	}
	b, err := m.box(sandboxID)
	if err != nil {
		return client.Execution{}, err
	}
	if idemKey != "" {
		if e, err := m.st.R.GetExecutionByIdempotencyKey(ctx, db.GetExecutionByIdempotencyKeyParams{SandboxID: sandboxID, IdempotencyKey: store.NS(idemKey)}); err == nil {
			return m.waitExecution(ctx, e.ID, req)
		}
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.Execution{}, err
	}
	timeout := m.cfg.Defaults.ExecTimeoutS
	if req.TimeoutS > 0 {
		timeout = min(req.TimeoutS, m.cfg.Defaults.MaxExecTimeoutS)
	}
	if req.Background && req.TimeoutS == 0 {
		timeout = 0 // servers run until stopped
	}

	b.lc.RLock()
	var syncRes *client.SyncResult
	doSync := b.row.WorkspaceMode == "host"
	if req.Sync != nil {
		doSync = *req.Sync && b.row.HostRepoPath != ""
	}
	if doSync {
		sr, err := m.syncWorkspace(ctx, b)
		if err != nil {
			b.lc.RUnlock()
			return client.Execution{}, fmt.Errorf("workspace sync before exec: %w", err)
		}
		syncRes = sr
	}
	re, err := m.startExec(ctx, b, req, idemKey, timeout)
	b.lc.RUnlock()
	if err != nil {
		return client.Execution{}, err
	}
	wait := req.Wait == nil || *req.Wait
	if req.Background {
		wait = false
	}
	if !wait {
		e := m.execView(ctx, re.id, req)
		e.Sync = syncRes
		return e, nil
	}
	select {
	case <-re.done:
	case <-ctx.Done():
		// The client went away; the command keeps running.
		e := m.execView(context.Background(), re.id, req)
		e.Sync = syncRes
		return e, nil
	}
	e := m.execView(ctx, re.id, req)
	e.Sync = syncRes
	return e, nil
}

func (m *Manager) startExec(ctx context.Context, b *Box, req client.ExecRequest, idemKey string, timeout int) (*runExec, error) {
	gc := b.guest()
	if gc == nil {
		return nil, errState("sandbox %s is not running", b.id)
	}
	seq, err := m.st.W.NextExecSeq(ctx, b.id)
	if err != nil {
		return nil, err
	}
	id := ids.New(ids.Execution)
	dir := m.cfg.Dir("executions", id)
	lg, err := output.Create(dir, m.cfg.Output.MaxStoredBytes, m.cfg.Output.RingBytes)
	if err != nil {
		return nil, err
	}
	so, se := output.Paths(dir)
	cmd := req.Cmd
	if cmd == "" {
		cmd = strings.Join(req.Argv, " ")
	}
	envKeys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	ek, _ := json.Marshal(envKeys)
	now := time.Now()
	if err := m.st.W.InsertExecution(ctx, db.InsertExecutionParams{
		ID: id, SandboxID: b.id, SeqInSandbox: seq, IdempotencyKey: store.NS(idemKey), Command: cmd, Cwd: req.Cwd,
		EnvKeys: string(ek), Tty: store.Bool(req.TTY), Background: store.Bool(req.Background), TimeoutS: int64(timeout),
		State: "running", StartedAt: now.UnixMilli(), StdoutPath: so, StderrPath: se,
	}); err != nil {
		lg.Close()
		if strings.Contains(err.Error(), "UNIQUE") && idemKey != "" {
			if e, err2 := m.st.R.GetExecutionByIdempotencyKey(ctx, db.GetExecutionByIdempotencyKeyParams{SandboxID: b.id, IdempotencyKey: store.NS(idemKey)}); err2 == nil {
				return m.runningExec(b, e.ID), nil
			}
		}
		return nil, err
	}
	re := &runExec{id: id, box: b, log: lg, dir: dir, background: req.Background, startMs: now.UnixMilli(),
		timeout: time.Duration(timeout) * time.Second, done: make(chan struct{}), lastOutput: now, status: "running"}
	b.mu.Lock()
	b.running[id] = re
	if !req.Background {
		b.busy++
		if b.state == StateReady {
			b.state = StateBusy
		}
	}
	b.row.ExecCount = seq
	b.mu.Unlock()
	b.touch()
	m.bus.Emit(b.id, id, "exec.started", map[string]any{"cmd": truncateStr(cmd, 300), "background": req.Background})

	greq := &guestv1.ExecRequest{
		ExecId: id, Argv: req.Argv, Command: req.Cmd, Cwd: req.Cwd, Env: req.Env, User: req.User, Tty: req.TTY,
		TimeoutS: uint32(timeout), Stdin: []byte(req.Stdin), KeepStdinOpen: req.KeepStdinOpen,
	}
	stream, err := gc.Exec(m.ctx, greq)
	if err != nil {
		re.finish(context.Background(), nil, "lost", "exec rpc: "+err.Error())
		return re, nil
	}
	go m.consume(re, stream)
	if timeout > 0 {
		go m.hostTimeout(re)
	}
	return re, nil
}

func (m *Manager) runningExec(b *Box, id string) *runExec {
	b.mu.Lock()
	defer b.mu.Unlock()
	if re, ok := b.running[id]; ok {
		return re
	}
	re := &runExec{id: id, box: b, done: make(chan struct{})}
	close(re.done)
	return re
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type frameStream interface {
	Recv() (*guestv1.ExecFrame, error)
}

// consume reads guest frames into the host log, acknowledges them, and
// re-attaches after disconnects (pause/resume, transient vsock errors).
func (m *Manager) consume(re *runExec, stream frameStream) {
	b := re.box
	var pendingAck uint64
	lastAck := time.Now()
	ack := func(force bool) {
		re.mu.Lock()
		seq := re.lastSeq
		re.mu.Unlock()
		if seq == 0 || seq == pendingAck || (!force && time.Since(lastAck) < 500*time.Millisecond) {
			return
		}
		if gc := b.guest(); gc != nil {
			actx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			if _, err := gc.Ack(actx, &guestv1.AckRequest{ExecId: re.id, Seq: seq}); err == nil {
				pendingAck = seq
				lastAck = time.Now()
			}
			cancel()
		}
	}
	for {
		f, err := stream.Recv()
		if err == nil {
			re.mu.Lock()
			if f.Seq > re.lastSeq {
				re.lastSeq = f.Seq
			}
			re.mu.Unlock()
			switch k := f.Kind.(type) {
			case *guestv1.ExecFrame_Started:
				re.mu.Lock()
				re.guestStart = f.TsMs
				re.mu.Unlock()
			case *guestv1.ExecFrame_Output:
				name := output.Stdout
				if k.Output.Stream == guestv1.Stream_STREAM_STDERR {
					name = output.Stderr
				}
				_ = re.log.Append(f.Seq, name, k.Output.Data)
				re.mu.Lock()
				re.lastOutput = time.Now()
				re.stalled = false
				re.mu.Unlock()
			case *guestv1.ExecFrame_Exit:
				ack(true)
				status := "succeeded"
				switch {
				case k.Exit.Error != "":
					status = "failed"
				case k.Exit.TimedOut:
					status = "timed_out"
				case k.Exit.Signal != 0:
					status = "killed"
				case k.Exit.ExitCode != 0:
					status = "failed"
				}
				re.finish(m.ctx, k.Exit, status, k.Exit.Error)
				return
			}
			ack(false)
			continue
		}
		// Stream broke. Decide whether to re-attach.
		select {
		case <-re.done:
			return
		default:
		}
		if m.ctx.Err() != nil {
			return
		}
		next, ok := m.reattach(re)
		if !ok {
			return
		}
		stream = next
	}
}

// reattach waits until the sandbox is running again and re-opens the
// frame stream after the last stored sequence number.
func (m *Manager) reattach(re *runExec) (frameStream, bool) {
	b := re.box
	for attempt := 0; ; attempt++ {
		select {
		case <-re.done:
			return nil, false
		case <-m.ctx.Done():
			return nil, false
		case <-time.After(time.Duration(min(attempt+1, 10)) * 200 * time.Millisecond):
		}
		switch b.State() {
		case StateDestroyed, StateLost, StateFailed:
			re.finish(context.Background(), nil, "lost", "sandbox "+b.State())
			return nil, false
		case StatePaused, StatePausing, StateBooting:
			continue
		}
		gc := b.guest()
		if gc == nil {
			continue
		}
		re.mu.Lock()
		after := re.lastSeq
		re.mu.Unlock()
		st, err := gc.Attach(m.ctx, &guestv1.AttachRequest{ExecId: re.id, AfterSeq: after})
		if err != nil {
			if attempt > 150 {
				re.finish(context.Background(), nil, "lost", "could not re-attach: "+err.Error())
				return nil, false
			}
			continue
		}
		return st, true
	}
}

// hostTimeout is a safety net in case the guest never reports the exit.
func (m *Manager) hostTimeout(re *runExec) {
	select {
	case <-re.done:
		return
	case <-time.After(re.timeout + 30*time.Second):
	}
	if gc := re.box.guest(); gc != nil {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		_, _ = gc.Signal(ctx, &guestv1.SignalRequest{ExecId: re.id, Signal: int32(unix.SIGKILL), ProcessGroup: true})
		cancel()
	}
	select {
	case <-re.done:
	case <-time.After(15 * time.Second):
		re.finish(context.Background(), nil, "timed_out", "guest did not report exit after timeout")
	}
}

// finish records the result exactly once.
func (re *runExec) finish(ctx context.Context, exit *guestv1.Exit, status, reason string) {
	re.mu.Lock()
	select {
	case <-re.done:
		re.mu.Unlock()
		return
	default:
	}
	re.exit, re.status, re.reason = exit, status, reason
	lastSeq := re.lastSeq
	re.mu.Unlock()
	b := re.box
	m := b.m
	// Files the command changed inside the VM. Everything is recorded
	// before the log closes, because closing ends SSE streams and clients
	// read the final row right after.
	if exit != nil && b.row.WorkspaceMode != "none" {
		if gc := b.guest(); gc != nil {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			// Compare against the guest's own clock: host/guest skew of a few
			// ms would otherwise hide files written by fast commands.
			re.mu.Lock()
			since := re.guestStart
			re.mu.Unlock()
			if since == 0 {
				since = re.startMs - 1000
			}
			resp, err := gc.Changes(cctx, &guestv1.ChangesRequest{Dir: "/workspace", SinceMs: since - 1, ExcludeDirs: m.cfg.Sync.Excludes, MaxFiles: 500})
			cancel()
			if err == nil {
				re.changed = resp.Paths
			}
		}
	}
	so, se := int64(0), int64(0)
	if re.log != nil {
		so, se = re.log.Sizes()
	}
	p := db.FinishExecutionParams{State: status, Reason: reason, FinishedAt: nullTime(time.Now()), StdoutBytes: so, StderrBytes: se,
		LastSeqAcked: int64(lastSeq), ID: re.id}
	if exit != nil {
		p.ExitCode = store.NI64(int64(exit.ExitCode))
		if exit.Signal != 0 {
			p.Signal = store.NI64(int64(exit.Signal))
		}
		p.OomKilled = store.Bool(exit.OomKilled)
		p.TimedOut = store.Bool(exit.TimedOut)
		p.DurationMs = store.NI64(exit.DurationMs)
		p.CpuMs = store.NI64(exit.UserCpuMs + exit.SysCpuMs)
		p.MaxRssKb = store.NI64(exit.MaxRssKb)
	} else {
		p.DurationMs = store.NI64(time.Now().UnixMilli() - re.startMs)
	}
	if len(re.changed) > 0 {
		cj, _ := json.Marshal(re.changed)
		_ = m.st.W.SetExecutionChangedFiles(context.Background(), db.SetExecutionChangedFilesParams{ChangedFiles: string(cj), ID: re.id})
	}
	_ = m.st.W.FinishExecution(context.Background(), p)
	if re.log != nil {
		_ = re.log.Close()
	}
	b.mu.Lock()
	delete(b.running, re.id)
	if !re.background {
		b.busy--
		if b.busy <= 0 {
			b.busy = 0
			if b.state == StateBusy {
				b.state = StateReady
			}
		}
	}
	b.mu.Unlock()
	b.touch()
	data := map[string]any{"status": status, "stdout_bytes": so, "stderr_bytes": se}
	if exit != nil {
		data["exit_code"] = exit.ExitCode
		data["duration_ms"] = exit.DurationMs
		if exit.OomKilled {
			data["oom_killed"] = true
		}
	}
	if reason != "" {
		data["reason"] = reason
	}
	m.bus.Emit(b.id, re.id, "exec.finished", data)
	close(re.done)
}

func (m *Manager) waitExecution(ctx context.Context, execID string, req client.ExecRequest) (client.Execution, error) {
	e, err := m.st.R.GetExecution(ctx, execID)
	if err != nil {
		return client.Execution{}, errNotFound("execution", execID)
	}
	wait := req.Wait == nil || *req.Wait
	if wait && e.State == "running" {
		if b, err := m.box(e.SandboxID); err == nil {
			re := m.runningExec(b, execID)
			select {
			case <-re.done:
			case <-ctx.Done():
			}
		}
	}
	return m.execView(ctx, execID, req), nil
}

// GetExecution returns an execution (with truncated output).
func (m *Manager) GetExecution(ctx context.Context, id string) (client.Execution, error) {
	if _, err := m.st.R.GetExecution(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return client.Execution{}, errNotFound("execution", id)
	}
	return m.execView(ctx, id, client.ExecRequest{}), nil
}

// ListExecutions lists recent executions in a sandbox.
func (m *Manager) ListExecutions(ctx context.Context, sandboxID, state string) ([]client.Execution, error) {
	p := db.ListExecutionsParams{SandboxID: sandboxID, Limit: 100}
	if state != "" {
		p.State = state
	}
	rows, err := m.st.R.ListExecutions(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]client.Execution, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowExec(r))
	}
	return out, nil
}

func rowExec(e db.Execution) client.Execution {
	v := client.Execution{ID: e.ID, SandboxID: e.SandboxID, Status: e.State, Command: e.Command, TimedOut: e.TimedOut == 1,
		OOMKilled: e.OomKilled == 1, StdoutBytes: e.StdoutBytes, StderrBytes: e.StderrBytes, Reason: e.Reason, StartedAt: e.StartedAt,
		ChangedFiles: []string{}}
	if e.ExitCode.Valid {
		c := int(e.ExitCode.Int64)
		v.ExitCode = &c
	}
	if e.Signal.Valid {
		v.Signal = int(e.Signal.Int64)
	}
	if e.DurationMs.Valid {
		v.DurationMs = e.DurationMs.Int64
	}
	if e.FinishedAt.Valid {
		v.FinishedAt = e.FinishedAt.Int64
	}
	_ = json.Unmarshal([]byte(e.ChangedFiles), &v.ChangedFiles)
	return v
}

func (m *Manager) execView(ctx context.Context, id string, req client.ExecRequest) client.Execution {
	e, err := m.st.R.GetExecution(ctx, id)
	if err != nil {
		return client.Execution{ID: id, Status: "unknown"}
	}
	v := rowExec(e)
	if v.Status == "running" {
		if b, err := m.box(e.SandboxID); err == nil {
			b.mu.Lock()
			re := b.running[id]
			b.mu.Unlock()
			if re != nil && re.log != nil {
				v.StdoutBytes, v.StderrBytes = re.log.Sizes()
			}
		}
	}
	strip := req.StripANSI == nil || *req.StripANSI
	lim := output.Limits{HeadLines: m.cfg.Output.HeadLines, HeadBytes: m.cfg.Output.HeadBytes, TailLines: m.cfg.Output.TailLines, TailBytes: m.cfg.Output.TailBytes}
	if req.MaxOutputBytes > 0 {
		lim.HeadBytes = req.MaxOutputBytes / 4
		lim.TailBytes = req.MaxOutputBytes - lim.HeadBytes
	}
	v.Stdout, v.StdoutTruncated = summarize(e.StdoutPath, lim, strip, id+" stdout")
	v.Stderr, v.StderrTruncated = summarize(e.StderrPath, lim, strip, id+" stderr")
	v.LogURLs = map[string]string{
		"stdout": "/v1/executions/" + id + "/output?stream=stdout",
		"stderr": "/v1/executions/" + id + "/output?stream=stderr",
		"stream": "/v1/executions/" + id + "/stream",
	}
	return v
}

// summarize reads a log file and returns cleaned, head+tail truncated text.
func summarize(path string, lim output.Limits, strip bool, hint string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return "", false
	}
	window := int64(lim.HeadBytes+lim.TailBytes) * 4
	var data []byte
	cut := false
	if fi.Size() <= window {
		data, _ = os.ReadFile(path)
	} else {
		head, _, _ := output.ReadRange(path, 0, int64(lim.HeadBytes)*2)
		tail, _, _ := output.ReadTail(path, int64(lim.TailBytes)*2)
		if i := strings.LastIndexByte(string(head), '\n'); i > 0 {
			head = head[:i+1]
		}
		if i := strings.IndexByte(string(tail), '\n'); i >= 0 && i < len(tail)-1 {
			tail = tail[i+1:]
		}
		data = append(append(head, []byte(fmt.Sprintf("\n... %d bytes omitted (full log: %s, use grep) ...\n", fi.Size()-int64(len(head)+len(tail)), hint))...), tail...)
		cut = true
	}
	if output.IsBinary(data) {
		return fmt.Sprintf("[%d bytes of binary output; use the output endpoint or sandbox_read_file]", fi.Size()), true
	}
	clean := output.Clean(data, strip)
	text, tr := output.Truncate(clean, lim, "full log: "+hint+", use grep")
	return text, tr || cut
}

// Output returns a raw page of an execution log.
func (m *Manager) Output(ctx context.Context, id, stream string, offset, limit int64) (client.OutputChunk, error) {
	e, err := m.st.R.GetExecution(ctx, id)
	if err != nil {
		return client.OutputChunk{}, errNotFound("execution", id)
	}
	p := e.StdoutPath
	if stream == output.Stderr {
		p = e.StderrPath
	} else {
		stream = output.Stdout
	}
	data, size, err := output.ReadRange(p, offset, limit)
	if err != nil && !os.IsNotExist(err) {
		return client.OutputChunk{}, err
	}
	if offset < 0 {
		offset = max(0, size+offset)
	}
	return client.OutputChunk{Stream: stream, Offset: offset, Data: string(output.Clean(data, false)), TotalSize: size,
		EOF: offset+int64(len(data)) >= size && e.State != "running"}, nil
}

// Grep searches an execution log.
func (m *Manager) Grep(ctx context.Context, id, stream, pattern string, maxMatches int) (client.GrepResult, error) {
	e, err := m.st.R.GetExecution(ctx, id)
	if err != nil {
		return client.GrepResult{}, errNotFound("execution", id)
	}
	if pattern == "" {
		return client.GrepResult{}, errInvalid("pattern is required")
	}
	res := client.GrepResult{Stream: stream, Matches: []client.GrepMatch{}}
	streams := []string{output.Stdout, output.Stderr}
	if stream == output.Stdout || stream == output.Stderr {
		streams = []string{stream}
	} else {
		res.Stream = "both"
	}
	for _, s := range streams {
		p := e.StdoutPath
		if s == output.Stderr {
			p = e.StderrPath
		}
		ms, tr, err := output.Grep(p, pattern, maxMatches)
		if err != nil && !os.IsNotExist(err) {
			return res, errInvalid("%v", err)
		}
		for _, mm := range ms {
			text := mm.Text
			if len(streams) > 1 {
				text = s + ": " + text
			}
			res.Matches = append(res.Matches, client.GrepMatch{Line: mm.Line, Text: text})
		}
		res.Truncated = res.Truncated || tr
	}
	return res, nil
}

// Subscribe streams frames of an execution after seq (for SSE).
func (m *Manager) Subscribe(ctx context.Context, id string, after uint64) ([]output.Frame, <-chan output.Frame, func(), error) {
	e, err := m.st.R.GetExecution(ctx, id)
	if err != nil {
		return nil, nil, nil, errNotFound("execution", id)
	}
	if b, err := m.box(e.SandboxID); err == nil {
		b.mu.Lock()
		re := b.running[id]
		b.mu.Unlock()
		if re != nil && re.log != nil {
			replay, live, cancel := re.log.Subscribe(after)
			return replay, live, cancel, nil
		}
	}
	frames, err := output.ReplayFinished(m.cfg.Dir("executions", id), after)
	if err != nil {
		return nil, nil, nil, err
	}
	ch := make(chan output.Frame)
	close(ch)
	return frames, ch, func() {}, nil
}

// Signal sends a signal to a running execution's process group.
func (m *Manager) Signal(ctx context.Context, id string, sig int) error {
	e, err := m.st.R.GetExecution(ctx, id)
	if err != nil {
		return errNotFound("execution", id)
	}
	if e.State != "running" {
		return errState("execution %s is %s", id, e.State)
	}
	b, err := m.box(e.SandboxID)
	if err != nil {
		return err
	}
	gc := b.guest()
	if gc == nil {
		return errState("sandbox not running")
	}
	if sig == 0 {
		sig = int(unix.SIGTERM)
	}
	_, err = gc.Signal(ctx, &guestv1.SignalRequest{ExecId: id, Signal: int32(sig), ProcessGroup: true})
	return err
}

// WriteStdin writes to a running execution's stdin.
func (m *Manager) WriteStdin(ctx context.Context, id string, data []byte, closeIt bool) error {
	e, err := m.st.R.GetExecution(ctx, id)
	if err != nil {
		return errNotFound("execution", id)
	}
	b, err := m.box(e.SandboxID)
	if err != nil {
		return err
	}
	gc := b.guest()
	if gc == nil {
		return errState("sandbox not running")
	}
	_, err = gc.WriteStdin(ctx, &guestv1.WriteStdinRequest{ExecId: id, Data: data, Close: closeIt})
	return err
}

var _ = io.EOF
