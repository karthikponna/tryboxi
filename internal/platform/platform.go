// Package platform inspects the host: memory pressure, OS support, and the
// checks behind `sandboxd doctor`.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/karthikponna/tryboxi/internal/scheduler"
)

// Check is one doctor result.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// FreeDiskGB returns free space on the filesystem holding path.
func FreeDiskGB(path string) float64 {
	for p := path; ; p = filepath.Dir(p) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			return float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
		}
		if p == filepath.Dir(p) {
			return 0
		}
	}
}

// CommonChecks run on every OS.
func CommonChecks(dataRoot string) []Check {
	free := FreeDiskGB(dataRoot)
	checks := []Check{{
		Name: "free disk", OK: free >= 10,
		Detail: fmt.Sprintf("%.1f GB free under %s", free, dataRoot),
		Fix:    "free at least 10 GB for images and snapshots",
	}}
	if fi, err := os.Stat(dataRoot); err == nil {
		checks = append(checks, Check{Name: "data root permissions", OK: fi.Mode().Perm()&0o077 == 0,
			Detail: fmt.Sprintf("%s mode %o", dataRoot, fi.Mode().Perm()), Fix: "chmod 700 " + dataRoot})
	}
	checks = append(checks, Check{Name: "arch", OK: runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64", Detail: runtime.GOOS + "/" + runtime.GOARCH})
	return checks
}

// PressureFunc adapts MemoryPressure for the scheduler.
func PressureFunc() func() scheduler.Pressure { return MemoryPressure }
