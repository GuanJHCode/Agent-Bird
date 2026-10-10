package process

import (
	"context"
	"os"
)

const hostPeerExpectationDomain = "agent-bird-filter-host-peer-v1"

// HostPeerExpectation is a caller-owned comparison value for a kernel peer.
// It cannot create a Handle or LiveDescendant authority.
type HostPeerExpectation struct {
	Domain   string
	PID      int
	RawBirth string
	EUID     int
}

func CaptureHostPeerExpectation(ctx context.Context) (HostPeerExpectation, error) {
	if ctx == nil || ctx.Err() != nil || os.Getpid() <= 0 || os.Getpid() > 1<<31-1 {
		return HostPeerExpectation{}, ErrProcessTreeUnknown
	}
	record, err := observePeerRecord(ctx, os.Getpid())
	if err != nil || record.pid != os.Getpid() || record.euid != os.Geteuid() || !validHostRawBirth(record.birth) {
		return HostPeerExpectation{}, ErrProcessTreeUnknown
	}
	return HostPeerExpectation{Domain: hostPeerExpectationDomain, PID: record.pid, RawBirth: record.birth, EUID: record.euid}, nil
}

func VerifyHostPeerExpectation(ctx context.Context, kernelPeerPID int, expected HostPeerExpectation) error {
	return verifyHostPeerExpectationWith(ctx, kernelPeerPID, expected, observePeerRecord)
}

func verifyHostPeerExpectationWith(ctx context.Context, kernelPeerPID int, expected HostPeerExpectation, observe func(context.Context, int) (peerRecord, error)) error {
	if ctx == nil || ctx.Err() != nil || observe == nil || !finiteContext(ctx) || !validHostPeerExpectation(expected) || kernelPeerPID <= 0 || kernelPeerPID > 1<<31-1 {
		return ErrProcessTreeUnknown
	}
	first, err := observe(ctx, kernelPeerPID)
	if err != nil || !hostExpectationMatches(first, kernelPeerPID, expected) || ctx.Err() != nil {
		return ErrProcessTreeUnknown
	}
	second, err := observe(ctx, kernelPeerPID)
	if err != nil || second != first || !hostExpectationMatches(second, kernelPeerPID, expected) || ctx.Err() != nil {
		return ErrProcessTreeUnknown
	}
	return nil
}

func finiteContext(ctx context.Context) bool { _, ok := ctx.Deadline(); return ok }
func validHostPeerExpectation(e HostPeerExpectation) bool {
	return e.Domain == hostPeerExpectationDomain && e.PID > 0 && e.PID <= 1<<31-1 && e.EUID == os.Geteuid() && validHostRawBirth(e.RawBirth)
}
func validHostRawBirth(raw string) bool {
	if raw == "" || len(raw) > 128 {
		return false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
func hostExpectationMatches(r peerRecord, kernelPID int, e HostPeerExpectation) bool {
	return validPeerRecord(r) && r.pid == kernelPID && r.pid == e.PID && r.birth == e.RawBirth && r.euid == e.EUID
}
