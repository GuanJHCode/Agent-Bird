package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestManagedDispatchReusesRequestAndRejectsChangedContent(t *testing.T) {
	for _, checkpoint := range []string{"fresh", "prepared", "handle-before-control"} {
		t.Run(checkpoint, func(t *testing.T) { testManagedDispatchCheckpoint(t, checkpoint) })
	}
}

func TestManagedDispatchContinuesAfterWaitTimeoutToResultOrFailure(t *testing.T) {
	for _, outcome := range []string{"delayed-result", "delayed-failed", "delayed-incomplete"} {
		t.Run(outcome, func(t *testing.T) { testManagedDispatchCheckpoint(t, outcome) })
	}
}

func testManagedDispatchCheckpoint(t *testing.T, checkpoint string) {
	root, err := os.MkdirTemp("/tmp", "bird-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "bin", "orchestrator")
	prepareControllerSkill(t, root)
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	script := `#!/bin/sh
set -eu
case "${1-}" in
--version) printf '%s\n' '1.2.5';;
--help) printf '%s\n' '--input-format --output-format --mode --model';;
--driver)
 "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/first.json"
 handle=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["handle"])' "$BIRD_FIXTURE/first.json")
 "$AGENT_BIRD_COMMAND" task wait --handle "$handle" --timeout-ms 5000 > "$BIRD_FIXTURE/wait.json"
 "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/second.json"
 python3 -c 'import json,sys; p=sys.argv[1]; r=json.load(open(p)); r["prompt"]="different task"; open(p,"w").write(json.dumps(r))' "$BIRD_FIXTURE/request.json"
 if "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/conflict.json" 2>&1; then exit 89; fi
 ;;
*)
 printf x >> "$TMPDIR/launch-count"
 printf '%s\n' '{"event":"result","result":{"conversation_id":"fixture","status":"SUCCESS","response":"reviewed"}}'
 ;;
esac
`
	if strings.HasPrefix(checkpoint, "delayed-") {
		script = strings.Replace(script, ` "$AGENT_BIRD_COMMAND" task wait --handle "$handle" --timeout-ms 5000`, ` "$AGENT_BIRD_COMMAND" task wait --handle "$handle" --timeout-ms 50 > "$BIRD_FIXTURE/timeout.json"
 python3 - "$BIRD_FIXTURE" <<'PYWAIT'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
first = json.loads((root / "timeout.json").read_text())
assert first["status"] == "timeout" and first["events"] == []
(root / "release-worker").write_text("continue the same task")
PYWAIT
 "$AGENT_BIRD_COMMAND" task wait --handle "$handle" --timeout-ms 5000`, 1)
		script = strings.Replace(script, " printf x >>", " while [ ! -f \"$BIRD_FIXTURE/release-worker\" ]; do sleep 0.02; done\n printf x >>", 1)
		if checkpoint == "delayed-failed" {
			script = strings.Replace(script, `"status":"SUCCESS","response":"reviewed"`, `"status":"ERROR","response":"","error":"fixture rejected"`, 1)
		} else if checkpoint == "delayed-incomplete" {
			script = strings.Replace(script, `"response":"reviewed"`, `"response":"`+strings.Repeat("x", 70*1024)+`"`, 1)
		}
	}
	if checkpoint == "prepared" || checkpoint == "handle-before-control" {
		preparation := `
 "$AGENT_BIRD_COMMAND" provider disable --provider agy --state-dir "$BIRD_FIXTURE/state" > /dev/null
 if "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/prepared-error.json" 2>&1; then exit 88; fi
 python3 - "$BIRD_FIXTURE" '` + checkpoint + `' <<'PYFIXTURE'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
records = list((root / "state/dispatch").glob("*/*/reservation.json"))
assert len(records) == 1
record = json.loads(records[0].read_text())
assert record["phase"] == "prepared"
assert "provider_disabled" in (root / "prepared-error.json").read_text()
handle = records[0].parent / "handle.json"
assert not handle.exists()
if sys.argv[2] == "handle-before-control":
    handle.write_text(json.dumps({"version":1, "run_id":record["run_id"], "state_dir":str(root / "state"), "task_ids":[record["run_id"]+"-work"], "status":"submitting"}))
    handle.chmod(0o600)
PYFIXTURE
 "$AGENT_BIRD_COMMAND" provider enable --provider agy --binary "$BIRD_FIXTURE/agy" --provider-lock "$BIRD_FIXTURE/provider-lock.json" --state-dir "$BIRD_FIXTURE/state" > /dev/null
`
		script = strings.Replace(script, "--driver)\n", "--driver)\n"+preparation, 1)
	}
	cli := filepath.Join(root, "agy")
	if err = os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: adapter.BinaryPin{Path: cli, Version: "1.2.5", SHA256: dispatchHash(script)}}
	lockPath := filepath.Join(root, "provider-lock.json")
	if err = writeExclusiveJSON(lockPath, lock); err != nil {
		t.Fatal(err)
	}
	req := dispatchRequest{Version: 1, RequestID: "review-one", Provider: "agy", ProviderLock: lockPath, Directory: root, Prompt: "只读调查\nKeep `literal` and $HOME text", Role: adapter.Reviewer, Acceptance: []string{"report evidence"}, MaxAttempts: 1, MaxActiveMS: 5000}
	if err = writeExclusiveJSON(filepath.Join(root, "request.json"), req); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "controller", "start", "--provider", "agy", "--state-dir", state, "--", "--driver")
	cmd.Env = environmentWith(environmentWith(environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH")), "BIRD_FIXTURE", root), "ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	if out, err := cmd.CombinedOutput(); err != nil {
		body, _ := os.ReadFile(filepath.Join(root, "first.json"))
		t.Fatalf("driver: %s %s %v", out, body, err)
	}
	type continuation struct {
		DeliveryMode      string     `json:"delivery_mode"`
		AutomaticCallback bool       `json:"automatic_callback"`
		NextAction        string     `json:"next_action"`
		Commands          [][]string `json:"commands"`
		Instruction       string     `json:"instruction"`
	}
	var first, second struct {
		Handle       string        `json:"handle"`
		RunID        string        `json:"run_id"`
		Existing     bool          `json:"existing"`
		Continuation *continuation `json:"continuation"`
	}
	for name, target := range map[string]any{"first.json": &first, "second.json": &second} {
		b, e := os.ReadFile(filepath.Join(root, name))
		if e != nil || json.Unmarshal(b, target) != nil {
			t.Fatalf("bad %s: %s %v", name, b, e)
		}
	}
	if first.Handle == "" || first.Handle != second.Handle || first.RunID != second.RunID || !second.Existing {
		t.Fatalf("duplicate identity changed: %+v %+v", first, second)
	}
	for _, item := range []struct {
		action string
		value  *continuation
	}{{"wait", first.Continuation}, {"inspect", second.Continuation}} {
		if item.value == nil {
			t.Fatal("dispatch returned no owner continuation")
		}
		c := item.value
		if c.DeliveryMode != "collect" || c.AutomaticCallback || c.NextAction != item.action || len(c.Commands) != 1 || c.Instruction == "" {
			t.Fatalf("wrong collect-only continuation: %+v", c)
		}
		command := c.Commands[0]
		if len(command) < 4 || command[0] != "task" || command[1] != item.action || command[2] != "--handle" || command[3] != first.Handle {
			t.Fatalf("continuation lost original handle: %q", command)
		}
	}
	var wait struct {
		Status                string           `json:"status"`
		Events                []contract.Event `json:"events"`
		CollectionProofSHA256 string           `json:"collection_proof_sha256"`
	}
	body, _ := os.ReadFile(filepath.Join(root, "wait.json"))
	if json.Unmarshal(body, &wait) != nil || wait.Status != "events" || len(wait.Events) != 1 {
		t.Fatalf("lost result: %s", body)
	}
	wantKind, wantText := contract.EventResult, "reviewed"
	if checkpoint == "delayed-failed" {
		// Native ERROR without response text is a durable failed event, but
		// does not promise a report artifact or the provider's error wording.
		wantKind, wantText = contract.EventFailed, ""
	} else if checkpoint == "delayed-incomplete" {
		wantKind, wantText = contract.EventFailed, `{"status":"incomplete","reason":"provider_critical_event_too_large"}`
	}
	event := wait.Events[0]
	if event.Kind != wantKind || wait.CollectionProofSHA256 == "" {
		t.Fatalf("missing outcome/proof: %+v", wait)
	}
	if wantText == "" {
		if event.Artifact != nil || event.ExitCode == nil {
			t.Fatalf("unexpected failed event evidence: %+v", event)
		}
	} else {
		if event.Artifact == nil {
			t.Fatalf("missing outcome artifact: %+v", event)
		}
		artifact, err := os.ReadFile(event.Artifact.Path)
		if err != nil || string(artifact) != wantText || dispatchHash(string(artifact)) != event.Artifact.SHA256 || int64(len(artifact)) != event.Artifact.Size {
			t.Fatalf("owner did not receive the verified outcome: %q %v", artifact, err)
		}
	}
	body, _ = os.ReadFile(filepath.Join(root, "conflict.json"))
	if !strings.Contains(string(body), "dispatch_request_conflict") {
		t.Fatalf("changed request not rejected: %s", body)
	}
	launches := 0
	err = filepath.WalkDir(filepath.Join(state, "host-spool"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "launch-count" {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if string(b) != "x" {
				t.Fatalf("duplicate execution: %q", b)
			}
			launches++
		}
		return nil
	})
	if err != nil || launches != 1 {
		t.Fatalf("launches=%d err=%v", launches, err)
	}
}
