package process

import "context"

const hostPeerExpectationDomain = "agent-bird-filter-host-peer-v1"

// HostPeerExpectation is a caller-owned comparison value for a kernel peer.
// It cannot create a Handle or LiveDescendant authority.
type HostPeerExpectation struct {
	Domain   string
	PID      int
	RawBirth string
	EUID     int
}

func CaptureHostPeerExpectation(context.Context) (HostPeerExpectation, error) {
	return HostPeerExpectation{}, ErrProcessTreeUnknown
}

func VerifyHostPeerExpectation(ctx context.Context, kernelPeerPID int, expected HostPeerExpectation) error {
	return verifyHostPeerExpectationWith(ctx, kernelPeerPID, expected, observePeerRecord)
}

func verifyHostPeerExpectationWith(context.Context, int, HostPeerExpectation, func(context.Context, int) (peerRecord, error)) error {
	return ErrProcessTreeUnknown
}
