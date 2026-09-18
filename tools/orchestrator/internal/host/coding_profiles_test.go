package host

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Exercise the real Host sandbox, materialization and candidate freeze. The
// fixtures deliberately try to write outside their workspace and into Git.
func TestCodingHostFreezesProviderCandidate(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderGrok, adapter.ProviderAGY} {
		t.Run(string(provider), func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(root, "home")
			if err = os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GROK_HOME", home)
			script := "#!/bin/sh\nprintf 'def add(a, b):\\n    return a + b\\n' > calc.py\nprintf forbidden > ../outside\nprintf forbidden > .git\n"
			if provider == adapter.ProviderGrok {
				script += `session=''
while [ $# -gt 0 ]; do
case "$1" in --session-id) shift; session=$1;; esac
shift
done
[ -n "$session" ] || exit 9
printf '%s\n' '{"type":"text","data":"implemented"}'
printf '%s\n' "{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"$session\"}"
`
			} else {
				script += "printf '%s\\n' '{\"event\":\"result\",\"result\":{\"conversation_id\":\"fixture\",\"status\":\"SUCCESS\",\"response\":\"implemented\"}}'\n"
			}
			binary := filepath.Join(root, "provider")
			if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			version := "1.2.5"
			if provider == adapter.ProviderGrok {
				version = "grok 1.0.34 (3736acbc8658)"
			}
			runCodingHost(t, root, provider, adapter.BinaryPin{Path: binary, Version: version, SHA256: hashBytes([]byte(script))}, 5000)
			if _, err = os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
				t.Fatal("workspace write escaped")
			}
		})
	}
}

// Explicit opt-in only. The directory must be fresh and is retained as evidence;
// this never retries or reuses a consumed native session/attempt.
func TestNativeCodingHost(t *testing.T) {
	name := os.Getenv("AGENT_BIRD_NATIVE_CODING_PROVIDER")
	if name == "" {
		t.Skip("native model execution requires explicit bounded authorization")
	}
	provider := adapter.Provider(name)
	if provider != adapter.ProviderGrok && provider != adapter.ProviderAGY {
		t.Fatal("unsupported native provider")
	}
	root := os.Getenv("AGENT_BIRD_NATIVE_CODING_EVIDENCE")
	if !filepath.IsAbs(root) {
		t.Fatal("absolute fresh evidence directory required")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	pin := adapter.BinaryPin{Path: os.Getenv("AGENT_BIRD_NATIVE_CODING_BINARY"), Version: os.Getenv("AGENT_BIRD_NATIVE_CODING_VERSION"), SHA256: os.Getenv("AGENT_BIRD_NATIVE_CODING_SHA256")}
	runCodingHost(t, canonical, provider, pin, 120000)
}

func runCodingHost(t *testing.T, root string, provider adapter.Provider, pin adapter.BinaryPin, budget int64) {
	t.Helper()
	repo, work := filepath.Join(root, "repo"), filepath.Join(root, "work")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", repo)
	baseline := []byte("def add(a, b):\n    return a - b\n")
	if err := os.WriteFile(filepath.Join(repo, "calc.py"), baseline, 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", repo, "add", "calc.py")
	git("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	base := git("-C", repo, "rev-parse", "HEAD")
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: budget, GrokSessionWrite: provider == adapter.ProviderGrok}
	inv, err := adapter.BuildInvocation(adapter.Request{Provider: provider, Binary: pin, Lock: &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: pin}, CWD: work, Prompt: "Fix calc.py: add(a, b) must return a + b. Read the file, use the native file edit/write tool to change only calc.py, then stop. Do not run terminal commands, use subagents or change configuration. Do not merely describe the change.", Profile: profile, Workspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"calc.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: root, ReservationID: "coding", RunID: "coding", TaskID: "implement", AttemptID: "first", SegmentID: "first", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: budget}
	result, err := h.ExecuteLaunch(context.Background(), grant, reportInvocation{base: inv})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Candidate struct {
			CandidateOID string `json:"candidate_oid"`
		} `json:"candidate"`
	}
	if err = json.Unmarshal(body, &artifact); err != nil || artifact.Candidate.CandidateOID == "" || artifact.Candidate.CandidateOID == base {
		t.Fatalf("candidate not frozen: %s %v", body, err)
	}
	// Verify behavior from the frozen candidate, and the controller checkout stays unchanged.
	changed := git("-C", repo, "show", artifact.Candidate.CandidateOID+":calc.py")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	check := exec.Command(python, "-c", changed+"\nassert add(2, 3) == 5\nassert add(-2, 3) == 1\n")
	check.Dir = work
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("candidate behavior: %s %v", out, err)
	}
	original, err := os.ReadFile(filepath.Join(repo, "calc.py"))
	if err != nil || string(original) != string(baseline) || git("-C", repo, "rev-parse", "HEAD") != base {
		t.Fatal("controller checkout changed")
	}
	if os.Getenv("AGENT_BIRD_NATIVE_CODING_PROVIDER") == "" && provider == adapter.ProviderGrok {
		if err := os.Remove(filepath.Join("/private/tmp", "codex-grok-"+grokSessionID(grant))); err != nil {
			t.Fatal(err)
		}
	}
}
