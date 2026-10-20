package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/karthikponna/tryboxi/internal/scheduler"
)

// MemoryPressure uses PSI: sustained "full" stalls are critical.
func MemoryPressure() scheduler.Pressure {
	b, err := os.ReadFile("/proc/pressure/memory")
	if err != nil {
		return scheduler.PressureNormal
	}
	var some, full float64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(strings.TrimPrefix(f[1], "avg10="), 64)
		if f[0] == "some" {
			some = v
		} else if f[0] == "full" {
			full = v
		}
	}
	switch {
	case full > 10:
		return scheduler.PressureCritical
	case some > 20:
		return scheduler.PressureWarn
	}
	return scheduler.PressureNormal
}

// Checks returns Linux-specific doctor checks.
func Checks(dataRoot string) []Check {
	var checks []Check
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	checks = append(checks, Check{Name: "/dev/kvm", OK: err == nil, Detail: fmt.Sprint(err), Fix: "enable KVM (nested virtualization in Lima: nestedVirtualization: true)"})
	if f != nil {
		f.Close()
	}
	_, err = os.Stat("/sys/fs/cgroup/cgroup.controllers")
	checks = append(checks, Check{Name: "cgroups v2", OK: err == nil, Detail: "unified hierarchy"})
	_, err = exec.LookPath("firecracker")
	checks = append(checks, Check{Name: "firecracker", OK: err == nil, Fix: "scripts/dev/lima-provision.sh installs it"})
	_, err = exec.LookPath("jailer")
	checks = append(checks, Check{Name: "jailer", OK: err == nil})
	_, err = exec.LookPath("nft")
	checks = append(checks, Check{Name: "nftables", OK: err == nil, Fix: "apt install nftables"})
	var st unix.Statfs_t
	reflink := false
	if unix.Statfs(dataRoot, &st) == nil {
		reflink = st.Type == 0x58465342 || st.Type == 0x9123683e // XFS, btrfs
	}
	checks = append(checks, Check{Name: "reflink filesystem", OK: reflink, Detail: dataRoot, Fix: "put the data root on XFS (mkfs.xfs -m reflink=1) or btrfs"})
	_, err = os.Stat("/proc/pressure/memory")
	checks = append(checks, Check{Name: "PSI", OK: err == nil})
	return append(checks, CommonChecks(dataRoot)...)
}
