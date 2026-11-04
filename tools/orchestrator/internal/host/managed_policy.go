package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

// managedWritePaths projects the Host materialized workspace into exact source
// files. Git metadata and symlink targets never become worker write authority.
func managedWritePaths(ctx context.Context, cwd string, spec *adapter.CandidateWorkspace) ([]string, []string, error) {
	fail := func() ([]string, []string, error) { return nil, nil, errors.New("managed_write_policy_untrusted") }
	if spec == nil || spec.Version != 1 || len(spec.Paths) == 0 {
		return fail()
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil || !filepath.IsAbs(cwd) || resolved != cwd {
		return fail()
	}
	info, err := os.Lstat(filepath.Join(cwd, ".git"))
	if err != nil || !info.Mode().IsRegular() {
		return fail()
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/git", "-C", cwd, "rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir", "--git-dir")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	body, err := cmd.Output()
	git := strings.Split(strings.TrimSpace(string(body)), "\n")
	if err != nil || len(git) != 3 || git[0] != cwd || git[1] == cwd || git[1] == git[2] {
		return fail()
	}
	protected := []string{filepath.Join(cwd, ".git")}
	for _, raw := range git[1:] {
		path, err := filepath.EvalSymlinks(raw)
		if err != nil || path != raw {
			return fail()
		}
		protected = append(protected, path)
	}
	paths := []string{}
	seen := map[string]bool{}
	for _, path := range spec.Paths {
		if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\x00\r\n") {
			return fail()
		}
		for _, part := range strings.Split(path, string(filepath.Separator)) {
			if strings.EqualFold(part, ".git") || part == ".codex" || part == ".claude" || part == ".claude.json" || part == ".mcp.json" {
				return fail()
			}
		}
		absolute := filepath.Join(cwd, path)
		for parent := absolute; parent != cwd; parent = filepath.Dir(parent) {
			info, err := os.Lstat(parent)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 || (parent == absolute && !info.Mode().IsRegular()) || (parent != absolute && !info.IsDir()) {
				return fail()
			}
		}
		if seen[absolute] {
			return fail()
		}
		seen[absolute] = true
		paths = append(paths, absolute)
	}
	return paths, protected, nil
}

// Claude writes through a sibling temporary file before rename. This narrowly
// derived namespace grants no parent directory or unrelated file authority.
func claudeAtomicWriteRules(paths []string) ([]string, error) {
	rules := []string{}
	for _, path := range paths {
		pattern := "^" + regexp.QuoteMeta(path) + `\.tmp\.[0-9]+\.[0-9a-f]{12}$`
		match, err := regexp.Compile(pattern)
		if err != nil {
			return nil, errors.New("managed_write_policy_untrusted")
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			return nil, errors.New("managed_write_policy_untrusted")
		}
		for _, entry := range entries {
			if match.MatchString(filepath.Join(filepath.Dir(path), entry.Name())) {
				return nil, errors.New("claude_atomic_path_collision")
			}
		}
		// Seatbelt regex does not implement counted repetitions. Expand the
		// fixed width while preserving the same language as the collision scan.
		nativePattern := strings.ReplaceAll(pattern, "[0-9a-f]{12}", strings.Repeat("[0-9a-f]", 12))
		rules = append(rules, "(allow file-write* (regex "+strconv.Quote(nativePattern)+"))")
	}
	return rules, nil
}
