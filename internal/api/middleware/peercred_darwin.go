package middleware

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerCred returns the uid and pid of a unix socket peer (LOCAL_PEERCRED).
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
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err == nil {
			uid, ok = int(cred.Uid), true
		}
		if p, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			pid = p
		}
	})
	return uid, pid, ok
}
