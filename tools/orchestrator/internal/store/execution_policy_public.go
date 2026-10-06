package store

// NormalizeExecutionTask applies the Store's canonical execution-policy
// validation and defaults without persisting any state. Public admission
// boundaries use it to make the same policy decision as SubmitPlan.
func NormalizeExecutionTask(task TaskSpec) (TaskSpec, error) {
	return normalizeExecutionTask(task)
}
