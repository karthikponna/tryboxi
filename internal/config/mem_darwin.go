package config

import "golang.org/x/sys/unix"

func memTotalMB() int {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int(v >> 20)
}
