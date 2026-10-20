// Package firecracker is the Linux backend: one Firecracker process per VM,
// started with setsid inside the VM's network namespace. All paths given to
// Firecracker are relative to the VM directory (its working directory), so
// a snapshot restores into any directory without path rewriting.
package firecracker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/karthikponna/tryboxi/internal/vmm"
)

const (
	apiSock   = "fc.sock"
	vsockSock = "v.sock"
	diskName  = "disk.img"
	guestCID  = 3
)

// Driver implements vmm.Driver with Firecracker.
type Driver struct {
	Binary string // firecracker binary (default: from PATH)
	ver    string
}

// New finds the firecracker binary.
func New() (*Driver, error) {
	bin, err := exec.LookPath("firecracker")
	if err != nil {
		return nil, fmt.Errorf("firecracker not found in PATH: %w", err)
	}
	d := &Driver{Binary: bin}
	if out, err := exec.Command(bin, "--version").Output(); err == nil {
		d.ver = strings.Fields(string(out) + " unknown unknown")[1]
	}
	return d, nil
}

func (d *Driver) Name() string    { return "firecracker" }
func (d *Driver) Version() string { return "firecracker-" + d.ver }

func (d *Driver) Capabilities() vmm.Capabilities {
	// Firecracker processes outlive the daemon (setsid), but reattaching
	// after a restart is not implemented yet, so the service treats VMs as
	// lost on restart like the macOS backend.
	return vmm.Capabilities{Backend: "firecracker", MemorySharingOnRestore: true, SurvivesDaemonRestart: false, SnapshotRestore: true}
}

// CloneFile uses FICLONE (XFS/btrfs reflink), falling back to a sparse copy.
func (d *Driver) CloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err == nil {
		return nil
	}
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if err := out.Truncate(fi.Size()); err != nil {
		return err
	}
	// copy_file_range keeps holes on most filesystems.
	var off int64
	for off < fi.Size() {
		n, err := unix.CopyFileRange(int(in.Fd()), &off, int(out.Fd()), nil, int(min(fi.Size()-off, 1<<30)), 0)
		if err != nil {
			if _, serr := in.Seek(off, io.SeekStart); serr != nil {
				return err
			}
			if _, serr := out.Seek(off, io.SeekStart); serr != nil {
				return err
			}
			_, err = io.Copy(out, in)
			return err
		}
		if n == 0 {
			break
		}
	}
	return nil
}

type machine struct {
	id   string
	dir  string
	cmd  *exec.Cmd
	http *http.Client

	mu    sync.Mutex
	state vmm.State
	done  chan vmm.ExitInfo
}

func (d *Driver) start(ctx context.Context, spec vmm.Spec) (*machine, error) {
	_ = os.Remove(filepath.Join(spec.Dir, apiSock))
	_ = os.Remove(filepath.Join(spec.Dir, vsockSock))
	var console io.Writer = io.Discard
	if spec.ConsoleLog != "" {
		f, err := os.OpenFile(spec.ConsoleLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		console = f
	}
	args := []string{d.Binary, "--api-sock", apiSock, "--id", strings.ReplaceAll(spec.ID, "_", "-")}
	if spec.Net.NetNS != "" {
		args = append([]string{"ip", "netns", "exec", spec.Net.NetNS}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Stdout, cmd.Stderr = console, console
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	m := &machine{id: spec.ID, dir: spec.Dir, cmd: cmd, state: vmm.StateStopped, done: make(chan vmm.ExitInfo, 1)}
	sock := filepath.Join(spec.Dir, apiSock)
	m.http = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dd net.Dialer
		return dd.DialContext(ctx, "unix", sock)
	}}, Timeout: 60 * time.Second}
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		m.state = vmm.StateStopped
		m.mu.Unlock()
		reason := "exited"
		if err != nil {
			reason = err.Error()
		}
		m.done <- vmm.ExitInfo{At: time.Now(), Reason: reason, Err: err}
		close(m.done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			_ = m.Kill()
			return nil, errors.New("firecracker API socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m, nil
}

func (m *machine) api(ctx context.Context, method, path string, body any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://fc"+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("firecracker %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(b))
	}
	return nil
}

func fcCmdline(c string) string {
	c = strings.ReplaceAll(c, "console=hvc0", "console=ttyS0")
	return c + " reboot=k panic=1 pci=off"
}

func (d *Driver) Boot(ctx context.Context, spec vmm.Spec) (vmm.Machine, error) {
	// Firecracker sees the root disk at a fixed relative path.
	if filepath.Dir(spec.RootDisk) != filepath.Clean(spec.Dir) || filepath.Base(spec.RootDisk) != diskName {
		return nil, fmt.Errorf("root disk must be %s inside the VM dir", diskName)
	}
	m, err := d.start(ctx, spec)
	if err != nil {
		return nil, err
	}
	steps := []struct {
		path string
		body any
	}{
		{"/machine-config", map[string]any{"vcpu_count": spec.VCPU, "mem_size_mib": spec.MemMB, "smt": false}},
		{"/boot-source", map[string]any{"kernel_image_path": spec.Kernel, "initrd_path": spec.Initrd, "boot_args": fcCmdline(spec.Cmdline)}},
		{"/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": diskName, "is_root_device": false, "is_read_only": false}},
		{"/vsock", map[string]any{"guest_cid": guestCID, "uds_path": vsockSock}},
	}
	for i, dk := range spec.ExtraDisks {
		rel, err := filepath.Rel(spec.Dir, dk.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			_ = m.Kill()
			return nil, fmt.Errorf("extra disk %s must be inside the VM dir", dk.Path)
		}
		id := fmt.Sprintf("disk%d", i+1)
		steps = append(steps, struct {
			path string
			body any
		}{"/drives/" + id, map[string]any{"drive_id": id, "path_on_host": rel, "is_root_device": false, "is_read_only": dk.ReadOnly}})
	}
	if spec.Net.Enabled {
		steps = append(steps, struct {
			path string
			body any
		}{"/network-interfaces/eth0", map[string]any{"iface_id": "eth0", "host_dev_name": spec.Net.TapName, "guest_mac": spec.Net.MAC}})
	}
	for _, s := range steps {
		if err := m.api(ctx, http.MethodPut, s.path, s.body); err != nil {
			_ = m.Kill()
			return nil, err
		}
	}
	if err := m.api(ctx, http.MethodPut, "/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		_ = m.Kill()
		return nil, err
	}
	m.setState(vmm.StateRunning)
	return m, nil
}

func (d *Driver) Restore(ctx context.Context, snap vmm.SnapshotFiles, spec vmm.Spec) (vmm.Machine, error) {
	m, err := d.start(ctx, spec)
	if err != nil {
		return nil, err
	}
	// Memory is mapped MAP_PRIVATE from the snapshot file, so clones share
	// page-cache pages until they write.
	body := map[string]any{
		"snapshot_path": snap.VMState,
		"mem_backend":   map[string]string{"backend_type": "File", "backend_path": snap.Mem},
		"resume_vm":     false,
	}
	if err := m.api(ctx, http.MethodPut, "/snapshot/load", body); err != nil {
		_ = m.Kill()
		return nil, err
	}
	m.setState(vmm.StatePaused)
	return m, nil
}

func (m *machine) setState(s vmm.State) {
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
}

func (m *machine) ID() string { return m.id }
func (m *machine) PID() int {
	if m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}

func (m *machine) State() vmm.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *machine) Pause(ctx context.Context) error {
	if err := m.api(ctx, http.MethodPatch, "/vm", map[string]string{"state": "Paused"}); err != nil {
		return err
	}
	m.setState(vmm.StatePaused)
	return nil
}

func (m *machine) Resume(ctx context.Context) error {
	if err := m.api(ctx, http.MethodPatch, "/vm", map[string]string{"state": "Resumed"}); err != nil {
		return err
	}
	m.setState(vmm.StateRunning)
	return nil
}

func (m *machine) Snapshot(ctx context.Context, dir string) (vmm.SnapshotFiles, error) {
	if m.State() != vmm.StatePaused {
		return vmm.SnapshotFiles{}, vmm.ErrNotPaused
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return vmm.SnapshotFiles{}, err
	}
	sf := vmm.SnapshotFiles{VMState: filepath.Join(dir, "vmstate"), Mem: filepath.Join(dir, "mem"), Disk: filepath.Join(dir, "disk.img")}
	if err := m.api(ctx, http.MethodPut, "/snapshot/create", map[string]string{
		"snapshot_type": "Full", "snapshot_path": sf.VMState, "mem_file_path": sf.Mem,
	}); err != nil {
		return sf, err
	}
	_ = os.Chmod(sf.Mem, 0o600)
	_ = os.Chmod(sf.VMState, 0o600)
	d := &Driver{}
	if err := d.CloneFile(filepath.Join(m.dir, diskName), sf.Disk); err != nil {
		return sf, err
	}
	if fi, err := os.Stat(sf.Mem); err == nil {
		sf.MemBytes = fi.Size()
	}
	if fi, err := os.Stat(sf.Disk); err == nil {
		sf.DiskBytes = fi.Size()
	}
	return sf, nil
}

func (m *machine) Stop(ctx context.Context) error {
	_ = m.api(ctx, http.MethodPut, "/actions", map[string]string{"action_type": "SendCtrlAltDel"})
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return m.Kill()
	}
}

func (m *machine) Kill() error {
	if m.cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-m.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-m.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}

func (m *machine) Done() <-chan vmm.ExitInfo { return m.done }

// DialVsock uses Firecracker's hybrid vsock: connect to the host socket and
// send "CONNECT <port>".
func (m *machine) DialVsock(ctx context.Context, port uint32) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", filepath.Join(m.dir, vsockSock))
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "OK ") {
		c.Close()
		return nil, fmt.Errorf("vsock handshake: %q %v", line, err)
	}
	_ = c.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufConn{Conn: c, r: br}, nil
	}
	return c, nil
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }
