//go:build !windows

package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hostbootstrap"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestOwnerPublicationDirectIPCReplayAndRestart(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "owner-publication-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	defer func() {
		if clean && !t.Failed() {
			_ = os.RemoveAll(root)
		} else {
			t.Log("retained fixture", root)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A real owned child supplies the original origin lifetime; no missing PID
	// or synthetic Wait observation is used to authorize the transition.
	child := exec.CommandContext(ctx, "/bin/cat")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = input.Close()
			_ = child.Wait()
		}
	}()
	oldBirth, err := process.Birth(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	var s *Server
	var served chan error
	var serveCancel context.CancelFunc
	open := func() {
		t.Helper()
		s, err = NewServer(state)
		if err != nil {
			t.Fatal(err)
		}
		var c context.Context
		c, serveCancel = context.WithCancel(ctx)
		served = make(chan error, 1)
		go func(server *Server) { served <- server.Serve(c) }(s)
	}
	closeServer := func() {
		t.Helper()
		if s == nil {
			return
		}
		serveCancel()
		_ = s.Close()
		select {
		case <-served:
		case <-time.After(3 * time.Second):
			t.Fatal("Coordinator did not join")
		}
		s = nil
	}
	open()
	defer closeServer()
	submitted := callServer(t, s.SocketPath(), ipc.KindSubmit, SubmitRequest{RunID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: child.Process.Pid, OriginBirth: oldBirth, HostGeneration: "old-generation", HostExecutable: "/private/bin/orchestrator", Tasks: []TaskRequest{{ID: "task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}})
	var receipt SubmitResponse
	decodePayload(t, submitted, &receipt)
	dir := filepath.Join(state, "host-bootstrap")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	bootstrap := filepath.Join(dir, receipt.LaunchID+".json")
	metadata := hostbootstrap.MetadataV2{Version: 2, SocketPath: s.SocketPath(), SpoolRoot: filepath.Join(state, "host-spool", receipt.LaunchID), ProducerID: receipt.LaunchID, Hello: hostbootstrap.HelloMetadata{LaunchID: receipt.LaunchID, OriginContextID: "origin", OriginPID: child.Process.Pid, OriginBirth: oldBirth, HostGeneration: "old-generation", Executable: "/private/bin/orchestrator"}}
	raw, _ := json.Marshal(metadata)
	raw = append(raw, '\n')
	if err = os.WriteFile(bootstrap, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_ = input.Close()
	err = child.Wait()
	waited = true
	if err != nil || child.ProcessState.ExitCode() != 0 {
		t.Fatal("original origin Wait", err)
	}
	t.Logf("original origin PID=%d Wait=0", child.Process.Pid)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	bound := callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "controller", OriginPID: os.Getpid(), OriginBirth: birth})
	var grant struct {
		OwnerCapability string `json:"owner_capability"`
		HostGeneration  string `json:"host_generation"`
	}
	decodePayload(t, bound, &grant)
	req := RebindOwnerRequest{RunID: "run", ControllerThread: "controller", ControlToken: receipt.ControlToken, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: grant.HostGeneration, OwnerMode: "local", OwnerCapability: grant.OwnerCapability}
	call := func(kind ipc.Kind) ipc.Envelope {
		t.Helper()
		response, e := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: kind, RequestID: "publication", Payload: jsonBytes(t, req)})
		if e != nil {
			t.Fatal(e)
		}
		return response
	}
	lease, err := hostbootstrap.Open(ctx, state, receipt.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	old := call(ipc.KindRebindOwner)
	lease.Close()
	if old.Kind != ipc.KindError || !strings.Contains(string(old.Payload), "owner_rebind_publication_protocol_required") || time.Since(start) > time.Second {
		t.Fatal("old RPC took lease or changed protocol", string(old.Payload))
	}
	// A structurally valid wrong prior must not be normalized into a new target.
	bad := bytes.Replace(raw, []byte("old-generation"), []byte("wrong-generation"), 1)
	if err = os.WriteFile(bootstrap, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if response := call(ipc.KindRebindOwnerPublished); response.Kind != ipc.KindError {
		t.Fatal("foreign prior accepted")
	}
	var count int
	if err = s.db.SQL().QueryRow(`SELECT COUNT(*) FROM owner_rebinds`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected prior mutated Store", count, err)
	}
	if err = os.WriteFile(bootstrap, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	failed := call(ipc.KindRebindOwnerPublished)
	if failed.Kind != ipc.KindError {
		t.Fatal("read-only directory unexpectedly published")
	}
	var phase string
	var scratch int64
	if err = s.db.SQL().QueryRow(`SELECT state,scratch_bytes FROM owner_rebind_publications`).Scan(&phase, &scratch); err != nil || phase != "pending" || scratch <= 0 {
		t.Fatal("missing frozen pending journal", phase, scratch, err)
	}
	if got, e := os.ReadFile(bootstrap); e != nil || !bytes.Equal(got, raw) {
		t.Fatal("failed stage changed prior", e)
	}
	closeServer()
	open()
	if err = s.db.SQL().QueryRow(`SELECT state FROM owner_rebind_publications`).Scan(&phase); err != nil || phase != "pending" {
		t.Fatal("restart finalized publication without repair", phase, err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Two independent direct IPC clients share the server-owned filesystem lease.
	requestRaw, _ := json.Marshal(req)
	responses := make([]ipc.Envelope, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i], errs[i] = ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindRebindOwnerPublished, RequestID: "direct-replay", Payload: requestRaw})
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || responses[0].Kind == ipc.KindError || responses[1].Kind == ipc.KindError || !bytes.Equal(responses[0].Payload, responses[1].Payload) {
		t.Fatal("direct replay failed", errs, string(responses[0].Payload), string(responses[1].Payload))
	}
	if err = s.db.SQL().QueryRow(`SELECT state,scratch_bytes FROM owner_rebind_publications`).Scan(&phase, &scratch); err != nil || phase != "published" || scratch != 0 {
		t.Fatal("not durably finalized", phase, scratch, err)
	}
	var expected []byte
	if err = s.db.SQL().QueryRow(`SELECT bootstrap_json FROM owner_rebind_publications`).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if got, e := os.ReadFile(bootstrap); e != nil || !bytes.Equal(got, expected) {
		t.Fatal("disk differs from immutable journal", e)
	}
	// Once published, replacing the named file by valid prior bytes must not yield
	// an ACK reconstructed merely from the in-memory target fields.
	if err = os.WriteFile(bootstrap, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if response := call(ipc.KindRebindOwnerPublished); response.Kind != ipc.KindError {
		t.Fatal("published replay lied about old bootstrap")
	}
	if got, e := os.ReadFile(bootstrap); e != nil || !bytes.Equal(got, raw) {
		t.Fatal("rejected replay changed filesystem", e)
	}
	if err = os.WriteFile(bootstrap, expected, 0600); err != nil {
		t.Fatal(err)
	}
	if response := call(ipc.KindRebindOwnerPublished); response.Kind == ipc.KindError {
		t.Fatal("exact published replay rejected", string(response.Payload))
	}
	closeServer()
	clean = true
}
