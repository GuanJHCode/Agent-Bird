package host

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostWorkspaceContinuationPreservesDirtyPathsAcrossRestart(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	if err = os.WriteFile(filepath.Join(repo, "owned"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	pin := adapter.BinaryPin{Path: "/private/bin/provider", Version: "fixture-v1", SHA256: strings.Repeat("a", 64)}
	p := adapter.InvocationPayload{Provider: "claude-code", Directory: filepath.Join(root, "work"), Prompt: "Implement", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: pin}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 10000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"owned"}, AutoDirectory: true}}
	raw, _ := json.Marshal(p)
	g := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "first", WorkRevision: 1, PlanRevision: 1, AdapterPayload: raw}
	r := adapter.Request{Provider: adapter.ProviderClaude, Binary: pin, Lock: p.ProviderLock, Profile: p.Profile, Workspace: p.CandidateWorkspace, CWD: p.Directory, Prompt: p.Prompt}
	inv, err := adapter.BuildInvocation(r)
	if err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(root, "spool")
	h, err := NewIPC(spool, "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BindContinuationOwner("owner"); err != nil {
		t.Fatal(err)
	}
	_, closeFirst, original, err := h.prepareCandidate(context.Background(), g, inv, p.Profile)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := original.Receipt.Worktree
	if err = os.WriteFile(filepath.Join(originalPath, "owned"), []byte("unfinished\n"), 0600); err != nil {
		t.Fatal(err)
	}
	closeFirst()
	h, err = NewIPC(spool, "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BindContinuationOwner("owner"); err != nil {
		t.Fatal(err)
	}
	g.SegmentID = "next"
	g.SessionKind, g.SessionID = "session-id", "native-session"
	g.Continuation = &contract.ContinuationRef{Version: 1, SourceKind: "host", Mode: contract.ContinuationQuestion, SourceSegmentID: "first", SourceEventID: "question", SourceEventRevision: 5, SourceEventSHA256: strings.Repeat("b", 64), SessionEventID: "init", AdapterPayloadSHA256: hashBytes(raw), OwnerContextID: "owner", WorkspaceSourceSegmentID: "first"}
	r.Session = adapter.SessionRef{Kind: adapter.SessionID, ID: g.SessionID}
	r.Continuation = g.Continuation
	inv, err = adapter.BuildInvocation(r)
	if err != nil {
		t.Fatal(err)
	}
	freeze, closeNext, resumed, err := h.prepareCandidate(context.Background(), g, inv, p.Profile)
	if err != nil {
		t.Fatalf("original prepared workspace not resumed: %v", err)
	}
	defer closeNext()
	body, err := os.ReadFile(filepath.Join(resumed.Receipt.Worktree, "owned"))
	if err != nil || string(body) != "unfinished\n" || resumed.Receipt.Worktree != originalPath || resumed.PrivateRef != original.PrivateRef {
		t.Fatalf("dirty workspace lost: %+v body=%q err=%v", resumed, body, err)
	}
	artifact, err := freeze(context.Background(), "Completed")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Candidate struct {
			Binding struct {
				SegmentID string `json:"segment_id"`
			} `json:"binding"`
		} `json:"candidate"`
	}
	if err = json.Unmarshal(artifact, &result); err != nil {
		t.Fatal(err)
	}
	if result.Candidate.Binding.SegmentID != "next" {
		t.Fatalf("candidate bound stale segment: %s", artifact)
	}
	again, err := freeze(context.Background(), "Completed")
	if err != nil || string(again) != string(artifact) {
		t.Fatalf("freeze replay changed: %v", err)
	}
}
