package store

import (
	"encoding/json"
)

type ReportArtifactResolutionRequest struct {
	Version    int    `json:"version"`
	RunID      string `json:"run_id"`
	IntakeID   string `json:"intake_id"`
	Resolution string `json:"resolution"`
	Reason     string `json:"reason"`
	RequestID  string `json:"request_id"`
}

func (r ReportArtifactResolutionRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || !retentionID(r.IntakeID) || !ValidDiagnosticsToken(r.RequestID) || len(r.Reason) < 1 || len(r.Reason) > 512 || (r.Resolution != "resolved" && r.Resolution != "abandoned") {
		return CodeError("report_retention_request_invalid")
	}
	return nil
}
func (r *ReportArtifactResolutionRequest) UnmarshalJSON(b []byte) error {
	type plain ReportArtifactResolutionRequest
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := ReportArtifactResolutionRequest(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type ReportArtifactRetentionPlanRequest struct {
	Version       int    `json:"version"`
	RunID         string `json:"run_id"`
	RequestID     string `json:"request_id"`
	AfterIntakeID string `json:"after_intake_id,omitempty"`
	Limit         int    `json:"limit"`
}

func (r ReportArtifactRetentionPlanRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || !ValidDiagnosticsToken(r.RequestID) || r.Limit < 1 || r.Limit > 8 || (r.AfterIntakeID != "" && !retentionID(r.AfterIntakeID)) {
		return CodeError("report_retention_request_invalid")
	}
	return nil
}
func (r *ReportArtifactRetentionPlanRequest) UnmarshalJSON(b []byte) error {
	type plain ReportArtifactRetentionPlanRequest
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := ReportArtifactRetentionPlanRequest(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type ReportArtifactRetentionItem struct {
	IntakeID      string `json:"intake_id"`
	ReservationID string `json:"reservation_id"`
	FactsSHA256   string `json:"facts_sha256"`
}
type ReportRetentionExclusion struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type ReportArtifactRetentionPlan struct {
	OwnerBinding      string                        `json:"owner_binding"`
	NextAfterIntakeID string                        `json:"next_after_intake_id,omitempty"`
	Scanned           int                           `json:"scanned"`
	Excluded          []ReportRetentionExclusion    `json:"excluded"`
	Version           int                           `json:"version"`
	RunID             string                        `json:"run_id"`
	RequestID         string                        `json:"request_id"`
	Items             []ReportArtifactRetentionItem `json:"items"`
	PlanSHA256        string                        `json:"plan_sha256"`
}

func (p ReportArtifactRetentionPlan) Validate() error {
	if p.Version != 1 || !retentionID(p.RunID) || !ValidDiagnosticsToken(p.RequestID) || len(p.Items) > 8 || p.Items == nil || p.Excluded == nil || !validDigest(p.OwnerBinding) || p.Scanned < 0 || p.Scanned > 64 || (p.NextAfterIntakeID != "" && !retentionID(p.NextAfterIntakeID)) {
		return CodeError("report_retention_plan_invalid")
	}
	q := p
	q.PlanSHA256 = ""
	b, e := json.Marshal(q)
	if e != nil || len(b)+64 > 8191 || p.PlanSHA256 != runtimeHash(string(b)) {
		return CodeError("report_retention_plan_invalid")
	}
	count := len(p.Items)
	reasons := map[string]bool{}
	for _, x := range p.Excluded {
		if reasons[x.Reason] || !reportRetentionExclusion(CodeError(x.Reason)) || x.Count < 1 || x.Count > 64 {
			return CodeError("report_retention_plan_invalid")
		}
		reasons[x.Reason] = true
		count += x.Count
	}
	if count != p.Scanned {
		return CodeError("report_retention_plan_invalid")
	}
	seen := map[string]bool{}
	for _, i := range p.Items {
		if seen[i.IntakeID] || !retentionID(i.IntakeID) || !retentionID(i.ReservationID) || !validDigest(i.FactsSHA256) {
			return CodeError("report_retention_plan_invalid")
		}
		seen[i.IntakeID] = true
	}
	return nil
}
func (p *ReportArtifactRetentionPlan) UnmarshalJSON(b []byte) error {
	type plain ReportArtifactRetentionPlan
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := ReportArtifactRetentionPlan(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*p = n
	return nil
}

type ReportGCRequest struct {
	Version          int    `json:"version"`
	RunID            string `json:"run_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (r ReportGCRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || r.ExpectedRevision < 0 || r.ExpectedRevision >= 9223372036854775806 {
		return CodeError("report_gc_request_invalid")
	}
	return nil
}
func (r *ReportGCRequest) UnmarshalJSON(b []byte) error {
	type plain ReportGCRequest
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := ReportGCRequest(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type ReportArtifactRetentionResult struct {
	Version   int    `json:"version"`
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	Deleted   int    `json:"deleted"`
}
type ReportGCStatus struct {
	SuccessTTLDays int    `json:"success_ttl_days"`
	FailureTTLDays int    `json:"failure_ttl_days"`
	Cursor         string `json:"cursor"`
	PendingPlan    bool   `json:"pending_plan"`
	Version        int    `json:"version"`
	RunID          string `json:"run_id"`
	Enabled        bool   `json:"enabled"`
	Revision       int64  `json:"revision"`
	Stopped        string `json:"stopped,omitempty"`
}
