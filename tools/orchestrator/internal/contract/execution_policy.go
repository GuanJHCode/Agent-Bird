package contract

import (
	"encoding/json"
	"errors"
)

type ExecutionPolicy struct {
	Version            int          `json:"version"`
	GroupMaxAttempts   int          `json:"group_max_attempts"`
	GroupMaxActiveMS   int64        `json:"group_max_active_ms"`
	TaskMaxActiveMS    int64        `json:"task_max_active_ms"`
	SegmentMaxActiveMS int64        `json:"segment_max_active_ms"`
	Retry              *RetryPolicy `json:"retry,omitempty"`
}
type RetryPolicy struct {
	Codes     []string `json:"codes"`
	BackoffMS []int64  `json:"backoff_ms,omitempty"`
}
type FailureFacts struct {
	Version     int    `json:"version"`
	Stage       string `json:"stage"`
	Code        string `json:"code"`
	WorkerState string `json:"worker_state"`
	ProbeState  string `json:"probe_state"`
}

func (p *ExecutionPolicy) UnmarshalJSON(raw []byte) error {
	type plain ExecutionPolicy
	var v plain
	if err := DecodeBridgeStrict(raw, &v); err != nil {
		return errors.New("execution_policy_invalid")
	}
	*p = ExecutionPolicy(v)
	return nil
}
func (p *RetryPolicy) UnmarshalJSON(raw []byte) error {
	type plain RetryPolicy
	var v plain
	if err := DecodeBridgeStrict(raw, &v); err != nil {
		return errors.New("execution_policy_invalid")
	}
	*p = RetryPolicy(v)
	return nil
}
func (f *FailureFacts) UnmarshalJSON(raw []byte) error {
	type plain FailureFacts
	var v plain
	if err := DecodeBridgeStrict(raw, &v); err != nil {
		return errors.New("failure_facts_invalid")
	}
	*f = FailureFacts(v)
	return nil
}
func (f *FailureFacts) ValidPrelaunchProbe() bool {
	return f != nil && f.Version == 1 && f.Stage == "prelaunch_probe" && f.WorkerState == "not_started" && f.ProbeState == "not_started" && (f.Code == "probe_start_eagain" || f.Code == "probe_start_eintr")
}

// Canonical policy bytes include every effective budget and retry grant.
func (p ExecutionPolicy) CanonicalJSON() ([]byte, error) { return json.Marshal(p) }

// Validation's wrapper never started; prior managed Git preparation may exist.
func (f *FailureFacts) ValidValidationStart() bool {
	return f != nil && f.Version == 1 && f.Stage == "validation_start" && f.WorkerState == "not_started" && f.ProbeState == "not_applicable" && (f.Code == "validation_start_eagain" || f.Code == "validation_start_eintr")
}
