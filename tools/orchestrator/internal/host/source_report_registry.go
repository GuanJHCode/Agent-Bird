package host

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Retention is bounded by the existing 20MiB report-control allowance, rather
// than by a new attempt/continuation count. This is a Host lifetime cache of
// already authorized bindings, not a second capability or business ledger.
const sourceReportRegistryLimit = 20 * 1024 * 1024

type sourceReportRegistry struct {
	mu       sync.Mutex
	bindings map[string]contract.ReportCapabilityRegistration
	bytes    int
}

func reportBinding(registration contract.ReportCapabilityRegistration) contract.ReportCapabilityRegistration {
	registration.ExecutionEpoch = 0
	return registration
}
func (r *sourceReportRegistry) remember(registration contract.ReportCapabilityRegistration) error {
	binding := reportBinding(registration)
	body, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, exists := r.bindings[binding.CapabilityID]; exists {
		if prior != binding {
			return errors.New("report_capability_conflict")
		}
		return nil
	}
	if len(body) > sourceReportRegistryLimit-r.bytes {
		return errors.New("report_registry_exhausted")
	}
	if r.bindings == nil {
		r.bindings = make(map[string]contract.ReportCapabilityRegistration)
	}
	r.bindings[binding.CapabilityID] = binding
	r.bytes += len(body)
	return nil
}
func (r *sourceReportRegistry) snapshot() []contract.ReportCapabilityRegistration {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]contract.ReportCapabilityRegistration, 0, len(r.bindings))
	for _, binding := range r.bindings {
		result = append(result, binding)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CapabilityID < result[j].CapabilityID })
	return result
}
func (r *sourceReportRegistry) replay(ctx context.Context, session *sourceSession) error {
	for _, binding := range r.snapshot() {
		binding.ExecutionEpoch = session.epoch
		if err := session.registerReportCapability(ctx, binding); err != nil {
			return err
		}
	}
	return nil
}
