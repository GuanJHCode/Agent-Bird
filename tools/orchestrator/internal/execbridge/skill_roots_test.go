package execbridge

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeTurnDiffCanProbeAllAncestorMarkersAfterNearestMatch(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(parent, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sealed, err := SealSkillRoots(work, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	for dir := work; ; dir = filepath.Dir(dir) {
		params := map[string]any{"path": (&url.URL{Scheme: "file", Path: filepath.Join(dir, ".git")}).String(), "sandbox": nil}
		if !sealed.Allows("fs/getMetadata", params) {
			t.Errorf("fixed native ancestor probe rejected: %s", dir)
		}
		if sealed.Allows("fs/readFile", params) {
			t.Error("metadata scope granted file content")
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if err := os.WriteFile(filepath.Join(parent, ".git"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := sealed.Verify(); err == nil {
		t.Error("ancestor marker replacement accepted")
	}
}

func skillFixture(t *testing.T) (string, string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, ".agents", "skills")
	if err := os.MkdirAll(filepath.Join(root, "example", "agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "example", "SKILL.md"), []byte("---\nname: example\ndescription: A fixture.\n---\nKeep the task isolated.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "example", "agents", "openai.yaml"), []byte("interface:\n  display_name: Example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return repo, root
}

func TestBoundSkillMetadataRemainsReadonlyAfterToolsEnabled(t *testing.T) {
	cwd, root := skillFixture(t)
	scope, err := SealSkillRoots(cwd, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	b := &Broker{tools: true, ready: true, skillRoots: scope, pending: map[string]requestInfo{}, seen: map[string]bool{}}
	uri := (&url.URL{Scheme: "file", Path: root}).String()
	raw := []byte(`{"id":1,"method":"fs/getMetadata","params":{"path":` + string(mustJSON(t, uri)) + `,"sandbox":null}}`)
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	if err := b.checkFrame(frame, raw, true); err != nil {
		t.Fatal("bound native skill metadata rejected:", err)
	}
	for _, method := range []string{"fs/readFile", "fs/walk", "fs/canonicalize", "fs/writeFile", "fs/remove", "process/start"} {
		if scope.Allows(method, map[string]any{"path": uri, "sandbox": nil}) {
			t.Fatalf("background metadata widened %s", method)
		}
	}
}

func TestProjectInstructionProbesAreSealedWithoutOpeningOtherFiles(t *testing.T) {
	cwd, _ := skillFixture(t)
	path := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(path, []byte("Keep isolated edits."), 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := SealSkillRoots(cwd, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md"} {
		params := map[string]any{"path": (&url.URL{Scheme: "file", Path: filepath.Join(cwd, name)}).String(), "sandbox": nil}
		if !scope.Allows("fs/getMetadata", params) {
			t.Errorf("native instruction probe rejected: %s", name)
		}
	}
	params := map[string]any{"path": (&url.URL{Scheme: "file", Path: filepath.Join(cwd, "ordinary.py")}).String(), "sandbox": nil}
	if scope.Allows("fs/getMetadata", params) || scope.Allows("fs/readFile", params) {
		t.Fatal("ordinary file entered native instruction scope")
	}
	if err := os.WriteFile(path, []byte("Changed instruction."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := scope.Verify(); err == nil {
		t.Fatal("changed project instruction accepted")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSkillRootScopeRejectsUnboundOrAmbiguousURI(t *testing.T) {
	cwd, root := skillFixture(t)
	scope, err := SealSkillRoots(cwd, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"file://localhost" + root, "file://" + root + "?q=1", "file://" + root + "#x",
		"file://" + root + "/../skills", "file://" + root + "-other", "file://" + root + "/example/SKILL.md",
		"file://" + root + "%2f", "file://" + root + "%252f", root,
	} {
		if scope.Allows("fs/getMetadata", map[string]any{"path": path, "sandbox": nil}) {
			t.Fatal("unbound URI accepted")
		}
	}
	uri := (&url.URL{Scheme: "file", Path: root}).String()
	if scope.Allows("fs/getMetadata", map[string]any{"path": uri}) {
		t.Fatal("missing sandbox accepted")
	}
	if scope.Allows("fs/getMetadata", map[string]any{"path": uri, "sandbox": map[string]any{}}) {
		t.Fatal("unknown policy accepted")
	}
	for _, extra := range []map[string]any{{"followSymlinks": true}, {"followSymlinks": false}, {"unknown": nil}} {
		extra["path"], extra["sandbox"] = uri, nil
		if scope.Allows("fs/getMetadata", extra) {
			t.Fatal("non-default metadata caller accepted")
		}
	}
}

func TestSkillRootSealDetectsContentMembershipAndMissingRootChanges(t *testing.T) {
	for _, change := range []string{"content", "membership", "new-root", "symlink"} {
		t.Run(change, func(t *testing.T) {
			cwd, root := skillFixture(t)
			scope, err := SealSkillRoots(cwd, []string{".git"})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "content":
				err = os.WriteFile(filepath.Join(root, "example", "SKILL.md"), []byte("changed"), 0600)
			case "membership":
				err = os.WriteFile(filepath.Join(root, "added"), []byte("new"), 0600)
			case "new-root":
				err = os.MkdirAll(filepath.Join(cwd, ".codex", "skills"), 0700)
			case "symlink":
				err = os.Symlink(filepath.Join(root, "example", "SKILL.md"), filepath.Join(root, "link"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if scope.Verify() == nil {
				t.Fatal("changed skill input accepted")
			}
		})
	}
}

func TestSkillRootSealRejectsSymlinkAndHardlink(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			cwd, root := skillFixture(t)
			target := filepath.Join(root, "example", "SKILL.md")
			link := filepath.Join(root, "link")
			var err error
			if kind == "symlink" {
				err = os.Symlink(target, link)
			} else {
				err = os.Link(target, link)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := SealSkillRoots(cwd, []string{".git"}); err == nil {
				t.Fatal("linked skill input accepted")
			}
		})
	}
}

func skillBroker(scope *SkillRoots) *Broker {
	return &Broker{tools: true, ready: true, skillRoots: scope, pending: map[string]requestInfo{}, seen: map[string]bool{}}
}

func checkSkillFrame(t *testing.T, b *Broker, message map[string]any, request bool) error {
	t.Helper()
	raw := mustJSON(t, message)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return b.checkFrame(decoded, raw, request)
}

func TestSkillMetadataExceptionRetainsFrameAndToolPolicy(t *testing.T) {
	cwd, root := skillFixture(t)
	scope, err := SealSkillRoots(cwd, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: root}).String()
	for _, method := range []string{"fs/readFile", "fs/walk", "fs/canonicalize", "fs/readDirectory", "fs/open", "fs/readBlock", "fs/close", "fs/writeFile", "fs/remove", "fs/copy", "fs/createDirectory", "process/start"} {
		t.Run(method, func(t *testing.T) {
			if err := checkSkillFrame(t, skillBroker(scope), map[string]any{"id": 1, "method": method, "params": map[string]any{"path": uri, "sandbox": nil}}, true); err == nil {
				t.Fatal("metadata exception admitted another method")
			}
		})
	}
	if err := checkSkillFrame(t, skillBroker(scope), map[string]any{"id": 1, "method": "fs/getMetadata", "params": map[string]any{"path": uri, "sandbox": nil}, "padding": strings.Repeat("x", maxFrame)}, true); err == nil {
		t.Fatal("metadata exception bypassed frame limit")
	}
	b := skillBroker(scope)
	b.ready = false
	if err := checkSkillFrame(t, b, map[string]any{"id": 1, "method": "fs/getMetadata", "params": map[string]any{"path": uri, "sandbox": nil}}, true); err == nil {
		t.Fatal("metadata exception bypassed initialization")
	}
}

func TestSkillMetadataResponseBoundToSealedRequest(t *testing.T) {
	for _, scenario := range []string{"present", "absent", "wrong-type", "wrong-size", "wrong-mtime", "missing-created", "fractional-created", "out-of-range-created", "symlink", "unexpected-error", "missing-error", "absent-permission-error", "absent-error-data", "absent-error-message", "mutated-before-response"} {
		t.Run(scenario, func(t *testing.T) {
			cwd, root := skillFixture(t)
			scope, err := SealSkillRoots(cwd, []string{".git"})
			if err != nil {
				t.Fatal(err)
			}
			path := root
			if strings.HasPrefix(scenario, "absent") || scenario == "missing-error" {
				// A nested CWD has an absent skill root even when the repo has skills.
				path = filepath.Join(cwd, "nested", ".agents", "skills")
				if err := os.Mkdir(filepath.Join(cwd, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				scope, err = SealSkillRoots(filepath.Join(cwd, "nested"), []string{".git"})
				if err != nil {
					t.Fatal(err)
				}
			}
			b := skillBroker(scope)
			if err := checkSkillFrame(t, b, map[string]any{"id": 1, "method": "fs/getMetadata", "params": map[string]any{"path": (&url.URL{Scheme: "file", Path: path}).String(), "sandbox": nil}}, true); err != nil {
				t.Fatal(err)
			}
			result := map[string]any{"isDirectory": true, "isFile": false, "isSymlink": false, "size": scope.probes[path].Size, "createdAtMs": 0, "modifiedAtMs": scope.probes[path].Modified / 1_000_000}
			response := map[string]any{"id": 1, "result": result}
			switch scenario {
			case "absent", "unexpected-error", "absent-permission-error", "absent-error-data", "absent-error-message":
				failure := map[string]any{"code": -32004, "message": "not found"}
				response = map[string]any{"id": 1, "error": failure}
				if scenario == "absent-permission-error" {
					failure["code"] = -32003
				}
				if scenario == "absent-error-data" {
					failure["data"] = map[string]any{"extra": true}
				}
				if scenario == "absent-error-message" {
					delete(failure, "message")
				}
			case "wrong-type":
				result["isDirectory"], result["isFile"] = false, true
			case "wrong-size":
				result["size"] = scope.probes[path].Size + 1
			case "wrong-mtime":
				result["modifiedAtMs"] = scope.probes[path].Modified/1_000_000 + 1
			case "missing-created":
				delete(result, "createdAtMs")
			case "fractional-created":
				result["createdAtMs"] = 0.5
			case "out-of-range-created":
				result["createdAtMs"] = 1e30
			case "symlink":
				result["isSymlink"] = true
			case "mutated-before-response":
				if err := os.Rename(root, root+"-moved"); err != nil {
					t.Fatal(err)
				}
			}
			err = checkSkillFrame(t, b, response, false)
			wantOK := scenario == "present" || scenario == "absent"
			if (err == nil) != wantOK {
				t.Fatalf("unexpected response validation: %v", err)
			}
		})
	}
}

func TestSkillRootsUseNearestProjectAndExactAncestorPaths(t *testing.T) {
	repo, repoSkills := skillFixture(t)
	nested := filepath.Join(repo, "parent", "child")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, nestedProject := range []bool{false, true} {
		if nestedProject {
			if err := os.WriteFile(filepath.Join(repo, "parent", ".git"), []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		scope, err := SealSkillRoots(nested, []string{".git"})
		if err != nil {
			t.Fatal(err)
		}
		if _, allowed := scope.probes[repoSkills]; allowed == nestedProject {
			t.Fatal("skill roots crossed nearest project boundary")
		}
		for _, root := range []string{nested, filepath.Dir(nested)} {
			uri := (&url.URL{Scheme: "file", Path: filepath.Join(root, ".agents", "skills")}).String()
			if !scope.Allows("fs/getMetadata", map[string]any{"path": uri, "sandbox": nil}) {
				t.Fatal("bound ancestor metadata rejected")
			}
		}
		project := repo
		if nestedProject {
			project = filepath.Dir(nested)
		}
		for _, marker := range []string{filepath.Join(nested, ".git"), filepath.Join(project, ".git"), filepath.Join(filepath.Dir(project), ".git")} {
			if !scope.Allows("fs/getMetadata", map[string]any{"path": (&url.URL{Scheme: "file", Path: marker}).String(), "sandbox": nil}) {
				t.Fatal("pinned resolver's exact marker probe rejected")
			}
		}
		// Fixed native turn-diff discovery probes all ancestor .git markers,
		// but instruction and skill content still stop at the nearest project.
		for _, unbound := range []string{filepath.Join(project, ".hg"), filepath.Join(filepath.Dir(project), ".agents", "skills"), filepath.Join(filepath.Dir(project), "AGENTS.md")} {
			if scope.Allows("fs/getMetadata", map[string]any{"path": (&url.URL{Scheme: "file", Path: unbound}).String(), "sandbox": nil}) {
				t.Fatal("root resolution widened beyond pinned project boundary")
			}
		}
	}
	for _, markers := range [][]string{nil, {""}, {".."}, {"../.git"}, {"/.git"}, {"a\\b"}, {"a\x00b"}} {
		if _, err := SealSkillRoots(nested, markers); err == nil {
			t.Fatal("unsafe project marker accepted")
		}
	}
}

func TestSkillInputDriftBlocksEnablingTools(t *testing.T) {
	cwd, root := skillFixture(t)
	scope, err := SealSkillRoots(cwd, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "example", "agents", "openai.yaml"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	b := skillBroker(scope)
	b.tools = false
	if err := b.EnableTools(); err == nil || b.tools {
		t.Fatal("changed skill input enabled tools")
	}
}

func TestSkillRootSealBoundsAndProjectConfigSkills(t *testing.T) {
	for _, scenario := range []string{"writable-file", "writable-directory", "oversize-file", "excess-depth", "config-skill-change", "config-empty-layer"} {
		t.Run(scenario, func(t *testing.T) {
			cwd, root := skillFixture(t)
			skill := filepath.Join(root, "example", "SKILL.md")
			var err error
			switch scenario {
			case "writable-file":
				err = os.Chmod(skill, 0660)
			case "writable-directory":
				err = os.Chmod(root, 0770)
			case "oversize-file":
				err = os.Truncate(skill, 1024*1024+1)
			case "excess-depth":
				err = os.MkdirAll(filepath.Join(root, strings.Repeat("nested/", 19)), 0700)
			case "config-skill-change", "config-empty-layer":
				configRoot := filepath.Join(cwd, ".codex", "skills")
				if err := os.MkdirAll(configRoot, 0700); err != nil {
					t.Fatal(err)
				}
				if scenario == "config-skill-change" {
					if err := os.WriteFile(filepath.Join(configRoot, "SKILL.md"), []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				scope, err := SealSkillRoots(cwd, []string{".git"})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configRoot, "SKILL.md"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := scope.Verify(); err == nil {
					t.Fatal("project config skill drift accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := SealSkillRoots(cwd, []string{".git"}); err == nil {
				t.Fatal("unsafe or excessive skill input accepted")
			}
		})
	}
}
