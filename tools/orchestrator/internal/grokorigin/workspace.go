//go:build !windows

package grokorigin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// These sources can introduce hooks, subprocess servers or additional agent
// tooling. Plain AGENTS/CLAUDE instruction files are deliberately not authority.
var workspaceCapabilities = []string{
	".grok/config.toml", ".mcp.json", ".claude/settings.json", ".claude/settings.local.json",
	".grok/hooks", ".claude/hooks", ".grok/plugins", ".claude/plugins", ".claude-plugin", ".grok-plugin",
	".grok/skills", ".grok/agents", ".grok/commands", ".claude/skills", ".claude/agents", ".claude/commands",
	".agents/skills", ".agents/commands", ".grok/lsp.json", ".lsp.json",
}

// inspectWorkspace refuses active project capability sources and returns all
// possible ancestor sources for the OS sandbox to deny, including ancestors
// above the current repository boundary. The latter seals late source creation
// and removal of a .git boundary after this read-only preflight.
func inspectWorkspace(workspace string) ([]string, error) {
	if !validPath(workspace) {
		return nil, errors.New("grokorigin_workspace_untrusted")
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil || canonical != workspace {
		return nil, errors.New("grokorigin_workspace_untrusted")
	}
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("grokorigin_workspace_untrusted")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("grokorigin_workspace_untrusted")
	}
	denied := make([]string, 0, len(workspaceCapabilities)*8)
	insideRepository := true
	for dir, depth := workspace, 0; ; dir, depth = filepath.Dir(dir), depth+1 {
		if depth >= 64 {
			return nil, errors.New("grokorigin_policy_limit")
		}
		for _, source := range workspaceCapabilities {
			path := filepath.Join(dir, filepath.FromSlash(source))
			denied = append(denied, path)
			if insideRepository {
				if capabilitySourcePresentOrUnsafe(dir, source) {
					return nil, errors.New("grokorigin_capability_source")
				}
			}
		}
		if insideRepository {
			marker, err := os.Lstat(filepath.Join(dir, ".git"))
			switch {
			case err == nil:
				if !marker.IsDir() && !marker.Mode().IsRegular() {
					return nil, errors.New("grokorigin_workspace_untrusted")
				}
				insideRepository = false
			case !os.IsNotExist(err):
				return nil, errors.New("grokorigin_workspace_untrusted")
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return denied, nil
}

func capabilitySourcePresentOrUnsafe(dir, relative string) bool {
	parts := strings.Split(relative, "/")
	for i, part := range parts {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return false
		}
		if err != nil || i == len(parts)-1 || !info.IsDir() {
			return true
		}
	}
	return false
}
