package process

import "time"

// OriginalSpawnedAt returns only this Handle's live monotonic Start boundary.
// A nil or reconstructed Handle has no such evidence.
func (h *Handle) OriginalSpawnedAt() time.Time {
	if h == nil {
		return time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.originalSpawnedAt
}
