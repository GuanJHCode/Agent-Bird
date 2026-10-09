package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const diagnosticsRetentionPlanBytes = 8192

type DiagnosticsRetentionGuard struct {
	Version                int    `json:"version"`
	ReservationID          string `json:"reservation_id"`
	NamespaceSHA256        string `json:"namespace_sha256"`
	TerminalReceivedAt     string `json:"terminal_received_at"`
	TerminalReceivedBefore string `json:"terminal_received_before"`
	TerminalVectorSHA256   string `json:"terminal_vector_sha256"`
}

func retentionTime(s string) (time.Time, error) {
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil || s != t.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, CodeError("diagnostics_retention_time_invalid")
	}
	return t, nil
}
func retentionID(s string) bool {
	return len(s) > 0 && len(s) <= 256 && utf8.ValidString(s) && !bytes.ContainsRune([]byte(s), 0)
}
func (g DiagnosticsRetentionGuard) Validate() error {
	at, e := retentionTime(g.TerminalReceivedAt)
	before, b := retentionTime(g.TerminalReceivedBefore)
	if g.Version != 1 || !ValidDiagnosticsToken(g.ReservationID) || !validDigest(g.NamespaceSHA256) || !validDigest(g.TerminalVectorSHA256) || e != nil || b != nil || !at.Before(before) || before.After(time.Now()) {
		return CodeError("diagnostics_retention_guard_invalid")
	}
	return nil
}

type DiagnosticsRetentionPlanRequest struct {
	Version                int    `json:"version"`
	RunID                  string `json:"run_id"`
	RequestID              string `json:"request_id"`
	TerminalReceivedBefore string `json:"terminal_received_before"`
	Limit                  int    `json:"limit"`
	AfterTaskID            string `json:"after_task_id,omitempty"`
}

func (r DiagnosticsRetentionPlanRequest) Validate() error {
	cutoff, e := retentionTime(r.TerminalReceivedBefore)
	if r.Version != 1 || !retentionID(r.RunID) || !ValidDiagnosticsToken(r.RequestID) || r.Limit < 1 || r.Limit > 8 || r.AfterTaskID != "" && !retentionID(r.AfterTaskID) || e != nil || cutoff.After(time.Now()) {
		return CodeError("diagnostics_retention_request_invalid")
	}
	return nil
}
func (r *DiagnosticsRetentionPlanRequest) UnmarshalJSON(b []byte) error {
	type plain DiagnosticsRetentionPlanRequest
	var p plain
	if e := strictDiagnosticsJSON(b, &p); e != nil {
		return e
	}
	n := DiagnosticsRetentionPlanRequest(p)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type DiagnosticsRetentionPlan struct {
	Version                int                        `json:"version"`
	RunID                  string                     `json:"run_id"`
	RequestID              string                     `json:"request_id"`
	TerminalReceivedBefore string                     `json:"terminal_received_before"`
	Limit                  int                        `json:"limit"`
	AfterTaskID            string                     `json:"after_task_id,omitempty"`
	Items                  []DiagnosticsDeleteRequest `json:"items"`
	Excluded               map[string]int             `json:"excluded"`
	Scanned                int                        `json:"scanned"`
	NextAfterTaskID        string                     `json:"next_after_task_id,omitempty"`
	PlanSHA256             string                     `json:"plan_sha256,omitempty"`
}

func (p DiagnosticsRetentionPlan) digest() (string, error) {
	p.PlanSHA256 = ""
	raw, e := json.Marshal(p)
	return runtimeHash(string(raw)), e
}
func retentionItemRequestID(p DiagnosticsRetentionPlan, item DiagnosticsDeleteRequest) string {
	// Canonical typed tuple, not delimiter-concatenated user IDs.
	tuple := struct {
		Version                                   int
		BatchID, RunID, TaskID, AttemptID, Cutoff string
		Revision                                  int64
	}{1, p.RequestID, p.RunID, item.TaskID, item.AttemptID, p.TerminalReceivedBefore, item.ExpectedRevision}
	raw, _ := json.Marshal(tuple)
	return "retention-" + runtimeHash(string(raw))
}
func (p DiagnosticsRetentionPlan) Validate() error {
	invalid := CodeError("diagnostics_retention_plan_invalid")
	r := DiagnosticsRetentionPlanRequest{Version: p.Version, RunID: p.RunID, RequestID: p.RequestID, TerminalReceivedBefore: p.TerminalReceivedBefore, Limit: p.Limit, AfterTaskID: p.AfterTaskID}
	if r.Validate() != nil || p.Items == nil || p.Excluded == nil || len(p.Items) > p.Limit || p.Scanned < 0 || p.Scanned > 64 || p.NextAfterTaskID != "" && (!retentionID(p.NextAfterTaskID) || p.NextAfterTaskID <= p.AfterTaskID) {
		return invalid
	}
	count := len(p.Items)
	for reason, n := range p.Excluded {
		if !retentionExclusion(reason) || n < 1 || n > 64 {
			return invalid
		}
		count += n
	}
	if count != p.Scanned {
		return invalid
	}
	seenReq, seenAttempt, seenReservation, seenNamespace := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	last := p.AfterTaskID
	for _, item := range p.Items {
		if item.Validate() != nil || item.Retention == nil || item.Retention.TerminalReceivedBefore != p.TerminalReceivedBefore || item.RequestID != retentionItemRequestID(p, item) || item.TaskID <= last || p.NextAfterTaskID != "" && item.TaskID > p.NextAfterTaskID || seenReq[item.RequestID] || seenAttempt[item.AttemptID] || seenReservation[item.Retention.ReservationID] || seenNamespace[item.Retention.NamespaceSHA256] {
			return invalid
		}
		last = item.TaskID
		seenReq[item.RequestID] = true
		seenAttempt[item.AttemptID] = true
		seenReservation[item.Retention.ReservationID] = true
		seenNamespace[item.Retention.NamespaceSHA256] = true
	}
	digest, e := p.digest()
	raw, m := json.Marshal(p)
	if e != nil || m != nil || len(raw) > diagnosticsRetentionPlanBytes || !validDigest(p.PlanSHA256) || digest != p.PlanSHA256 {
		return invalid
	}
	return nil
}
func (p *DiagnosticsRetentionPlan) UnmarshalJSON(b []byte) error {
	type plain DiagnosticsRetentionPlan
	var v plain
	if len(b) > diagnosticsRetentionPlanBytes+1 || strictDiagnosticsJSON(b, &v) != nil {
		return CodeError("diagnostics_retention_plan_invalid")
	}
	n := DiagnosticsRetentionPlan(v)
	if e := n.Validate(); e != nil {
		return e
	}
	*p = n
	return nil
}
func retentionExclusion(code string) bool {
	switch code {
	case "diagnostics_no_attempt", "diagnostics_not_enabled", "diagnostics_grant_invalid", "diagnostics_delete_not_terminal", "diagnostics_unavailable", "diagnostics_not_expired", "diagnostics_state_invalid", "diagnostics_retention_evidence_invalid", "diagnostics_retention_evidence_too_large":
		return true
	}
	return false
}
func retentionErrorCode(e error) string {
	if e == nil {
		return ""
	}
	if errors.Is(e, context.Canceled) {
		return "diagnostics_cancelled"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "diagnostics_timeout"
	}
	s := e.Error()
	if s == "conflict" || s == "not_found" || retentionExclusion(s) || strings.HasPrefix(s, "diagnostics_") && len(s) <= 64 && ValidDiagnosticsToken(s) {
		return s
	}
	return "diagnostics_unavailable"
}

type DiagnosticsRetentionItemResult struct {
	TaskID    string                   `json:"task_id"`
	AttemptID string                   `json:"attempt_id"`
	RequestID string                   `json:"request_id"`
	Status    string                   `json:"status"`
	Error     string                   `json:"error,omitempty"`
	Result    *DiagnosticsDeleteResult `json:"result,omitempty"`
}
type DiagnosticsRetentionApplyResult struct {
	Version        int                              `json:"version"`
	RunID          string                           `json:"run_id"`
	RequestID      string                           `json:"request_id"`
	PlanSHA256     string                           `json:"plan_sha256"`
	Items          []DiagnosticsRetentionItemResult `json:"items"`
	RemainingItems int                              `json:"remaining_items"`
	Stopped        string                           `json:"stopped,omitempty"`
}
