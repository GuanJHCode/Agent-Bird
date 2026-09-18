package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestGrokSessionSandboxScopeAndNoReuse(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home, work, scratch := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "scratch")
	for _, p := range []string{home, work} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GROK_HOME", home)
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000, GrokSessionWrite: true}
	grant := contract.LaunchCommand{CommandID: "test-command", RunID: "test-run", TaskID: "test-task"}
	cmd := process.Command{Path: "/bin/sh", Dir: work}
	prepared, err := prepareGrokCommand(context.Background(), cmd, profile, grant, scratch)
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(home, "sessions", grokEncodeCWD(work), grokSessionID(grant))
	// Run a harmless shell under the exact generated sandbox policy, using
	// the provider argv only to locate the policy (never invoke Grok here).
	rules := prepared.Args[1]
	run := exec.Command("/usr/bin/sandbox-exec", "-p", rules, "/bin/sh", "-c", `echo yes > "$1"; echo no > "$2"; echo no > "$3"; exit 0`, "test", filepath.Join(session, "answer"), filepath.Join(work, "forbidden"), filepath.Join(home, "config.toml"))
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("sandbox: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(session, "answer")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(work, "forbidden"), filepath.Join(home, "config.toml")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("write escaped: %s", p)
		}
	}
	if _, err := prepareGrokCommand(context.Background(), cmd, profile, grant, scratch); err == nil {
		t.Fatal("reused session intent")
	}
	// This known fixture never launches a Provider; remove only its short socket scratch.
	for i, arg := range prepared.Args {
		if arg == "--leader-socket" {
			if err := os.Remove(filepath.Dir(prepared.Args[i+1])); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestGrokSessionRequiresPermissionBeforeWrites(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
	_, err := prepareGrokCommand(context.Background(), process.Command{Path: "/bin/true", Dir: root}, profile, contract.LaunchCommand{CommandID: "c"}, filepath.Join(root, "scratch"))
	if err == nil {
		t.Fatal("missing authorization accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "scratch")); !os.IsNotExist(err) {
		t.Fatal("allocated scratch before authorization")
	}
}

func TestGrokSessionRejectsExistingAndUntrustedPaths(t *testing.T) {
	for _, kind := range []string{"existing-session", "symlink-sessions", "symlink-policy", "long-workspace", "public-sessions"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			home := filepath.Join(root, "home")
			work := filepath.Join(root, "work")
			if kind == "long-workspace" {
				work = filepath.Join(root, strings.Repeat("w", 240))
			}
			for _, p := range []string{home, work} {
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("GROK_HOME", home)
			grant := contract.LaunchCommand{CommandID: kind, RunID: "r", TaskID: "t"}
			sessions := filepath.Join(home, "sessions")
			outside := filepath.Join(root, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "existing-session":
				if err := os.MkdirAll(filepath.Join(sessions, grokEncodeCWD(work), grokSessionID(grant)), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink-sessions":
				if err := os.Symlink(outside, sessions); err != nil {
					t.Fatal(err)
				}
			case "symlink-policy":
				if err := os.Symlink(filepath.Join(outside, "policy"), filepath.Join(home, "config.toml")); err != nil {
					t.Fatal(err)
				}
			case "public-sessions":
				if err := os.Mkdir(sessions, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(sessions, 0777); err != nil {
					t.Fatal(err)
				}
			}
			profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000, GrokSessionWrite: true}
			_, err := prepareGrokCommand(context.Background(), process.Command{Path: "/bin/true", Dir: work}, profile, grant, filepath.Join(root, "scratch"))
			if err == nil {
				t.Fatalf("accepted %s", kind)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("outside touched: %v %v", entries, err)
			}
		})
	}
}

func TestGrokHostLaunchUsesAuthorizedSession(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	for _, p := range []string{home, work} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(work, "INPUT.txt"), []byte("fixture-readonly"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "provider")
	script := []byte("#!/bin/sh\nsession=''\nwhile [ $# -gt 0 ]; do\ncase \"$1\" in --session-id) shift; session=$1;; esac\nshift\ndone\n[ -n \"$session\" ] || exit 9\nanswer=$(cat INPUT.txt)\nprintf '%s\\n' \"{\\\"type\\\":\\\"text\\\",\\\"data\\\":\\\"$answer\\\"}\"\nprintf '%s\\n' \"{\\\"type\\\":\\\"end\\\",\\\"stopReason\\\":\\\"end_turn\\\",\\\"sessionId\\\":\\\"$session\\\"}\"\n")
	if err := os.WriteFile(binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	pin := adapter.BinaryPin{Path: binary, Version: "grok 1.0.34 (3736acbc8658)", SHA256: hashBytes(script)}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 5000, GrokSessionWrite: true}
	inv, err := adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderGrok, Binary: pin, Lock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok, Protocol: adapter.ProtocolID(adapter.ProviderGrok), Binary: pin}, CWD: work, Prompt: "read INPUT.txt", Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: root, ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 5000}
	socket := filepath.Join("/private/tmp", "codex-grok-"+grokSessionID(grant))
	t.Cleanup(func() {
		if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	result, err := h.ExecuteLaunch(context.Background(), grant, reportInvocation{base: inv})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	answer, err := os.ReadFile(result.ArtifactPath)
	if err != nil || string(answer) != "fixture-readonly" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions", grokEncodeCWD(work), grokSessionID(grant))); err != nil {
		t.Fatal(err)
	}
}
