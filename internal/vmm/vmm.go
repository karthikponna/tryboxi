// Package vmm defines the hypervisor-neutral driver interface that the
// Firecracker (Linux) and Virtualization.framework (macOS) backends implement.
package vmm

import (
	"context"
	"errors"
	"net"
	"os"
	"time"
)

// GuestPort is the vsock port sandbox-agent listens on.
const GuestPort = 52

// Disk is an extra block device.
type Disk struct {
	Path     string
	ReadOnly bool
}

// Network describes the VM's single NIC. Exactly one backend-specific
// field is used.
type Network struct {
	Enabled bool
	MAC     string
	// File is one end of a unix datagram socketpair carrying raw ethernet
	// frames (macOS userspace networking).
	File *os.File
	// TapName is a host TAP device inside NetNS (Linux).
	TapName string
	NetNS   string
}

// Spec fully describes a VM. Restoring a snapshot requires an identical
// Spec apart from disk paths and the network attachment.
type Spec struct {
	ID         string
	Dir        string // per-VM working directory owned by the daemon
	Kernel     string
	Initrd     string
	Cmdline    string
	RootDisk   string
	ExtraDisks []Disk
	VCPU       int
	MemMB      int
	Net        Network
	ConsoleLog string
	// MachineID is opaque backend data that must be reused on restore
	// (Virtualization.framework generic machine identifier).
	MachineID []byte
}

// State of a machine as seen by the driver.
type State string

const (
	StateRunning State = "running"
	StatePaused  State = "paused"
	StateStopped State = "stopped"
	StateError   State = "error"
)

// ExitInfo describes how a machine stopped.
type ExitInfo struct {
	At     time.Time
	Reason string
	Err    error
}

// SnapshotFiles lists what a snapshot consists of. Paths are inside the
// directory passed to Snapshot.
type SnapshotFiles struct {
	VMState   string // device + CPU state (and memory on macOS)
	Mem       string // separate memory file (Firecracker); empty on macOS
	Disk      string // root disk clone taken while paused
	MachineID []byte
	MemBytes  int64
	DiskBytes int64
}

// Machine is a booted (or restored) VM.
type Machine interface {
	ID() string
	State() State
	// PID is the VMM process id, or 0 when the VM is hosted in-process.
	PID() int
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
	// Snapshot requires the machine to be paused. It writes VM state into dir
	// and clones the root disk there.
	Snapshot(ctx context.Context, dir string) (SnapshotFiles, error)
	// Stop requests a clean power-off and waits up to the context deadline,
	// then forces it.
	Stop(ctx context.Context) error
	Kill() error
	Done() <-chan ExitInfo
	DialVsock(ctx context.Context, port uint32) (net.Conn, error)
}

// Capabilities advertise backend behaviour that callers plan around.
type Capabilities struct {
	Backend                string
	MemorySharingOnRestore bool // clones share snapshot memory pages
	SurvivesDaemonRestart  bool
	SnapshotRestore        bool
}

// Driver creates machines.
type Driver interface {
	Name() string
	Version() string
	Capabilities() Capabilities
	Boot(ctx context.Context, spec Spec) (Machine, error)
	// Restore starts a machine from snapshot files. The machine is returned
	// paused; the caller resumes it after attaching the network.
	Restore(ctx context.Context, snap SnapshotFiles, spec Spec) (Machine, error)
	// CloneFile makes a copy-on-write copy of src at dst.
	CloneFile(src, dst string) error
}

var (
	ErrUnsupported = errors.New("vmm: operation not supported by this backend")
	ErrNotPaused   = errors.New("vmm: machine must be paused")
)
