package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Losing the artifact or echoing a dynamic error would hide the cause or leak
// provider paths/credentials into the owner's durable collect stream.
func TestLaunchFailureCollectsSafeDiagnostic(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		code  string
	}{
		{"payload", errors.New("invalid_json"), "invocation_payload_invalid"},
		{"workspace", errors.New("profile_workspace_untrusted"), "profile_workspace_untrusted"},
		{"workspace_prefix", errors.New("profile_workspace_untrusted: PRIVATE_SENTINEL"), "launch_failed"},
		{"validation", errors.New("candidate_validation_source_invalid"), "candidate_validation_source_invalid"},
		{"review", errors.New("candidate_review_source_invalid"), "candidate_review_source_invalid"},
		{"integration", errors.New("candidate_integration_source_invalid"), "candidate_integration_source_invalid"},
		{"input_changed", errors.New("candidate_input_changed"), "candidate_input_changed"},
		{"target", errors.New("candidate_target_mismatch"), "candidate_target_mismatch"},
		{"missing_invocation", errors.New("invocation_unavailable"), "invocation_unavailable"},
		{"unknown", errors.New("PRIVATE_SENTINEL /credentials/token.json"), "launch_failed"},
		{"prefix", errors.New("invalid_json: PRIVATE_SENTINEL"), "launch_failed"},
		{"wrapped", fmt.Errorf("PRIVATE_SENTINEL: %w", errors.New("invalid_json")), "launch_failed"},
		{"field", &json.UnmarshalTypeError{Value: "PRIVATE_SENTINEL", Type: reflect.TypeOf(0), Field: "SECRET_FIELD"}, "launch_failed"},
		{"nil", nil, "launch_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
			meta := launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1}
			if err = h.recordLaunchFailed(a, "run", a.TaskID, meta, tc.cause); err != nil {
				t.Fatal(err)
			}
			got, err := h.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].Kind != contract.EventFailed || got[1].Kind != contract.EventExited || got[0].Artifact == nil {
				t.Fatalf("missing failed diagnostic: %#v", got)
			}
			e := got[0]
			if e.Process != nil || e.ExitCode == nil || *e.ExitCode != -1 {
				t.Fatalf("failure changed process outcome: %+v", e)
			}
			if e.Artifact.ID != "launch-failure" || e.Artifact.Path != filepath.Join(root, a.ID, a.SegmentID, "launch-failure", "result-output.bin") {
				t.Fatalf("diagnostic occupied candidate path: %+v", e.Artifact)
			}
			_, recovered, recoverErr := readRecoveredLaunchFailure(e.Artifact.Path)
			if recoverErr != nil || string(recovered) != tc.code {
				t.Fatalf("diagnostic recovery: %s %v", recovered, recoverErr)
			}
			body, err := os.ReadFile(e.Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			if e.Artifact.Size != int64(len(body)) || e.Artifact.SHA256 != hex.EncodeToString(sum[:]) || e.PayloadHash != e.Artifact.SHA256 || len(body) > 256 {
				t.Fatalf("artifact hash/size mismatch: %+v", e.Artifact)
			}
			var document map[string]any
			if err = json.Unmarshal(body, &document); err != nil {
				t.Fatal(err)
			}
			if len(document) != 4 || document["version"] != float64(1) || document["kind"] != "launch_failure" || document["stage"] != "prelaunch" || document["cause_code"] != tc.code {
				t.Fatalf("unsafe diagnostic schema: %s", body)
			}
			if strings.Contains(string(body), "PRIVATE_SENTINEL") || strings.Contains(string(body), "SECRET_FIELD") {
				t.Fatalf("leaked error: %s", body)
			}
			info, err := os.Stat(e.Artifact.Path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("artifact mode: %v %v", info, err)
			}
			sp, err := events.Open(filepath.Join(root, a.ID, a.SegmentID))
			if err != nil {
				t.Fatal(err)
			}
			var launch map[string]any
			if err = sp.ReadMetadata("launch.json", &launch); err != nil {
				t.Fatal(err)
			}
			if launch["phase"] != "exited" || launch["cause_code"] != tc.code {
				t.Fatalf("lost safe cause on exit: %+v", launch)
			}
			if _, err = os.Stat(filepath.Join(root, a.ID, a.SegmentID, "result-output.bin")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("root result path occupied: %v", err)
			}
		})
	}
}

// Source fallback after an execution error must not replace original evidence.
func TestLaunchFailurePreservesExistingTerminalEvidence(t *testing.T) {
	for _, kind := range []string{contract.EventResult, contract.EventFailed, contract.EventUnknown} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
			sp, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			meta := launchMetadata{commandID: "c", workRevision: 1, executionEpoch: 1}
			path, err := sp.WriteArtifact([]byte("existing candidate or failure evidence"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.appendPhaseEvent(sp, "r", "t", a, meta, kind, "existing-hash", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if kind != contract.EventUnknown {
				if _, err = h.appendPhaseEvent(sp, "r", "t", a, meta, contract.EventExited, "exit-hash", nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err = sp.WriteMetadata("launch.json", map[string]any{"phase": "preserve", "marker": "original"}); err != nil {
				t.Fatal(err)
			}
			before, err := h.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			err = h.recordLaunchFailed(a, "r", "t", meta, errors.New("invalid_json"))
			if (kind == contract.EventUnknown && err == nil) || (kind != contract.EventUnknown && err != nil) {
				t.Fatalf("existing outcome error=%v kind=%s", err, kind)
			}
			after, err := h.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("fallback appended terminal events: before=%d after=%d", len(before), len(after))
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "existing candidate or failure evidence" {
				t.Fatalf("artifact changed: %q %v", body, err)
			}
			var launch map[string]any
			if err = sp.ReadMetadata("launch.json", &launch); err != nil || launch["marker"] != "original" {
				t.Fatalf("launch metadata changed: %+v %v", launch, err)
			}
			if _, err = os.Stat(filepath.Join(root, "a", "s", "launch-failure")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unneeded diagnostic on existing terminal: %v", err)
			}
		})
	}
}

func TestLaunchFailureRejectsAmbiguousPartialEvidence(t *testing.T) {
	for _, mode := range []string{"process", "artifact", "partial-result"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
			sp, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			meta := launchMetadata{commandID: "c", workRevision: 1, executionEpoch: 1}
			switch mode {
			case "artifact":
				_, err = sp.WriteArtifact([]byte("keep"))
			case "process":
				_, err = h.appendPhaseEvent(sp, "r", "t", a, meta, contract.EventSpawned, "spawn", &process.Identity{PID: 123, PGID: 123, BirthKnown: true, Birth: "birth"}, nil, nil)
			case "partial-result":
				_, err = h.appendPhaseEvent(sp, "r", "t", a, meta, contract.EventResult, "result", nil, nil, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := h.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			if err = h.recordLaunchFailed(a, "r", "t", meta, errors.New("invalid_json")); err == nil {
				t.Fatal("ambiguous existing evidence classified as unspawned failure")
			}
			after, err := h.Collect(context.Background(), a)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("partial evidence changed: %v", err)
			}
		})
	}
}

type launchFailureDBSink struct{ db *store.DB }

func (s launchFailureDBSink) SendEvent(ctx context.Context, _ uint64, e contract.Event) (contract.DurableAck, error) {
	return s.db.CommitHostEvent(ctx, e)
}

// An artifact must not upgrade failed status into an acceptable candidate.
func TestLaunchFailureDiagnosticCannotBeAcceptedOrReleaseCandidateDependency(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plan, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "r", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "g", Executable: "/private/bin/orchestrator"}, Tasks: []store.TaskSpec{
		{ID: "edit", RunID: "r", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)},
		{ID: "check", RunID: "r", MaxAttempts: 1, Dependencies: []string{"edit"}, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","source_task":"edit","command":["/usr/bin/true"]}}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "g", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, id, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	spoolRoot := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(spoolRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	if err = h.recordLaunchFailed(a, "r", a.TaskID, launchMetadata{commandID: grant.CommandID, workRevision: grant.WorkRevision, executionEpoch: 1}, errors.New("invalid_json")); err != nil {
		t.Fatal(err)
	}
	if err = h.PublishAll(ctx, launchFailureDBSink{db}, 1); err != nil {
		t.Fatal(err)
	}
	page, err := db.CollectPending(ctx, "edit", "", 0, false)
	if err != nil || len(page.Events) != 1 || page.Events[0].Artifact == nil {
		t.Fatalf("diagnostic not collected: %+v %v", page, err)
	}
	restarted, err := NewIPC(spoolRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.PublishAll(ctx, launchFailureDBSink{db}, 1); err != nil {
		t.Fatal(err)
	}
	replayed, err := db.CollectPending(ctx, "edit", "", 0, false)
	if err != nil || !reflect.DeepEqual(page, replayed) {
		t.Fatalf("durable restart/replay changed diagnostic: %v", err)
	}
	e := page.Events[0]
	if _, err = os.ReadFile(e.Artifact.Path); err != nil {
		t.Fatalf("nested artifact lost after durable ACK/reopen: %v", err)
	}
	if e.Kind != contract.EventFailed {
		t.Fatalf("diagnostic became result: %+v", e)
	}
	_, err = db.ReviewResult(ctx, store.ReviewSpec{TaskID: "edit", WorkRevision: grant.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "accept"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("failure accepted: %v", err)
	}
	if _, err = db.ClaimReady(ctx, id, 1, 2); !errors.Is(err, store.ErrNoReady) {
		t.Fatalf("candidate dependency released: %v", err)
	}
}

func TestLaunchFailureAfterPreparedAndReplay(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
	sp, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	meta := launchMetadata{commandID: "c", workRevision: 1, executionEpoch: 1}
	if _, err = h.appendPhaseEvent(sp, "r", "t", a, meta, contract.EventPrepared, "prepared", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = h.recordLaunchFailed(a, "r", "t", meta, errors.New("candidate_validation_source_invalid")); err != nil {
		t.Fatal(err)
	}
	before, err := h.Collect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 || before[0].Kind != contract.EventPrepared || before[1].Kind != contract.EventFailed || before[1].Artifact == nil || before[2].Kind != contract.EventExited {
		t.Fatalf("prepared failure=%+v", before)
	}
	if err = h.recordLaunchFailed(a, "r", "t", meta, errors.New("invalid_json")); err != nil {
		t.Fatal(err)
	}
	after, err := h.Collect(context.Background(), a)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("duplicate failure changed evidence: %v", err)
	}
	if err = sp.AckThrough(before[2].Sequence); err != nil {
		t.Fatal(err)
	}
	if err = sp.AckThrough(before[2].Sequence); err != nil {
		t.Fatal(err)
	}
	pending, err := sp.ReadAfter(before[2].Sequence)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ACK replay pending=%d err=%v", len(pending), err)
	}
}

func TestLaunchFailureDoesNotReclassifyUnknown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "host")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
	if err = h.recordLaunchUnknown(a, "r", "t", launchMetadata{commandID: "c"}, errors.New("invalid_json")); err != nil {
		t.Fatal(err)
	}
	got, err := h.Collect(context.Background(), a)
	if err != nil || len(got) != 1 || got[0].Kind != contract.EventUnknown || got[0].Artifact != nil {
		t.Fatalf("unknown changed: %+v %v", got, err)
	}
	sp, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	var phase map[string]any
	if err = sp.ReadMetadata("launch.json", &phase); err != nil {
		t.Fatal(err)
	}
	if phase["cause_code"] != "process_failed" || phase["phase"] != "unknown" {
		t.Fatalf("unknown cause reclassified: %+v", phase)
	}
	if _, err = os.Stat(filepath.Join(root, "a", "s", "launch-failure")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown acquired prelaunch artifact: %v", err)
	}
}

// A crash after artifact fsync and before failed event append must reuse the
// original cause/hash, not loop forever conflicting with a new generic cause.
func TestLaunchFailureRecoversArtifactBeforeFirstEvent(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			grant := contract.LaunchCommand{CommandID: "c", ReservationID: "reservation", RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", WorkRevision: 1, GrantedActiveMS: 1000}
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.acceptGrant(grant); err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
			sp, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			diagnostic, err := events.Open(filepath.Join(root, "a", "s", "launch-failure"))
			if err != nil {
				t.Fatal(err)
			}
			original := []byte(`{"version":1,"kind":"launch_failure","stage":"prelaunch","cause_code":"invocation_payload_invalid"}`)
			path, err := diagnostic.WriteArtifact(original)
			if err != nil {
				t.Fatal(err)
			}
			if metadata {
				if err = sp.WriteMetadata("launch.json", map[string]any{"phase": "failed", "cause_code": "invocation_payload_invalid"}); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := restarted.acceptGrant(grant)
			if err != nil || !duplicate {
				t.Fatalf("ledger replay duplicate=%v err=%v", duplicate, err)
			}
			if err = restarted.recoverDuplicateGrant(grant, 2); err != nil {
				t.Fatalf("pre-event artifact recovery failed: %v", err)
			}
			records, err := restarted.Collect(context.Background(), a)
			if err != nil || len(records) != 2 || records[0].Kind != contract.EventFailed || records[1].Kind != contract.EventExited || records[0].Artifact == nil {
				t.Fatalf("recovered events=%+v err=%v", records, err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != string(original) {
				t.Fatalf("durable diagnostic overwritten: %q %v", body, err)
			}
			sum := sha256.Sum256(original)
			if records[0].PayloadHash != hex.EncodeToString(sum[:]) || records[0].Artifact.SHA256 != records[0].PayloadHash {
				t.Fatal("recovery changed artifact binding")
			}
			var phase map[string]any
			if err = sp.ReadMetadata("launch.json", &phase); err != nil || phase["phase"] != "exited" || phase["cause_code"] != "invocation_payload_invalid" {
				t.Fatalf("recovery lost original cause: %+v %v", phase, err)
			}
			if err = restarted.recoverDuplicateGrant(grant, 2); err != nil {
				t.Fatal(err)
			}
			again, err := restarted.Collect(context.Background(), a)
			if err != nil || !reflect.DeepEqual(records, again) {
				t.Fatal("second replay changed events")
			}
		})
	}
}

func TestLaunchFailureRecoveryRejectsUntrustedDiagnostic(t *testing.T) {
	valid := `{"version":1,"kind":"launch_failure","stage":"prelaunch","cause_code":"invocation_payload_invalid"}`
	cases := []struct {
		name, body      string
		symlink, public bool
	}{
		{"unknown_cause", strings.Replace(valid, "invocation_payload_invalid", "PRIVATE_SENTINEL", 1), false, false},
		{"extra_field", strings.Replace(valid, "{", "{\"PRIVATE_SENTINEL\":\"secret\",", 1), false, false},
		{"duplicate_field", strings.Replace(valid, "{", "{\"cause_code\":\"PRIVATE_SENTINEL\",", 1), false, false},
		{"wrong_stage", strings.Replace(valid, "prelaunch", "review", 1), false, false},
		{"wrong_version", strings.Replace(valid, "\"version\":1", "\"version\":2", 1), false, false},
		{"trailing", valid + ` {"secret":"PRIVATE_SENTINEL"}`, false, false},
		{"oversize", strings.Repeat("x", 300), false, false},
		{"symlink", valid, true, false}, {"public_file", valid, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{CommandID: "c", ReservationID: "reservation", RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", WorkRevision: 1, GrantedActiveMS: 1000}
			if _, err = h.acceptGrant(grant); err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
			if _, err = h.spool(a); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "a", "s", "launch-failure")
			if _, err = events.Open(dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "result-output.bin")
			target := path
			if tc.symlink {
				target = filepath.Join(t.TempDir(), "outside")
			}
			mode := os.FileMode(0600)
			if tc.public {
				mode = 0644
			}
			if err = os.WriteFile(target, []byte(tc.body), mode); err != nil {
				t.Fatal(err)
			}
			if tc.symlink {
				if err = os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err = h.recoverDuplicateGrant(grant, 2); err == nil {
				t.Fatal("untrusted diagnostic recovered as failed/exited")
			}
			got, err := h.Collect(context.Background(), a)
			if err != nil || len(got) != 0 {
				t.Fatalf("untrusted diagnostic published: %+v %v", got, err)
			}
			body, err := os.ReadFile(target)
			if err != nil || string(body) != tc.body {
				t.Fatal("untrusted artifact overwritten")
			}
		})
	}
}

func TestLaunchFailureRecoveryKeepsPartialTerminalUnknown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "host")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "c", ReservationID: "reservation", RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", WorkRevision: 1, GrantedActiveMS: 1000}
	if _, err = h.acceptGrant(grant); err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
	sp, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := events.Open(filepath.Join(root, "a", "s", "launch-failure"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"kind":"launch_failure","stage":"prelaunch","cause_code":"invocation_payload_invalid"}`)
	path, err := diagnostic.WriteArtifact(body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	exit := -1
	ref := &contract.ArtifactRef{ID: "launch-failure", Path: path, Size: int64(len(body)), SHA256: hash}
	if _, err = h.appendPhaseEvent(sp, "r", "t", a, launchMetadata{commandID: "c", workRevision: 1, executionEpoch: 1}, contract.EventFailed, hash, nil, &exit, ref); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewIPC(root, "host")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := restarted.acceptGrant(grant); err != nil || !duplicate {
		t.Fatalf("grant replay=%v %v", duplicate, err)
	}
	if err = restarted.recoverDuplicateGrant(grant, 2); err != nil {
		t.Fatal(err)
	}
	records, err := restarted.Collect(context.Background(), a)
	if err != nil || len(records) != 2 || records[0].Kind != contract.EventFailed || records[1].Kind != contract.EventUnknown {
		t.Fatalf("partial terminal misclassified: %+v %v", records, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(body) {
		t.Fatalf("original failure changed: %v", err)
	}
	var phase map[string]any
	if err = sp.ReadMetadata("launch.json", &phase); err != nil || phase["phase"] != "unknown" {
		t.Fatalf("missing reconcile metadata: %+v %v", phase, err)
	}
}
