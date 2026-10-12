package store

import (
	"encoding/json"
)

type HostArtifactResolutionRequest struct {
	Version       int    `json:"version"`
	RunID         string `json:"run_id"`
	ReservationID string `json:"reservation_id"`
	Resolution    string `json:"resolution"`
	Reason        string `json:"reason"`
	RequestID     string `json:"request_id"`
}

func (r HostArtifactResolutionRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || !retentionID(r.ReservationID) || !ValidDiagnosticsToken(r.RequestID) || len(r.Reason) < 1 || len(r.Reason) > 512 || (r.Resolution != "resolved" && r.Resolution != "abandoned") {
		return CodeError("host_retention_request_invalid")
	}
	return nil
}
func (r *HostArtifactResolutionRequest) UnmarshalJSON(b []byte) error {
	type plain HostArtifactResolutionRequest
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := HostArtifactResolutionRequest(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type HostArtifactRetentionPlanRequest struct {
	Version            int    `json:"version"`
	RunID              string `json:"run_id"`
	RequestID          string `json:"request_id"`
	AfterReservationID string `json:"after_reservation_id,omitempty"`
	Limit              int    `json:"limit"`
}

func (r HostArtifactRetentionPlanRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || !ValidDiagnosticsToken(r.RequestID) || r.Limit < 1 || r.Limit > 8 || (r.AfterReservationID != "" && !retentionID(r.AfterReservationID)) {
		return CodeError("host_retention_request_invalid")
	}
	return nil
}
func (r *HostArtifactRetentionPlanRequest) UnmarshalJSON(b []byte) error {
	type plain HostArtifactRetentionPlanRequest
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := HostArtifactRetentionPlanRequest(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type HostArtifactRetentionItem struct {
	ReservationID string `json:"reservation_id"`
	FactsSHA256   string `json:"facts_sha256"`
}
type HostRetentionExclusion struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type HostArtifactRetentionPlan struct {
	OwnerBinding           string                      `json:"owner_binding"`
	NextAfterReservationID string                      `json:"next_after_reservation_id,omitempty"`
	Scanned                int                         `json:"scanned"`
	Excluded               []HostRetentionExclusion    `json:"excluded"`
	Version                int                         `json:"version"`
	RunID                  string                      `json:"run_id"`
	RequestID              string                      `json:"request_id"`
	Items                  []HostArtifactRetentionItem `json:"items"`
	PlanSHA256             string                      `json:"plan_sha256"`
}

func (p HostArtifactRetentionPlan) Validate() error {
	if p.Version != 1 || !retentionID(p.RunID) || !ValidDiagnosticsToken(p.RequestID) || len(p.Items) > 8 || p.Items == nil || p.Excluded == nil || !validDigest(p.OwnerBinding) || p.Scanned < 0 || p.Scanned > 64 || (p.NextAfterReservationID != "" && !retentionID(p.NextAfterReservationID)) {
		return CodeError("host_retention_plan_invalid")
	}
	q := p
	q.PlanSHA256 = ""
	b, e := json.Marshal(q)
	if e != nil || len(b)+64 > 8191 || p.PlanSHA256 != runtimeHash(string(b)) {
		return CodeError("host_retention_plan_invalid")
	}
	count := len(p.Items)
	reasons := map[string]bool{}
	for _, x := range p.Excluded {
		if reasons[x.Reason] || !hostRetentionExclusion(CodeError(x.Reason)) || x.Count < 1 || x.Count > 64 {
			return CodeError("host_retention_plan_invalid")
		}
		reasons[x.Reason] = true
		count += x.Count
	}
	if count != p.Scanned {
		return CodeError("host_retention_plan_invalid")
	}
	seen := map[string]bool{}
	for _, i := range p.Items {
		if seen[i.ReservationID] || !retentionID(i.ReservationID) || !validDigest(i.FactsSHA256) {
			return CodeError("host_retention_plan_invalid")
		}
		seen[i.ReservationID] = true
	}
	return nil
}
func (p *HostArtifactRetentionPlan) UnmarshalJSON(b []byte) error {
	type plain HostArtifactRetentionPlan
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := HostArtifactRetentionPlan(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*p = n
	return nil
}

type HostGCRequest struct {
	Version          int    `json:"version"`
	RunID            string `json:"run_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (r HostGCRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || r.ExpectedRevision < 0 || r.ExpectedRevision >= 9223372036854775806 {
		return CodeError("host_gc_request_invalid")
	}
	return nil
}
func (r *HostGCRequest) UnmarshalJSON(b []byte) error {
	type plain HostGCRequest
	var x plain
	if e := strictDiagnosticsJSON(b, &x); e != nil {
		return e
	}
	n := HostGCRequest(x)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type HostArtifactRetentionResult struct {
	Version   int    `json:"version"`
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	Deleted   int    `json:"deleted"`
}
type HostGCStatus struct {
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
