package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/karthikponna/tryboxi/internal/scheduler"
)

// MemoryPressure reads the kernel's VM pressure level (1 normal, 2 warn,
// 4 critical).
func MemoryPressure() scheduler.Pressure {
	v, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return scheduler.PressureNormal
	}
	switch {
	case v >= 4:
		return scheduler.PressureCritical
	case v >= 2:
		return scheduler.PressureWarn
	}
	return scheduler.PressureNormal
}

// MacOSMajor returns the major macOS version.
func MacOSMajor() int {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return n
}

// Checks returns macOS-specific doctor checks.
func Checks(dataRoot string) []Check {
	ver, _ := unix.Sysctl("kern.osproductversion")
	checks := []Check{
		{Name: "macOS version", OK: MacOSMajor() >= 14, Detail: "macOS " + ver, Fix: "macOS 14 or newer is needed for VM save/restore"},
		{Name: "Apple Silicon", OK: runtime.GOARCH == "arm64", Detail: runtime.GOARCH},
	}
	exe, _ := os.Executable()
	out, err := exec.Command("codesign", "-d", "--entitlements", "-", "--xml", exe).CombinedOutput()
	ok := err == nil && strings.Contains(string(out), "com.apple.security.virtualization")
	checks = append(checks, Check{Name: "virtualization entitlement", OK: ok, Detail: exe,
		Fix: "make sign  (codesign --entitlements build/vz.entitlements -s - " + exe + ")"})
	fsType := "unknown"
	var st unix.Statfs_t
	if err := unix.Statfs(dataRoot, &st); err == nil {
		fsType = unix.ByteSliceToString(st.Fstypename[:])
	}
	checks = append(checks, Check{Name: "APFS (clonefile)", OK: fsType == "apfs", Detail: fmt.Sprintf("%s is on %s", dataRoot, fsType)})
	return append(checks, CommonChecks(dataRoot)...)
}
