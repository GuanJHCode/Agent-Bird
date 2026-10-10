package process

import (
	"context"
	"os"
)

// These are deliberately private kernel observations. Identity is a public,
// serializable status view and must never become authority for a peer process.
type peerAnchor struct {
	pid        int
	birth      string
	euid, pgid int
}

type peerRecord struct {
	pid, ppid  int
	birth      string
	euid, pgid int
}

// LiveDescendant is an in-memory capability for one exact peer process. Its
// fields intentionally remain private so it cannot be serialized or rebuilt.
type LiveDescendant struct {
	handle *Handle
	peer   peerRecord
}

func (h *Handle) BindLiveDescendant(ctx context.Context, peerPID int) (*LiveDescendant, error) {
	return h.bindLiveDescendantWith(ctx, peerPID, observePeerRecord)
}

func (p *LiveDescendant) Verify(ctx context.Context) error {
	return p.verifyWith(ctx, observePeerRecord)
}

func (h *Handle) bindLiveDescendantWith(ctx context.Context, peerPID int, observe func(context.Context, int) (peerRecord, error)) (*LiveDescendant, error) {
	peer, err := h.verifyLiveDescendantRecordWith(ctx, peerPID, nil, observe)
	if err != nil {
		return nil, ErrProcessTreeUnknown
	}
	return &LiveDescendant{handle: h, peer: peer}, nil
}

func (p *LiveDescendant) verifyWith(ctx context.Context, observe func(context.Context, int) (peerRecord, error)) error {
	if p == nil || p.handle == nil || !validPeerRecord(p.peer) {
		return ErrProcessTreeUnknown
	}
	_, err := p.handle.verifyLiveDescendantRecordWith(ctx, p.peer.pid, &p.peer, observe)
	return err
}

func (h *Handle) VerifyLiveDescendant(ctx context.Context, peerPID int) error {
	return h.verifyLiveDescendantWith(ctx, peerPID, observePeerRecord)
}

// verifyLiveDescendantWith exists to exercise the bounded kernel-observation
// protocol without allowing callers to supply an authority observation.
func (h *Handle) verifyLiveDescendantWith(ctx context.Context, peerPID int, observe func(context.Context, int) (peerRecord, error)) error {
	_, err := h.verifyLiveDescendantRecordWith(ctx, peerPID, nil, observe)
	return err
}

func (h *Handle) verifyLiveDescendantRecordWith(ctx context.Context, peerPID int, expected *peerRecord, observe func(context.Context, int) (peerRecord, error)) (peerRecord, error) {
	if h == nil || peerPID <= 0 || observe == nil || ctx == nil || ctx.Err() != nil {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	h.mu.Lock()
	if h.peerOwner != h || h.waited || h.peerRevoked || h.peerAnchor == nil {
		h.mu.Unlock()
		return peerRecord{}, ErrProcessTreeUnknown
	}
	anchor := *h.peerAnchor
	h.mu.Unlock()
	if !validPeerAnchor(anchor) {
		return peerRecord{}, ErrProcessTreeUnknown
	}

	chain := make([]peerRecord, 0, 8)
	seen := make(map[int]struct{}, 8)
	pid := peerPID
	for len(chain) < 64 {
		if ctx.Err() != nil {
			return peerRecord{}, ErrProcessTreeUnknown
		}
		if _, exists := seen[pid]; exists {
			return peerRecord{}, ErrProcessTreeUnknown
		}
		seen[pid] = struct{}{}
		record, err := observe(ctx, pid)
		if err != nil || !validPeerRecord(record) || record.pid != pid || record.euid != anchor.euid || record.pgid != anchor.pgid {
			return peerRecord{}, ErrProcessTreeUnknown
		}
		if len(chain) == 0 && expected != nil && record != *expected {
			return peerRecord{}, ErrProcessTreeUnknown
		}
		chain = append(chain, record)
		if matchesPeerAnchor(record, anchor) {
			break
		}
		if record.ppid <= 1 {
			return peerRecord{}, ErrProcessTreeUnknown
		}
		pid = record.ppid
	}
	if len(chain) == 64 && !matchesPeerAnchor(chain[len(chain)-1], anchor) {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	if !matchesPeerAnchor(chain[len(chain)-1], anchor) {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	// Recheck the complete path in reverse. Reading only its endpoints would
	// miss a parent that exited or was reparented during the first walk.
	for i := len(chain) - 1; i >= 0; i-- {
		current, err := observe(ctx, chain[i].pid)
		if err != nil || current != chain[i] {
			return peerRecord{}, ErrProcessTreeUnknown
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.peerOwner != h || h.waited || h.peerRevoked || h.peerAnchor == nil || *h.peerAnchor != anchor || ctx.Err() != nil {
		return peerRecord{}, ErrProcessTreeUnknown
	}
	return chain[0], nil
}

func validPeerAnchor(a peerAnchor) bool {
	return a.pid > 0 && a.birth != "" && a.euid == os.Geteuid() && a.pgid > 0
}
func validPeerRecord(r peerRecord) bool {
	return r.pid > 0 && r.ppid >= 0 && r.birth != "" && r.euid >= 0 && r.pgid > 0
}
func matchesPeerAnchor(r peerRecord, a peerAnchor) bool {
	return r.pid == a.pid && r.birth == a.birth && r.euid == a.euid && r.pgid == a.pgid
}
