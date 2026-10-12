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
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
)

func TestCodexPluginCandidateToolsArePrivateAndDurable(t *testing.T) {
	h, g, old, env := codexOriginFixture(t)
	repo := old.WorkingDirectory()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %s: %v", b, e)
		}
		return string(b)
	}
	git("init", "-q")
	if e := os.WriteFile(filepath.Join(repo, "answer.txt"), []byte("before\n"), 0755); e != nil {
		t.Fatal(e)
	}
	git("add", "answer.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	var payload adapter.InvocationPayload
	if e := json.Unmarshal(g.AdapterPayload, &payload); e != nil {
		t.Fatal(e)
	}
	payload.Profile.Role = adapter.Implementer
	payload.Profile.Permission = adapter.WorkspaceWrite
	payload.Profile.CodexHostToolsVersion = 1
	payload.Profile.CLIModelDefault = false
	payload.Profile.Model = adapter.CodexPluginCompatibilityModel
	payload.Directory = filepath.Join(filepath.Dir(repo), "candidate")
	payload.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1, AutoDirectory: true, RepoRoot: repo, BaseOID: strings.TrimSpace(git("rev-parse", "HEAD")), Paths: []string{"answer.txt", "new.txt"}}
	inv, e := adapter.BuildCodexPluginInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: payload.ProviderLock.Binary, CWD: payload.Directory, Prompt: payload.Prompt, Profile: payload.Profile, Lock: payload.ProviderLock, Workspace: payload.CandidateWorkspace})
	if e != nil {
		t.Fatal(e)
	}
	g.AdapterPayload, _ = json.Marshal(payload)
	ctx := context.Background()
	freeze, closeCandidate, candidate, e := h.prepareCandidate(ctx, g, inv, inv.ExecutionProfile())
	if e != nil {
		t.Fatal(e)
	}
	defer closeCandidate()
	prepared, e := h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{Candidate: candidate})
	if e != nil {
		t.Fatal(e)
	}
	if prepared.Origin.Workspace != repo || candidate.Receipt.Worktree == repo {
		t.Fatal("provider received candidate write root")
	}
	tools, closeTools, e := openCodexCandidateTools(ctx, *candidate, prepared.Protected)
	if e != nil {
		t.Fatal(e)
	}
	defer closeTools()
	read := callCodexReadTool(t, tools, "read_file", `{"path":"answer.txt"}`)
	var file struct {
		SHA256 string `json:"sha256"`
	}
	if e = json.Unmarshal([]byte(read.Text), &file); e != nil || !read.Success {
		t.Fatal(read, e)
	}
	for _, tc := range []struct {
		path, hash, content string
		want                bool
	}{{"answer.txt", file.SHA256, "after\n", true}, {"new.txt", "", "new\n", true}, {"outside.txt", "", "forbidden", false}, {"answer.txt", file.SHA256, "stale", false}} {
		args, _ := json.Marshal(map[string]string{"path": tc.path, "expectedSHA256": tc.hash, "content": tc.content})
		result := callCodexReadTool(t, tools, "write_file", string(args))
		if result.Success != tc.want {
			t.Fatalf("%s: %#v", tc.path, result)
		}
	}
	original, e := os.ReadFile(filepath.Join(repo, "answer.txt"))
	if e != nil || string(original) != "before\n" {
		t.Fatal("original changed", e)
	}
	for _, phase := range []string{"intent", "receipt"} {
		st, e := os.Stat(filepath.Join(prepared.Protected.Path, "codex-tool-000001-"+phase+".json"))
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatal("missing private durable write evidence", e)
		}
	}
	if e = closeTools(); e != nil {
		t.Fatal(e)
	}
	artifact, e := freeze(ctx, "done")
	if e != nil || !json.Valid(artifact) {
		t.Fatal("freeze", e)
	}
	if e = codexCheckManagedBinding(prepared, codexsession.ThreadBinding{ThreadID: "00000000-0000-4000-8000-000000000001", Model: "gpt-5.6-sol", ModelProvider: "openai", HistoryMode: "paginated", RolloutPath: filepath.Join(prepared.Layout.Home.Path, "sessions", "2026", "10", "12", "rollout-2026-10-12T00-00-00-00000000-0000-4000-8000-000000000001.jsonl")}); e != nil {
		t.Fatal("compatibility model binding", e)
	}
}

func TestCodexWriteArgumentsRejectNull(t *testing.T) {
	for _, raw := range []string{`{"path":"a","expectedSHA256":"","content":null}`, `{"path":"a","expectedSHA256":null,"content":"x"}`, `{"path":"a","path":"b","expectedSHA256":"","content":"x"}`} {
		if _, ok := codexWriteArguments([]byte(raw)); ok {
			t.Fatal(raw)
		}
	}
}
