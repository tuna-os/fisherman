//go:build !linux

package auth

import (
	"errors"
	"net"
)

// PeerCreds is unavailable off Linux: every peer is unattributable.
func PeerCreds(conn net.Conn) (Creds, error) {
	return Creds{}, errors.New("auth: SO_PEERCRED needs Linux")
}
