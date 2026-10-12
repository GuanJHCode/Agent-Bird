package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestDiagnosticsPolicyPublicRequestAndPlanCommitment(t *testing.T) {
	legacy := TaskRequest{ID: "task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}
	baseline := ConfigTaskPlanDigest("run", 1, []TaskRequest{legacy})
	legacyBytes := `{"run_id":"run","plan_revision":1,"tasks":[{"id":"task","max_attempts":1,"adapter":{"kind":"fake"}}]}`
	wantLegacy := sha256.Sum256(append([]byte("agent-bird-resolved-plan-v1\x00"), []byte(legacyBytes)...))
	if baseline != hex.EncodeToString(wantLegacy[:]) {
		t.Fatal("omitted consent changed legacy plan bytes")
	}
	var task TaskRequest
	if err := json.Unmarshal([]byte(`{"id":"task","max_attempts":1,"adapter":{"kind":"fake"},"diagnostics":{"version":1,"mode":"detailed"}}`), &task); err != nil {
		t.Fatalf("explicit detailed policy is not accepted: %v", err)
	}
	detailed := ConfigTaskPlanDigest("run", 1, []TaskRequest{task})
	if detailed == baseline {
		t.Fatal("detailed consent missing from plan commitment")
	}
	var use install.ConfigGenerationTaskUse
	if err := json.Unmarshal([]byte(`{"TaskID":"task","PlanRevision":1,"MaxAttempts":1,"AdapterPayload":{"kind":"fake"},"Diagnostics":{"version":1,"mode":"detailed"}}`), &use); err != nil {
		t.Fatal(err)
	}
	if got := install.ConfigSubmissionPlanDigest("run", []install.ConfigGenerationTaskUse{use}); got != detailed {
		t.Fatalf("install receiver omitted diagnostics: %s / %s", got, detailed)
	}
	if err := json.Unmarshal([]byte(`{"id":"task","max_attempts":1,"adapter":{"kind":"fake"},"diagnostics":{"version":1,"mode":"summary"}}`), &task); err != nil {
		t.Fatal(err)
	}
	summary := ConfigTaskPlanDigest("run", 1, []TaskRequest{task})
	if summary == detailed || summary == baseline {
		t.Fatal("explicit policy identity collapsed")
	}
}

func TestDiagnosticsPolicyIPCSubmitPersistsConsent(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "diagnostics-submit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		cancel()
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server did not join")
		}
	}()
	pid, birth, exe := testHostIdentity(t)
	for _, mode := range []string{"omitted", "summary", "detailed"} {
		t.Run(mode, func(t *testing.T) {
			task := TaskRequest{ID: "task-" + mode, MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}
			if mode != "omitted" {
				task.Diagnostics = &contract.DiagnosticsPolicy{Version: 1, Mode: mode}
			}
			response := callServer(t, s.SocketPath(), ipc.KindSubmit, SubmitRequest{RunID: "run-" + mode, ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation-" + mode, HostExecutable: exe, Tasks: []TaskRequest{task}})
			var receipt SubmitResponse
			decodePayload(t, response, &receipt)
			if receipt.LaunchID == "" || receipt.ControlToken == "" {
				t.Fatal("submit did not commit", receipt)
			}
			var count int
			if err := s.db.SQL().QueryRow(`SELECT COUNT(*) FROM task_diagnostics_policies WHERE task_id=?`, task.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if mode == "omitted" {
				if count != 0 {
					t.Fatal("implicit consent created")
				}
				return
			}
			var body, hash string
			if err := s.db.SQL().QueryRow(`SELECT body_json,policy_sha256 FROM task_diagnostics_policies WHERE task_id=? AND run_id=?`, task.ID, "run-"+mode).Scan(&body, &hash); err != nil {
				t.Fatal(err)
			}
			want, _ := task.Diagnostics.CanonicalJSON()
			digest := sha256.Sum256(want)
			if count != 1 || body != string(want) || hash != hex.EncodeToString(digest[:]) {
				t.Fatal("IPC lost consent", count, body, hash)
			}
		})
	}
}

func TestDiagnosticsPolicyPublicRequestRejectsAmbiguousConsent(t *testing.T) {
	for _, raw := range []string{
		`{"id":"t","diagnostics":null}`,
		`{"id":"t","Diagnostics":{"version":1,"mode":"detailed"}}`,
		`{"id":"t","diagnostics":{"version":1,"mode":"detailed"},"diagnostics":{"version":1,"mode":"summary"}}`,
		`{"id":"t","diagnostics":{"Version":1,"mode":"detailed"}}`,
		`{"id":"t","diagnostics":{"version":1,"mode":"detailed","mode":"summary"}}`,
		`{"id":"t","diagnostics":{"version":2,"mode":"detailed"}}`,
	} {
		original := TaskRequest{ID: "untouched"}
		task := original
		if err := json.Unmarshal([]byte(raw), &task); err == nil || task.ID != original.ID {
			t.Fatalf("ambiguous consent accepted or mutated receiver: %s %+v %v", raw, task, err)
		}
	}
}
