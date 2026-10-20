// Package initd does PID 1 duties: switching from the initramfs to the real
// root disk, mounting pseudo filesystems, configuring the network, and
// powering off.
package initd

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// AgentPath is where the agent lives on the real root filesystem.
const AgentPath = "/sbin/sandbox-agent"

// InInitramfs reports whether we are running as /init from the initramfs.
func InInitramfs() bool {
	if os.Getpid() != 1 {
		return false
	}
	_, err := os.Stat("/init")
	_, err2 := os.Stat("/.sandboxd-initramfs")
	return err == nil && err2 == nil
}

func cmdlineValue(key, def string) string {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return def
	}
	for _, f := range strings.Fields(string(b)) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return def
}

// SwitchRoot mounts the root block device, installs this binary on it, and
// execs it as PID 1 on the new root. It only returns on error.
func SwitchRoot() error {
	_ = os.MkdirAll("/dev", 0o755)
	_ = os.MkdirAll("/proc", 0o555)
	if err := unix.Mount("devtmpfs", "/dev", "devtmpfs", 0, ""); err != nil && !errors.Is(err, unix.EBUSY) {
		return fmt.Errorf("mount devtmpfs: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil && !errors.Is(err, unix.EBUSY) {
		return fmt.Errorf("mount proc: %w", err)
	}
	dev := cmdlineValue("sbx.root", "/dev/vda")
	fstype := cmdlineValue("sbx.rootfstype", "ext4")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(dev); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("root device %s did not appear", dev)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.MkdirAll("/newroot", 0o755); err != nil {
		return err
	}
	// commit=1 bounds what a VM crash can lose to about a second of writes,
	// which is what host-side recovery can then extract.
	data := ""
	if fstype == "ext4" {
		data = "commit=1"
	}
	if err := unix.Mount(dev, "/newroot", fstype, 0, data); err != nil {
		return fmt.Errorf("mount %s: %w", dev, err)
	}
	if err := copyFile("/init", "/newroot"+AgentPath, 0o755); err != nil {
		return fmt.Errorf("install agent: %w", err)
	}
	_ = os.Remove("/init")
	_ = os.Remove("/.sandboxd-initramfs")
	_ = unix.Unmount("/proc", unix.MNT_DETACH)
	_ = os.MkdirAll("/newroot/dev", 0o755)
	if err := unix.Mount("/dev", "/newroot/dev", "", unix.MS_MOVE, ""); err != nil {
		_ = unix.Unmount("/dev", unix.MNT_DETACH)
	}
	if err := os.Chdir("/newroot"); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	return unix.Exec(AgentPath, []string{AgentPath}, os.Environ())
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

type mnt struct {
	src, dst, fstype string
	flags            uintptr
	data             string
}

// Setup mounts pseudo filesystems and basic runtime state on the real root.
func Setup() {
	_ = unix.Mount("", "/", "", unix.MS_REMOUNT, "")
	mounts := []mnt{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620,gid=5"},
		{"tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
	}
	for _, m := range mounts {
		_ = os.MkdirAll(m.dst, 0o755)
		if err := unix.Mount(m.src, m.dst, m.fstype, m.flags, m.data); err != nil && !errors.Is(err, unix.EBUSY) {
			log.Printf("initd: mount %s: %v", m.dst, err)
		}
	}
	_ = os.Remove("/dev/ptmx")
	_ = os.Symlink("pts/ptmx", "/dev/ptmx")
	for _, l := range [][2]string{{"/proc/self/fd", "/dev/fd"}, {"/proc/self/fd/0", "/dev/stdin"}, {"/proc/self/fd/1", "/dev/stdout"}, {"/proc/self/fd/2", "/dev/stderr"}} {
		_ = os.Symlink(l[0], l[1])
	}
	_ = os.MkdirAll("/run/lock", 0o1777)
	_ = os.Chmod("/tmp", 0o1777)
	_ = os.MkdirAll("/var/lib/sandbox-agent/spool", 0o700)
	if lo, err := netlink.LinkByName("lo"); err == nil {
		_ = netlink.LinkSetUp(lo)
	}
	if b, err := os.ReadFile("/etc/hostname"); err == nil {
		_ = unix.Sethostname([]byte(strings.TrimSpace(string(b))))
	}
}

// SetHostname sets the kernel hostname and /etc/hostname, /etc/hosts.
func SetHostname(name string) error {
	if name == "" {
		return nil
	}
	if err := unix.Sethostname([]byte(name)); err != nil {
		return err
	}
	_ = os.WriteFile("/etc/hostname", []byte(name+"\n"), 0o644)
	hosts := fmt.Sprintf("127.0.0.1 localhost\n127.0.1.1 %s\n::1 localhost ip6-localhost ip6-loopback\n", name)
	return os.WriteFile("/etc/hosts", []byte(hosts), 0o644)
}

// ConfigureNetwork brings up the first non-loopback interface with a static
// address, default route, and resolv.conf.
func ConfigureNetwork(ipCIDR, gateway string, dns []string, mtu int) error {
	if ipCIDR == "" {
		return nil
	}
	var link netlink.Link
	deadline := time.Now().Add(5 * time.Second)
	for link == nil {
		links, err := netlink.LinkList()
		if err != nil {
			return err
		}
		for _, l := range links {
			if l.Attrs().Name != "lo" && l.Type() == "device" {
				link = l
				break
			}
		}
		if link == nil {
			if time.Now().After(deadline) {
				return errors.New("no network interface found")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if mtu > 0 {
		_ = netlink.LinkSetMTU(link, mtu)
	}
	addr, err := netlink.ParseAddr(ipCIDR)
	if err != nil {
		return err
	}
	if err := netlink.AddrReplace(link, addr); err != nil {
		return fmt.Errorf("addr: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	if gateway != "" {
		gw := net.ParseIP(gateway)
		_ = netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: gw})
	}
	if len(dns) > 0 {
		var b strings.Builder
		for _, d := range dns {
			fmt.Fprintf(&b, "nameserver %s\n", d)
		}
		b.WriteString("options edns0 trust-ad\n")
		_ = os.Remove("/etc/resolv.conf")
		if err := os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// SetTime sets the wall clock and returns the previous skew.
func SetTime(unixNs int64) (time.Duration, error) {
	skew := time.Since(time.Unix(0, unixNs))
	tv := unix.NsecToTimeval(unixNs)
	return skew, unix.Settimeofday(&tv)
}

// Reseed mixes host entropy into the kernel pool and credits it, so cloned
// VMs diverge immediately.
func Reseed(entropy []byte) error {
	if len(entropy) == 0 {
		return errors.New("no entropy")
	}
	f, err := os.OpenFile("/dev/urandom", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	// struct rand_pool_info { int entropy_count; int buf_size; __u32 buf[]; }
	buf := make([]byte, 8+len(entropy))
	*(*int32)(unsafe.Pointer(&buf[0])) = int32(len(entropy) * 8)
	*(*int32)(unsafe.Pointer(&buf[4])) = int32(len(entropy))
	copy(buf[8:], entropy)
	const rndAddEntropy = 0x40085203
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), rndAddEntropy, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		_, werr := f.Write(entropy)
		return werr
	}
	return nil
}

// Poweroff syncs disks and powers the VM off (or reboots).
func Poweroff(reboot bool) {
	unix.Sync()
	cmd := unix.LINUX_REBOOT_CMD_POWER_OFF
	if reboot {
		cmd = unix.LINUX_REBOOT_CMD_RESTART
	}
	_ = unix.Reboot(cmd)
	os.Exit(0)
}

// RootDevice returns the root block device from the kernel cmdline.
func RootDevice() string { return cmdlineValue("sbx.root", "/dev/vda") }

// Console reopens stdout/stderr on /dev/console so logs reach the host.
func Console() {
	f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_ = unix.Dup2(int(f.Fd()), 1)
	_ = unix.Dup2(int(f.Fd()), 2)
	log.SetOutput(os.Stderr)
}
