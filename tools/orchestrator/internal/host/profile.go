package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func sandboxCommand(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, grok ...grokSandbox) (process.Command, error) {
	return sandboxCommandWithPolicy(ctx, cmd, profile, scratch, grok, nil)
}

func sandboxCommandWithPolicy(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, grok []grokSandbox, home *codexHome) (process.Command, error) {
	if runtime.GOOS != "darwin" {
		return cmd, errors.New("profile_sandbox_unsupported")
	}
	cwd, err := filepath.EvalSymlinks(cmd.Dir)
	if err != nil || cwd != cmd.Dir {
		return cmd, errors.New("profile_workspace_untrusted")
	}
	if profile == nil || profile.Version != 1 || profile.TimeoutMS < 1 || profile.TimeoutMS > 3_600_000 || ((profile.Role != adapter.Reviewer || profile.Permission != adapter.ReadOnly) && (profile.Role != adapter.Implementer || profile.Permission != adapter.WorkspaceWrite)) {
		return cmd, errors.New("profile_role_invalid")
	}
	if len(grok) > 1 || (len(grok) == 1) != profile.GrokSessionWrite {
		return cmd, errors.New("grok_session_write_required")
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		return cmd, err
	}
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return cmd, err
	}
	writeFilter := "(subpath " + strconv.Quote(scratch) + ")"
	if home != nil {
		if home.path != filepath.Join(scratch, "codex-home") || home.runtime != filepath.Join(scratch, "codex-runtime") || !filepath.IsAbs(home.source) {
			return cmd, errors.New("codex_home_unverified")
		}
		overlap := func(a, b string) bool {
			return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
		}
		if overlap(home.source, cwd) || overlap(home.source, scratch) {
			return cmd, errors.New("codex_source_home_overlap")
		}
		if profile.Role == adapter.Implementer && overlap(cwd, scratch) {
			return cmd, errors.New("codex_runtime_workspace_overlap")
		}
		for _, path := range []string{home.path, home.runtime} {
			if err := grokOwnedDirectory(path, false, true); err != nil {
				return cmd, err
			}
		}
		writeFilter = "(require-all " + writeFilter + " (require-not (subpath " + strconv.Quote(home.path) + ")))"
	}
	rules := []string{"(version 1)", "(allow default)", "(deny file-write*)", `(allow file-write* (literal "/dev/null"))`, "(allow file-write* " + writeFilter + ")"}
	if len(grok) == 1 {
		for _, path := range []string{grok[0].session, grok[0].socket} {
			if err := grokOwnedDirectory(path, false, true); err != nil {
				return cmd, err
			}
			rules = append(rules, "(allow file-write* (subpath "+strconv.Quote(path)+"))")
		}
	}
	// Production scratch is state/host-spool/launch/attempt/segment/scratch.
	spoolRoot := filepath.Dir(filepath.Dir(filepath.Dir(scratch)))
	if filepath.Base(filepath.Dir(spoolRoot)) == "host-spool" {
		state := filepath.Dir(filepath.Dir(spoolRoot))
		rules = append(rules, "(deny file-read* (subpath "+strconv.Quote(filepath.Dir(spoolRoot))+"))", "(deny file-write* (subpath "+strconv.Quote(state)+"))", "(allow file-read* (subpath "+strconv.Quote(scratch)+"))", "(allow file-write* "+writeFilter+")")
		for _, entry := range cmd.Env {
			capPath, ok := strings.CutPrefix(entry, "ORCHESTRATOR_REPORT_CAPABILITY=")
			if !ok {
				continue
			}
			reports := filepath.Join(spoolRoot, ".reports")
			relative, err := filepath.Rel(reports, capPath)
			parts := strings.Split(relative, string(filepath.Separator))
			if err != nil || len(parts) != 2 || parts[0] == ".." || parts[1] != "capability.json" {
				return cmd, errors.New("report_capability_path_untrusted")
			}
			rules = append(rules, "(allow file-read* (literal "+strconv.Quote(capPath)+"))", "(allow file-read* file-write* (subpath "+strconv.Quote(filepath.Join(filepath.Dir(capPath), "artifacts"))+"))")
			for parent := filepath.Dir(capPath); parent != state; parent = filepath.Dir(parent) {
				rules = append(rules, "(allow file-read-metadata (literal "+strconv.Quote(parent)+"))")
			}
		}

		for _, name := range []string{"control", "owners", "host-bootstrap"} {
			rules = append(rules, "(deny file-read* (subpath "+strconv.Quote(filepath.Join(state, name))+"))")
		}
		for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
			rules = append(rules, "(deny file-read* (literal "+strconv.Quote(filepath.Join(state, name))+"))")
		}
	}
	if profile.Role == adapter.Implementer {
		// A linked worktree has its own git file and an external common directory.
		// Workers can change source files, but Git candidate/ref operations stay in
		// the existing Git Host mechanism and are not authorized by this profile.
		info, err := os.Lstat(filepath.Join(cwd, ".git"))
		if err != nil || !info.Mode().IsRegular() {
			return cmd, errors.New("isolated_worktree_required")
		}
		out, err := exec.CommandContext(ctx, "/usr/bin/git", "-C", cwd, "rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir", "--git-dir").Output()
		paths := strings.Split(strings.TrimSpace(string(out)), "\n")
		if err != nil || len(paths) != 3 || paths[0] != cwd || paths[1] == cwd || paths[2] == paths[1] {
			return cmd, errors.New("isolated_worktree_required")
		}
		rules = append(rules, "(allow file-write* (subpath "+strconv.Quote(cwd)+"))", "(deny file-write* (literal "+strconv.Quote(filepath.Join(cwd, ".git"))+"))")
		for _, p := range paths[1:] {
			rules = append(rules, "(deny file-write* (subpath "+strconv.Quote(p)+"))")
		}
	}
	if home != nil {
		rules = append(rules, "(deny file-write* (subpath "+strconv.Quote(home.path)+"))")
		if home.provisionSystem {
			rules = append(rules, "(allow file-write* (subpath "+strconv.Quote(filepath.Join(home.path, "skills/.system"))+"))")
		}
		rules = append(rules, "(allow file-write* (literal "+strconv.Quote(filepath.Join(home.path, "installation_id"))+"))", "(deny file-write* (subpath "+strconv.Quote(home.source)+"))")
	}
	// TMPDIR is task-owned; no authentication/config directories are made writable.
	cmd.Env = append(cmd.Env, "TMPDIR="+scratch)
	cmd.Args = append([]string{"-p", strings.Join(rules, "\n"), cmd.Path}, cmd.Args...)
	cmd.Path = "/usr/bin/sandbox-exec"
	return cmd, nil
}
