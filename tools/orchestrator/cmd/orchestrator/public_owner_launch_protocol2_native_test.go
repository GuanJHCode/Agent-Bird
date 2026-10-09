package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const protocol2EnvironmentSentinel = "source-only-owner-launch-v2-sentinel"

// runProtocol2NativeFixture preserves the production CLI route for public
// operations and original-owner.  Only source-host installation is replaced so
// the test can retain the original FirstHost Cmd.Wait and kill that FirstHost
// after its signed Start acknowledgement but before it writes owner payload.
func runProtocol2NativeFixture() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "public-succession-origin":
		_ = os.Setenv("OWNER_LAUNCH_SOURCE_SENTINEL", protocol2EnvironmentSentinel)
		err = runPublicRebindOrigin()
	case "source-host":
		err = publicSuccessionOwnedHostLauncher(os.Args[2:])
	case "public-succession-owned-host":
		if trigger := os.Getenv("OWNER_LAUNCH_PROTOCOL2_PREBIND_TRIGGER"); trigger != "" {
			err = protocol2PreBindOwnedHost(os.Args[2:], trigger)
		} else {
			err = publicSuccessionOwnedHost(os.Args[2:])
		}
	case "original-owner":
		if os.Getenv("OWNER_LAUNCH_SOURCE_SENTINEL") != "" {
			err = errors.New("coordinator leaked source environment before bootstrap")
		} else {
			err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
		}
	default:
		err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, `{"version":1,"status":"error","error":%q}`+"\n", errorCode(err))
		os.Exit(2)
	}
}

func protocol2PreBindOwnedHost(args []string, trigger string) error {
	previous := ownerLaunchBeforePayload
	defer func() { ownerLaunchBeforePayload = previous }()
	ownerLaunchBeforePayload = func(_ context.Context, bootstrap contract.OwnerRuntimeBootstrap) error {
		if _, err := os.Stat(trigger); err != nil {
			return nil // only the first attempt may kill itself
		}
		marker := struct {
			OwnerID              string `json:"owner_id"`
			AttemptHash          string `json:"attempt_sha256"`
			Incarnation          uint64 `json:"incarnation"`
			CoordinatorPublicKey string `json:"coordinator_public_key"`
		}{bootstrap.Prepared.Intent.Scope.OwnerID, bootstrap.Prepared.LaunchAttempt.AttemptSHA256, bootstrap.Prepared.Intent.Scope.Incarnation, bootstrap.Prepared.CoordinatorPublicKey}
		raw, err := json.Marshal(marker)
		if err != nil {
			return err
		}
		if err = os.WriteFile(trigger+".marker", append(raw, '\n'), 0600); err != nil {
			return err
		}
		if err = os.Remove(trigger); err != nil {
			return err
		}
		return syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	if err := publicSuccessionOwnedHost(args); err != nil && !errors.Is(err, syscall.EINTR) {
		return err
	}
	return nil
}

func TestPublicOwnerLaunchProtocol2PreBindKill(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "owner-launch-v2-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	t.Cleanup(func() {
		if clean && !t.Failed() {
			_ = os.RemoveAll(root)
		} else {
			t.Log("retained protocol2 fixture", root)
		}
	})
	state := filepath.Join(root, "state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(image)
	cli, worker := filepath.Join(root, "protocol2-native-cli-fixture"), filepath.Join(root, "protocol2-native-worker-fixture")
	for _, path := range []string{cli, worker} {
		if err = os.WriteFile(path, image, 0700); err != nil {
			t.Fatal(err)
		}
	}
	trigger, marker, release := filepath.Join(root, "kill-first-host"), filepath.Join(root, "worker-marker"), filepath.Join(root, "worker-release")
	if err = os.WriteFile(trigger, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OWNER_LAUNCH_SOURCE_SENTINEL", "")
	t.Setenv("OWNER_LAUNCH_PROTOCOL2_PREBIND_TRIGGER", trigger)
	t.Setenv("ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	server, err := coordinator.NewCLIServer(state, false)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stopServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0600)
		stopServe()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if e := server.Shutdown(shutdown); e != nil {
			t.Error("Coordinator Shutdown", e)
		}
		if e := server.Close(); e != nil {
			t.Error("Coordinator Close", e)
		}
		select {
		case e := <-serveDone:
			if e != nil {
				t.Error("Coordinator Serve", e)
			}
		case <-shutdown.Done():
			t.Error("Coordinator Serve did not join")
		}
		if t.Failed() {
			for _, name := range []string{"source-host.log", "coordinator.log"} {
				if raw, e := os.ReadFile(filepath.Join(state, name)); e == nil {
					t.Logf("%s: %s", name, raw)
				}
			}
		}
	})
	origin := startPublicRebindOrigin(t, ctx, cli)
	birth, err := process.Birth(origin.command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) string {
		t.Helper()
		p := filepath.Join(root, name)
		raw, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(p, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	call := func(args ...string) []byte {
		t.Helper()
		reply := origin.call(t, args)
		if reply.ExitCode != 0 {
			t.Fatalf("public %s failed exit%d stderr=%s stdout=%s", args[0], reply.ExitCode, reply.Stderr, reply.Stdout)
		}
		return reply.Stdout
	}
	bindPath := write("owner-bind.json", coordinator.OwnerBindRequest{ControllerThread: "protocol2-controller", OriginPID: origin.command.Process.Pid, OriginBirth: birth})
	var local struct {
		OwnerCapability string `json:"owner_capability"`
		OriginContextID string `json:"origin_context_id"`
		HostGeneration  string `json:"host_generation"`
	}
	if err = json.Unmarshal(call("owner-bind", "--state-dir", state, "--request", bindPath), &local); err != nil || local.OwnerCapability == "" {
		t.Fatal("local owner bind", err)
	}
	request := map[string]any{"owner_mode": "local", "owner_capability": local.OwnerCapability, "run_id": "protocol2-run", "controller_thread": "protocol2-controller", "plan_revision": 1, "origin_context_id": local.OriginContextID, "origin_pid": origin.command.Process.Pid, "origin_birth": birth, "host_generation": local.HostGeneration, "delivery_mode": "collect", "tasks": []any{map[string]any{"id": "worker", "max_attempts": 1, "max_active_ms": 30000, "completion_policy": "owner_review", "execution_policy": contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 1, GroupMaxActiveMS: 60000, TaskMaxActiveMS: 60000, SegmentMaxActiveMS: 30000}, "adapter": map[string]any{"kind": "fake", "args": []string{worker, marker, release}, "directory": root}}}}
	submit := origin.call(t, []string{"submit", "--owner-launch-protocol", "2", "--state-dir", state, "--request", write("submit.json", request)})
	var submitted struct {
		Status      string `json:"status"`
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(submit.Stdout, &submitted); err != nil || submitted.ControlFile == "" || submitted.Status != "queued_host_unavailable" || submit.ExitCode != 2 {
		t.Fatalf("pre-Bind submit did not retain control: %+v stdout=%s stderr=%s err=%v", submit, submit.Stdout, submit.Stderr, err)
	}
	capability, err := loadControlCapability(submitted.ControlFile)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := loadHostBootstrap(capability)
	if err != nil || bootstrap.OwnerLaunchProtocol != 2 {
		t.Fatal("protocol2 bootstrap lost", err)
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var killed struct {
		OwnerID              string `json:"owner_id"`
		AttemptHash          string `json:"attempt_sha256"`
		Incarnation          uint64 `json:"incarnation"`
		CoordinatorPublicKey string `json:"coordinator_public_key"`
	}
	raw, err := os.ReadFile(trigger + ".marker")
	if err != nil || json.Unmarshal(raw, &killed) != nil || killed.Incarnation != 1 {
		t.Fatal("pre-payload SIGKILL marker absent", err)
	}
	pub, err := hex.DecodeString(killed.CoordinatorPublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatal("invalid fixture public key")
	}
	readAttempt := func(number int) contract.OriginalOwnerLaunchAttempt {
		t.Helper()
		var body string
		if e := db.QueryRow(`SELECT attempt_json FROM owner_launch_attempts WHERE owner_id=? AND attempt_no=?`, killed.OwnerID, number).Scan(&body); e != nil {
			t.Fatal(e)
		}
		var a contract.OriginalOwnerLaunchAttempt
		if json.Unmarshal([]byte(body), &a) != nil || a.Validate() != nil {
			t.Fatal("invalid attempt")
		}
		return a
	}
	waitReceipt := func(a contract.OriginalOwnerLaunchAttempt) contract.OriginalOwnerLaunchReceipt {
		t.Helper()
		for end := time.Now().Add(13 * time.Second); ; {
			var body string
			e := db.QueryRow(`SELECT receipt_json FROM owner_launch_receipts WHERE owner_id=? AND attempt_hash=? AND phase='waited'`, killed.OwnerID, a.AttemptSHA256).Scan(&body)
			if e == nil {
				var receipt contract.OriginalOwnerLaunchReceipt
				if json.Unmarshal([]byte(body), &receipt) != nil || contract.VerifyOriginalOwnerLaunchReceipt(receipt, ed25519.PublicKey(pub), a) != nil {
					t.Fatal("original Wait receipt invalid")
				}
				return receipt
			}
			if time.Now().After(end) {
				t.Fatal("original Coordinator-owned Wait receipt absent", e)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	first := readAttempt(1)
	firstExit := waitReceipt(first)
	if first.AttemptSHA256 != killed.AttemptHash || firstExit.Child == nil || firstExit.ExitCode == nil || *firstExit.ExitCode != 2 {
		t.Fatal("pre-bootstrap Owner did not exit through original Wait", firstExit)
	}
	hostWait := publicSuccessionReadWait(t, state, "first_host", first.EffectiveIntent.FirstHost.PID)
	if hostWait.ExitCode != -1 || hostWait.WaitError == "" {
		t.Fatal("FirstHost did not actually SIGKILL", hostWait)
	}
	var baseRaw, phase, taskStatus string
	var control, applied, attempts, segments, budgets int
	var ready, nextReady, maxActive int64
	if err = db.QueryRow(`SELECT o.intent_json,o.phase,o.control_revision,o.applied_control_revision,t.status,tr.ready_sequence,tr.max_active_ms,(SELECT next_ready_sequence FROM runtime_meta),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime) FROM execution_owners o JOIN tasks t ON t.run_id=o.run_id JOIN task_runtime tr ON tr.task_id=t.id WHERE o.owner_id=? AND t.id='worker'`, killed.OwnerID).Scan(&baseRaw, &phase, &control, &applied, &taskStatus, &ready, &maxActive, &nextReady, &attempts, &segments, &budgets); err != nil || phase != "intended" || control != 0 || applied != 0 || taskStatus != "ready" || ready <= 0 || maxActive != 30000 || attempts != 0 || segments != 0 || budgets != 0 {
		t.Fatal("pre-Bind launch changed business state", phase, control, applied, taskStatus, ready, attempts, segments, budgets, err)
	}
	if _, err = os.Stat(filepath.Join(bootstrap.SpoolRoot, ".coordinator-recovery.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unbound child created recovery ledger", err)
	}
	recovery := write("recover.json", map[string]any{"version": 1, "task_id": "worker", "work_revision": 1, "control_file": submitted.ControlFile, "owner_capability": local.OwnerCapability, "host_sha256": hex.EncodeToString(digest[:])})
	var recovered struct {
		Status string `json:"status"`
	}
	if err = json.Unmarshal(call("recover-host", "--state-dir", state, "--request", recovery), &recovered); err != nil || recovered.Status != "host_ready" {
		t.Fatal("public recovery failed", err)
	}
	second := readAttempt(2)
	var binding contract.OriginalOwnerBinding
	var bindingRaw string
	if err = db.QueryRow(`SELECT original_binding_json FROM execution_owners WHERE owner_id=? AND is_current=1 AND phase='bound'`, killed.OwnerID).Scan(&bindingRaw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(bindingRaw), &binding); err != nil {
		t.Fatal(err)
	}
	if second.BaseIntent != first.BaseIntent || second.EffectiveIntent.Scope != first.EffectiveIntent.Scope || second.EffectiveIntent.FirstHost == first.EffectiveIntent.FirstHost || binding.Scope != first.BaseIntent.Scope || binding.LaunchAttemptSHA256 != second.AttemptSHA256 || contract.VerifyOriginalOwnerBinding(binding, ed25519.PublicKey(pub)) != nil {
		t.Fatal("retry changed base/owner or lost launch binding")
	}
	workerIdentity := publicSuccessionWorker(t, marker)
	if workerIdentity.ParentPID != binding.Owner.PID {
		t.Fatal("worker parent is not exact signed Owner", workerIdentity.ParentPID, binding.Owner.PID)
	}
	chain, e := process.AncestryContext(ctx, workerIdentity.PID)
	if e != nil || chain[binding.Owner.PID] != binding.Owner.Birth || chain[second.Supervisor.PID] != second.Supervisor.Birth {
		t.Fatal("worker lost original Owner/Supervisor ancestry", e)
	}

	running := publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "running", "running")
	var baseAfter string
	var nextAfter, grant int64
	if err = db.QueryRow(`SELECT intent_json,(SELECT next_ready_sequence FROM runtime_meta),(SELECT granted_ms FROM budget_runtime WHERE segment_id=?),(SELECT COUNT(*) FROM attempts) FROM execution_owners WHERE owner_id=?`, running.SegmentID, killed.OwnerID).Scan(&baseAfter, &nextAfter, &grant, &attempts); err != nil || baseAfter != baseRaw || nextAfter != nextReady || grant != 30000 || attempts != 1 {
		t.Fatal("retry spent another queue slot or execution grant", err, nextAfter, grant, attempts)
	}
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	terminal := publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "result_ready", "exited")
	if terminal.AttemptID != running.AttemptID || terminal.SegmentID != running.SegmentID {
		t.Fatal("completion replaced execution")
	}
	publicSuccessionExit(t, bootstrap, terminal.AttemptID, terminal.SegmentID, workerIdentity, true)
	var collection coordinator.CollectResponse
	if err = json.Unmarshal(call("collect", "--state-dir", state, "--task-id", "worker", "--control-file", submitted.ControlFile), &collection); err != nil {
		t.Fatal(err)
	}
	var result contract.Event
	for _, event := range collection.Events {
		if event.Kind == contract.EventResult {
			if result.EventID != "" {
				t.Fatal("duplicate result")
			}
			result = event
		}
	}
	if result.EventID == "" || result.ActionSlot == "" {
		t.Fatal("no actionable collected result")
	}
	call("accept", "--state-dir", state, "--task-id", "worker", "--control-file", submitted.ControlFile, "--work-revision", "1", "--event-id", result.EventID, "--event-revision", strconv.FormatInt(result.EventRevision, 10), "--event-hash", result.PayloadHash, "--action-slot", result.ActionSlot, "--decision", "accept", "--command-id", "protocol2-accept")
	ack := write("ack.json", map[string]any{"version": 1, "task_id": "worker", "control_file": submitted.ControlFile, "delivery_id": collection.DeliveryID, "collection_proof_sha256": collection.CollectionProofSHA256, "decisions": []any{map[string]any{"event_id": result.EventID, "event_revision": result.EventRevision, "event_hash": result.PayloadHash, "action_slot": result.ActionSlot, "decision": "handled", "command_id": "protocol2-ack"}}})
	call("ack", "--state-dir", state, "--request", ack)
	publicSuccessionStatus(t, ctx, cli, state, submitted.ControlFile, "completed", "exited")
	secondExit := waitReceipt(second)
	if secondExit.ExitCode == nil || *secondExit.ExitCode != 0 || secondExit.Child == nil || secondExit.Child.PID != binding.Owner.PID {
		t.Fatal("successful original Wait missing", secondExit)
	}
	secondHostWait := publicSuccessionReadWait(t, state, "first_host", binding.FirstHost.PID)
	if secondHostWait.ExitCode != 0 || secondHostWait.WaitError != "" {
		t.Fatal("recovered FirstHost original Wait", secondHostWait)
	}
	for _, actor := range []contract.OriginalOwnerTransport{first.EffectiveIntent.FirstHost, *firstExit.Child, binding.FirstHost, *secondExit.Child} {
		if gone, e := process.IdentityExitedContext(ctx, actor.PID, actor.Birth); e != nil || !gone {
			t.Fatal("owned actor not reaped", actor, e)
		}
	}
	if os.Getenv("OWNER_LAUNCH_SOURCE_SENTINEL") != "" {
		t.Fatal("Coordinator received source environment")
	}
	if err = filepath.WalkDir(state, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if bytes.Contains(data, []byte(protocol2EnvironmentSentinel)) {
			return fmt.Errorf("source environment leaked to durable state: %s", entry.Name())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	origin.close(t)
	t.Logf("protocol2 pre-Bind original Wait recovery: same OwnerID/incarnation, ready=%d grant=%d; FirstHost SIGKILL, first Owner exit%d, second Owner/Host exit0", ready, grant, *firstExit.ExitCode)
	clean = true
}
