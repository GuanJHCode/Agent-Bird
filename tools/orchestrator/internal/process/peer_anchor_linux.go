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
	stat, uid, err := linuxObserveAncestor(ctx, pid)
	if err != nil {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	return peerRecord{pid: pid, ppid: stat.ppid, birth: "linux:" + boot + ":" + stat.start, euid: uid, pgid: stat.pgid}, nil
}
