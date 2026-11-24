//go:build linux

package auth

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// PeerCreds returns the kernel's credentials for the other end of conn and
// the start time of that process. It fails for non-UNIX connections and for
// a peer whose start time cannot be read (it has exited, or its PID is not
// visible here); callers then treat the peer as unattributable.
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
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(int(ucred.Pid)) + "/stat")
	if err != nil {
		return Creds{}, fmt.Errorf("auth: peer start time: %w", err)
	}
	start, err := parseStartTime(string(stat))
	if err != nil {
		return Creds{}, err
	}
	return Creds{PID: int(ucred.Pid), UID: ucred.Uid, GID: ucred.Gid, StartTime: start}, nil
}
