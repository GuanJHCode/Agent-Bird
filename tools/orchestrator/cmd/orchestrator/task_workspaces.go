package main

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// Reserve only the private parent and plan worktree paths here. Host remains
// the only worktree creator, under the existing Git journal, immutable base
// and candidate scope checks.
func prepareTaskWorkspaces(state, handle string, tasks []coordinator.TaskRequest) error {
	for i := range tasks {
		raws := append([]json.RawMessage{tasks[i].AdapterPayload}, tasks[i].Fallbacks...)
		for j, raw := range raws {
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil {
				return err
			}
			if p.CandidateWorkspace == nil && p.CandidateAction == nil {
				continue
			}
			if p.CandidateAction != nil && p.CandidateAction.Operation == "integrate" {
				continue
			}
			if p.Directory == "" {
				if p.CandidateWorkspace != nil {
					p.CandidateWorkspace.AutoDirectory = true
				}
				if p.CandidateAction != nil {
					p.CandidateAction.AutoDirectory = true
				}
				root, err := taskWorkspaceRoot(state)
				if err != nil {
					return err
				}
				key := sha256.Sum256([]byte(handle + "\x00" + tasks[i].ID))
				p.Directory = filepath.Join(root, "workspace-"+hex.EncodeToString(key[:12]))
				raws[j], err = json.Marshal(p)
				if err != nil {
					return err
				}
			}
		}
		tasks[i].AdapterPayload = raws[0]
		if len(raws) > 1 {
			tasks[i].Fallbacks = raws[1:]
		}
	}
	return nil
}

// The workspace root is a private sibling of the selected runtime state. Runtime
// guards protect the entire state tree, including dispatch handles; keeping Git
// worktrees beside handles would make every automatic implementation inadmissible.
func taskWorkspaceRoot(state string) (string, error) {
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || filepath.Dir(state) == state {
		return "", codeError("task_workspace_root_untrusted")
	}
	// Create only missing canonical ancestors, rejecting symlinks before descending.
	if err := taskWorkspaceParent(filepath.Dir(state)); err != nil {
		return "", err
	}
	if info, err := os.Lstat(state); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", codeError("task_workspace_root_untrusted")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	key := sha256.Sum256([]byte(state))
	root := filepath.Join(filepath.Dir(state), "codex-orchestrator-workspaces-"+hex.EncodeToString(key[:12]))
	if root == state {
		return "", codeError("task_workspace_root_untrusted")
	}
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := privateHandleParent(filepath.Join(root, "workspace")); err != nil {
		return "", codeError("task_workspace_root_untrusted")
	}
	return root, nil
}

func taskWorkspaceParent(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err = taskWorkspaceParent(filepath.Dir(path)); err != nil {
			return err
		}
		if err = os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return codeError("task_workspace_root_untrusted")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return codeError("task_workspace_root_untrusted")
	}
	return nil
}
