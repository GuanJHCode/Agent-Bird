//go:build linux

package process

import (
	"context"
)

func capturePeerAnchor(ctx context.Context, pid int) (peerAnchor, error) {
	r, err := observePeerRecord(ctx, pid)
	if err != nil {
		return peerAnchor{}, err
	}
	return peerAnchor{pid: r.pid, birth: r.birth, euid: r.euid, pgid: r.pgid}, nil
}

func observePeerRecord(ctx context.Context, pid int) (peerRecord, error) {
	boot, err := linuxBoot(ctx)
	if err != nil {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	before, uidBefore, err := linuxPeerObservation(ctx, pid)
	if err != nil {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	after, uidAfter, err := linuxPeerObservation(ctx, pid)
	if err != nil || before != after || uidBefore != uidAfter || !linuxLivePeerState(before.state) {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	return peerRecord{pid: pid, ppid: before.ppid, birth: "linux:" + boot + ":" + before.start, euid: uidBefore, pgid: before.pgid}, nil
}

func linuxPeerObservation(ctx context.Context, pid int) (linuxStat, int, error) {
	stat, err := linuxStatFor(ctx, pid)
	if err != nil || !linuxLivePeerState(stat.state) {
		return linuxStat{}, 0, ErrProcessTreeUnknown
	}
	uid, err := linuxUID(ctx, pid)
	if err != nil {
		return linuxStat{}, 0, ErrProcessTreeUnknown
	}
	return stat, uid, nil
}

func linuxLivePeerState(state byte) bool {
	switch state {
	case 'R', 'S', 'D', 'I', 'T', 't', 'W':
		return true
	default:
		return false
	}
}
