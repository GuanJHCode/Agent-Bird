package main

import (
	"context"
	"slices"
)

// Reject invalid workspace/provider requests before owner lookup, without
// changing the original bytes used by the authenticated configuration choice.
// The resolved plan is checked again after owner authentication.
func preflightTaskPlanBeforeOwner(ctx context.Context, handle string, plan taskPlan) error {
	plan.Tasks = slices.Clone(plan.Tasks)
	for i := range plan.Tasks {
		task := &plan.Tasks[i]
		if task.Diagnostics != nil {
			if err := task.Diagnostics.Validate(); err != nil {
				return err
			}
			policy := *task.Diagnostics
			task.Diagnostics = &policy
		}
		task.Dependencies = slices.Clone(task.Dependencies)
		task.AdapterPayload = slices.Clone(task.AdapterPayload)
		task.Fallbacks = slices.Clone(task.Fallbacks)
		for j := range task.Fallbacks {
			task.Fallbacks[j] = slices.Clone(task.Fallbacks[j])
		}
	}
	if err := prepareTaskWorkspaces(handle, plan.Tasks); err != nil {
		return err
	}
	if err := preflightTasks(plan.Tasks); err != nil {
		return err
	}
	return preflightProviders(ctx, plan.Tasks)
}
