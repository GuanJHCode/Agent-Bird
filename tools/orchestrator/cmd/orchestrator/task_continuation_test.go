package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTaskContinuationPreservesHandlesAndDecisionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, handleStatus, responseStatus, wantAction string
		existing                                       bool
		commands                                       int
	}{
		{"new-multiple-tasks", "queued", "queued", "wait", false, 2},
		{"uncertain-submit", "submitting", "submitting", "reconcile", false, 1},
		{"uncertain-replay", "submitting", "submitting", "reconcile", true, 1},
		{"reconciled-replay", "submitting", "existing", "inspect", true, 1},
		{"existing-work", "queued", "existing", "inspect", true, 1},
		{"rejected", "rejected", "rejected", "report_blocker", false, 0},
		{"rejected-replay", "rejected", "existing", "report_blocker", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := taskHandle{Version: 1, RunID: "original-run", StateDir: "/private/original-state", ControlFile: "/private/CONTROL_SENTINEL", TaskIDs: []string{"first", "second"}, Status: tc.handleStatus}
			path := "/private/space $HOME `literal`/handle.json"
			receipt := taskSubmissionReceipt(path, h, tc.responseStatus, tc.existing)
			if receipt["handle"] != path || receipt["run_id"] != h.RunID || receipt["status"] != tc.responseStatus || !reflect.DeepEqual(receipt["task_ids"], h.TaskIDs) {
				t.Fatalf("original receipt changed: %+v", receipt)
			}
			next := receipt["continuation"].(taskContinuation)
			if next.DeliveryMode != "collect" || next.AutomaticCallback || next.NextAction != tc.wantAction || len(next.Commands) != tc.commands {
				t.Fatalf("unsafe continuation: %+v", next)
			}
			for i, args := range next.Commands {
				if args[0] != "task" || args[1] != tc.wantAction || args[2] != "--handle" || args[3] != path {
					t.Fatalf("changed command scope: %q", args)
				}
				if tc.wantAction == "wait" && (len(args) != 8 || args[5] != h.TaskIDs[i] || args[7] != "30000") {
					t.Fatalf("lost task or bounded wait: %q", args)
				}
			}
			encoded, err := json.Marshal(receipt)
			if err != nil || strings.Contains(string(encoded), "CONTROL_SENTINEL") || strings.Contains(string(encoded), "original-state") {
				t.Fatalf("control route exposed in continuation: %s %v", encoded, err)
			}
		})
	}
}
