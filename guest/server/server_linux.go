// Package server implements the guest.v1.Guest gRPC service.
package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/guest/execd"
	"github.com/karthikponna/tryboxi/guest/files"
	"github.com/karthikponna/tryboxi/guest/initd"
	"github.com/karthikponna/tryboxi/guest/proc"
)

const ProtocolVersion = 1

// Version is set at build time via -ldflags.
var Version = "dev"

// Server is the guest agent service.
type Server struct {
	guestv1.UnimplementedGuestServer

	Execs    *execd.Manager
	bootID   string
	bootTime time.Time
	lastSeen atomic.Int64 // monotonic nanos of last host contact
	deadman  atomic.Int64 // seconds; 0 disables
	mono0    time.Time
	configMu sync.Mutex
}

func New() *Server {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	s := &Server{
		Execs:    execd.NewManager("/var/lib/sandbox-agent/spool"),
		bootID:   hex.EncodeToString(b),
		bootTime: time.Now(),
		mono0:    time.Now(),
	}
	s.deadman.Store(600)
	s.touch()
	return s
}

func (s *Server) touch() { s.lastSeen.Store(int64(time.Since(s.mono0))) }

// Interceptors record host contact for the dead-man switch.
func (s *Server) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		s.touch()
		return h(ctx, req)
	}
}

func (s *Server) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		s.touch()
		return h(srv, ss)
	}
}

// RunDeadman powers the VM off if the host has not been in contact for the
// configured period. Large monotonic jumps (VM paused/restored) reset it.
func (s *Server) RunDeadman() {
	const tick = 5 * time.Second
	prev := time.Now()
	for {
		time.Sleep(tick)
		now := time.Now()
		if gap := now.Sub(prev); gap > 3*tick {
			s.touch() // we were suspended; don't count the gap
		}
		prev = now
		d := s.deadman.Load()
		if d <= 0 {
			continue
		}
		idle := time.Since(s.mono0) - time.Duration(s.lastSeen.Load())
		if idle > time.Duration(d)*time.Second {
			log.Printf("sandbox-agent: no host contact for %s, powering off", idle.Round(time.Second))
			initd.Poweroff(false)
		}
	}
}

func (s *Server) Ping(ctx context.Context, _ *guestv1.PingRequest) (*guestv1.PingResponse, error) {
	return &guestv1.PingResponse{UptimeMs: uptimeMs(), BootId: s.bootID}, nil
}

func (s *Server) Version(ctx context.Context, _ *guestv1.VersionRequest) (*guestv1.VersionResponse, error) {
	var u unix.Utsname
	_ = unix.Uname(&u)
	return &guestv1.VersionResponse{
		AgentVersion:    Version,
		ProtocolVersion: ProtocolVersion,
		KernelRelease:   unix.ByteSliceToString(u.Release[:]),
		Arch:            runtime.GOARCH,
	}, nil
}

func uptimeMs() int64 {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts)
	return ts.Nano() / 1e6
}

func (s *Server) Heartbeat(req *guestv1.HeartbeatRequest, stream grpc.ServerStreamingServer[guestv1.Beat]) error {
	iv := time.Duration(req.IntervalMs) * time.Millisecond
	if iv < 200*time.Millisecond {
		iv = 2 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	var seq uint64
	for {
		seq++
		b := &guestv1.Beat{Seq: seq, UptimeMs: uptimeMs(), RunningExecs: uint32(s.Execs.Running()), OomKills: uint32(execd.OOMKills())}
		b.Load1 = load1()
		b.MemAvailKb = memAvailKB()
		var st unix.Statfs_t
		if unix.Statfs("/", &st) == nil {
			b.DiskFreeKb = st.Bavail * uint64(st.Bsize) / 1024
		}
		if err := stream.Send(b); err != nil {
			return err
		}
		s.touch()
		select {
		case <-stream.Context().Done():
			return nil
		case <-t.C:
		}
	}
}

func load1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func memAvailKB() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemAvailable:"); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return n
		}
	}
	return 0
}

func (s *Server) Exec(req *guestv1.ExecRequest, stream grpc.ServerStreamingServer[guestv1.ExecFrame]) error {
	e, err := s.Execs.Start(req)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return s.stream(e, 0, stream)
}

func (s *Server) Attach(req *guestv1.AttachRequest, stream grpc.ServerStreamingServer[guestv1.ExecFrame]) error {
	e, ok := s.Execs.Get(req.ExecId)
	if !ok {
		return status.Errorf(codes.NotFound, "exec %s not found", req.ExecId)
	}
	return s.stream(e, req.AfterSeq, stream)
}

func (s *Server) stream(e *execd.Exec, after uint64, stream grpc.ServerStreamingServer[guestv1.ExecFrame]) error {
	return e.Frames(after, stream.Context().Done(), func(b []byte) error {
		var f guestv1.ExecFrame
		if err := unmarshal(b, &f); err != nil {
			return err
		}
		return stream.Send(&f)
	})
}

func (s *Server) Ack(ctx context.Context, req *guestv1.AckRequest) (*guestv1.AckResponse, error) {
	s.Execs.Ack(req.ExecId, req.Seq)
	return &guestv1.AckResponse{}, nil
}

func (s *Server) Signal(ctx context.Context, req *guestv1.SignalRequest) (*guestv1.SignalResponse, error) {
	if err := s.Execs.Signal(req.ExecId, int(req.Signal), req.ProcessGroup); err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &guestv1.SignalResponse{}, nil
}

func (s *Server) WriteStdin(ctx context.Context, req *guestv1.WriteStdinRequest) (*guestv1.WriteStdinResponse, error) {
	if err := s.Execs.WriteStdin(req.ExecId, req.Data, req.Close); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &guestv1.WriteStdinResponse{}, nil
}

func lookupOwner(name string) (int, int) {
	if name == "" {
		return -1, -1
	}
	u, err := user.Lookup(name)
	if err != nil {
		return -1, -1
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid
}

func (s *Server) PutFiles(stream grpc.ClientStreamingServer[guestv1.PutFilesChunk, guestv1.PutFilesResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	dest := first.DestDir
	if dest == "" {
		dest = "/workspace"
	}
	uid, gid := lookupOwner(first.Owner)
	pr, pw := io.Pipe()
	type result struct {
		r   files.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := files.Extract(pr, dest, first.Delete, uid, gid)
		_, _ = io.Copy(io.Discard, pr)
		done <- result{r, err}
	}()
	if len(first.Data) > 0 {
		if _, err := pw.Write(first.Data); err != nil {
			pw.CloseWithError(err)
		}
	}
	for {
		c, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			pw.CloseWithError(err)
			<-done
			return err
		}
		if _, err := pw.Write(c.Data); err != nil {
			break
		}
	}
	pw.Close()
	res := <-done
	if res.err != nil {
		return status.Error(codes.InvalidArgument, res.err.Error())
	}
	return stream.SendAndClose(&guestv1.PutFilesResponse{
		FilesWritten: uint32(res.r.Written), FilesDeleted: uint32(res.r.Deleted), BytesWritten: res.r.Bytes,
	})
}

type chunkWriter struct {
	stream grpc.ServerStreamingServer[guestv1.DataChunk]
}

func (w chunkWriter) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += 256 * 1024 {
		end := min(off+256*1024, len(p))
		if err := w.stream.Send(&guestv1.DataChunk{Data: append([]byte(nil), p[off:end]...)}); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

func (s *Server) GetFiles(req *guestv1.GetFilesRequest, stream grpc.ServerStreamingServer[guestv1.DataChunk]) error {
	if _, err := os.Lstat(req.Path); err != nil {
		return status.Error(codes.NotFound, err.Error())
	}
	bw := bufio.NewWriterSize(chunkWriter{stream}, 256*1024)
	if err := files.Archive(bw, req.Path, req.Exclude, req.MaxBytes); err != nil {
		if errors.Is(err, files.ErrTooLarge) {
			return status.Error(codes.ResourceExhausted, err.Error())
		}
		return status.Error(codes.Internal, err.Error())
	}
	return bw.Flush()
}

func (s *Server) Stat(ctx context.Context, req *guestv1.StatRequest) (*guestv1.StatResponse, error) {
	fi, err := os.Stat(req.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &guestv1.StatResponse{}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &guestv1.StatResponse{Exists: true, IsDir: fi.IsDir(), Size: uint64(fi.Size()), Mode: uint32(fi.Mode()), MtimeMs: fi.ModTime().UnixMilli()}, nil
}

func agentCred() *syscall.Credential {
	u, err := user.Lookup("agent")
	if err != nil {
		return nil
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/agent", "LANG=C.UTF-8", "GIT_OPTIONAL_LOCKS=0"}
	out, errb, st, err := proc.Output(ctx, proc.Cmd{Argv: append([]string{"git", "-c", "safe.directory=*", "-C", dir}, args...), Dir: dir, Env: env, Cred: agentCred()})
	if err != nil {
		return nil, err
	}
	if st.ExitCode() != 0 {
		return out, errors.New(strings.TrimSpace(string(errb)))
	}
	return out, nil
}

func lines(b []byte) []string {
	var out []string
	for _, l := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
		if len(l) > 0 {
			out = append(out, string(l))
		}
	}
	return out
}

func (s *Server) Diff(ctx context.Context, req *guestv1.DiffRequest) (*guestv1.DiffResponse, error) {
	dir := req.Dir
	if dir == "" {
		dir = "/workspace"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp := &guestv1.DiffResponse{}
	if _, err := os.Stat(dir + "/.git"); err != nil {
		return resp, nil
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	hasHead := err == nil
	resp.Head = strings.TrimSpace(string(head))
	untracked, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err == nil {
		for _, p := range bytes.Split(untracked, []byte{0}) {
			if len(p) > 0 {
				resp.Untracked = append(resp.Untracked, string(p))
			}
		}
	}
	if hasHead {
		if patch, err := git(ctx, dir, "diff", "--binary", "--no-color", "--no-ext-diff", "HEAD"); err == nil {
			resp.Patch = patch
		}
		if names, err := git(ctx, dir, "diff", "--name-only", "--no-color", "HEAD"); err == nil {
			resp.Changed = lines(names)
		}
	}
	resp.Changed = append(resp.Changed, resp.Untracked...)
	return resp, nil
}

func (s *Server) SyncTime(ctx context.Context, req *guestv1.SyncTimeRequest) (*guestv1.SyncTimeResponse, error) {
	skew, err := initd.SetTime(req.UnixNs)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &guestv1.SyncTimeResponse{SkewMs: skew.Milliseconds()}, nil
}

func (s *Server) Reseed(ctx context.Context, req *guestv1.ReseedRequest) (*guestv1.ReseedResponse, error) {
	if len(req.Entropy) < 32 {
		return nil, status.Error(codes.InvalidArgument, "need at least 32 bytes of entropy")
	}
	if err := initd.Reseed(req.Entropy); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	s.bootID = hex.EncodeToString(b)
	return &guestv1.ReseedResponse{}, nil
}

func (s *Server) Configure(ctx context.Context, req *guestv1.ConfigureRequest) (*guestv1.ConfigureResponse, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := initd.SetHostname(req.Hostname); err != nil {
		return nil, status.Errorf(codes.Internal, "hostname: %v", err)
	}
	if err := initd.ConfigureNetwork(req.IpCidr, req.Gateway, req.Dns, int(req.Mtu)); err != nil {
		return nil, status.Errorf(codes.Internal, "network: %v", err)
	}
	if req.GrowRootfs {
		out, errb, st, err := proc.Output(ctx, proc.Cmd{Argv: []string{"resize2fs", initd.RootDevice()}})
		if err != nil || st.ExitCode() != 0 {
			log.Printf("resize2fs: %v %s %s", err, out, errb)
		}
	}
	if req.DeadmanS > 0 {
		s.deadman.Store(int64(req.DeadmanS))
	}
	if req.Env != nil {
		s.Execs.SetDefaultEnv(req.Env)
	}
	return &guestv1.ConfigureResponse{}, nil
}

func (s *Server) Shutdown(ctx context.Context, req *guestv1.ShutdownRequest) (*guestv1.ShutdownResponse, error) {
	go func() {
		time.Sleep(100 * time.Millisecond)
		initd.Poweroff(req.Reboot)
	}()
	return &guestv1.ShutdownResponse{}, nil
}
