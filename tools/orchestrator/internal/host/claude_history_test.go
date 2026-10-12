package host

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeHistoryPreparationRetainsOriginalConfigAndTranscriptIdentity(t *testing.T) {
	for _, fault := range []string{"none", "config", "project", "transcript", "skip", "unknown_skip", "unknown_version", "missing_transcript", "symlink_transcript"} {
		t.Run(fault, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(root, "original-config")
			if err = os.Mkdir(config, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			t.Setenv("CLAUDE_CODE_SKIP_PROMPT_HISTORY", "")
			h, err := NewIPC(filepath.Join(root, "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			if err = h.BindContinuationOwner("owner"); err != nil {
				t.Fatal(err)
			}
			g := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "first", WorkRevision: 1, PlanRevision: 1, AdapterPayload: []byte(`{"provider":"claude-code","profile":{"version":1}}`)}
			pin := adapter.BinaryPin{Version: "2.1.285 (Claude Code)"}
			cmd := process.Command{Path: "/usr/bin/true", Dir: root}
			first, project, err := h.prepareClaudeHistory(cmd, g, pin)
			if err != nil {
				t.Fatal(err)
			}
			if commandEnvironment(first, "CLAUDE_CONFIG_DIR") != config || len(commandEnvironment(first, "CLAUDE_CODE_PROJECT_DIR_NAME")) > 64 || project == "" {
				t.Fatal("original config was switched or project scope missing")
			}
			transcript := filepath.Join(project, "native-session.jsonl")
			if err = os.WriteFile(transcript, []byte("{\"session_id\":\"native-session\"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			if err != nil {
				t.Fatal(err)
			}
			if err = spool.Append(contract.Event{Version: 1, ProducerID: "producer", EventID: "init", Kind: contract.EventSession, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, Sequence: 1, SessionKind: "session-id", SessionID: "native-session"}); err != nil {
				t.Fatal(err)
			}
			g.SegmentID = "second"
			g.SessionKind, g.SessionID = "session-id", "native-session"
			g.Continuation = &contract.ContinuationRef{SessionEventID: "init"}
			if _, again, err := h.prepareClaudeHistory(cmd, g, pin); err != nil || again != project {
				t.Fatalf("first native transcript not bound: %s %v", again, err)
			}
			switch fault {
			case "config":
				other := filepath.Join(root, "other-config")
				os.Mkdir(other, 0700)
				setCommandEnvironment(&cmd, "CLAUDE_CONFIG_DIR", other)
			case "project":
				os.Rename(project, project+".saved")
				os.Mkdir(project, 0700)
			case "transcript":
				os.Rename(transcript, transcript+".saved")
				os.WriteFile(transcript, []byte("replacement"), 0600)
			case "skip":
				setCommandEnvironment(&cmd, "CLAUDE_CODE_SKIP_PROMPT_HISTORY", "1")
			case "unknown_skip":
				setCommandEnvironment(&cmd, "CLAUDE_CODE_SKIP_PROMPT_HISTORY", "yes")
			case "unknown_version":
				pin.Version = "fixture-unknown"
			case "missing_transcript":
				os.Remove(transcript)
			case "symlink_transcript":
				os.Rename(transcript, transcript+".saved")
				os.Symlink(transcript+".saved", transcript)
			}
			g.SegmentID = "third"
			resumed, again, err := h.prepareClaudeHistory(cmd, g, pin)
			if fault == "none" {
				if err != nil || again != project || commandEnvironment(resumed, "CLAUDE_CONFIG_DIR") != config {
					t.Fatalf("history scope changed: %s %v", again, err)
				}
			} else if err == nil {
				t.Fatal("unsafe history admitted")
			}
		})
	}
}

func TestClaudeHistorySandboxAllowsOnlyAttemptProject(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config")
	os.Mkdir(config, 0700)
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CODE_SKIP_PROMPT_HISTORY", "")
	repo, work := filepath.Join(root, "repo"), filepath.Join(root, "work")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	git("init", "-q", repo)
	git("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "base")
	git("-C", repo, "worktree", "add", "--detach", work)
	for _, role := range []adapter.Role{adapter.Reviewer, adapter.Implementer} {
		t.Run(string(role), func(t *testing.T) {
			h, err := NewIPC(filepath.Join(root, "spool-"+string(role)), "producer")
			if err != nil {
				t.Fatal(err)
			}
			h.BindContinuationOwner("owner")
			g := contract.LaunchCommand{RunID: "run", TaskID: string(role), AttemptID: string(role), SegmentID: "first", WorkRevision: 1, PlanRevision: 1, AdapterPayload: []byte(`{"provider":"claude-code"}`)}
			profile := &adapter.ExecutionProfile{Version: 1, Role: role, Permission: adapter.ReadOnly, TimeoutMS: 1000}
			if role == adapter.Implementer {
				profile.Permission = adapter.WorkspaceWrite
			}
			cmd, project, err := h.prepareClaudeHistory(process.Command{Path: "/bin/sh", Dir: work}, g, adapter.BinaryPin{Version: "2.1.285 (Claude Code)"})
			if err != nil {
				t.Fatal(err)
			}
			sibling := filepath.Join(config, "projects", "sibling")
			os.Mkdir(sibling, 0700)
			global := filepath.Join(config, "settings.json")
			os.WriteFile(global, []byte("original"), 0600)
			allowed := filepath.Join(project, "state")
			other := filepath.Join(sibling, "state")
			rootFile := filepath.Join(config, "global-write")
			cmd.Args = []string{"-c", `printf allowed > "$1"; printf forbidden > "$2"; printf forbidden > "$3"; printf forbidden > "$4"; exit 0`, "worker", allowed, other, global, rootFile}
			wrapped, err := sandboxCommandWithState(context.Background(), cmd, profile, filepath.Join(root, "scratch-"+string(role)), project)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(wrapped.Args, " "), "(allow file-write* (subpath \""+config+"\"))") {
				t.Fatal("auth config home became writable")
			}
			child, err := process.Start(context.Background(), wrapped)
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(allowed)
			if err != nil || string(body) != "allowed" {
				t.Fatalf("attempt history write failed: %q %v", body, err)
			}
			body, err = os.ReadFile(global)
			if err != nil || string(body) != "original" {
				t.Fatal("global settings write escaped")
			}
			for _, path := range []string{other, rootFile} {
				if _, err = os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("outside history write escaped: %s", path)
				}
			}
		})
	}
}

func TestContinuationOriginMetadataRejectsSymlinkAndOwnerRebinding(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BindContinuationOwner("owner"); err != nil {
		t.Fatal(err)
	}
	if err = h.BindContinuationOwner("different"); err == nil {
		t.Fatal("logical Host owner rebound")
	}
	origin := continuationOrigin{Version: 1, RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "first", WorkRevision: 1, PlanRevision: 1, Provider: "claude-code", AdapterPayloadSHA256: strings.Repeat("a", 64), OwnerContextID: "owner"}
	body, _ := json.Marshal(origin)
	path := filepath.Join(root, "origin.json")
	os.WriteFile(path, body, 0600)
	alias := filepath.Join(root, "alias.json")
	os.Symlink(path, alias)
	if _, err = readContinuationOrigin(alias); err == nil {
		t.Fatal("symlink origin accepted")
	}
	os.Chmod(path, 0644)
	if _, err = readContinuationOrigin(path); err == nil {
		t.Fatal("nonprivate origin accepted")
	}
}
