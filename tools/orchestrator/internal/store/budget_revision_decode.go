package store

import (
	"bytes"
	"encoding/json"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const MaxBudgetRevisionRequestBytes = 64 * 1024

// DecodeBudgetRevisionRequest is shared by the file and IPC entry points.
// In particular, revision zero must be an explicit CAS choice, never a
// missing/null field silently converted to Go's zero value.
func DecodeBudgetRevisionRequest(raw []byte) (BudgetRevisionRequest, error) {
	var request BudgetRevisionRequest
	invalid := CodeError("budget_revision_invalid")
	if len(raw) > MaxBudgetRevisionRequestBytes || contract.DecodeOwnerRuntimeStrict(raw, &request) != nil {
		return request, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields["expected_revision"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["expected_revision"]), []byte("null")) {
		return request, invalid
	}
	normalized, _, err := canonicalBudgetRevisionRequest(request)
	return normalized, err
}
