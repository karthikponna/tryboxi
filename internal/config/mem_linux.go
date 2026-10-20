package config

import "golang.org/x/sys/unix"

func memTotalMB() int {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0
	}
	return int(uint64(si.Totalram) * uint64(si.Unit) >> 20)
}
