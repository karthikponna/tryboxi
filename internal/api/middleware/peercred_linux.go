package middleware

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerCred returns the uid and pid of a unix socket peer (SO_PEERCRED).
func PeerCred(c net.Conn) (uid, pid int, ok bool) {
	uc, isUnix := c.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	_ = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil {
			uid, pid, ok = int(cred.Uid), int(cred.Pid), true
		}
	})
	return uid, pid, ok
}
