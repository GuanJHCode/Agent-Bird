package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

// These are Store transaction fixtures, not native Git/provider acceptance.
func TestIndexedAcceptChecksDurableSourceAndCoverage(t *testing.T) {
	for _, kind := range []string{"valid_reopen", "valid_config", "valid_policy_retry", "validate_manifest", "validate_source", "review_missing", "review_grant", "review_input", "review_config", "review_manifest", "review_validation", "review_revision", "review_provider", "integrate_vector", "integrate_missing_proof"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			tasks := indexedPlanTasks(t)
			lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok, Protocol: adapter.ProtocolID(adapter.ProviderGrok), Binary: adapter.BinaryPin{Path: "/private/grok", Version: "1.0.34", SHA256: strings.Repeat("b", 64)}}
			if kind == "valid_policy_retry" {
				lock.Binary.Version = "grok 1.0.34 (3736acbc8658)"
			}
			for i := range tasks {
				if strings.HasPrefix(tasks[i].ID, "r") {
					var p map[string]any
					_ = json.Unmarshal(tasks[i].AdapterPayload, &p)
					p["provider_lock"] = lock
					p["profile"] = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly}
					if kind == "valid_policy_retry" {
						p["directory"] = "/private/review"
						p["profile"].(*adapter.ExecutionProfile).GrokSessionWrite = true
						p["profile"].(*adapter.ExecutionProfile).TimeoutMS = 180000
						action := p["candidate_action"].(map[string]any)
						action["target"] = adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}
						action["dependency_input_version"] = 1
						if tasks[i].ID == "r2" {
							tasks[i].MaxAttempts = 3
							tasks[i].ExecutionPolicy = &contract.ExecutionPolicy{Version: 1, Retry: &contract.RetryPolicy{Codes: []string{"probe_start_eagain"}, BackoffMS: []int64{5000, 20000}}}
						}
					}
					tasks[i].AdapterPayload, _ = json.Marshal(p)
				}
			}
			spec := PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks}
			if kind == "valid_config" {
				spec = bindIndexedConfigPlan(t, db, spec)
			}
			plan, err := db.SubmitPlan(ctx, spec)
			if spec.ConfigSubmission != nil {
				spec.ConfigSubmission.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: spec.Run.OriginContextID, OriginPID: spec.Run.OriginPID, OriginBirth: spec.Run.OriginBirth, HostGeneration: spec.Host.HostGeneration, PID: 10, Birth: "host", Executable: spec.Host.Executable}, 1)
			if err != nil {
				t.Fatal(err)
			}
			var sequenceOffset int64
			for step := 0; step < 6; step++ {
				g, err := db.ClaimReady(ctx, hostID, 1, 2)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "valid_policy_retry" && g.TaskID == "r2" {
					original := g
					fixture := budgetBatchFixture{db: db, host: hostID, grant: g, sequence: int64(2 * step)}
					failure := policyRejection(&fixture, "probe_start_eagain")
					if _, err = db.CommitHostEvent(ctx, failure); err != nil {
						t.Fatal(err)
					}
					sequenceOffset++
					var before, after string
					var due, reopened int64
					if err = db.sql.QueryRow(`SELECT revision_payload,eligible_at_unix_ms FROM retry_decisions WHERE task_id='r2'`).Scan(&before, &due); err != nil {
						t.Fatal("indexed policy retry did not queue", err)
					}
					if _, err = db.CommitHostEvent(ctx, failure); err != nil {
						t.Fatal("duplicate failure changed policy decision", err)
					}
					_ = db.Close()
					db, err = Open(path)
					if err != nil {
						t.Fatal(err)
					}
					if err = db.sql.QueryRow(`SELECT revision_payload,eligible_at_unix_ms FROM retry_decisions WHERE task_id='r2'`).Scan(&after, &reopened); err != nil || after != before || reopened != due {
						t.Fatal("reopen changed retry authority or backoff", err)
					}
					if _, err = db.ClaimReady(ctx, hostID, 1, 2); !errors.Is(err, ErrNoReady) {
						t.Fatal("retry bypassed durable backoff", err)
					}
					if _, err = db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1 WHERE task_id='r2'`); err != nil {
						t.Fatal(err)
					}
					g, err = db.ClaimReady(ctx, hostID, 1, 2)
					if err != nil || g.TaskID != "r2" || g.AttemptID == original.AttemptID || g.WorkRevision != original.WorkRevision || !reflect.DeepEqual(g.Input, original.Input) || !reflect.DeepEqual(g.DependencyInputs, original.DependencyInputs) {
						t.Fatal("indexed policy retry changed immutable inputs", err)
					}
					oldPayload, _ := adapter.DecodeInvocationPayload(original.AdapterPayload)
					nextPayload, _ := adapter.DecodeInvocationPayload(g.AdapterPayload)
					nextPayload.Directory = oldPayload.Directory
					if !reflect.DeepEqual(oldPayload, nextPayload) {
						t.Fatal("indexed retry changed assignment/config/provider")
					}
				}
				binding := gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
				d := map[string]any{"version": 1, "binding": binding}
				if g.Input != nil {
					raw, e := os.ReadFile(g.Input.Event.Artifact.Path)
					if e != nil || json.Unmarshal(raw, &d) != nil {
						t.Fatal("source artifact", e)
					}
					var original map[string]json.RawMessage
					_ = json.Unmarshal(raw, &original)
					if v, ok := original["validation"]; ok {
						d["validation"] = v
					}
					d["binding"] = binding
					d["input_event_id"], d["input_sha256"], d["owner_decision_id"] = g.Input.Event.EventID, g.Input.Event.PayloadHash, g.Input.DecisionCommandID
				}
				payload, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
				if err != nil {
					t.Fatal(err)
				}
				if payload.ConfigGeneration != nil {
					d["config_generation"] = payload.ConfigGeneration
				}
				switch g.TaskID {
				case "edit":
					d["candidate"] = gitops.CandidateReceipt{Binding: &binding, BaseOID: strings.Repeat("a", 40), CandidateOID: strings.Repeat("b", 40), TreeOID: strings.Repeat("c", 40), PrivateRef: "refs/private/candidate"}
					d["candidate_receipt_sha256"] = strings.Repeat("d", 64)
				case "validate":
					d["stage"] = "validate"
					v := map[string]any{"candidate_oid": strings.Repeat("b", 40), "tree_oid": strings.Repeat("c", 40), "binding": binding, "passed": true, "exit_code": 0}
					d["validation"], d["validation_digest"] = v, contract.IndexedReviewJSONSHA256(v)
					d["target"] = gitops.ReviewBinding{FinalCandidateOID: strings.Repeat("b", 40), PlanRevision: 1, ValidationDigest: contract.IndexedReviewJSONSHA256(v)}
					m := contract.IndexedReviewManifest{Version: 1, Scope: contract.IndexedReviewScope, CandidateReceiptSHA256: strings.Repeat("d", 64), BaseOID: strings.Repeat("a", 40), CandidateOID: strings.Repeat("b", 40), TreeOID: strings.Repeat("c", 40), Pages: []contract.IndexedReviewPage{{Ordinal: 0, ChunkOrdinals: []int{0, 1, 2}}}}
					for i, p := range []string{"a", "b", "c"} {
						m.Chunks = append(m.Chunks, contract.IndexedReviewChunk{Ordinal: i, Path: p, Side: "candidate", Mode: "100644", BlobOID: strings.Repeat("d", 40), Length: 4, WholeSHA256: strings.Repeat("e", 64), SHA256: strings.Repeat("e", 64)})
					}
					d["review_index"] = m
				case "r0", "r1", "r2":
					d["stage"], d["provider_lock"], d["review_scope"] = "review", lock, contract.IndexedReviewScope
					d["review"] = map[string]any{"decision": "approve", "summary": "reviewed assigned chunks"}
					d["target"].(map[string]any)["review_revision"] = g.WorkRevision
					raw, _ := json.Marshal(d["review_index"])
					var m contract.IndexedReviewManifest
					_ = json.Unmarshal(raw, &m)
					ri := payload.CandidateAction.ReviewIndex
					assigned, e := contract.IndexedReviewAssignment(m, ri.Shards, ri.ReviewOrdinal)
					if e != nil {
						t.Fatal(e)
					}
					proof := &contract.IndexedReviewCoverage{Version: 1, ManifestSHA256: contract.IndexedReviewJSONSHA256(m), AssignmentSHA256: contract.IndexedReviewJSONSHA256(assigned), GrantIntentSHA256: g.LaunchIntentHash, AcceptedInputSHA256: contract.IndexedReviewJSONSHA256(g.Input), ConsumerInputSHA256: strings.Repeat("f", 64), ConsumerSnapshotSHA256: strings.Repeat("a", 64), Shards: ri.Shards, ReviewOrdinal: ri.ReviewOrdinal, ConfigGeneration: payload.ConfigGeneration}
					d["review_coverage"], d["review_assignment_sha256"], d["review_input_sha256"], d["review_snapshot_sha256"] = proof, proof.AssignmentSHA256, proof.ConsumerInputSHA256, proof.ConsumerSnapshotSHA256
				case "integrate":
					d["stage"] = "integrate"
					d["dependency_inputs_sha256"] = contract.IndexedReviewJSONSHA256(g.DependencyInputs)
					d["integration"] = map[string]any{"state": "integrated", "candidate_oid": strings.Repeat("b", 40), "target_head": strings.Repeat("b", 40)}
				}
				target := g.TaskID == "validate" && strings.HasPrefix(kind, "validate_") || g.TaskID == "r2" && strings.HasPrefix(kind, "review_") || g.TaskID == "integrate" && strings.HasPrefix(kind, "integrate_")
				if target {
					switch kind {
					case "validate_manifest", "review_manifest":
						delete(d, "review_index")
					case "validate_source":
						d["input_event_id"] = "substituted"
					case "review_missing", "integrate_missing_proof":
						delete(d, "review_coverage")
					case "review_grant":
						d["review_coverage"].(*contract.IndexedReviewCoverage).GrantIntentSHA256 = strings.Repeat("a", 64)
					case "review_input":
						d["review_coverage"].(*contract.IndexedReviewCoverage).AcceptedInputSHA256 = strings.Repeat("b", 64)
					case "review_config":
						d["review_coverage"].(*contract.IndexedReviewCoverage).ConfigGeneration = &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("b", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1"}
					case "review_validation":
						var v map[string]any
						_ = json.Unmarshal(d["validation"].(json.RawMessage), &v)
						v["candidate_oid"] = strings.Repeat("c", 40)
						d["validation"] = v
					case "review_revision":
						d["binding"] = gitops.CandidateBinding{RunID: "run", TaskID: g.TaskID, WorkRevision: 2}
					case "review_provider":
						changed := *lock
						changed.Binary.SHA256 = strings.Repeat("c", 64)
						d["provider_lock"] = &changed
					case "integrate_vector":
						d["dependency_inputs_sha256"] = strings.Repeat("a", 64)
					}
				}
				raw, _ := json.Marshal(d)
				artifact := filepath.Join(t.TempDir(), g.TaskID+".json")
				if err = os.WriteFile(artifact, raw, 0600); err != nil {
					t.Fatal(err)
				}
				e := contract.Event{Version: 1, ProducerID: hostID, EventID: g.TaskID + "-result", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: int64(2*step+1) + sequenceOffset, Kind: contract.EventResult, PayloadHash: runtimeHash(string(raw)), Artifact: &contract.ArtifactRef{ID: g.TaskID, Path: artifact, Size: int64(len(raw)), SHA256: runtimeHash(string(raw))}}
				if _, err = db.CommitHostEvent(ctx, e); err != nil {
					t.Fatal(err)
				}
				done := e
				zero := 0
				done.EventID = g.TaskID + "-exited"
				done.Sequence++
				done.Kind = contract.EventExited
				done.ExitCode = &zero
				if _, err = db.CommitHostEvent(ctx, done); err != nil {
					t.Fatal(err)
				}
				page, err := db.CollectPending(ctx, g.TaskID, "", 0, false)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "valid_policy_retry" && g.TaskID == "r2" {
					if len(page.Events) != 2 || page.Events[0].Kind != contract.EventLaunchRejected || page.Events[0].AttemptID == g.AttemptID {
						t.Fatalf("missing original failure history: %+v", page)
					}
					page.Events = page.Events[1:]
				}
				if len(page.Events) != 1 || page.Events[0].Kind != contract.EventResult || page.Events[0].AttemptID != g.AttemptID {
					t.Fatalf("pending=%+v", page)
				}
				r := page.Events[0]
				spec := ReviewSpec{TaskID: g.TaskID, WorkRevision: g.WorkRevision, EventID: r.EventID, EventRevision: r.EventRevision, EventHash: r.PayloadHash, ActionSlot: r.ActionSlot, Decision: "accept", CommandID: "accept-" + g.TaskID}
				_, err = db.ReviewResult(ctx, spec)
				if target {
					if err == nil {
						t.Fatal("invalid indexed result accepted")
					}
					var count int
					var status string
					if err = db.sql.QueryRow(`SELECT COUNT(*) FROM review_decisions WHERE task_id=?`, g.TaskID).Scan(&count); err != nil || count != 0 {
						t.Fatalf("partial decision %d %v", count, err)
					}
					if err = db.sql.QueryRow(`SELECT status FROM tasks WHERE id=?`, g.TaskID).Scan(&status); err != nil || status != "result_ready" {
						t.Fatalf("mutated task %s %v", status, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("%s accept: %v", g.TaskID, err)
				}
				if g.TaskID == "integrate" && (kind == "valid_reopen" || kind == "valid_config") {
					if err = os.Remove(artifact); err != nil {
						t.Fatal(err)
					}
					if err = db.Close(); err != nil {
						t.Fatal(err)
					}
					db, err = Open(path)
					if err != nil {
						t.Fatal(err)
					}
					if status, err := db.ReviewResult(ctx, spec); err != nil || status != "accepted" {
						t.Fatalf("replay resampled artifact %s %v", status, err)
					}
				}
			}
		})
	}
}
