//go:build darwin

package process

import (
	"context"
	"fmt"
)

func capturePeerAnchor(ctx context.Context, pid int) (peerAnchor, error) {
	r, err := observePeerRecord(ctx, pid)
	if err != nil {
		return peerAnchor{}, err
	}
	return peerAnchor{pid: r.pid, birth: r.birth, euid: r.euid, pgid: r.pgid}, nil
}

func observePeerRecord(ctx context.Context, pid int) (peerRecord, error) {
	first, err := darwinPeerRecord(ctx, pid)
	if err != nil {
		return peerRecord{}, err
	}
	second, err := darwinPeerRecord(ctx, pid)
	if err != nil || first != second {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	return first, nil
}

func darwinPeerRecord(ctx context.Context, pid int) (peerRecord, error) {
	k, err := darwinProcessRecord(ctx, pid)
	if err != nil || !darwinLivePeerState(k.Proc.P_stat) || k.Eproc.Ppid < 0 || k.Eproc.Pgid <= 0 {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	return peerRecord{pid: int(k.Proc.P_pid), ppid: int(k.Eproc.Ppid), birth: fmt.Sprintf("darwin:%d:%d", k.Proc.P_starttime.Sec, k.Proc.P_starttime.Usec), euid: int(k.Eproc.Ucred.Uid), pgid: int(k.Eproc.Pgid)}, nil
}

func darwinLivePeerState(state int8) bool { return state == 2 || state == 3 || state == 4 }
