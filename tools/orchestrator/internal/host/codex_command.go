package host

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/pelletier/go-toml/v2"
)

// Construction only; no launcher/probe/compiler/sandbox actor is invoked. The
// original process launcher independently rechecks both executable identities.
func codexManagedCommand(ctx context.Context, p codexPreparedState, sourceEnv []string, spoolRoot string) (process.Command, codexPreparedState, error) {
	var empty process.Command
	if err := codexValidateSourceEnvironment(sourceEnv); err != nil {
		return empty, p, err
	}
	if runtime.GOOS != "darwin" || !codexCanonicalPath(spoolRoot) || spoolRoot == string(os.PathSeparator) || p.Layout.State.Path != p.Origin.StatePath || p.Layout.State.Path != filepath.Join(spoolRoot, p.Origin.Scope.AttemptID, "codex-state") || !strings.HasPrefix(p.Origin.ProtectedPath, spoolRoot+string(os.PathSeparator)) || codexVerifyManagedReferences(ctx, p) != nil || p.Descriptor.Backend != p.Origin.Backend || p.Descriptor.Lock != p.Origin.Lock {
		return empty, p, errCodexStateFile
	}
	env, err := codexOwnedCommandEnvironment(sourceEnv, p)
	if err != nil {
		return empty, p, err
	}
	args := []string{"-p", codexManagedSandboxRules(p, spoolRoot), p.Origin.Lock.Binary.Path, "app-server", "--strict-config"}
	for _, control := range p.Projection.ManagedOverrides() {
		args = append(args, "-c", control)
	}
	for _, path := range [][2]string{{"sqlite_home", p.Layout.SQLite.Path}, {"log_dir", p.Layout.Auxiliary.Runtime[3].Path}} {
		override, err := codexRuntimePathOverride(path[0], path[1])
		if err != nil {
			return empty, p, err
		}
		args = append(args, "-c", override)
	}
	cmd := process.Command{Path: "/usr/bin/sandbox-exec", PinnedPath: p.Origin.Lock.Binary.Path, PinnedSHA256: p.Origin.Lock.Binary.SHA256, Dir: p.Origin.Workspace, Args: args, Env: env, ExactEnv: true}
	digest, err := process.ExecutableDigest(ctx, cmd.Path)
	if err != nil || !codexHexDigest(digest) {
		return empty, p, errCodexStateFile
	}
	p.Launch = codexLaunchExpectation{Path: cmd.Path, SHA256: digest}
	if codexVerifyManagedReferences(ctx, p) != nil || ctx.Err() != nil {
		return empty, p, errCodexStateFile
	}
	return cmd, p, nil
}

func codexManagedSandboxRules(p codexPreparedState, spoolRoot string) string {
	rules := []string{"(version 1)", "(allow default)", "(deny file-write*)", "(deny file-link)", `(allow file-write* (literal "/dev/null"))`}
	for _, root := range []codexStateRoot{p.Layout.Sessions, p.Layout.Auxiliary.Runtime[0], p.Layout.Auxiliary.Runtime[1], p.Layout.Auxiliary.Runtime[2], p.Layout.SQLite, p.Layout.Auxiliary.Runtime[3], p.Layout.Auxiliary.Runtime[4]} {
		rules = append(rules, "(allow file-write* (subpath "+strconv.Quote(root.Path)+"))", "(deny file-write-unlink file-write-mode file-write-owner (literal "+strconv.Quote(root.Path)+"))")
	}
	rules = append(rules, "(allow file-write* (literal "+strconv.Quote(p.Layout.Home.Path+string(os.PathSeparator)+"installation_id")+"))")
	// Worker read authority excludes its control-plane spool, then permits
	// only its original state below that scope. Root/ancestor replacements and
	// generated config/auth references retain the surrounding write denial.
	rules = append(rules, "(deny file-read* (subpath "+strconv.Quote(spoolRoot)+"))", "(allow file-read* (subpath "+strconv.Quote(p.Layout.State.Path)+"))")
	// Canonicalizing the owned home observes its two original ancestors. Their
	// exact metadata is permitted; directory contents and all other spool paths
	// remain outside the worker's read authority.
	for _, parent := range []string{spoolRoot, filepath.Join(spoolRoot, p.Origin.Scope.AttemptID)} {
		rules = append(rules, "(allow file-read-metadata (literal "+strconv.Quote(parent)+"))")
	}
	return strings.Join(rules, "\n")
}

func codexRuntimePathOverride(key, value string) (string, error) {
	body, err := toml.Marshal(map[string]any{key: value})
	if err != nil {
		return "", errCodexStateFile
	}
	return strings.TrimSpace(string(body)), nil
}

func codexOwnedCommandEnvironment(sourceEnv []string, p codexPreparedState) ([]string, error) {
	if err := codexValidateSourceEnvironment(sourceEnv); err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, entry := range sourceEnv {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) {
			return nil, errCodexStateFile
		}
		if _, duplicate := values[key]; duplicate {
			return nil, errCodexStateFile
		}
		values[key] = value
	}
	values["CODEX_HOME"] = p.Layout.Home.Path
	values["TMPDIR"] = p.Layout.Auxiliary.Runtime[4].Path
	// Native structured completion does not inherit legacy helper authority.
	values["ORCHESTRATOR_REPORT_EXECUTABLE"] = ""
	values["ORCHESTRATOR_REPORT_CAPABILITY"] = ""
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env, nil
}
