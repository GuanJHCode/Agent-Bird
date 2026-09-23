package main

// taskContinuation describes work still owned by the calling main agent. It is
// not an automatic callback, a new task, or permission to accept/retry results.
type taskContinuation struct {
	DeliveryMode      string     `json:"delivery_mode"`
	AutomaticCallback bool       `json:"automatic_callback"`
	NextAction        string     `json:"next_action"`
	Commands          [][]string `json:"commands,omitempty"`
	Instruction       string     `json:"instruction"`
}

func taskSubmissionReceipt(path string, h taskHandle, status string, existing bool) map[string]any {
	next := taskContinuation{
		DeliveryMode: "collect", NextAction: "wait",
		Instruction: "No automatic callback. Keep this main-agent turn active: wait on every original handle, verify artifacts, make authorized owner decisions, and present outcomes. Queued/running is not completion; do not ask the user to poll.",
	}
	switch {
	case status == "submitting":
		next.NextAction = "reconcile"
		next.Instruction = "Submission is unresolved. Reconcile this original handle; do not redispatch, replace the request, or infer that no worker ran. Continue tracking other pending handles."
	case h.Status == "rejected":
		next.NextAction = "report_blocker"
		next.Instruction = "Report the rejection and preserve the request and budget. Continue tracking other pending handles; do not retry or substitute a provider automatically."
	case existing:
		next.NextAction = "inspect"
	}
	if next.NextAction == "wait" {
		for _, taskID := range h.TaskIDs {
			next.Commands = append(next.Commands, []string{"task", "wait", "--handle", path, "--task-id", taskID, "--timeout-ms", "30000"})
		}
	} else if next.NextAction == "inspect" || next.NextAction == "reconcile" {
		next.Commands = [][]string{{"task", next.NextAction, "--handle", path}}
	}
	receipt := map[string]any{"version": 1, "handle": path, "run_id": h.RunID, "task_ids": h.TaskIDs, "status": status, "continuation": next}
	if existing {
		receipt["existing"] = true
	}
	return receipt
}
