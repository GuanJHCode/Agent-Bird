//go:build !darwin && !linux

package ipc

import (
	"errors"
	"net"
)

func PeerPID(net.Conn) (int, error) { return 0, errors.New("owner_peer_platform_unsupported") }
