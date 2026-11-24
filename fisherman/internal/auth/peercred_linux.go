//go:build linux

package auth

import (
	"fmt"
	"net"
	"syscall"
)

// PeerCreds returns the kernel's credentials for the other end of conn.
// It fails for non-UNIX connections; callers then treat the peer as
// unattributable.
func PeerCreds(conn net.Conn) (Creds, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Creds{}, fmt.Errorf("auth: not a UNIX connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Creds{}, err
	}
	var ucred *syscall.Ucred
	var scErr error
	if err := raw.Control(func(fd uintptr) {
		ucred, scErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Creds{}, err
	}
	if scErr != nil {
		return Creds{}, scErr
	}
	return Creds{PID: int(ucred.Pid), UID: ucred.Uid, GID: ucred.Gid}, nil
}
