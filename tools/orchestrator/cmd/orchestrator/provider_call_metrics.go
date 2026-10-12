package main

import (
	"path/filepath"
	"time"
)

// Monotonic, process-local measurements. RPC durations are nested inside phase
// totals; they must not be added to them. Diagnostics never authorize a task.
type providerCallMetrics struct {
	start, phase time.Time
	durations    map[string]time.Duration
}

func newProviderCallMetrics() *providerCallMetrics {
	now := time.Now()
	return &providerCallMetrics{start: now, phase: now, durations: map[string]time.Duration{}}
}
func (m *providerCallMetrics) mark(name string) {
	now := time.Now()
	m.durations[name] += now.Sub(m.phase)
	m.phase = now
}
func (m *providerCallMetrics) add(name string, start time.Time) {
	m.durations[name] += time.Since(start)
}
func (m *providerCallMetrics) finish(result *providerCallResult, err error) {
	m.durations["total"] = time.Since(m.start)
	result.TimingsMS = make(map[string]int64, len(m.durations))
	for name, d := range m.durations {
		result.TimingsMS[name] = d.Milliseconds()
	}
	if result.Handle == "" || privateHandleParent(result.Handle) != nil {
		return
	}
	status, reason := result.Status, result.FailureReason
	if err != nil {
		status = "failed"
		reason = errorCode(err)
	}
	// Best effort only: an occupied/untrusted diagnostic path must not change a
	// committed result. writeExclusiveJSON never replaces an existing file.
	_ = writeExclusiveJSON(filepath.Join(filepath.Dir(result.Handle), "call-metrics.json"), struct {
		Version       int              `json:"version"`
		CallID        string           `json:"call_id"`
		Provider      string           `json:"provider"`
		Status        string           `json:"status"`
		FailureReason string           `json:"failure_reason,omitempty"`
		ProcessExited bool             `json:"process_exited"`
		TimingsMS     map[string]int64 `json:"timings_ms"`
	}{1, result.CallID, result.Provider, status, reason, result.ProcessExited, result.TimingsMS})
}
