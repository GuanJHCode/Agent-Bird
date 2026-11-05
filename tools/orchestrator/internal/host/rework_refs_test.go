package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func refsPromptFixture(t *testing.T) (string, contract.LaunchCommand, adapter.InvocationPayload) {
	t.Helper()
	state := t.TempDir()
	root := filepath.Join(state, "rework-objects")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	makeRef := func(kind string, revision int, prompt, feedback, acceptance string) adapter.ReworkObjectRef {
		b := adapter.ReworkBody{Version: 3, Kind: kind, Owner: "owner", RunID: "run", TaskID: "task", WorkRevision: revision, Prompt: prompt, Feedback: feedback, Acceptance: acceptance}
		raw, _ := json.Marshal(b)
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		path := filepath.Join(root, hash+".json")
		if e := os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		info, _ := os.Stat(path)
		st := info.Sys().(*syscall.Stat_t)
		return adapter.ReworkObjectRef{ID: hash, Owner: "owner", RunID: "run", TaskID: "task", WorkRevision: revision, Kind: kind, Size: int64(len(raw)), SHA256: hash, Path: path, Device: uint64(st.Dev), Inode: st.Ino}
	}
	refs := adapter.ReworkRefs{Version: 3, Owner: "owner", RunID: "run", TaskID: "task", WorkRevision: 3, Brief: makeRef("brief", 1, "original immutable acceptance", "", ""), Conditions: []adapter.ReworkObjectRef{makeRef("condition", 1, "", strings.Repeat("long反馈", 12000), "keep original A"), makeRef("condition", 2, "", "fix second defect", "keep original B")}}
	c := adapter.ReworkContext{Version: 3, RunID: "run", TaskID: "task", PreviousWorkRevision: 2, PreviousCandidateOID: strings.Repeat("a", 40), CandidateEventID: "event", CandidateSHA256: strings.Repeat("b", 64), Source: "owner", OwnerDecisionID: "command", RefsV3: &refs}
	p := adapter.InvocationPayload{Prompt: adapter.ReworkPromptMarker, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, BaseOID: c.PreviousCandidateOID, Rework: &c}}
	return state, contract.LaunchCommand{RunID: "run", TaskID: "task", WorkRevision: 3}, p
}
func TestReworkRefsHostPreservesEveryConditionDeterministically(t *testing.T) {
	state, g, p := refsPromptFixture(t)
	text, e := ReworkPrompt(g, p, state)
	if e != nil {
		t.Fatal(e)
	}
	again, e := ReworkPrompt(g, p, state)
	if e != nil || again != text {
		t.Fatal("nondeterministic assembly")
	}
	for _, s := range []string{"original immutable acceptance", strings.Repeat("long反馈", 12000), "keep original A", "fix second defect", "keep original B"} {
		if strings.Count(text, s) != 1 {
			t.Fatalf("condition absent/duplicated %q", s[:min(20, len(s))])
		}
	}
	if strings.Index(text, "keep original A") > strings.Index(text, "fix second defect") {
		t.Fatal("historical condition order lost")
	}
}
func TestReworkRefsHostRejectsInvalidReferences(t *testing.T) {
	for _, kind := range []string{"owner", "run", "task", "revision", "size", "hash", "path", "missing", "truncated", "tampered", "symlink", "marker", "root", "unknown-version", "hardlink", "mode", "root-mode"} {
		t.Run(kind, func(t *testing.T) {
			state, g, p := refsPromptFixture(t)
			r := &p.CandidateWorkspace.Rework.RefsV3.Conditions[0]
			switch kind {
			case "owner":
				r.Owner = "other"
			case "run":
				r.RunID = "other"
			case "task":
				r.TaskID = "other"
			case "revision":
				r.WorkRevision = 2
			case "size":
				r.Size++
			case "hash":
				r.SHA256 = strings.Repeat("0", 64)
			case "path":
				r.Path = filepath.Join(state, "outside")
			case "missing":
				os.Remove(r.Path)
			case "truncated":
				os.Truncate(r.Path, r.Size-1)
			case "tampered":
				f, _ := os.OpenFile(r.Path, os.O_WRONLY, 0)
				f.WriteAt([]byte("X"), 20)
				f.Close()
			case "symlink":
				held := r.Path + ".held"
				os.Rename(r.Path, held)
				os.Symlink(held, r.Path)
			case "marker":
				p.Prompt = "fake"
			case "root":
				state = filepath.Join(state, "other")
			case "hardlink":
				os.Link(r.Path, r.Path+".link")
			case "mode":
				os.Chmod(r.Path, 0644)
			case "root-mode":
				os.Chmod(filepath.Join(state, "rework-objects"), 0755)
			case "unknown-version":
				p.CandidateWorkspace.Rework.Version = 4
			}
			if _, e := ReworkPrompt(g, p, state); e == nil {
				t.Fatal("invalid ref accepted")
			}
		})
	}
}
