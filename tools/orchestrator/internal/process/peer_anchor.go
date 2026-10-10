package process

import "context"

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

func (h *Handle) VerifyLiveDescendant(ctx context.Context, peerPID int) error {
	return h.verifyLiveDescendantWith(ctx, peerPID, observePeerRecord)
}

// verifyLiveDescendantWith exists to exercise the bounded kernel-observation
// protocol without allowing callers to supply an authority observation.
func (h *Handle) verifyLiveDescendantWith(ctx context.Context, peerPID int, observe func(context.Context, int) (peerRecord, error)) error {
	if h == nil || peerPID <= 0 || observe == nil || ctx == nil || ctx.Err() != nil {
		return ErrProcessTreeUnknown
	}
	h.mu.Lock()
	if h.waited || h.peerRevoked || h.peerAnchor == nil {
		h.mu.Unlock()
		return ErrProcessTreeUnknown
	}
	anchor := *h.peerAnchor
	h.mu.Unlock()
	if !validPeerAnchor(anchor) {
		return ErrProcessTreeUnknown
	}

	chain := make([]peerRecord, 0, 8)
	seen := make(map[int]struct{}, 8)
	pid := peerPID
	for len(chain) < 64 {
		if ctx.Err() != nil {
			return ErrProcessTreeUnknown
		}
		if _, exists := seen[pid]; exists {
			return ErrProcessTreeUnknown
		}
		seen[pid] = struct{}{}
		record, err := observe(ctx, pid)
		if err != nil || !validPeerRecord(record) || record.pid != pid || record.euid != anchor.euid || record.pgid != anchor.pgid {
			return ErrProcessTreeUnknown
		}
		chain = append(chain, record)
		if matchesPeerAnchor(record, anchor) {
			break
		}
		if record.ppid <= 1 {
			return ErrProcessTreeUnknown
		}
		pid = record.ppid
	}
	if len(chain) == 64 && !matchesPeerAnchor(chain[len(chain)-1], anchor) {
		return ErrProcessTreeUnknown
	}
	if !matchesPeerAnchor(chain[len(chain)-1], anchor) {
		return ErrProcessTreeUnknown
	}
	// Recheck the complete path in reverse. Reading only its endpoints would
	// miss a parent that exited or was reparented during the first walk.
	for i := len(chain) - 1; i >= 0; i-- {
		current, err := observe(ctx, chain[i].pid)
		if err != nil || current != chain[i] {
			return ErrProcessTreeUnknown
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waited || h.peerRevoked || h.peerAnchor == nil || *h.peerAnchor != anchor || ctx.Err() != nil {
		return ErrProcessTreeUnknown
	}
	return nil
}

func validPeerAnchor(a peerAnchor) bool {
	return a.pid > 0 && a.birth != "" && a.euid >= 0 && a.pgid > 0
}
func validPeerRecord(r peerRecord) bool {
	return r.pid > 0 && r.ppid >= 0 && r.birth != "" && r.euid >= 0 && r.pgid > 0
}
func matchesPeerAnchor(r peerRecord, a peerAnchor) bool {
	return r.pid == a.pid && r.birth == a.birth && r.euid == a.euid && r.pgid == a.pgid
}
