package contract

// BudgetPolicy v2 is an explicit total group allowance. Version 1 requests
// keep their historical per-segment max_active_ms and 120 minute group cap.
type BudgetPolicy struct {
	Version               int   `json:"version"`
	MaxActiveMSPerSegment int64 `json:"max_active_ms_per_segment"`
	MaxActiveMSTotal      int64 `json:"max_active_ms_total"`
}

const MaxAttempts = 3

// New submissions are bounded by encoded adapter bytes; retained legacy
// receipts remain readable and are never rewritten to fit the new limit.
const MaxAdapterPayloadBytes = 16 * 1024
