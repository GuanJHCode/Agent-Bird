package coordinator

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Shared data only: business plans carry no owner/peer validation flag.
type ConfigTaskPlan struct {
	RunID        string        `json:"run_id"`
	PlanRevision int           `json:"plan_revision"`
	Tasks        []TaskRequest `json:"tasks"`
}
type ConfigPreparedPlan struct {
	OwnerScopeSHA256      string                        `json:"owner_scope_sha256"`
	SchemaVersion         int                           `json:"schema_version"`
	Mode                  string                        `json:"mode"`
	OriginalRequestSHA256 string                        `json:"original_request_sha256"`
	OriginalPlanSHA256    string                        `json:"original_plan_sha256"`
	OriginalPlan          ConfigTaskPlan                `json:"original_plan"`
	ResolvedPlan          ConfigTaskPlan                `json:"resolved_plan"`
	ResolvedPlanSHA256    string                        `json:"resolved_plan_sha256"`
	Use                   *install.ConfigPreparationUse `json:"use,omitempty"`
}

// Pure frozen-structure validation; no current Enabled/model/default lookup,
// materialization, peer I/O or recursive install flock is used by abandonment.
func ValidateConfigPreparedStructure(p ConfigPreparedPlan, u install.ConfigPreparationUse) error {
	invalid := store.CodeError("config_preparation_invalid")
	if p.SchemaVersion != 2 || p.Mode != "generation" || p.OwnerScopeSHA256 != u.OwnerScopeSHA256 || p.Use == nil || !reflect.DeepEqual(*p.Use, u) || p.OriginalRequestSHA256 != u.RequestSHA256 || p.OriginalPlan.RunID != u.RunID || p.ResolvedPlan.RunID != u.RunID || p.OriginalPlan.PlanRevision < 1 || p.OriginalPlan.PlanRevision != p.ResolvedPlan.PlanRevision || len(p.OriginalPlan.Tasks) == 0 || len(p.OriginalPlan.Tasks) > 64 || len(p.ResolvedPlan.Tasks) != len(p.OriginalPlan.Tasks) || p.OriginalPlanSHA256 != ConfigTaskPlanDigest(p.OriginalPlan.RunID, p.OriginalPlan.PlanRevision, p.OriginalPlan.Tasks) || p.ResolvedPlanSHA256 != ConfigTaskPlanDigest(p.ResolvedPlan.RunID, p.ResolvedPlan.PlanRevision, p.ResolvedPlan.Tasks) {
		return invalid
	}
	ids := make([]string, len(p.OriginalPlan.Tasks))
	for i, t := range p.ResolvedPlan.Tasks {
		original := p.OriginalPlan.Tasks[i]
		ids[i] = original.ID
		left, right := original, t
		left.AdapterPayload = nil
		left.Fallbacks = nil
		right.AdapterPayload = nil
		right.Fallbacks = nil
		if !reflect.DeepEqual(left, right) || len(original.Fallbacks) != len(t.Fallbacks) {
			return invalid
		}
		before := append([]json.RawMessage{original.AdapterPayload}, original.Fallbacks...)
		after := append([]json.RawMessage{t.AdapterPayload}, t.Fallbacks...)
		for j, raw := range after {
			var exact adapter.InvocationPayload
			if contract.DecodeBridgeStrict(before[j], &exact) != nil {
				return invalid
			}
			prior, e := adapter.DecodeInvocationPayload(before[j])
			if e != nil {
				return e
			}
			inv, e := adapter.DecodeInvocationPayload(raw)
			if e != nil {
				return e
			}
			if (prior.Profile == nil) != (inv.Profile == nil) || prior.Provider != inv.Provider || prior.ConfigGeneration != nil || prior.ConfigModelSource != "" || inv.ConfigGeneration == nil || u.Ref == nil || *inv.ConfigGeneration != *u.Ref || inv.ValidateConfigModelSource() != nil {
				return invalid
			}
			source := "not-applicable"
			if prior.Profile != nil {
				source = "saved-default"
				if prior.Profile.CLIModelDefault {
					source = "cli-default"
				} else if prior.Profile.Model != "" {
					source = "explicit"
				}
			}
			if inv.ConfigModelSource != source {
				return invalid
			}
			if prior.Profile != nil && (prior.Profile.CLIModelDefault != inv.Profile.CLIModelDefault || prior.Profile.Model != "" && prior.Profile.Model != inv.Profile.Model) {
				return invalid
			}
		}
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, u.TaskIDs) {
		return invalid
	}
	return nil
}

// Exact canonical bytes close CPP/task-plan keys, aliases and duplicates while
// preserving adapter RawMessage bytes. Each adapter is separately checked by
// its declared struct schema in ValidateConfigPreparedStructure.
func DecodeConfigPreparedPlan(raw []byte) (ConfigPreparedPlan, error) {
	var p ConfigPreparedPlan
	if len(raw) > 128<<10 || json.Unmarshal(raw, &p) != nil {
		return p, store.CodeError("config_preparation_invalid")
	}
	body, e := json.Marshal(p)
	if e != nil || !bytes.Equal(bytes.TrimSuffix(raw, []byte("\n")), body) {
		return p, store.CodeError("config_preparation_invalid")
	}
	return p, nil
}
