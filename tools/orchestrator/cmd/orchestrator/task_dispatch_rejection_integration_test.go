package main

import (
	"bytes"
	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/instance"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatchPublicCLIReplaysRejectedDecision(t *testing.T) {
	root := privateTaskTemp(t)
	state := filepath.Join(root, "state")
	if err := instance.PrepareStateDir(state); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, err := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var submits atomic.Int32
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				req, e := ipc.Read(conn)
				if e != nil {
					return
				}
				response := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: req.RequestID, Epoch: 1}
				switch req.Kind {
				case ipc.KindReady:
					response.Payload = json.RawMessage(`{"status":"ready","epoch":1,"capabilities":["interruption_feedback_v1","session_provider_lifecycle_v1"]}`)
				case ipc.KindOwnerBind:
					var owner coordinator.OwnerBindRequest
					if json.Unmarshal(req.Payload, &owner) != nil {
						return
					}
					grant := map[string]any{"version": 1, "kind": "local-owner", "controller_thread": owner.ControllerThread, "origin_pid": owner.OriginPID, "origin_birth": owner.OriginBirth, "origin_context_id": "fixture", "host_generation": "fixture"}
					path := filepath.Join(state, "owner.json")
					data, _ := json.Marshal(grant)
					if os.WriteFile(path, data, 0600) != nil {
						return
					}
					delete(grant, "kind")
					grant["owner_mode"] = "local"
					grant["owner_capability"] = path
					response.Payload, _ = json.Marshal(grant)
				case ipc.KindProviderControl:
					response.Payload = json.RawMessage(`{"provider":"agy","enabled":true,"status":"enabled"}`)
				case ipc.KindSubmit:
					submits.Add(1)
					response.Kind = ipc.KindError
					response.Payload = json.RawMessage(`{"error":"invalid_submit"}`)
				default:
					return
				}
				_ = ipc.Write(conn, response)
			}()
		}
	}()
	prepareControllerSkill(t, root)
	bin := deliveryTestBinary(t, filepath.Join(root, "bin", "orchestrator"))
	script := `#!/bin/sh
set -eu
case "${1-}" in
--version) echo 1.2.5;;
--help) echo '--input-format --output-format --mode --model --add-dir';;
--driver)
 for name in first second; do
   set +e
   "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/$name.json"
   result=$?
   set -e
   echo "$result" > "$BIRD_FIXTURE/$name.exit"
 done
 handle=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["handle"])' "$BIRD_FIXTURE/first.json")
 python3 - "$handle" <<'PYCHECK'
import json,sys
r=json.load(open(sys.argv[1])); assert r["status"]=="rejected" and r["error"]=="invalid_submit" and not r.get("control_file")
PYCHECK
 python3 - "$BIRD_FIXTURE/request.json" <<'PYREQ'
import json,sys
p=sys.argv[1]; r=json.load(open(p)); r["prompt"]="changed"; open(p,"w").write(json.dumps(r))
PYREQ
 if "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/conflict.json" 2>&1; then exit 92; fi
 python3 - "$BIRD_FIXTURE/request.json" "$handle" <<'PYLEGACY'
import json,sys
p=sys.argv[1]; r=json.load(open(p)); r["prompt"]="review"; open(p,"w").write(json.dumps(r))
p=sys.argv[2]; r=json.load(open(p)); r.pop("error",None); open(p,"w").write(json.dumps(r))
PYLEGACY
 if "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/legacy.json" 2>&1; then exit 93; fi
 python3 - "$handle" <<'PYUNKNOWN'
import json,sys
p=sys.argv[1]; r=json.load(open(p)); r["status"]="future-state"; r["control_file"]="original-control"; open(p,"w").write(json.dumps(r))
PYUNKNOWN
 if "$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state" > "$BIRD_FIXTURE/unknown.json" 2>&1; then exit 94; fi
 ;;
*) exit 91;;
esac
`
	cli := filepath.Join(root, "agy")
	if err = os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "provider-lock.json")
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: adapter.BinaryPin{Path: cli, Version: "1.2.5", SHA256: dispatchHash(script)}}
	if err = writeExclusiveJSON(lockPath, lock); err != nil {
		t.Fatal(err)
	}
	req := dispatchRequest{Version: 1, RequestID: "rejected", Provider: "agy", ProviderLock: lockPath, Directory: root, Prompt: "review", Role: adapter.Reviewer, Acceptance: []string{"evidence"}, MaxAttempts: 1, MaxActiveMS: 5000}
	if err = writeExclusiveJSON(filepath.Join(root, "request.json"), req); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, "controller", "start", "--provider", "agy", "--state-dir", state, "--", "--driver")
	cmd.Env = environmentWith(environmentWith(environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH")), "BIRD_FIXTURE", root), "ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("driver: %s %v", out, e)
	}
	for _, name := range []string{"first", "second"} {
		status, _ := os.ReadFile(filepath.Join(root, name+".exit"))
		body, _ := os.ReadFile(filepath.Join(root, name+".json"))
		if string(status) != "2\n" {
			t.Errorf("%s must fail: exit=%q body=%s", name, status, body)
		}
		var decision struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err = json.NewDecoder(bytes.NewReader(body)).Decode(&decision); err != nil || decision.Status != "rejected" || decision.Error != "invalid_submit" {
			t.Errorf("%s decision=%s decode=%v", name, body, err)
		}
	}
	for name, code := range map[string]string{"conflict": "dispatch_request_conflict", "legacy": "submit_rejected_error_unavailable", "unknown": "dispatch_checkpoint_invalid"} {
		body, _ := os.ReadFile(filepath.Join(root, name+".json"))
		if !strings.Contains(string(body), code) {
			t.Errorf("%s missing %s: %s", name, code, body)
		}
		if strings.Contains(string(body), `"status":"existing"`) {
			t.Errorf("%s returned success: %s", name, body)
		}
	}
	if submits.Load() != 1 {
		t.Fatalf("Submit count=%d", submits.Load())
	}
}
