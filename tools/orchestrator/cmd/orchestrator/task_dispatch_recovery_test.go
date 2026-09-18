package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDispatchCheckpointDistinguishesPreSubmitFromUncertainLaunch(t *testing.T) {
	for _, phase := range []string{"preparing", "prepared", "handle-before-control", "control-before-receipt", "rejected"} {
		t.Run(phase, func(t *testing.T) {
			root := privateTaskTemp(t)
			expected := dispatchReservation{Version: 1, RequestSHA256: "request", ControllerThread: "owner", RunID: "run"}
			record := expected
			record.Phase = "preparing"
			if phase != "preparing" {
				record.Phase = "prepared"
				if err := writeExclusiveJSON(filepath.Join(root, "plan.json"), taskPlan{RunID: "run", PlanRevision: 1}); err != nil {
					t.Fatal(err)
				}
				data, _ := os.ReadFile(filepath.Join(root, "plan.json"))
				record.PlanSHA256 = dispatchHash(string(data))
			}
			if err := writeExclusiveJSON(filepath.Join(root, "reservation.json"), record); err != nil {
				t.Fatal(err)
			}
			if phase == "handle-before-control" || phase == "control-before-receipt" || phase == "rejected" {
				h := taskHandle{Version: 1, RunID: "run", StateDir: root, TaskIDs: []string{"run-work"}, Status: "submitting"}
				if phase == "control-before-receipt" {
					h.ControlFile = filepath.Join(root, "original-control.json")
				}
				if phase == "rejected" {
					h.Status = "rejected"
				}
				if err := writeExclusiveJSON(filepath.Join(root, "handle.json"), h); err != nil {
					t.Fatal(err)
				}
			}
			_, h, err := loadDispatchCheckpoint(root, root, expected)
			if err != nil {
				t.Fatal(err)
			}
			switch phase {
			case "preparing", "prepared":
				if h != nil {
					t.Fatal("invented existing launch")
				}
			case "handle-before-control":
				if h == nil || h.ControlFile != "" {
					t.Fatal("lost pre-submit receipt boundary")
				}
			case "control-before-receipt":
				if h == nil || h.ControlFile == "" {
					t.Fatal("uncertain launch treated as safe to submit")
				}
			case "rejected":
				if h == nil || h.Status != "rejected" {
					t.Fatal("rejected attempt reset")
				}
			}
		})
	}
}

func TestDispatchCheckpointRejectsChangedPlanAndPrematureHandle(t *testing.T) {
	for _, kind := range []string{"changed-plan", "premature-handle", "different-request"} {
		t.Run(kind, func(t *testing.T) {
			root := privateTaskTemp(t)
			expected := dispatchReservation{Version: 1, RequestSHA256: "request", ControllerThread: "owner", RunID: "run"}
			record := expected
			record.Phase = "prepared"
			if err := writeExclusiveJSON(filepath.Join(root, "plan.json"), taskPlan{RunID: "run", PlanRevision: 1}); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(root, "plan.json"))
			record.PlanSHA256 = dispatchHash(string(data))
			if kind == "changed-plan" {
				if err := os.WriteFile(filepath.Join(root, "plan.json"), []byte(`{"run_id":"foreign"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "premature-handle" {
				record.Phase = "preparing"
				record.PlanSHA256 = ""
				if err := writeExclusiveJSON(filepath.Join(root, "handle.json"), taskHandle{Version: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "different-request" {
				record.RequestSHA256 = "changed"
			}
			if err := writeExclusiveJSON(filepath.Join(root, "reservation.json"), record); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadDispatchCheckpoint(root, root, expected); err == nil {
				t.Fatal("untrusted recovery evidence accepted")
			}
		})
	}
}
