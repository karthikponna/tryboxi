//go:build darwin

// Package vz is the macOS backend built on Apple's Virtualization.framework.
// VMs are hosted inside the daemon process: if the daemon exits, they stop.
package vz

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/sys/unix"

	"github.com/karthikponna/tryboxi/internal/vmm"
)

// Driver implements vmm.Driver.
type Driver struct{}

func New() *Driver { return &Driver{} }

func (d *Driver) Name() string    { return "vz" }
func (d *Driver) Version() string { return osVersion() }

func (d *Driver) Capabilities() vmm.Capabilities {
	return vmm.Capabilities{Backend: "vz", MemorySharingOnRestore: false, SurvivesDaemonRestart: false, SnapshotRestore: true}
}

func osVersion() string {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return "unknown"
	}
	return "macos-" + v
}

// CloneFile uses APFS clonefile(2): instant and copy-on-write.
func (d *Driver) CloneFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}

type machine struct {
	id     string
	spec   vmm.Spec
	vm     *vz.VirtualMachine
	config *vz.VirtualMachineConfiguration
	mid    []byte

	mu    sync.Mutex
	state vmm.State
	done  chan vmm.ExitInfo
	once  sync.Once
}

func (d *Driver) buildConfig(spec vmm.Spec) (*vz.VirtualMachineConfiguration, []byte, error) {
	opts := []vz.LinuxBootLoaderOption{vz.WithCommandLine(spec.Cmdline)}
	if spec.Initrd != "" {
		opts = append(opts, vz.WithInitrd(spec.Initrd))
	}
	boot, err := vz.NewLinuxBootLoader(spec.Kernel, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("bootloader: %w", err)
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, uint(spec.VCPU), uint64(spec.MemMB)*1024*1024)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}

	var mid *vz.GenericMachineIdentifier
	if len(spec.MachineID) > 0 {
		mid, err = vz.NewGenericMachineIdentifierWithData(spec.MachineID)
	} else {
		mid, err = vz.NewGenericMachineIdentifier()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("machine identifier: %w", err)
	}
	platform, err := vz.NewGenericPlatformConfiguration(vz.WithGenericMachineIdentifier(mid))
	if err != nil {
		return nil, nil, fmt.Errorf("platform: %w", err)
	}
	cfg.SetPlatformVirtualMachineConfiguration(platform)

	if spec.ConsoleLog != "" {
		att, err := vz.NewFileSerialPortAttachment(spec.ConsoleLog, true)
		if err != nil {
			return nil, nil, fmt.Errorf("console: %w", err)
		}
		con, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(att)
		if err != nil {
			return nil, nil, err
		}
		cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{con})
	}

	disks := []vmm.Disk{{Path: spec.RootDisk}}
	disks = append(disks, spec.ExtraDisks...)
	var storage []vz.StorageDeviceConfiguration
	for _, dk := range disks {
		att, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(dk.Path, dk.ReadOnly, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync)
		if err != nil {
			return nil, nil, fmt.Errorf("disk %s: %w", dk.Path, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return nil, nil, err
		}
		storage = append(storage, blk)
	}
	cfg.SetStorageDevicesVirtualMachineConfiguration(storage)

	if spec.Net.Enabled {
		if spec.Net.File == nil {
			return nil, nil, errors.New("network enabled but no file handle")
		}
		att, err := vz.NewFileHandleNetworkDeviceAttachment(spec.Net.File)
		if err != nil {
			return nil, nil, fmt.Errorf("net attachment: %w", err)
		}
		nic, err := vz.NewVirtioNetworkDeviceConfiguration(att)
		if err != nil {
			return nil, nil, err
		}
		if spec.Net.MAC != "" {
			hw, err := net.ParseMAC(spec.Net.MAC)
			if err != nil {
				return nil, nil, err
			}
			mac, err := vz.NewMACAddress(hw)
			if err != nil {
				return nil, nil, err
			}
			nic.SetMACAddress(mac)
		}
		cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})
	}

	ent, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, nil, err
	}
	cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{ent})

	sock, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, nil, err
	}
	cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{sock})

	if ok, err := cfg.Validate(); !ok || err != nil {
		return nil, nil, fmt.Errorf("invalid VM configuration: %w", err)
	}
	return cfg, mid.DataRepresentation(), nil
}

func (d *Driver) newMachine(spec vmm.Spec) (*machine, error) {
	cfg, mid, err := d.buildConfig(spec)
	if err != nil {
		return nil, err
	}
	vm, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return nil, fmt.Errorf("new vm: %w", err)
	}
	m := &machine{id: spec.ID, spec: spec, vm: vm, config: cfg, mid: mid, state: vmm.StateStopped, done: make(chan vmm.ExitInfo, 1)}
	go m.watch()
	return m, nil
}

func (m *machine) watch() {
	for st := range m.vm.StateChangedNotify() {
		switch st {
		case vz.VirtualMachineStateRunning:
			m.setState(vmm.StateRunning)
		case vz.VirtualMachineStatePaused:
			m.setState(vmm.StatePaused)
		case vz.VirtualMachineStateStopped:
			m.setState(vmm.StateStopped)
			m.finish(vmm.ExitInfo{At: time.Now(), Reason: "stopped"})
			return
		case vz.VirtualMachineStateError:
			m.setState(vmm.StateError)
			m.finish(vmm.ExitInfo{At: time.Now(), Reason: "error", Err: errors.New("virtualization framework reported an error state")})
			return
		}
	}
}

func (m *machine) finish(info vmm.ExitInfo) {
	m.once.Do(func() {
		m.done <- info
		close(m.done)
	})
}

func (m *machine) setState(s vmm.State) {
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
}

func (d *Driver) Boot(ctx context.Context, spec vmm.Spec) (vmm.Machine, error) {
	m, err := d.newMachine(spec)
	if err != nil {
		return nil, err
	}
	if err := m.vm.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	m.setState(vmm.StateRunning)
	return m, nil
}

func (d *Driver) Restore(ctx context.Context, snap vmm.SnapshotFiles, spec vmm.Spec) (vmm.Machine, error) {
	spec.MachineID = snap.MachineID
	m, err := d.newMachine(spec)
	if err != nil {
		return nil, err
	}
	if ok, err := m.config.ValidateSaveRestoreSupport(); !ok || err != nil {
		return nil, fmt.Errorf("configuration does not support restore: %w", err)
	}
	if err := m.vm.RestoreMachineStateFromURL(snap.VMState); err != nil {
		return nil, fmt.Errorf("restore state: %w", err)
	}
	m.setState(vmm.StatePaused)
	return m, nil
}

func (m *machine) ID() string { return m.id }
func (m *machine) PID() int   { return 0 }

func (m *machine) State() vmm.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *machine) Pause(ctx context.Context) error {
	if m.vm.State() == vz.VirtualMachineStatePaused {
		return nil
	}
	if err := m.vm.Pause(); err != nil {
		return err
	}
	m.setState(vmm.StatePaused)
	return nil
}

func (m *machine) Resume(ctx context.Context) error {
	if m.vm.State() == vz.VirtualMachineStateRunning {
		return nil
	}
	if err := m.vm.Resume(); err != nil {
		return err
	}
	m.setState(vmm.StateRunning)
	return nil
}

func (m *machine) Snapshot(ctx context.Context, dir string) (vmm.SnapshotFiles, error) {
	if m.vm.State() != vz.VirtualMachineStatePaused {
		return vmm.SnapshotFiles{}, vmm.ErrNotPaused
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return vmm.SnapshotFiles{}, err
	}
	sf := vmm.SnapshotFiles{
		VMState:   filepath.Join(dir, "vmstate"),
		Disk:      filepath.Join(dir, "disk.img"),
		MachineID: m.mid,
	}
	if err := m.vm.SaveMachineStateToPath(sf.VMState); err != nil {
		return sf, fmt.Errorf("save state: %w", err)
	}
	_ = os.Chmod(sf.VMState, 0o600)
	if err := unix.Clonefile(m.spec.RootDisk, sf.Disk, unix.CLONE_NOFOLLOW); err != nil {
		return sf, fmt.Errorf("clone disk: %w", err)
	}
	if fi, err := os.Stat(sf.VMState); err == nil {
		sf.MemBytes = fi.Size()
	}
	if fi, err := os.Stat(sf.Disk); err == nil {
		sf.DiskBytes = fi.Size()
	}
	return sf, nil
}

func (m *machine) Stop(ctx context.Context) error {
	if m.State() == vmm.StateStopped {
		return nil
	}
	if m.vm.CanRequestStop() {
		_, _ = m.vm.RequestStop()
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return m.Kill()
	}
}

func (m *machine) Kill() error {
	if m.State() == vmm.StateStopped {
		return nil
	}
	if m.vm.CanStop() {
		if err := m.vm.Stop(); err != nil {
			return err
		}
	}
	select {
	case <-m.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}

func (m *machine) Done() <-chan vmm.ExitInfo { return m.done }

func (m *machine) DialVsock(ctx context.Context, port uint32) (net.Conn, error) {
	devs := m.vm.SocketDevices()
	if len(devs) == 0 {
		return nil, errors.New("no vsock device")
	}
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := devs[0].Connect(port)
		if err != nil {
			ch <- res{nil, err}
			return
		}
		ch <- res{c, nil}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
}
