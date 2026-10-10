//go:build !darwin && !linux

package process

import "context"

func capturePeerAnchor(context.Context, int) (peerAnchor, error) {
	return peerAnchor{}, ErrProcessTreeUnknown
}
func observePeerRecord(context.Context, int) (peerRecord, error) {
	return peerRecord{}, ErrProcessTreeUnknown
}
