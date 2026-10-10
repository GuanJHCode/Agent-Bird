package process

import "time"

// OriginalSpawnedAt returns only this Handle's live monotonic Start boundary.
// A nil or reconstructed Handle has no such evidence.
func (h *Handle) OriginalSpawnedAt() time.Time {
	return time.Time{} // Fail-closed scaffold for the Host lifecycle regression.
}
