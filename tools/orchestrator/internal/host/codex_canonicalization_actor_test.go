//go:build darwin

package host

import (
	"os"
	"path/filepath"
	"testing"
)

// ROOT-only: original 219 operations and original Handle/pipe/cleanup consumer,
// then exact ancestor metadata/data and owned-home EvalSymlinks observations.
// EvalSymlinks is a Go file API observation, not proof of Rust's exact syscall.
func TestManagedCodexSandboxCanonicalizationAPIActor(t *testing.T) {
	codexSandboxLinkActor(t, true)
}

func codexSandboxCanonicalizationExtendPlan(plan codexSandboxPlan, retained []string, p codexPreparedState, spool string) (codexSandboxPlan, []string, error) {
	plan.Operations = append([]codexSandboxOperation(nil), plan.Operations...)
	retained = append([]string(nil), retained...)
	add := func(name, kind, path, expect string) {
		plan.Operations = append(plan.Operations, codexSandboxOperation{Name: name, Kind: kind, Path: path, Expect: expect})
	}
	attempt := filepath.Join(spool, p.Origin.Scope.AttemptID)
	add("owned_home_canonicalize", "canonicalize", p.Layout.Home.Path, "success")
	add("original_spool_metadata", "metadata", spool, "success")
	add("original_attempt_metadata", "metadata", attempt, "success")
	add("original_spool_list", "list", spool, "permission")
	add("original_attempt_list", "list", attempt, "permission")
	add("original_spool_control_read", "read", filepath.Join(spool, "control-probe"), "permission")
	for _, sibling := range []struct{ name, path string }{
		{"other_attempt", filepath.Join(spool, "other-attempt")},
		{"other_segment", filepath.Join(attempt, "other-segment")},
	} {
		if err := os.Mkdir(sibling.path, 0700); err != nil {
			return plan, retained, err
		}
		file := filepath.Join(sibling.path, "synthetic-control")
		if err := codexActorWriteFile(file, []byte("SYNTHETIC_CONTROL_ONLY\n"), os.O_EXCL); err != nil {
			return plan, retained, err
		}
		retained = append(retained, sibling.path, file)
		add(sibling.name+"_metadata", "metadata", sibling.path, "permission")
		add(sibling.name+"_list", "list", sibling.path, "permission")
		add(sibling.name+"_file_metadata", "metadata", file, "permission")
		add(sibling.name+"_file_read", "read", file, "permission")
	}
	return plan, retained, codexSandboxLinkValidatePlan(plan)
}
