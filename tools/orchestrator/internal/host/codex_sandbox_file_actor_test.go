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
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const codexSandboxOptIn = "root-reviewed-c06-sandbox-file-api-v1"
const codexSandboxWireLimit = 128 << 10
const codexSandboxOperationLimit = 256

var codexSandboxName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Test-only bounded direct file APIs. No shell, network, SQLite API, provider,
// product selector, Session or exit capability is supplied by this helper.
type codexSandboxOperation struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Other  string `json:"other"`
	Expect string `json:"expect"`
}
type codexSandboxPlan struct {
	Version    int                     `json:"version"`
	Base       string                  `json:"base"`
	Operations []codexSandboxOperation `json:"operations"`
}
type codexSandboxObservation struct {
	Name       string `json:"name"`
	ErrorClass string `json:"error_class"`
	Matched    bool   `json:"matched"`
}
type codexSandboxReport struct {
	Version      int                       `json:"version"`
	Planned      int                       `json:"planned"`
	Incomplete   bool                      `json:"incomplete"`
	Observations []codexSandboxObservation `json:"observations"`
}

func codexSandboxDecode(body []byte, out any) error {
	if len(body) == 0 || len(body) > codexSandboxWireLimit || !utf8.Valid(body) || !codexRecordUnicode(body) {
		return errCodexStateFile
	}
	decode := func(raw []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		work := 32768
		v, err := codexProtectedValue(d, 0, &work)
		if err != nil {
			return nil, err
		}
		if _, err := d.Token(); err != io.EOF {
			return nil, errCodexStateFile
		}
		return v, nil
	}
	a, err := decode(body)
	if err != nil || json.Unmarshal(body, out) != nil {
		return errCodexStateFile
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return errCodexStateFile
	}
	b, err := decode(encoded)
	if err != nil || !reflect.DeepEqual(a, b) {
		return errCodexStateFile
	}
	return nil
}
func codexSandboxValidatePlan(plan codexSandboxPlan) error {
	if plan.Version != 1 || !codexCanonicalPath(plan.Base) || !strings.HasPrefix(plan.Base, "/private/tmp/") || len(plan.Operations) == 0 || len(plan.Operations) > codexSandboxOperationLimit {
		return errCodexStateFile
	}
	seen := map[string]bool{}
	for _, op := range plan.Operations {
		if !codexSandboxName.MatchString(op.Name) || seen[op.Name] || !codexCanonicalPath(op.Path) || !strings.HasPrefix(op.Path, plan.Base+"/") || len(op.Path) > 4096 || strings.ContainsRune(op.Path, 0) {
			return errCodexStateFile
		}
		seen[op.Name] = true
		switch op.Kind {
		case "read", "overwrite", "create", "remove", "chmod", "mkdir":
			if op.Other != "" {
				return errCodexStateFile
			}
		case "replace", "rename":
			if !codexCanonicalPath(op.Other) || !strings.HasPrefix(op.Other, plan.Base+"/") || len(op.Other) > 4096 || strings.ContainsRune(op.Other, 0) || op.Other == op.Path {
				return errCodexStateFile
			}
		default:
			return errCodexStateFile
		}
		if op.Expect != "success" && op.Expect != "permission" {
			return errCodexStateFile
		}
	}
	return nil
}
func codexSandboxErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, syscall.EACCES):
		return "eacces"
	case errors.Is(err, syscall.EPERM):
		return "eperm"
	case errors.Is(err, syscall.ENOENT):
		return "enoent"
	case errors.Is(err, syscall.ENOTEMPTY):
		return "enotempty"
	case errors.Is(err, syscall.EEXIST):
		return "eexist"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "other"
	}
}
func codexSandboxMatched(expect string, err error) bool {
	return expect == "success" && err == nil || expect == "permission" && (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM))
}
func codexSandboxFileOperation(op codexSandboxOperation) error {
	switch op.Kind {
	case "read":
		f, err := os.Open(op.Path)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(f, 4097))
		if len(body) > 4096 {
			readErr = errCodexStateFile
		}
		return errors.Join(readErr, f.Close())
	case "overwrite":
		return codexActorWriteFile(op.Path, []byte("SYNTHETIC_WRITE\n"), os.O_TRUNC)
	case "create":
		return codexActorWriteFile(op.Path, []byte("SYNTHETIC_CREATED\n"), os.O_EXCL)
	case "remove":
		return os.Remove(op.Path)
	case "chmod":
		return os.Chmod(op.Path, 0500)
	case "mkdir":
		return os.MkdirAll(op.Path, 0700)
	case "replace":
		// Temp file is in an explicitly allowed runtime root; destination is
		// existing for negative cases. Failure retains its exact temp bytes.
		if err := codexActorWriteFile(op.Other, []byte("SYNTHETIC_ATOMIC_SAVE\n"), os.O_EXCL); err != nil {
			return err
		}
		return os.Rename(op.Other, op.Path)
	case "rename":
		return os.Rename(op.Path, op.Other)
	}
	return errCodexStateFile
}
func codexSandboxRun(ctx context.Context, plan codexSandboxPlan) codexSandboxReport {
	r := codexSandboxReport{Version: 1, Planned: len(plan.Operations), Observations: []codexSandboxObservation{}}
	if codexSandboxValidatePlan(plan) != nil {
		r.Incomplete = true
		return r
	}
	for _, op := range plan.Operations {
		err := ctx.Err()
		if err == nil {
			err = codexSandboxFileOperation(op)
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

func TestManagedCodexSandboxFileAPIHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-1] != "codex-sandbox-file-api" {
		t.Skip("explicit owned helper argv only")
	}
	if os.Getenv("C06_SANDBOX_FILE_OPT_IN") != codexSandboxOptIn {
		os.Exit(91)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	watchdog := time.AfterFunc(20*time.Second, func() { os.Exit(93) })
	defer watchdog.Stop()
	if err := codexSandboxProtocol(ctx, os.Stdin, os.Stdout); err != nil {
		os.Exit(92)
	}
	os.Exit(0) // No Go PASS text enters the retained pipe.
}

// Exactly one bounded JSON value, so no child stdin EOF is needed to reply.
// Original retained-pipe byte ordering/Close ownership stays in the parent.
func codexSandboxProtocol(ctx context.Context, input io.Reader, output io.Writer) error {
	var body json.RawMessage
	err := json.NewDecoder(io.LimitReader(input, codexSandboxWireLimit+1)).Decode(&body)
	var plan codexSandboxPlan
	if err != nil || codexSandboxDecode(body, &plan) != nil || codexSandboxValidatePlan(plan) != nil {
		return errCodexStateFile
	}
	r := codexSandboxRun(ctx, plan)
	encoded, err := json.Marshal(r)
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

// Applies only an argv tail to the real builder. No production seam changes;
// original wrapper, env, rules, cwd and independent provider pin stay exact.
func codexSandboxCommand(ctx context.Context, cmd process.Command, p codexPreparedState, helper, digest string) (process.Command, error) {
	if cmd.Path != p.Launch.Path || p.Launch.Path != "/usr/bin/sandbox-exec" || cmd.PinnedPath != p.Origin.Lock.Binary.Path || cmd.PinnedSHA256 != p.Origin.Lock.Binary.SHA256 || len(cmd.Args) < 5 || cmd.Args[0] != "-p" || cmd.Args[2] != cmd.PinnedPath || cmd.Args[3] != "app-server" || cmd.Args[4] != "--strict-config" || codexVerifyActorCommand(ctx, codexActorCommand{Path: helper, SHA256: digest, Scenario: "result"}) != nil {
		return process.Command{}, errCodexStateFile
	}
	cmd.Args = []string{cmd.Args[0], cmd.Args[1], helper, "-test.run=^TestManagedCodexSandboxFileAPIHelperProcess$", "--", "codex-sandbox-file-api"}
	return cmd, nil
}

// Every pre-existing negative target is synthetic and owned. Placeholder
// thread/seal bytes are file-policy probes, not protected Session/seal proof.
func codexSandboxBuildPlan(base string, p codexPreparedState, spool, workspace, source string) (codexSandboxPlan, []string, error) {
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
	for _, v := range []struct{ name, path string }{{"source", source}, {"protected", spool}} {
		target := filepath.Join(v.path, "alias-probe")
		if err := write(target); err != nil {
			return plan, nil, err
		}
		link := filepath.Join(p.Layout.Auxiliary.Runtime[1].Path, v.name+"-symlink")
		hard := filepath.Join(p.Layout.Auxiliary.Runtime[1].Path, v.name+"-hardlink")
		if err := os.Symlink(target, link); err != nil {
			return plan, nil, err
		}
		if err := os.Link(target, hard); err != nil {
			return plan, nil, err
		}
		retained = append(retained, target, link, hard)
		add(v.name+"_symlink_write", "overwrite", link, "", "permission")
		add(v.name+"_hardlink_write", "overwrite", hard, "", "permission")
	}
	return plan, retained, codexSandboxValidatePlan(plan)
}

// Metadata+hash evidence uses only the explicit synthetic fixture targets.
// Directory timestamps/entries can legitimately change through allowed saves.
type codexSandboxFingerprint struct {
	Dev        uint64 `json:"dev"`
	Inode      uint64 `json:"inode"`
	UID        uint32 `json:"uid"`
	Mode       uint32 `json:"mode"`
	Links      uint64 `json:"links"`
	Size       int64  `json:"size"`
	MtimeNS    int64  `json:"mtime_ns"`
	SHA256     string `json:"sha256"`
	LinkSHA256 string `json:"link_sha256"`
}

func codexSandboxSnapshot(paths []string) (map[string]codexSandboxFingerprint, error) {
	result := map[string]codexSandboxFingerprint{}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, errCodexStateFile
		}
		v := codexSandboxFingerprint{Dev: uint64(st.Dev), Inode: st.Ino, UID: st.Uid, Mode: uint32(st.Mode)}
		if !info.IsDir() {
			v.Links = uint64(st.Nlink)
			v.Size = info.Size()
			v.MtimeNS = info.ModTime().UnixNano()
			if info.Mode()&os.ModeSymlink != 0 {
				link, err := os.Readlink(path)
				if err != nil {
					return nil, err
				}
				v.LinkSHA256 = codexDigest([]byte(link))
			}
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			before, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, err
			}
			body, readErr := io.ReadAll(io.LimitReader(f, (16<<10)+1))
			after, statErr := f.Stat()
			closeErr := f.Close()
			if readErr != nil || statErr != nil || closeErr != nil || len(body) > 16<<10 || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				return nil, errCodexStateFile
			}
			end, endErr := os.Lstat(path)
			if endErr != nil || !os.SameFile(info, end) || info.Mode() != end.Mode() || info.Size() != end.Size() || !info.ModTime().Equal(end.ModTime()) || info.Mode()&os.ModeSymlink == 0 && !os.SameFile(info, before) {
				return nil, errCodexStateFile
			}
			v.SHA256 = codexDigest(body)
		}
		result[path] = v
	}
	return result, nil
}

func TestManagedCodexSandboxFileAPIActor(t *testing.T) {
	if os.Getenv("C06_SANDBOX_FILE_OPT_IN") != codexSandboxOptIn || os.Getenv("C06_SANDBOX_FILE_EVIDENCE_DIR") == "" {
		t.Skip("ROOT reviewed double opt-in kernel file API actor")
	}
	evidence := os.Getenv("C06_SANDBOX_FILE_EVIDENCE_DIR")
	root, err := codexAnchorStateRoot(context.Background(), evidence)
	if err != nil || len(root.Parents) == 0 || root.Parents[len(root.Parents)-1].Mode != 0700 {
		t.Fatal("fresh canonical owned evidence required")
	}
	base := filepath.Join(evidence, "file-api")
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
		after, afterErr := codexSandboxSnapshot(preserved)
		unchanged := before != nil && afterErr == nil && reflect.DeepEqual(before, after)
		fdAfter, fdAfterErr := os.ReadDir("/dev/fd")
		body, _ := json.MarshalIndent(map[string]any{"version": 1, "builder_kernel_file_api_only": true, "phase": phase, "failed": t.Failed(), "public_entry": false, "execute_launch": false, "provider_executed": false, "model_turns": 0, "original_handle": handle != nil, "pid": pid, "pgid": pgid, "original_wait": waited, "group_absent": absent, "exit_code": exit, "startup_cleanup_present": startup, "cleanup_class": cleanupClass, "pipe_jobs_joined": joined, "preserved_unchanged": unchanged, "snapshot_before": before, "snapshot_after": after, "file_api": observations, "fd_observed": fdErr == nil && fdAfterErr == nil, "fd_before": len(fdBefore), "fd_after": len(fdAfter), "fd_verdict": "observation_only_unisolated_go_runtime"}, "", "  ")
		if codexActorWriteFile(filepath.Join(base, "sandbox-evidence.json"), append(body, '\n'), os.O_EXCL) != nil {
			t.Error("durable original evidence failed")
		}
		if handle != nil && (!waited || !absent) || !joined {
			t.Error("original worker/pipe ownership unproved; halt")
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
	env := []string{"HOME=" + base, "CODEX_HOME=" + source, "C06_SANDBOX_FILE_OPT_IN=" + codexSandboxOptIn}
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
	cmd, err = codexSandboxCommand(parent, cmd, prepared, helper, digest)
	if err != nil {
		t.Fatal("helper exact tail")
	}
	plan, paths, err := codexSandboxBuildPlan(base, prepared, spool, workspace, source)
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
	// The builder's references remain checked independently after alias setup.
	if codexVerifyManagedReferences(parent, prepared) != nil {
		t.Fatal("original refs drift before Start")
	}
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
