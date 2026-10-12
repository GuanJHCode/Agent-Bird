package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// TestExitSuccessFixturePolicyRequiresFakeAdapter pins the only guard on the
// exit_success_fixture completion policy. That policy settles a task to
// completed from a result event alone, without checking any artifact digest
// (host_events.go), so the invariant "an incomplete diagnostic artifact is never
// accepted as success" only holds for it as long as admission refuses the policy
// for anything but the fake adapter. No test in the tree covered that gate.
func TestExitSuccessFixturePolicyRequiresFakeAdapter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		allowed bool
	}{
		{"fake adapter may use it", `{"kind":"fake"}`, true},
		{"codex adapter may not", `{"kind":"codex"}`, false},
		{"absent discriminator may not", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			_, err = db.SubmitPlan(ctx, PlanSpec{
				Run:  RunSpec{ID: "run-fixture-gate", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
				Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
				Tasks: []TaskSpec{
					{ID: "gated", RunID: "run-fixture-gate", MaxAttempts: 3,
						CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(tc.payload)},
				},
			})
			if tc.allowed {
				if err != nil {
					t.Fatalf("fake adapter plan refused: err=%v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidDAG) {
				t.Fatalf("err=%v want invalid_dag for payload %s", err, tc.payload)
			}
			if _, err = db.RunID(ctx, "gated"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("refused plan left a partial task: err=%v", err)
			}
		})
	}
}
