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

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

func TestManagedDispatchReusesRequestAndRejectsChangedContent(t *testing.T) {
	for _, checkpoint := range []string{"fresh", "prepared", "handle-before-control", "submit-uncertain"} {
		t.Run(checkpoint, func(t *testing.T) { testManagedDispatchCheckpoint(t, checkpoint) })
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
	bin = deliveryTestBinary(t, bin)
	script := `#!/bin/sh
set -eu
case "${1-}" in
--version) printf '%s\n' '1.2.5';;
--help) printf '%s\n' '--input-format --output-format --mode --model --add-dir';;
--driver)
 "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" routing status --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/routing.json"
 "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/first.json"
 handle=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["handle"])' "$BIRD_FIXTURE/first.json")
 "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task wait --handle "$handle" --timeout-ms 5000 > "$BIRD_FIXTURE/wait.json"
 python3 -c 'import json,sys; p=sys.argv[1]; r=json.load(open(p)); r["status"]="submitting"; open(p,"w").write(json.dumps(r))' "$handle"
 "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task reconcile --handle "$handle" > "$BIRD_FIXTURE/reconciled.json"
 "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/second.json"
 python3 -c 'import json,sys; p=sys.argv[1]; r=json.load(open(p)); r["prompt"]="different task"; open(p,"w").write(json.dumps(r))' "$BIRD_FIXTURE/request.json"
 if "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/conflict.json" 2>&1; then exit 89; fi
 ;;
*)
 if "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$TMPDIR/worker-recursion.json" 2>&1; then exit 90; fi
 printf x >> "$TMPDIR/launch-count"
 printf '%s\n' '{"event":"result","result":{"conversation_id":"fixture","status":"SUCCESS","response":"reviewed"}}'
 ;;
esac
`
	if checkpoint != "fresh" && checkpoint != "submit-uncertain" {
		preparation := `
 "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" provider disable --provider agy --state-dir "$BIRD_FIXTURE/state" > /dev/null
 if "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/prepared-error.json" 2>&1; then exit 88; fi
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
 "${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" provider enable --provider agy --binary "$BIRD_FIXTURE/agy" --provider-lock "$BIRD_FIXTURE/provider-lock.json" --state-dir "$BIRD_FIXTURE/state" > /dev/null
`
		script = strings.Replace(script, "--driver)\n", "--driver)\n"+preparation, 1)
	}
	if checkpoint == "submit-uncertain" {
		script = strings.Replace(script, `r["status"]="submitting"`, `r["status"]="submit_uncertain"`, 1)
		// Identical dispatch itself reconciles the genuine uncertain status;
		// never require callers to rewrite it to the pre-submit status.
		script = strings.Replace(script, `"${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task reconcile --handle "$handle"`, `"${BIRD_TEST_WRAPPER:-$AGENT_BIRD_COMMAND}" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state"`, 1)
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
	var first, second struct {
		Handle   string `json:"handle"`
		RunID    string `json:"run_id"`
		Existing bool   `json:"existing"`
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
	var wait struct {
		Status string            `json:"status"`
		Events []json.RawMessage `json:"events"`
	}
	body, _ := os.ReadFile(filepath.Join(root, "wait.json"))
	if json.Unmarshal(body, &wait) != nil || wait.Status != "events" || len(wait.Events) != 1 {
		t.Fatalf("lost result: %s", body)
	}
	body, _ = os.ReadFile(filepath.Join(root, "conflict.json"))
	if !strings.Contains(string(body), "dispatch_request_conflict") {
		t.Fatalf("changed request not rejected: %s", body)
	}
	var reconciled taskHandle
	if err = readPrivateJSON(first.Handle, &reconciled); err != nil || reconciled.Status != "reconciled" {
		t.Fatalf("uncertain receipt was not reconciled: %+v %v", reconciled, err)
	}
	launches := 0
	err = filepath.WalkDir(filepath.Join(state, "host-spool"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "worker-recursion.json" {
			body, err := os.ReadFile(path)
			var denied struct {
				Version int    `json:"version"`
				Status  string `json:"status"`
				Error   string `json:"error"`
			}
			// A sandboxed worker may fail strict owner observation before it
			// reaches the coordinator's independent worker ancestry guard.
			if err != nil || json.Unmarshal(body, &denied) != nil || denied.Version != 1 || denied.Status != "error" || (denied.Error != "worker_dispatch_forbidden" && denied.Error != "current_owner_unavailable") {
				t.Fatalf("worker recursion was not refused: %s %v", body, err)
			}
		}
		if d.Name() == "launch-count" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "worker-recursion.json")); err != nil {
				t.Fatalf("worker did not exercise recursion: %v", err)
			}
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
