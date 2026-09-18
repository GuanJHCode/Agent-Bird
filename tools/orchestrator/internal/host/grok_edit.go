package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Translate the Host-validated file ownership into process-local native edit
// grants. Never accept a rule from the request, grant a directory/glob, or alter
// Grok's existing deny/ask rules. The OS sandbox and freeze checks remain intact.
func authorizeGrokEdits(cmd process.Command, profile *adapter.ExecutionProfile, prepared *gitops.PreparedWorkspace) (process.Command, error) {
	if profile == nil || profile.Role != adapter.Implementer || profile.Permission != adapter.WorkspaceWrite || !profile.GrokSessionWrite || prepared == nil {
		return cmd, errors.New("grok_edit_profile_invalid")
	}
	root := prepared.Receipt.Worktree
	if root != cmd.Dir || !filepath.IsAbs(root) || filepath.Clean(root) != root || !grokLiteralPath(root) || len(prepared.Paths) == 0 || len(prepared.Paths) > 256 {
		return cmd, errors.New("grok_edit_path_unrepresentable")
	}
	if canonical, err := filepath.EvalSymlinks(root); err != nil || canonical != root {
		return cmd, errors.New("grok_edit_path_unrepresentable")
	}
	for _, arg := range cmd.Args {
		if arg == "--allow" || strings.HasPrefix(arg, "--allow=") || arg == "--allowedTools" || strings.HasPrefix(arg, "--allowedTools=") {
			return cmd, errors.New("grok_edit_legacy_allow_forbidden")
		}
	}
	rules := make([]string, 0, len(prepared.Paths)*2)
	seen := map[string]bool{}
	for _, rel := range prepared.Paths {
		if rel == "" || rel == "." || rel == ".." || len(rel) > 512 || filepath.IsAbs(rel) || filepath.ToSlash(filepath.Clean(rel)) != rel || strings.HasPrefix(rel, "../") || !grokLiteralPath(rel) || seen[rel] {
			return cmd, errors.New("grok_edit_path_unrepresentable")
		}
		seen[rel] = true
		current := root
		parts := strings.Split(rel, "/")
		for index, part := range parts {
			if strings.EqualFold(part, ".git") {
				return cmd, errors.New("grok_edit_path_unrepresentable")
			}
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				continue
			} // newly authorized file/parent, still rooted
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return cmd, errors.New("grok_edit_path_unrepresentable")
			}
			if index < len(parts)-1 {
				if !info.IsDir() {
					return cmd, errors.New("grok_edit_path_unrepresentable")
				}
			} else {
				if !info.Mode().IsRegular() {
					return cmd, errors.New("grok_edit_path_unrepresentable")
				}
				if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
					return cmd, errors.New("grok_edit_path_unrepresentable")
				}
			}
		}
		if actual, err := filepath.Rel(root, current); err != nil || actual != filepath.FromSlash(rel) {
			return cmd, errors.New("grok_edit_path_unrepresentable")
		}
		rules = append(rules, "--allow", "Edit("+current+")")
	}
	cmd.Args = append(append([]string(nil), cmd.Args...), rules...)
	return cmd, nil
}

func grokLiteralPath(path string) bool {
	if strings.TrimSpace(path) != path || strings.ContainsAny(path, "*?[]{}(),\\") {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
