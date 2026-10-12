package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Only runs in the offline acceptance container, with its own private package
// and real coordinator. The queued fixture deliberately never starts a Host.
func TestLinuxPortableFollowupNative(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("AGENT_BIRD_LINUX_FOLLOWUP_NATIVE") != "1" {
		t.Skip("requires explicit offline Linux package acceptance")
	}
	if os.Geteuid() != 65534 {
		t.Fatal("requires unprivileged container")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.MkdirTemp("/tmp", "lf-")
	must(err)
	defer os.RemoveAll(root)
	plugin, state, dest := filepath.Join(root, "plugin"), filepath.Join(root, "state"), filepath.Join(root, "install")
	must(os.Mkdir(plugin, 0700))
	archive, err := os.Open("/input/package.tar")
	must(err)
	defer archive.Close()
	r := tar.NewReader(archive)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		must(err)
		if h.Typeflag != tar.TypeReg || filepath.IsAbs(h.Name) || filepath.Clean(h.Name) != h.Name || strings.HasPrefix(h.Name, "../") || (h.Mode != 0600 && h.Mode != 0700) {
			t.Fatal("invalid package fixture")
		}
		p := filepath.Join(plugin, h.Name)
		must(os.MkdirAll(filepath.Dir(p), 0700))
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode))
		must(err)
		_, err = io.Copy(f, r)
		must(err)
		must(f.Close())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	server, err := coordinator.NewServer(state)
	must(err)
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serveCtx) }()
	defer func() {
		stop()
		must(server.Close())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("coordinator not drained")
		}
	}()
	rpc := func(kind ipc.Kind, value any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(value)
		must(err)
		response, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: string(kind), Payload: raw})
		must(err)
		if response.Kind == ipc.KindError {
			t.Fatalf("%s: %s", kind, response.Payload)
		}
		return response.Payload
	}
	birth, err := process.Birth(os.Getpid())
	must(err)
	var request coordinator.SubmitRequest
	must(json.Unmarshal(rpc(ipc.KindOwnerBind, coordinator.OwnerBindRequest{ControllerThread: "owner", OriginPID: os.Getpid(), OriginBirth: birth}), &request))
	request.RunID, request.PlanRevision, request.DeliveryMode = "followup", 1, "collect"
	request.HostExecutable, err = os.Executable()
	must(err)
	request.Tasks = []coordinator.TaskRequest{{ID: "task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"kind":"fake","args":["/bin/true"],"directory":"/tmp"}`)}}
	var submitted coordinator.SubmitResponse
	must(json.Unmarshal(rpc(ipc.KindSubmit, request), &submitted))
	b := hostBootstrap{Version: 2, SocketPath: server.SocketPath(), SpoolRoot: filepath.Join(state, "host-spool"), ProducerID: submitted.LaunchID, Hello: contract.HostHello{LaunchID: submitted.LaunchID, LaunchToken: submitted.LaunchToken, OriginContextID: request.OriginContextID, OriginPID: request.OriginPID, OriginBirth: request.OriginBirth, HostGeneration: request.HostGeneration, Executable: request.HostExecutable}}
	bootstrap, err := writeBootstrap(state, b)
	must(err)
	control, err := writeControlCapability(state, request.RunID, request.ControllerThread, submitted.ControlToken, bootstrap, submitted.LaunchID, submitted.LaunchToken)
	must(err)
	handle := filepath.Join(root, "handle.json")
	must(writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: request.RunID, StateDir: state, ControlFile: control, TaskIDs: []string{"task"}}))
	calls := 0
	invoke := func(destination string, args ...string) ([]byte, error) {
		t.Helper()
		calls++
		full := append([]string{"plugin-run", "--plugin-root", plugin, "--install-root", destination, "--state-dir", state, "--"}, args...)
		body, err := exec.CommandContext(ctx, "/test/codex-orchestrator", full...).CombinedOutput()
		// Log only action/result, never the owner capability contents.
		t.Logf("call=%d action=%s output=%s", calls, args[0], body)
		return body, err
	}
	check, err := invoke(dest, "check")
	must(err)
	var installed struct {
		Binary string `json:"binary"`
	}
	must(json.Unmarshal(check, &installed))
	if !strings.HasPrefix(installed.Binary, dest+"/versions/") {
		t.Fatal("not installed")
	}
	before, err := os.Stat(installed.Binary)
	must(err)
	for _, action := range []string{"status", "summary", "collect", "wait-events"} {
		args := []string{action, "--state-dir", state, "--task-id", "task", "--control-file", control}
		if action == "wait-events" {
			args = append(args, "--timeout-ms", "1")
		}
		body, err := invoke(dest, args...)
		must(err)
		var observed map[string]json.RawMessage
		must(json.Unmarshal(body, &observed))
		if action == "status" && string(observed["status"]) != `"ready"` {
			t.Fatalf("unexpected task state: %s", body)
		}
		if action == "summary" && string(observed["run_id"]) != `"followup"` {
			t.Fatalf("wrong run: %s", body)
		}
		if action == "collect" || action == "wait-events" {
			var events []contract.Event
			must(json.Unmarshal(observed["events"], &events))
			if len(events) != 0 {
				t.Fatal("queued fixture has unexpected events")
			}
		}
	}
	for _, action := range []string{"inspect", "status", "collect", "wait"} {
		args := []string{"task", action, "--handle", handle}
		if action == "wait" {
			args = append(args, "--timeout-ms", "1")
		}
		_, err := invoke(dest, args...)
		must(err)
	}
	bad, err := loadControlCapability(control)
	must(err)
	bad.ControlToken = "wrong-owner-token"
	badFile := filepath.Join(root, "wrong-owner.json")
	must(writeExclusiveJSON(badFile, bad))
	if _, err := invoke(dest, "collect", "--state-dir", state, "--task-id", "task", "--control-file", badFile); err == nil {
		t.Fatal("wrong owner collected")
	}
	for _, action := range []string{"run", "submit", "resume", "answer", "ack", "rework"} {
		denied := filepath.Join(root, "denied-"+action)
		body, err := invoke(denied, "task", action, "--handle", handle)
		if err == nil || !strings.Contains(string(body), "portable_action_unsupported") {
			t.Fatal("mutation admitted", action, err)
		}
		if _, err := os.Lstat(denied); !os.IsNotExist(err) {
			t.Fatal("denied action installed runtime")
		}
	}
	after, err := os.Stat(installed.Binary)
	must(err)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("followup replaced installed binary")
	}
	t.Logf("native Linux installed followups=%d ownerUDS=true originalCLIWait=true queued task unchanged; no pending-result/Host/provider qualification", calls)
}
