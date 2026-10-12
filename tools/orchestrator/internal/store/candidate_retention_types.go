package store

import (
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"path/filepath"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type CandidateRetentionResolutionRequest struct {
	Version    int    `json:"version"`
	RunID      string `json:"run_id"`
	RequestID  string `json:"request_id"`
	Resolution string `json:"resolution"`
	Reason     string `json:"reason"`
}

func (r CandidateRetentionResolutionRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || !ValidDiagnosticsToken(r.RequestID) || (r.Resolution != "resolved" && r.Resolution != "abandoned") || len(r.Reason) < 1 || len(r.Reason) > 512 {
		return CodeError("candidate_retention_request_invalid")
	}
	return nil
}

type CandidateRetentionPlanRequest struct {
	Version         int    `json:"version"`
	RunID           string `json:"run_id"`
	RequestID       string `json:"request_id"`
	AfterResourceID string `json:"after_resource_id,omitempty"`
	Limit           int    `json:"limit"`
}

func (r CandidateRetentionPlanRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || !ValidDiagnosticsToken(r.RequestID) || r.Limit < 1 || r.Limit > 8 || (r.AfterResourceID != "" && !validDigest(r.AfterResourceID)) {
		return CodeError("candidate_retention_request_invalid")
	}
	return nil
}

type CandidateRetentionItem struct {
	ResourceID  string `json:"resource_id"`
	FactsSHA256 string `json:"facts_sha256"`
}
type CandidateRetentionExclusion struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}
type CandidateRetentionPlan struct {
	Version             int                           `json:"version"`
	RunID               string                        `json:"run_id"`
	RequestID           string                        `json:"request_id"`
	OwnerBinding        string                        `json:"owner_binding"`
	Items               []CandidateRetentionItem      `json:"items"`
	Excluded            []CandidateRetentionExclusion `json:"excluded"`
	Scanned             int                           `json:"scanned"`
	NextAfterResourceID string                        `json:"next_after_resource_id,omitempty"`
	PlanSHA256          string                        `json:"plan_sha256"`
}

func (p CandidateRetentionPlan) Validate() error {
	bad := CodeError("candidate_retention_plan_invalid")
	if p.Version != 1 || !retentionID(p.RunID) || !ValidDiagnosticsToken(p.RequestID) || !validDigest(p.OwnerBinding) || p.Items == nil || p.Excluded == nil || len(p.Items) > 8 || p.Scanned < 0 || p.Scanned > 64 || (p.NextAfterResourceID != "" && !validDigest(p.NextAfterResourceID)) {
		return bad
	}
	seen := map[string]bool{}
	count := len(p.Items)
	for _, x := range p.Items {
		if !validDigest(x.ResourceID) || !validDigest(x.FactsSHA256) || seen[x.ResourceID] {
			return bad
		}
		seen[x.ResourceID] = true
	}
	seen = map[string]bool{}
	for _, x := range p.Excluded {
		if x.Reason == "" || len(x.Reason) > 64 || x.Count < 1 || x.Count > 64 || seen[x.Reason] {
			return bad
		}
		seen[x.Reason] = true
		count += x.Count
	}
	if count != p.Scanned {
		return bad
	}
	q := p
	q.PlanSHA256 = ""
	raw, err := json.Marshal(q)
	if err != nil || len(raw)+64 > 8192 || p.PlanSHA256 != runtimeHash(string(raw)) {
		return bad
	}
	return nil
}

// Immutable Store authority for exactly one fresh maintenance actor. The
// child receives this only after its actual Identity is recorded as running.
type CandidateMaintenanceGrant struct {
	Version          int                                     `json:"version"`
	OperationID      string                                  `json:"operation_id"`
	RunID            string                                  `json:"run_id"`
	ResourceID       string                                  `json:"resource_id"`
	Generation       int                                     `json:"generation"`
	OwnerBinding     string                                  `json:"owner_binding"`
	FactsSHA256      string                                  `json:"facts_sha256"`
	Header           gitops.CandidateResourceHeader          `json:"header"`
	SpoolRoot        string                                  `json:"spool_root"`
	Root             StoreRootNodes                          `json:"root"`
	JournalIdentity  gitopsworker.MaintenanceJournalIdentity `json:"journal_identity"`
	OperationJournal string                                  `json:"operation_journal"`
	Executable       string                                  `json:"executable"`
	ExecutableSHA256 string                                  `json:"executable_sha256"`
	GrantSHA256      string                                  `json:"grant_sha256"`
}
type CandidateMaintenanceState struct {
	Grant   CandidateMaintenanceGrant    `json:"grant"`
	State   string                       `json:"state"`
	Child   *process.Identity            `json:"child,omitempty"`
	Outcome *CandidateMaintenanceOutcome `json:"outcome,omitempty"`
}
type CandidateMaintenanceOutcome struct {
	Version int                    `json:"version"`
	State   string                 `json:"state"`
	Code    string                 `json:"code,omitempty"`
	Cleanup *gitops.CleanupOutcome `json:"cleanup,omitempty"`
}
type CandidateRetentionApplyRequest struct {
	Plan CandidateRetentionPlan `json:"plan"`
}
type CandidateRetentionApplyResult struct {
	Version   int                         `json:"version"`
	RunID     string                      `json:"run_id"`
	RequestID string                      `json:"request_id"`
	Results   []CandidateMaintenanceState `json:"results"`
}
type CandidateGCRequest struct {
	Version          int    `json:"version"`
	RunID            string `json:"run_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type CandidateGCStatus struct {
	Version  int    `json:"version"`
	RunID    string `json:"run_id"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
	LastTick string `json:"last_tick,omitempty"`
	Stopped  string `json:"stopped,omitempty"`
}

func (g CandidateMaintenanceGrant) Validate() error {
	bad := CodeError("candidate_maintenance_grant_invalid")
	if g.Version != 1 || !validDigest(g.OperationID) || !validDigest(g.ResourceID) || !retentionID(g.RunID) || g.Generation < 1 || !validDigest(g.OwnerBinding) || !validDigest(g.FactsSHA256) || g.Header.Validate() != nil || g.Header.ResourceID != g.ResourceID || g.Header.Source.Binding.RunID != g.RunID || !filepath.IsAbs(g.Executable) || !validDigest(g.ExecutableSHA256) || !filepath.IsAbs(g.Root.StateRoot) || g.Root.StateInode == 0 || g.Root.DatabaseInode == 0 {
		return bad
	}
	if g.SpoolRoot != filepath.Join(g.Root.StateRoot, "host-spool", g.Header.Source.HostID) || filepath.Dir(g.OperationJournal) != g.Root.StateRoot || !strings.HasPrefix(filepath.Base(g.OperationJournal), "candidate-maintenance-") || !validDigest(strings.TrimPrefix(filepath.Base(g.OperationJournal), "candidate-maintenance-")) {
		return bad
	}
	if g.JournalIdentity.Directory.Inode <= 0 || g.JournalIdentity.Events.Inode <= 0 || g.JournalIdentity.Lock.Inode <= 0 {
		return bad
	}
	q := g
	q.GrantSHA256 = ""
	b, e := json.Marshal(q)
	if e != nil || len(b)+64 > 24576 || g.GrantSHA256 != runtimeHash(string(b)) {
		return bad
	}
	return nil
}
func (r CandidateGCRequest) Validate() error {
	if r.Version != 1 || !retentionID(r.RunID) || r.ExpectedRevision < 0 || r.ExpectedRevision >= 9223372036854775806 {
		return CodeError("candidate_gc_request_invalid")
	}
	return nil
}
func (r *CandidateRetentionResolutionRequest) UnmarshalJSON(b []byte) error {
	type plain CandidateRetentionResolutionRequest
	var p plain
	if e := strictDiagnosticsJSON(b, &p); e != nil {
		return e
	}
	x := CandidateRetentionResolutionRequest(p)
	if e := x.Validate(); e != nil {
		return e
	}
	*r = x
	return nil
}
func (r *CandidateRetentionPlanRequest) UnmarshalJSON(b []byte) error {
	type plain CandidateRetentionPlanRequest
	var p plain
	if e := strictDiagnosticsJSON(b, &p); e != nil {
		return e
	}
	x := CandidateRetentionPlanRequest(p)
	if e := x.Validate(); e != nil {
		return e
	}
	*r = x
	return nil
}
func (r *CandidateRetentionPlan) UnmarshalJSON(b []byte) error {
	type plain CandidateRetentionPlan
	var p plain
	if e := strictDiagnosticsJSON(b, &p); e != nil {
		return e
	}
	x := CandidateRetentionPlan(p)
	if e := x.Validate(); e != nil {
		return e
	}
	*r = x
	return nil
}
func (r *CandidateGCRequest) UnmarshalJSON(b []byte) error {
	type plain CandidateGCRequest
	var p plain
	if e := strictDiagnosticsJSON(b, &p); e != nil {
		return e
	}
	x := CandidateGCRequest(p)
	if e := x.Validate(); e != nil {
		return e
	}
	*r = x
	return nil
}
