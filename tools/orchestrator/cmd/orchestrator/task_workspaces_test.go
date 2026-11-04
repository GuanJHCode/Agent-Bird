package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
)

func TestTaskWorkspaceAllocationIdentityAndFrozenPaths(t *testing.T) {
	root := privateTaskTemp(t)
	state := filepath.Join(root, "state")
	seen := map[string]bool{}
	for _, handle := range []string{"owner-a/request-a/handle", "owner-a/request-b/handle", "owner-b/request-a/handle"} {
		tasks := []coordinator.TaskRequest{{ID: "work", AdapterPayload: json.RawMessage(`{"candidate_workspace":{"version":1}}`)}}
		path := filepath.Join(state, "dispatch", handle)
		if err := prepareTaskWorkspaces(state, path, tasks); err != nil {
			t.Fatal(err)
		}
		p, err := adapter.DecodeInvocationPayload(tasks[0].AdapterPayload)
		if err != nil {
			t.Fatal(err)
		}
		if seen[p.Directory] {
			t.Fatalf("workspace collision: %s", p.Directory)
		}
		seen[p.Directory] = true
		assertOutsideState(t, state, p.Directory)
		info, err := os.Stat(filepath.Dir(p.Directory))
		if err != nil || info.Mode().Perm() != 0700 || !owned(info) {
			t.Fatalf("workspace root not private: %v %v", info, err)
		}
		frozen := append([]byte(nil), tasks[0].AdapterPayload...)
		if err := prepareTaskWorkspaces(state, path, tasks); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(frozen, tasks[0].AdapterPayload) {
			t.Fatal("frozen payload changed")
		}
	}
	// Old saved automatic paths, explicit directories, integration and readonly
	// tasks retain their exact payload and do not depend on the new root.
	for _, raw := range []string{
		`{"directory":"/old/state/workspace","candidate_workspace":{"version":1,"auto_directory":true}}`,
		`{"directory":"/explicit/workspace","candidate_workspace":{"version":1}}`,
		`{"directory":"/repo","candidate_action":{"version":1,"operation":"integrate"}}`,
		`{"directory":"/readonly"}`,
	} {
		tasks := []coordinator.TaskRequest{{ID: "work", AdapterPayload: json.RawMessage(raw)}}
		if err := prepareTaskWorkspaces("/nonexistent/unused", filepath.Join(root, "handle"), tasks); err != nil {
			t.Fatal(err)
		}
		if string(tasks[0].AdapterPayload) != raw {
			t.Fatal("existing payload changed")
		}
	}
}

func TestTaskWorkspaceRootRejectsUnsafeExistingPaths(t *testing.T) {
	for _, mode := range []string{"public-root", "file-root", "symlink-root", "symlink-parent", "symlink-state"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTaskTemp(t)
			state := filepath.Join(root, "state")
			workspaceRoot, err := taskWorkspaceRoot(state)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "public-root":
				err = os.Chmod(workspaceRoot, 0755)
			case "file-root":
				if err = os.Remove(workspaceRoot); err == nil {
					err = os.WriteFile(workspaceRoot, []byte("foreign"), 0600)
				}
			case "symlink-root":
				if err = os.Remove(workspaceRoot); err == nil {
					err = os.Symlink(root, workspaceRoot)
				}
			case "symlink-parent":
				alias := filepath.Join(root, "alias")
				err = os.Symlink(root, alias)
				state = filepath.Join(alias, "state")
			case "symlink-state":
				err = os.Symlink(root, state)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = taskWorkspaceRoot(state); err == nil {
				t.Fatal("unsafe workspace root accepted")
			}
		})
	}
}

func TestTaskWorkspaceRootUsesSelectedDefaultState(t *testing.T) {
	root := privateTaskTemp(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	state, err := resolveState("")
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot, err := taskWorkspaceRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(workspaceRoot) != filepath.Dir(state) {
		t.Fatalf("workspace not beside selected default state: %s, %s", state, workspaceRoot)
	}
	assertOutsideState(t, state, workspaceRoot)
}

func assertOutsideState(t *testing.T, state, directory string) {
	t.Helper()
	contains := func(parent, child string) bool {
		for {
			if child == parent {
				return true
			}
			next := filepath.Dir(child)
			if next == child {
				return false
			}
			child = next
		}
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || contains(state, directory) || contains(directory, state) {
		t.Fatalf("workspace overlaps protected state: %s, %s", state, directory)
	}
}
