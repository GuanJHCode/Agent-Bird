package host

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func validationStartGrant(t *testing.T, h *Host) (contract.LaunchCommand, process.Command) {
	t.Helper()
	tool := "/usr/bin/true"
	digest, err := process.ExecutableDigest(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	p := adapter.InvocationPayload{Kind: "candidate", Directory: filepath.Join(t.TempDir(), "workspace"), CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", AutoDirectory: true, SourceTask: "edit", Command: []string{tool}, CommandSHA256: digest}}
	raw, _ := json.Marshal(p)
	g := managedHostGrant(t, h, contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: 1, AdapterPayload: raw})
	g = validationToolGrantForTest(t, h, g, digest)
	cleanupValidationToolCopies(t, h)
	sealed, e := h.sealValidationTool(context.Background(), g, tool, digest, p.Directory, filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "scratch"))
	if e != nil {
		t.Fatal(e)
	}
	return g, process.Command{Path: sealed, SealedPath: sealed, SealedSHA256: digest, PinnedPath: tool, PinnedSHA256: digest, Dir: p.Directory}
}
func TestValidationLiveStartProofPublicationAndRecovery(t *testing.T) {
	for _, kind := range []string{"live", "untyped", "unknown", "stop", "cancel", "artifact_only", "phase_only", "terminal_replay", "consume_once"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			g, cmd := validationStartGrant(t, h)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, proof := process.StartWithCommandStarter(ctx, cmd, func(*exec.Cmd) error { return syscall.EAGAIN })
			if kind == "untyped" {
				proof = errors.New("validation_start_eagain")
			}
			a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID}
			meta := metadataForLaunch(g)
			sp, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.appendPhaseEvent(sp, g.RunID, g.TaskID, a, meta, contract.EventPrepared, "prepared", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err = publishLaunchPhase(sp, "starting", a, g.RunID, g.TaskID, cmd, nil, nil); err != nil {
				t.Fatal(err)
			}
			if kind == "unknown" {
				if err = h.recordLaunchUnknown(a, g.RunID, g.TaskID, meta, errors.New("uncertain")); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "stop" {
				h.stopRequested[g.SegmentID] = true
			}
			if kind == "cancel" {
				cancel()
			}
			if kind == "artifact_only" || kind == "phase_only" {
				body, _ := json.Marshal(validationStartDiagnostic{Version: 1, Kind: "validation_start_failure", Stage: "validation_start", Code: "validation_start_eagain"})
				if _, err = h.writeHostArtifact(meta, "terminal", 0, "prelaunch", "launch-failure", body); err != nil {
					t.Fatal(err)
				}
				if kind == "phase_only" {
					if err = publishLaunchPhase(sp, "not_started", a, g.RunID, g.TaskID, cmd, nil, nil); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				err = h.recordValidationStartRejected(ctx, g, cmd, proof)
				if (err == nil) != (kind == "live" || kind == "terminal_replay" || kind == "consume_once") {
					t.Fatalf("publication %s: %v", kind, err)
				}
				if kind == "consume_once" && h.recordValidationStartRejected(ctx, g, cmd, proof) == nil {
					t.Fatal("live proof reused")
				}
			}
			if kind == "artifact_only" || kind == "phase_only" || kind == "terminal_replay" {
				reopened, e := NewIPC(root, "host")
				if e != nil {
					t.Fatal(e)
				}
				if e = reopened.recoverDuplicateGrant(g, 2); e != nil {
					t.Fatal("cold recovery stalled", e)
				}
			}
			events, err := sp.Read()
			if err != nil {
				t.Fatal(err)
			}
			rejected, unknown := 0, 0
			for _, e := range events {
				if e.Kind == contract.EventLaunchRejected {
					rejected++
					if !e.Failure.ValidValidationStart() || e.Process != nil || e.ExitCode != nil || e.ActiveMS != 0 {
						t.Fatal("forged worker lifecycle", e)
					}
				}
				if e.Kind == contract.EventUnknown {
					unknown++
				}
			}
			wantReject := 0
			if kind == "live" || kind == "terminal_replay" || kind == "consume_once" {
				wantReject = 1
			}
			if rejected != wantReject {
				t.Fatalf("rejected=%d want %d", rejected, wantReject)
			}
			if (kind == "artifact_only" || kind == "phase_only") && unknown != 1 {
				t.Fatalf("cold incomplete proof became %d UNKNOWN", unknown)
			}
		})
	}
}
func TestValidationSealedToolPinAndBoundary(t *testing.T) {
	for _, kind := range []string{"native", "script", "env", "changed", "worker_ancestor", "fake_native", "writable_sealed"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			cleanupValidationToolCopies(t, h)
			tool := "/usr/bin/true"
			if kind == "script" {
				tool = filepath.Join(t.TempDir(), "script")
				if err = os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "fake_native" {
				tool = filepath.Join(t.TempDir(), "fake-native")
				if err = os.WriteFile(tool, []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 0, 0, 0}, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "env" {
				tool = "/usr/bin/env"
			}
			digest, err := process.ExecutableDigest(ctx, tool)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "changed" {
				digest = strings.Repeat("f", 64)
			}
			workspace := t.TempDir()
			if kind == "worker_ancestor" {
				workspace = filepath.Dir(h.spoolRoot)
			}
			g := validationToolGrantForTest(t, h, contract.LaunchCommand{RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", CommandID: "c", WorkRevision: 1, PlanRevision: 1}, digest)
			if kind == "writable_sealed" {
				existing, e := h.sealValidationTool(ctx, g, tool, digest, workspace, filepath.Join(h.spoolRoot, "a", "s", "scratch"))
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Chmod(existing, 0700); e != nil {
					t.Fatal(e)
				}
			}
			sealed, err := h.sealValidationTool(ctx, g, tool, digest, workspace, filepath.Join(h.spoolRoot, "a", "s", "scratch"))
			if (err == nil) != (kind == "native") {
				t.Fatalf("seal %s: %v", kind, err)
			}
			if err == nil {
				if sealed == tool {
					t.Fatal("executed mutable original tool")
				}
				got, e := process.ExecutableDigest(ctx, sealed)
				if e != nil || got != digest {
					t.Fatal("sealed copy changed", e)
				}
				for _, path := range []string{sealed, filepath.Dir(sealed)} {
					info, e := os.Lstat(path)
					if e != nil || info.Mode().Perm() != 0500 {
						t.Fatal("sealed mode", e)
					}
				}
			}
		})
	}
}

// Only after the owned actor is done may test cleanup unseal its owned dirs.
func cleanupValidationToolCopies(t *testing.T, h *Host) {
	t.Helper()
	t.Cleanup(func() {
		root := filepath.Join(h.spoolRoot, ".validation-tools")
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		for _, entry := range entries {
			if entry.IsDir() {
				if err := validationOwnedDir(filepath.Join(root, entry.Name()), 0500, false); err != nil {
					t.Error(err)
					continue
				}
				if err := os.Chmod(filepath.Join(root, entry.Name()), 0700); err != nil {
					t.Error(err)
				}
			}
		}
	})
}

func validationToolGrantForTest(t *testing.T, h *Host, g contract.LaunchCommand, pin string) contract.LaunchCommand {
	t.Helper()
	g.HostID = h.producerID
	if g.WorkRevision < 1 {
		g.WorkRevision = 1
	}
	if g.PlanRevision < 1 {
		g.PlanRevision = 1
	}
	if g.HostArtifacts == nil {
		g = withHostArtifactsForTest(t, g)
	}
	scope, err := contract.ValidationToolScopeSHA256(g, pin)
	if err != nil {
		t.Fatal(err)
	}
	g.ValidationTool = &contract.ValidationToolGrant{Version: 1, ReservationID: "tool-" + scope[:32], ScopeSHA256: scope, ToolSHA256: pin, MaximumBytes: contract.ValidationToolMaximumBytes}
	if err = contract.RequireValidationTool(g, pin); err != nil {
		t.Fatal(err)
	}
	return g
}
func TestValidationToolCleanupJournalIdentity(t *testing.T) {
	for _, mode := range []string{"removed", "absent", "changed_bytes", "changed_inode", "missing_journal", "creating"} {
		t.Run(mode, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "host-spool", "host"), "host")
			if err != nil {
				t.Fatal(err)
			}
			g, cmd := validationStartGrant(t, h)
			dir := filepath.Dir(cmd.SealedPath)
			switch mode {
			case "absent":
				if err = os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(cmd.SealedPath); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(filepath.Join(dir, "journal.json")); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(dir); err != nil {
					t.Fatal(err)
				}
			case "changed_bytes":
				if err = os.Chmod(cmd.SealedPath, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(cmd.SealedPath, []byte("changed"), 0500); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(cmd.SealedPath, 0500); err != nil {
					t.Fatal(err)
				}
			case "changed_inode":
				if err = os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(cmd.SealedPath, cmd.SealedPath+".owned-backup"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(cmd.SealedPath, []byte("unrelated"), 0500); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(dir, 0500); err != nil {
					t.Fatal(err)
				}
			case "missing_journal":
				if err = os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(filepath.Join(dir, "journal.json")); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(dir, 0500); err != nil {
					t.Fatal(err)
				}
			case "creating":
				j, e := readValidationToolJournal(dir, g)
				if e != nil {
					t.Fatal(e)
				}
				j.Phase = "creating"
				j.ContentSHA256 = ""
				if err = os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err = writeValidationToolJournal(dir, j); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(dir, 0500); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := h.removeValidationTool(g)
			valid := mode == "removed" || mode == "absent"
			if (err == nil) != valid {
				t.Fatal("cleanup", mode, err)
			}
			if !valid {
				if _, e := os.Lstat(cmd.SealedPath); e != nil {
					t.Fatal("uncertain file deleted", e)
				}
				return
			}
			if receipt.Validate() != nil {
				t.Fatal("receipt invalid", receipt)
			}
			if _, e := os.Lstat(cmd.SealedPath); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("tool retained", e)
			}
			again, e := h.removeValidationTool(g)
			if e != nil || *again != *receipt {
				t.Fatal("cleanup replay", e)
			}
			j, e := readValidationToolJournal(dir, g)
			if e != nil || j.Phase != "removed" {
				t.Fatal("removed journal", e)
			}
		})
	}
}

func TestValidationToolSealedPrepareFailureTerminates(t *testing.T) {
	for _, mode := range []string{"profile_failure", "cleanup_failure"} {
		t.Run(mode, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "host-spool", "host"), "host")
			if err != nil {
				t.Fatal(err)
			}
			g, cmd := validationStartGrant(t, h)
			if err = os.MkdirAll(cmd.Dir, 0700); err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "scratch")
			if err = os.MkdirAll(filepath.Dir(scratch), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(scratch, []byte("owned regular file forces real sandbox preparation refusal"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "cleanup_failure" {
				if err = os.Chmod(cmd.SealedPath, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(cmd.SealedPath, []byte("changed tool bytes"), 0500); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(cmd.SealedPath, 0500); err != nil {
					t.Fatal(err)
				}
			}
			meta := metadataForLaunch(g)
			meta.startProcess = func(context.Context, process.Command) (*process.Handle, error) {
				t.Fatal("worker Start entered after preparation error")
				return nil, nil
			}
			meta.prepareCandidate = func(ctx context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
				wrapped, e := sandboxCommand(ctx, cmd, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 5000}, scratch)
				return wrapped, nil, func() {}, e
			}
			a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID}
			result, err := h.execute(context.Background(), a, g.RunID, g.TaskID, cmd, false, meta)
			if err == nil {
				t.Fatal("preparation error disappeared")
			}
			records, e := h.Collect(context.Background(), a)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "profile_failure" {
				if result.Status != "failed" || len(records) != 2 || records[1].Kind != contract.EventLaunchRejected || records[1].ValidationToolCleanup == nil || records[1].Failure != nil {
					t.Fatal("pre-Start tool refusal lacks cleanup terminal", result, records)
				}
				if _, e = os.Lstat(cmd.SealedPath); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("unneeded binary retained", e)
				}
			} else {
				if result.Status != "unknown" || len(records) != 2 || records[1].Kind != contract.EventUnknown || records[1].ValidationToolCleanup != nil {
					t.Fatal("failed cleanup did not preserve UNKNOWN", result, records)
				}
				if _, e = os.Lstat(cmd.SealedPath); e != nil {
					t.Fatal("uncertain binary deleted", e)
				}
			}
		})
	}
}
