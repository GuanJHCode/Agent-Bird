package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

func TestConfigPreparationRejectedCompetitorCannotReleaseCommittedUse(t *testing.T) {
	for _, route := range []string{"second-explicit-handle", "low-level-submit"} {
		t.Run(route, func(t *testing.T) {
			root, exe, state := configPreparedPackage(t)
			ctx := context.Background()
			scope, _ := contract.ConfigGenerationOwnerScope("owner", "birth")
			defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "", Codex: ""}
			if _, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &defaults}); err != nil {
				t.Fatal(err)
			}
			plan := taskPlan{RunID: "run", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "task", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}}
			db, g, reader := configRealOwner(t, state, "owner")
			p1, err := prepareConfigTaskPlan(ctx, state, exe, scope, plan, strings.Repeat("1", 64), false, nil, nil, reader)
			if err != nil {
				t.Fatal("first preparation", err)
			}
			p2 := p1
			if route == "second-explicit-handle" {
				p2, err = prepareConfigTaskPlan(ctx, state, exe, scope, plan, strings.Repeat("1", 64), false, nil, nil, reader)
				if err != nil {
					t.Fatal("second handle's exact CPP replay", err)
				}
			} else {
				// The existing low-level receiver also accepts this original ref,
				// full plan digest and use; it does not require the CPP writer.
				if err = install.ValidateConfigGenerationSubmission(exe, scope, "run", p2.ResolvedPlanSHA256, configTaskUses(p2.ResolvedPlan.Tasks)); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := configPreparedSubmit(t, db, g, exe, p2, "second-submit-token")
			if err != nil {
				t.Fatal("P2 first committed Submit", err)
			}
			host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: g.ID, OriginPID: 111, OriginBirth: "birth", HostGeneration: g.ID, PID: 222, Birth: "synthetic-host", Executable: exe}, 1)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := db.ClaimReady(ctx, host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := configPreparedSubmit(t, db, g, exe, p1, "first-submit-token"); err == nil {
				t.Fatal("P1 duplicate run unexpectedly committed")
			}
			handle := filepath.Join(root, "first-handle.json")
			h := taskHandle{Version: 1, StateDir: state, RunID: "run", TaskIDs: []string{"task"}, Status: "submitting", ControlFile: filepath.Join(root, "first-control.json")}
			if err = writeExclusiveJSON(handle, h); err != nil {
				t.Fatal(err)
			}
			// Both actual frontends use this durable rejection path. A generic
			// conflict cannot release another submitter's shared original use.
			if err = recordRejectedConfigSubmission(handle, &h, p1); err == nil || err.Error() != "config_rejection_use_retained" {
				t.Fatal("retention was reported as cleanup success", err)
			}
			var saved taskHandle
			if err = readPrivateJSON(handle, &saved); err != nil || saved.Status != "rejected" || saved.ControlFile != "" {
				t.Fatal("rejected handle fence absent", saved, err)
			}
			pin := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(exe))), "..", "pins", "task.json")
			if _, err = os.Lstat(pin); err != nil {
				t.Errorf("P1 conflict erased P2's original use while attempt %s was not terminal: %v", grant.AttemptID, err)
			}
			if err = install.ValidateConfigGenerationSubmission(exe, scope, "run", p1.ResolvedPlanSHA256, configTaskUses(p1.ResolvedPlan.Tasks)); err != nil {
				t.Errorf("committed original use no longer verifies: %v", err)
			}
			snapshot, err := db.TaskSnapshot(ctx, "task")
			if err != nil || snapshot.Status != "running" {
				t.Fatal("fixture did not leave original attempt running", snapshot, err)
			}
		})
	}
	// Store identities are trusted pure fixtures, not actual worker/kernel
	// ownership or an actual lost-ACK/RPC transport observation.
}
