package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type publicOriginReply struct {
	PID, ExitCode  int
	Stdout, Stderr []byte
	WaitError      string
}

// The old source is a real parent of each public CLI action. EOF retires it
// through its original Cmd.Wait, independently of the surviving Coordinator.
func runPublicRebindOrigin() error {
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var args []string
		if err := decoder.Decode(&args); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := exec.CommandContext(ctx, os.Args[0], args...)
		command.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
		var out, stderr bytes.Buffer
		command.Stdout, command.Stderr = &out, &stderr
		err := command.Run()
		cancel()
		if command.ProcessState == nil {
			return fmt.Errorf("controlled public CLI did not start: %w", err)
		}
		reply := publicOriginReply{PID: command.Process.Pid, ExitCode: command.ProcessState.ExitCode(), Stdout: out.Bytes(), Stderr: stderr.Bytes()}
		if err != nil {
			reply.WaitError = err.Error()
		}
		if err = encoder.Encode(reply); err != nil {
			return err
		}
	}
}

type publicRebindOrigin struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *os.File
	decoder *json.Decoder
	done    chan error
	closed  bool
}

func startPublicRebindOrigin(t *testing.T, ctx context.Context, cli string) *publicRebindOrigin {
	t.Helper()
	cmd := exec.CommandContext(ctx, cli, "public-succession-origin")
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o := &publicRebindOrigin{command: cmd, input: input, output: output.(*os.File), decoder: json.NewDecoder(output), done: make(chan error, 1)}
	go func() { o.done <- cmd.Wait() }()
	t.Cleanup(func() { o.close(t) })
	return o
}
func (o *publicRebindOrigin) call(t *testing.T, args []string) publicOriginReply {
	t.Helper()
	if err := json.NewEncoder(o.input).Encode(args); err != nil {
		t.Fatal(err)
	}
	if err := o.output.SetReadDeadline(time.Now().Add(35 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var reply publicOriginReply
	if err := o.decoder.Decode(&reply); err != nil {
		t.Fatal("controlled origin reply", err)
	}
	t.Logf("old-origin public CLI %s PID=%d original Wait=%d error=%s", args[0], reply.PID, reply.ExitCode, reply.WaitError)
	return reply
}
func (o *publicRebindOrigin) close(t *testing.T) {
	t.Helper()
	if o.closed {
		return
	}
	o.closed = true
	_ = o.input.Close()
	select {
	case err := <-o.done:
		if err != nil || o.command.ProcessState == nil || o.command.ProcessState.ExitCode() != 0 {
			t.Error("old origin original Wait", err)
		}
		t.Logf("old origin PID=%d original Wait=%v", o.command.Process.Pid, err)
	case <-time.After(5 * time.Second):
		_ = o.command.Process.Kill()
		<-o.done
		t.Error("forced old origin cleanup")
	}
}

func publicRebindSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var result []any
	for _, table := range []string{"runs", "host_launches", "runtime_hosts", "owner_rebinds", "owner_succession_rebind_proofs", "owner_succession_authorizations", "owner_initial_recoveries", "execution_owners", "tasks", "task_runtime", "runtime_meta", "attempts", "segments", "budget_runtime", "storage_usage"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1,2")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		result = append(result, table)
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result = append(result, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func exercisePublicPendingAuthorizationRebind(t *testing.T, ctx context.Context, cli, state, control string, db *sql.DB, origin *publicRebindOrigin, pause string, args []string) []byte {
	t.Helper()
	if err := os.WriteFile(pause, nil, 0600); err != nil {
		t.Fatal(err)
	}
	reply := origin.call(t, args)
	if reply.ExitCode != 2 || len(reply.Stdout) != 0 {
		t.Fatal("pending authorization did not stop before Prepare", reply.ExitCode, string(reply.Stdout), string(reply.Stderr))
	}
	var stopped contract.OriginalOwnerTransport
	body, err := os.ReadFile(pause + ".finished")
	if err != nil || json.Unmarshal(body, &stopped) != nil || stopped.Validate() != nil {
		t.Fatal("missing real pre-Prepare FirstHost", err)
	}
	wait := publicSuccessionReadWait(t, state, "first_host", stopped.PID)
	if wait.ExitCode != 0 || wait.WaitError != "" {
		t.Fatal("pre-Prepare FirstHost original Wait", wait)
	}
	if status, e := process.ObserveIdentityContext(ctx, stopped.PID, stopped.Birth); e != nil || status != process.IdentityExited {
		t.Fatal("pre-Prepare FirstHost not reaped", status, e)
	}
	var owners, authorizations, attempts, current int
	if err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM execution_owners),(SELECT COUNT(*) FROM owner_succession_authorizations),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM execution_owners WHERE is_current=1)`).Scan(&owners, &authorizations, &attempts, &current); err != nil || owners != 1 || authorizations != 1 || attempts != 0 || current != 0 {
		t.Fatal("authorization was consumed before rebind", owners, authorizations, attempts, current, err)
	}
	capability, err := loadControlCapability(control)
	if err != nil {
		t.Fatal(err)
	}
	response, err := call(ctx, state, ipc.KindResume, coordinator.ResumeRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: "worker", ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, WorkRevision: 1}, CommandID: args[len(args)-1]})
	if err != nil {
		t.Fatal("original authenticated receipt replay", err)
	}
	originalReceipt := append(response.Payload, '\n')
	oldBirth, err := process.Birth(origin.command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	origin.close(t)
	if status, e := process.ObserveIdentityContext(ctx, origin.command.Process.Pid, oldBirth); e != nil || status != process.IdentityExited {
		t.Fatal("old origin not affirmatively exited", status, e)
	}
	newBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(filepath.Dir(pause), "replacement-owner.json")
	if err = writeExclusiveJSON(request, map[string]any{"controller_thread": capability.ControllerThread, "origin_pid": os.Getpid(), "origin_birth": newBirth}); err != nil {
		t.Fatal(err)
	}
	var grant struct {
		OwnerCapability string `json:"owner_capability"`
		HostGeneration  string `json:"host_generation"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, cli, "owner-bind", "--state-dir", state, "--request", request), &grant); err != nil || grant.OwnerCapability == "" {
		t.Fatal("new public owner grant", err)
	}
	rebind := filepath.Join(filepath.Dir(pause), "rebind.json")
	if err = writeExclusiveJSON(rebind, map[string]any{"version": 1, "owner_mode": "local", "owner_capability": grant.OwnerCapability, "control_file": control}); err != nil {
		t.Fatal(err)
	}
	bootstrapBefore, err := os.ReadFile(capability.BootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	// Real filesystem failure after the RPC commits: bootstrap remains readable
	// while its private directory rejects the replacement file creation.
	dir := filepath.Dir(capability.BootstrapPath)
	if err = os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	failed := exec.CommandContext(ctx, cli, "rebind-owner", "--state-dir", state, "--request", rebind)
	failed.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	output, writeErr := failed.CombinedOutput()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if writeErr == nil || failed.ProcessState == nil || failed.ProcessState.ExitCode() != 2 {
		t.Fatal("bootstrap replacement fault did not fail", writeErr, string(output))
	}
	t.Logf("first rebind original Wait=%v; bootstrap replacement stderr=%s", writeErr, output)
	var pid, proofs int
	var generation string
	if err = db.QueryRow(`SELECT r.origin_pid,l.host_generation,(SELECT COUNT(*) FROM owner_succession_rebind_proofs) FROM runs r JOIN tasks t ON t.run_id=r.id JOIN task_runtime tr ON tr.task_id=t.id JOIN host_launches l ON l.id=tr.host_launch_id WHERE t.id='worker'`).Scan(&pid, &generation, &proofs); err != nil || pid != os.Getpid() || generation != grant.HostGeneration || proofs != 1 {
		t.Fatal("DB did not commit before bootstrap failure", pid, generation, proofs, err)
	}
	if content, e := os.ReadFile(capability.BootstrapPath); e != nil || !bytes.Equal(content, bootstrapBefore) {
		t.Fatal("failed bootstrap write changed old bytes", e)
	}
	snapshot := publicRebindSnapshot(t, db)
	runBinary(t, ctx, cli, "rebind-owner", "--state-dir", state, "--request", rebind)
	if snapshot != publicRebindSnapshot(t, db) {
		t.Fatal("same live origin replay changed DB or quota")
	}
	rebound, err := loadHostBootstrap(capability)
	if err != nil || rebound.Hello.OriginPID != os.Getpid() || rebound.Hello.OriginBirth != newBirth || rebound.Hello.HostGeneration != grant.HostGeneration {
		t.Fatal("lost bootstrap publication was not repaired", err)
	}
	if err = os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	body = runBinary(t, ctx, cli, args...)
	if !bytes.Equal(body, originalReceipt) {
		t.Fatal("rebind changed immutable public receipt", string(body), string(originalReceipt))
	}
	next := publicSuccessionBinding(t, db)
	if next.Scope.OriginalOriginPID != os.Getpid() || next.Scope.OriginalOriginBirth != newBirth || next.Scope.HostGeneration != grant.HostGeneration {
		t.Fatal("successor lost derived origin", next.Scope)
	}
	return body
}
