//go:build darwin

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Separate ROOT-only worker-creation actor. Original 4239 preinserted-link
// actor and its FAIL remain unchanged. This actor launches only a synthetic
// helper through the real builder; it does not execute Codex/M3/Host turns.
const codexSandboxLinkOptIn = "root-reviewed-c06-sandbox-link-api-v1"

func codexSandboxLinkValidatePlan(plan codexSandboxPlan) error {
	copyPlan := plan
	copyPlan.Operations = append([]codexSandboxOperation(nil), plan.Operations...)
	for i, op := range copyPlan.Operations {
		switch op.Kind {
		case "link", "symlink":
			copyPlan.Operations[i].Kind = "rename" // Same strict two-path/name shape.
		case "absent", "singlelink", "metadata", "list", "canonicalize":
			copyPlan.Operations[i].Kind = "read" // Same strict one-path/name shape.
		}
	}
	return codexSandboxValidatePlan(copyPlan)
}

func codexSandboxLinkOperation(op codexSandboxOperation) error {
	switch op.Kind {
	case "metadata":
		_, err := os.Stat(op.Path)
		return err
	case "list":
		f, err := os.Open(op.Path)
		if err != nil {
			return err
		}
		_, err = f.Readdirnames(1) // Bounded data/list operation, not just Stat.
		if err == io.EOF {
			err = nil
		}
		return errors.Join(err, f.Close())
	case "canonicalize":
		path, err := filepath.EvalSymlinks(op.Path)
		if err == nil && path != op.Path {
			err = errCodexStateFile
		}
		return err
	case "link":
		return os.Link(op.Path, op.Other)
	case "symlink":
		return os.Symlink(op.Path, op.Other)
	case "absent":
		_, err := os.Lstat(op.Path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errCodexStateFile
	case "singlelink":
		info, err := os.Lstat(op.Path)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || st.Nlink != 1 {
			return errCodexStateFile
		}
		f, err := os.Open(op.Path)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(f, 4097))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(body, []byte("SYNTHETIC_ATOMIC_SAVE\n")) {
			return errCodexStateFile
		}
		return nil
	default:
		return codexSandboxFileOperation(op)
	}
}

func codexSandboxLinkRun(ctx context.Context, plan codexSandboxPlan) codexSandboxReport {
	r := codexSandboxReport{Version: 1, Planned: len(plan.Operations), Observations: []codexSandboxObservation{}}
	if codexSandboxLinkValidatePlan(plan) != nil {
		r.Incomplete = true
		return r
	}
	for _, op := range plan.Operations {
		err := ctx.Err()
		if err == nil {
			err = codexSandboxLinkOperation(op)
		}
		matched := codexSandboxMatched(op.Expect, err)
		r.Observations = append(r.Observations, codexSandboxObservation{Name: op.Name, ErrorClass: codexSandboxErrorClass(err), Matched: matched})
		if !matched {
			r.Incomplete = len(r.Observations) < r.Planned
			break
		}
	}
	return r
}

func codexSandboxLinkProtocol(ctx context.Context, input io.Reader, output io.Writer) error {
	var body json.RawMessage
	if json.NewDecoder(io.LimitReader(input, codexSandboxWireLimit+1)).Decode(&body) != nil {
		return errCodexStateFile
	}
	var plan codexSandboxPlan
	if codexSandboxDecode(body, &plan) != nil || codexSandboxLinkValidatePlan(plan) != nil {
		return errCodexStateFile
	}
	encoded, err := json.Marshal(codexSandboxLinkRun(ctx, plan))
	if err != nil || len(encoded) > codexSandboxWireLimit {
		return errCodexStateFile
	}
	encoded = append(encoded, '\n')
	n, err := output.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	return err
}

func TestManagedCodexSandboxLinkAPIHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-1] != "codex-sandbox-link-api" {
		t.Skip("explicit owned helper argv only")
	}
	if os.Getenv("C06_SANDBOX_LINK_OPT_IN") != codexSandboxLinkOptIn {
		os.Exit(91)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	watchdog := time.AfterFunc(20*time.Second, func() { os.Exit(93) })
	defer watchdog.Stop()
	if codexSandboxLinkProtocol(ctx, os.Stdin, os.Stdout) != nil {
		os.Exit(92)
	}
	os.Exit(0)
}

func codexSandboxLinkCommand(ctx context.Context, cmd process.Command, p codexPreparedState, helper, digest string) (process.Command, error) {
	if len(cmd.Args) < 2 || strings.Count(cmd.Args[1], "(deny file-link)") != 1 {
		return process.Command{}, errCodexStateFile
	}
	out, err := codexSandboxCommand(ctx, cmd, p, helper, digest)
	if err != nil {
		return process.Command{}, err
	}
	out.Args[3] = "-test.run=^TestManagedCodexSandboxLinkAPIHelperProcess$"
	out.Args[5] = "codex-sandbox-link-api"
	return out, nil
}

func codexSandboxLinkBuildPlan(base string, p codexPreparedState, spool, workspace, source string) (codexSandboxPlan, []string, error) {
	plan := codexSandboxPlan{Version: 1, Base: base, Operations: []codexSandboxOperation{}}
	retained := []string{source, workspace, spool, p.Protected.Path, p.Layout.State.Path, p.Layout.Home.Path}
	add := func(name, kind, path, other, expect string) {
		plan.Operations = append(plan.Operations, codexSandboxOperation{name, kind, path, other, expect})
	}
	write := func(path string) error { return codexActorWriteFile(path, []byte("SYNTHETIC_EXISTING\n"), os.O_EXCL) }
	runtime := []struct {
		name string
		root codexStateRoot
	}{{"sessions", p.Layout.Sessions}, {"skills", p.Layout.Auxiliary.Runtime[0]}, {"home_tmp", p.Layout.Auxiliary.Runtime[1]}, {"locks", p.Layout.Auxiliary.Runtime[2]}, {"sqlite", p.Layout.SQLite}, {"log", p.Layout.Auxiliary.Runtime[3]}, {"tmp", p.Layout.Auxiliary.Runtime[4]}}
	for _, v := range runtime {
		retained = append(retained, v.root.Path)
		for _, kind := range []string{"chmod", "remove", "rename"} {
			other := ""
			if kind == "rename" {
				other = v.root.Path + "-moved"
			}
			add("root_"+v.name+"_"+kind, kind, v.root.Path, other, "permission")
		}
		path := filepath.Join(v.root.Path, "file-api-probe")
		add(v.name+"_create", "create", path, "", "success")
		add(v.name+"_read", "read", path, "", "success")
		add(v.name+"_save", "replace", path, path+"-save", "success")
	}
	for i, family := range codexSQLiteFamilies {
		for j, suffix := range []string{"", "-wal", "-shm"} {
			path := filepath.Join(p.Layout.SQLite.Path, family+".sqlite"+suffix)
			name := fmt.Sprintf("sqlite_%d_%d", i, j)
			add(name+"_create", "create", path, "", "success")
			add(name+"_read", "read", path, "", "success")
			add(name+"_save", "replace", path, path+"-save", "success")
		}
	}
	rollout := codexActorRollout(p.Layout.Home.Path)
	add("rollout_mkdir", "mkdir", filepath.Dir(rollout), "", "success")
	add("rollout_create", "create", rollout, "", "success")
	add("rollout_read", "read", rollout, "", "success")
	add("rollout_save", "replace", rollout, rollout+"-save", "success")
	installation := filepath.Join(p.Layout.Home.Path, "installation_id")
	add("installation_remove", "remove", installation, "", "success")
	add("installation_create", "create", installation, "", "success")
	add("installation_read", "read", installation, "", "success")
	// Re-save the literal using an authorized tmp source, with no wildcard
	// permission granted for installation_id.pending in the surrounding home.
	add("installation_save", "replace", installation, filepath.Join(p.Layout.Auxiliary.Runtime[1].Path, "installation-save"), "success")
	scratch := p.Layout.Auxiliary.Runtime[4].Path
	targets := []struct{ name, path, read string }{
		{"workspace", filepath.Join(workspace, "input.txt"), "success"},
		{"source_config", filepath.Join(source, "config.toml"), "success"},
		{"source_auth", filepath.Join(source, "auth.json"), "success"},
		{"owned_config", filepath.Join(p.Layout.Home.Path, "config.toml"), "success"},
		{"owned_auth", filepath.Join(p.Layout.Home.Path, "auth.json"), "success"},
		{"origin", filepath.Join(p.Protected.Path, "codex-origin.json"), "permission"},
		{"thread", filepath.Join(p.Protected.Path, "codex-thread.json"), "permission"},
		{"seal", filepath.Join(p.Protected.Path, "codex-session-seal.json"), "permission"},
		{"control", filepath.Join(spool, "control-probe"), "permission"},
	}
	for _, v := range targets {
		if v.name == "workspace" || v.name == "thread" || v.name == "seal" || v.name == "control" {
			if err := write(v.path); err != nil {
				return plan, nil, err
			}
		}
		retained = append(retained, v.path)
		add(v.name+"_read", "read", v.path, "", v.read)
		for _, kind := range []string{"overwrite", "remove", "replace"} {
			other := ""
			if kind == "replace" {
				other = filepath.Join(scratch, v.name+"-replace")
			}
			add(v.name+"_"+kind, kind, v.path, other, "permission")
		}
	}
	for _, v := range []struct{ name, path string }{{"workspace", workspace}, {"source", source}, {"owned_home", p.Layout.Home.Path}, {"state", p.Layout.State.Path}, {"protected", p.Protected.Path}, {"spool", spool}} {
		add(v.name+"_create", "create", filepath.Join(v.path, "forbidden-created"), "", "permission")
		add(v.name+"_root_chmod", "chmod", v.path, "", "permission")
		add(v.name+"_root_rename", "rename", v.path, v.path+"-moved", "permission")
		add(v.name+"_root_remove", "remove", v.path, "", "permission")
	}
	// No runtime aliases are installed by the parent. Every Link/Symlink below
	// is an actual child syscall after the initial empty inventory is verified.
	for _, v := range []struct{ name, path string }{{"source", source}, {"protected", spool}} {
		target := filepath.Join(v.path, "alias-probe")
		if err := write(target); err != nil {
			return plan, nil, err
		}
		retained = append(retained, target)
		for _, root := range runtime {
			hard := filepath.Join(root.root.Path, v.name+"-hardlink")
			add(v.name+"_link_"+root.name, "link", target, hard, "permission")
			add(v.name+"_absent_"+root.name, "absent", hard, "", "success")
		}
		alias := filepath.Join(p.Layout.Auxiliary.Runtime[1].Path, v.name+"-symlink")
		add(v.name+"_symlink_create", "symlink", target, alias, "success")
		read := "success"
		if v.name == "protected" {
			read = "permission"
		}
		add(v.name+"_symlink_read", "read", alias, "", read)
		add(v.name+"_symlink_write", "overwrite", alias, "", "permission")
	}
	for _, root := range runtime {
		original := filepath.Join(root.root.Path, "file-api-probe")
		hard := filepath.Join(scratch, "runtime-"+root.name+"-hardlink")
		add("runtime_link_"+root.name, "link", original, hard, "permission")
		add("runtime_absent_"+root.name, "absent", hard, "", "success")
		add("runtime_unchanged_"+root.name, "singlelink", original, "", "success")
	}
	return plan, retained, codexSandboxLinkValidatePlan(plan)
}

func TestManagedCodexSandboxLinkAPIActor(t *testing.T) {
	codexSandboxLinkActor(t, false)
}

func codexSandboxLinkActor(t *testing.T, canonicalization bool) {
	if os.Getenv("C06_SANDBOX_LINK_OPT_IN") != codexSandboxLinkOptIn || os.Getenv("C06_SANDBOX_LINK_EVIDENCE_DIR") == "" {
		t.Skip("ROOT reviewed double opt-in kernel file API actor")
	}
	evidence := os.Getenv("C06_SANDBOX_LINK_EVIDENCE_DIR")
	root, err := codexAnchorStateRoot(context.Background(), evidence)
	if err != nil || len(root.Parents) == 0 || root.Parents[len(root.Parents)-1].Mode != 0700 {
		t.Fatal("fresh canonical owned evidence required")
	}
	base := filepath.Join(evidence, "link-api")
	if os.Mkdir(base, 0700) != nil {
		t.Fatal("fresh no-reuse case required")
	}
	parent, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var handle *process.Handle
	var stream io.ReadWriteCloser
	joined := true
	phase := "preparation"
	var observations codexSandboxReport
	var plan codexSandboxPlan
	initialVerified := false
	var before map[string]codexSandboxFingerprint
	var preserved []string
	fdBefore, fdErr := os.ReadDir("/dev/fd")
	// Before Start and any subsequent assertion: keep h+err, all pipe jobs and
	// original startup cleanup under the same parent, never reconstruct a PID.
	defer func() {
		if stream != nil {
			_ = stream.Close()
		}
		waited, absent := false, false
		pid, pgid, exit := 0, 0, -1
		var cleanupClass = "none"
		var startup = false
		if handle != nil {
			id := handle.Identity()
			pid, pgid = id.PID, id.PGID
			if view, ok := handle.DuplexLaunchState(); ok && view.StartupCleanup != nil {
				startup = true
				cleanupClass = codexSandboxErrorClass(view.StartupCleanup.Result)
			} else if handle.ConfirmTreeExited() != nil {
				deadline, _ := parent.Deadline()
				stopDeadline := time.Now().Add(10 * time.Second)
				if limit := deadline.Add(-time.Second); limit.Before(stopDeadline) {
					stopDeadline = limit
				}
				stopCtx, stopCancel := context.WithDeadline(parent, stopDeadline)
				cleanupClass = codexSandboxErrorClass(handle.Stop(stopCtx))
				stopCancel()
			}
			waited = handle.Wait(parent) == nil
			absent = handle.ConfirmTreeExited() == nil
			exit = handle.ExitCode()
		}
		targetsAbsent, targetCount := true, 0
		for _, op := range plan.Operations {
			if op.Kind == "link" {
				targetCount++
				if _, e := os.Lstat(op.Other); !errors.Is(e, os.ErrNotExist) {
					targetsAbsent = false
				}
			}
		}
		after, afterErr := codexSandboxSnapshot(preserved)
		unchanged := before != nil && afterErr == nil && reflect.DeepEqual(before, after)
		fdAfter, fdAfterErr := os.ReadDir("/dev/fd")
		body, _ := json.MarshalIndent(map[string]any{"version": 1, "builder_kernel_file_api_only": true, "phase": phase, "failed": t.Failed(), "public_entry": false, "execute_launch": false, "provider_executed": false, "model_turns": 0, "original_handle": handle != nil, "pid": pid, "pgid": pgid, "original_wait": waited, "group_absent": absent, "exit_code": exit, "startup_cleanup_present": startup, "cleanup_class": cleanupClass, "pipe_jobs_joined": joined, "preserved_unchanged": unchanged, "snapshot_before": before, "snapshot_after": after, "file_api": observations, "initial_inventory_verified": initialVerified, "parent_inserted_hardlinks": false, "hardlink_targets_absent": targetsAbsent, "hardlink_target_count": targetCount, "fd_observed": fdErr == nil && fdAfterErr == nil, "fd_before": len(fdBefore), "fd_after": len(fdAfter), "fd_verdict": "observation_only_unisolated_go_runtime"}, "", "  ")
		if codexActorWriteFile(filepath.Join(base, "hardlink-evidence.json"), append(body, '\n'), os.O_EXCL) != nil {
			t.Error("durable original evidence failed")
		}
		if handle != nil && (!waited || !absent) || !joined {
			t.Error("original worker/pipe ownership unproved; halt")
		}
		if !targetsAbsent {
			t.Error("denied link left an actual target")
		}
		if before != nil && !unchanged {
			t.Error("synthetic protected/source/workspace bytes or identity changed")
		}
	}()
	source, workspace, spool := filepath.Join(base, "source"), filepath.Join(base, "workspace"), filepath.Join(base, "spool")
	for _, path := range []string{source, workspace, spool} {
		if os.Mkdir(path, 0700) != nil {
			t.Fatal("fresh fixture setup")
		}
	}
	if codexActorWriteFile(filepath.Join(source, "config.toml"), []byte("model=\"gpt-fixed\"\n"), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(source, "auth.json"), []byte("SYNTHETIC_AUTH_ONLY"), os.O_EXCL) != nil {
		t.Fatal("synthetic source")
	}
	h, err := NewIPC(spool, "sandbox-producer")
	if err != nil || h.BindContinuationOwner("sandbox-owner") != nil {
		t.Fatal("owned Host")
	}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 60000}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: adapter.BinaryPin{Path: codexActorProviderPath, SHA256: adapter.CodexSHA256, Version: adapter.CodexVersion}}
	payload, _ := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderCodex), Directory: workspace, Prompt: "synthetic file API", Profile: &profile, ProviderLock: &lock})
	g := contract.LaunchCommand{RunID: "sandbox-run", TaskID: "sandbox-task", AttemptID: "sandbox-attempt", SegmentID: "segment-1", WorkRevision: 1, PlanRevision: 1, CommandID: "command-1", HostID: "sandbox-host", ExecutionEpoch: 1, AdapterPayload: payload}
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: lock.Binary, Lock: &lock, Profile: &profile, CWD: workspace, Prompt: "synthetic file API"})
	if err != nil {
		t.Fatal("private invocation")
	}
	if _, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}); err != nil {
		t.Fatal("original spool")
	}
	env := []string{"HOME=" + base, "CODEX_HOME=" + source, "C06_SANDBOX_LINK_OPT_IN=" + codexSandboxLinkOptIn}
	prepared, err := h.prepareManagedCodexState(parent, g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal("actual constructor")
	}
	cmd, prepared, err := codexManagedCommand(parent, prepared, env, spool)
	if err != nil {
		t.Fatal("actual trusted builder")
	}
	helper, err := os.Executable()
	if err == nil {
		helper, err = filepath.EvalSymlinks(helper)
	}
	if err != nil {
		t.Fatal("actual helper")
	}
	digest, err := process.ExecutableDigest(parent, helper)
	if err != nil {
		t.Fatal("helper hash")
	}
	cmd, err = codexSandboxLinkCommand(parent, cmd, prepared, helper, digest)
	if err != nil {
		t.Fatal("helper exact tail")
	}
	var paths []string
	plan, paths, err = codexSandboxLinkBuildPlan(base, prepared, spool, workspace, source)
	if err == nil && canonicalization {
		plan, paths, err = codexSandboxCanonicalizationExtendPlan(plan, paths, prepared, spool)
	}
	if err != nil {
		t.Fatal("explicit bounded operations")
	}
	preserved = paths
	before, err = codexSandboxSnapshot(paths)
	if err != nil {
		t.Fatal("prelaunch synthetic snapshot")
	}
	body, err := json.Marshal(plan)
	if err != nil || len(body) > codexSandboxWireLimit {
		t.Fatal("bounded plan")
	}
	if codexActorWriteFile(filepath.Join(base, "operations.json"), append(body, '\n'), os.O_EXCL) != nil {
		t.Fatal("durable operation plan")
	}
	// Real full initial admission, including all runtime entries, before Start.
	if codexVerifyManagedReferences(parent, prepared) != nil || codexVerifyInitialPrepared(parent, prepared) != nil {
		t.Fatal("invalid original initial inventory before Start")
	}
	initialVerified = true
	parentDeadline, _ := parent.Deadline()
	workDeadline, err := codexActorExecutionDeadline(parentDeadline, time.Now())
	if err != nil {
		t.Fatal("zero Start if cleanup reserve exhausted")
	}
	work, workCancel := context.WithDeadline(parent, workDeadline)
	defer workCancel()
	phase = "start"
	if codexVerifyActorCommand(work, codexActorCommand{Path: helper, SHA256: digest, Scenario: "result"}) != nil {
		t.Fatal("actual helper drift before original Start")
	}
	handle, stream, err = process.StartDuplex(work, cmd)
	if handle != nil {
		id := handle.Identity()
		view, ok := handle.DuplexLaunchState()
		identity, _ := json.Marshal(map[string]any{"pid": id.PID, "pgid": id.PGID, "birth_known": id.BirthKnown, "birth": id.Birth, "actual_executable": id.Executable, "actual_executable_sha256": id.ExecutableSHA256, "duplex_view": ok, "spawned_at": view.SpawnedAt, "startup_cleanup_present": view.StartupCleanup != nil, "helper_sha256": digest})
		if codexActorWriteFile(filepath.Join(base, "original-handle.json"), append(identity, '\n'), os.O_EXCL) != nil {
			t.Error("original identity evidence")
		}
	}
	if err != nil || handle == nil || stream == nil {
		t.Fatal("original StartDuplex failed; h+err retained")
	}
	type exchangeResult struct {
		body []byte
		err  error
	}
	phase = "exchange"
	written, read := make(chan error, 1), make(chan exchangeResult, 1)
	joined = false
	wireBody := append(append([]byte(nil), body...), '\n')
	go func() {
		n, err := stream.Write(wireBody)
		if err == nil && n != len(wireBody) {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	// The helper uses a bounded JSON value rather than waiting on stdin EOF.
	go func() {
		var raw json.RawMessage
		d := json.NewDecoder(io.LimitReader(stream, codexSandboxWireLimit+1))
		err := d.Decode(&raw)
		read <- exchangeResult{raw, err}
	}()
	var reply exchangeResult
	var writeErr error
	gotWrite, gotRead := false, false
	for !gotWrite || !gotRead {
		select {
		case writeErr = <-written:
			gotWrite = true
		case reply = <-read:
			gotRead = true
		case <-work.Done():
			_ = stream.Close()
			if !gotWrite {
				select {
				case writeErr = <-written:
					gotWrite = true
				case <-parent.Done():
				}
			}
			if !gotRead {
				select {
				case reply = <-read:
					gotRead = true
				case <-parent.Done():
				}
			}
			joined = gotWrite && gotRead
			t.Fatal("bounded original pipe exchange failed")
		}
	}
	joined = true
	if writeErr != nil || reply.err != nil || codexSandboxDecode(reply.body, &observations) != nil || observations.Version != 1 || observations.Planned != len(plan.Operations) {
		t.Fatal("exact helper reply/pipe failure")
	}
	phase = "original_wait"
	if handle.Wait(work) != nil || handle.ConfirmTreeExited() != nil || handle.ExitCode() != 0 {
		t.Fatal("original helper Wait/tree proof failed")
	}
	phase = "file_api"
	if observations.Incomplete || len(observations.Observations) != len(plan.Operations) {
		t.Fatal("first file API mismatch; subsequent operations not executed")
	}
	for i, op := range plan.Operations {
		v := observations.Observations[i]
		if v.Name != op.Name || !v.Matched || v.ErrorClass != "none" && v.ErrorClass != "eacces" && v.ErrorClass != "eperm" {
			t.Fatal("kernel file API mismatch; evidence retained")
		}
	}
	phase = "completed"
}
