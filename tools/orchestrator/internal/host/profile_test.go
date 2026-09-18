package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type profiledInvocation struct {
	launchInvocation
	profile adapter.ExecutionProfile
}

func (i profiledInvocation) ExecutionProfile() *adapter.ExecutionProfile { return &i.profile }

func TestProfileSandboxEnforcesReviewerAndIsolatedImplementer(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	work := filepath.Join(root, "work")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("init", repo)
	git("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
	git("-C", repo, "worktree", "add", "--detach", work)
	for _, role := range []adapter.Role{adapter.Reviewer, adapter.Implementer} {
		t.Run(string(role), func(t *testing.T) {
			target := filepath.Join(work, string(role))
			outside := filepath.Join(root, "outside-"+string(role))
			inv := profiledInvocation{launchInvocation: launchInvocation{args: []string{"/bin/sh", "-c", `printf allowed > "$1"; printf forbidden > "$2"; exit 0`, "worker", target, outside}, dir: work}, profile: adapter.ExecutionProfile{Version: 1, Role: role, Permission: adapter.ReadOnly, TimeoutMS: 2000}}
			if role == adapter.Implementer {
				inv.profile.Permission = adapter.WorkspaceWrite
			}
			cmd, err := sandboxCommand(context.Background(), invocationCommand(inv), &inv.profile, filepath.Join(root, "scratch-"+string(role)))
			if err != nil {
				t.Fatal(err)
			}
			child, err := process.Start(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}

			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("outside write escaped sandbox: %v", err)
			}
			_, err = os.Stat(target)
			if role == adapter.Reviewer && !os.IsNotExist(err) {
				t.Fatal("reviewer wrote workspace")
			}
			if role == adapter.Implementer && err != nil {
				t.Fatalf("implementer write denied: %v", err)
			}
		})
	}
}

func TestProfileSandboxCannotReadControlCredentials(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	control := filepath.Join(state, "control")
	if err = os.MkdirAll(control, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(control, "secret.json")
	if err = os.WriteFile(secret, []byte("controller-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(state, "host-spool", "launch", "attempt", "segment", "scratch")
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
	cmd, err := sandboxCommand(context.Background(), process.Command{Path: "/bin/cat", Args: []string{secret}, Dir: root, Env: os.Environ()}, profile, scratch)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(cmd.Path, cmd.Args...).Output()
	if err == nil || len(out) > 0 {
		t.Fatalf("control credential readable: exit=%v bytes=%d", err, len(out))
	}
	profile.Permission = adapter.WorkspaceWrite
	if _, err = sandboxCommand(context.Background(), process.Command{Path: "/bin/true", Dir: root}, profile, scratch); err == nil {
		t.Fatal("role permission mismatch accepted")
	}
}

func TestProfileCannotReadAnotherReportCapability(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	launch := filepath.Join(state, "host-spool", "launch")
	own := filepath.Join(launch, ".reports", "own", "capability.json")
	victim := filepath.Join(state, "host-spool", "victim-launch", ".reports", "victim", "capability.json")
	for _, path := range []string{own, victim} {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("test-capability"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scratch := filepath.Join(launch, "attempt", "segment", "scratch")
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
	for _, tc := range []struct {
		path    string
		allowed bool
	}{{own, true}, {victim, false}} {
		cmd, err := sandboxCommand(context.Background(), process.Command{Path: "/bin/cat", Args: []string{tc.path}, Dir: root, Env: []string{"ORCHESTRATOR_REPORT_CAPABILITY=" + own}}, profile, scratch)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(cmd.Path, cmd.Args...).Output()
		if tc.allowed && (err != nil || len(out) == 0) {
			t.Fatal("own report capability unavailable")
		}
		if !tc.allowed && (err == nil || len(out) != 0) {
			t.Fatal("cross-launch report capability readable")
		}
	}
}

func TestLaunchRechecksProviderPinAfterInvocationWasBuilt(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "typed"}[typed], func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(root, "provider")
			source := []byte("#!/bin/sh\nexit 0\n")
			if err = os.WriteFile(binary, source, 0700); err != nil {
				t.Fatal(err)
			}
			pin := adapter.BinaryPin{Path: binary, Version: "test", SHA256: hashBytes(source)}
			req := adapter.Request{Provider: adapter.ProviderClaude, Binary: pin, CWD: root, Prompt: "inspect", Permission: adapter.Permission{Mode: "plan"}}
			if typed {
				req.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
				req.Lock = &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: pin}
			}
			inv, err := adapter.BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(binary, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
				t.Fatal(err)
			}
			h, err := NewIPC(filepath.Join(root, "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.ExecuteLaunch(context.Background(), contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", ReservationID: "reservation", CommandID: "command", GrantedActiveMS: 1000}, reportInvocation{base: inv})
			if err == nil || err.Error() != "binary_pin_mismatch" {
				t.Fatalf("replaced provider not refused at launch: %v", err)
			}
		})
	}
}

func TestHostRejectsUnmanagedWriteBeforeExecution(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	inv := profiledInvocation{launchInvocation: launchInvocation{args: []string{"/usr/bin/true"}, dir: root}, profile: adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}}
	_, err = h.ExecuteLaunch(context.Background(), contract.LaunchCommand{CommandID: "c", ReservationID: "r", RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", WorkRevision: 1, GrantedActiveMS: 1000}, inv)
	if err == nil || err.Error() != "managed_workspace_required" {
		t.Fatalf("unmanaged host write: %v", err)
	}
}
