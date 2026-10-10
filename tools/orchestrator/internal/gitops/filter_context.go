package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const (
	filterConfigMaximumBytes     = 256 << 10
	filterAttributeMaximumBytes  = 64 << 10
	filterAttributesMaximumBytes = 1 << 20
)

type filterGitContextKey struct{}
type filterGitPrivateKey struct{}

// FilterGitContext is live Host-only evidence. It intentionally has no JSON
// representation: a decoded payload cannot acquire a pinned executor.
type FilterGitContext struct {
	repoRoot, commonDir          string
	repoIdentity, commonIdentity DirectoryIdentity
	manifest                     contract.GitFilterManifestV1
	git, clean, smudge           filterPinnedFile
	config                       []byte
	attributes                   map[string]filterPinnedFile
	digest                       string
	mu                           sync.Mutex
	closed                       bool
}

type filterPinnedFile struct {
	path, sha256 string
	identity     FileIdentity
}

func FilterConfigurationSHA256(m contract.GitFilterManifestV1) string {
	v := struct {
		Version  int    `json:"version"`
		DriverID string `json:"driver_id"`
		Clean    string `json:"clean_command"`
		Smudge   string `json:"smudge_command"`
	}{1, m.DriverID, filterShellCommand(m.Clean), filterShellCommand(m.Smudge)}
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func filterShellCommand(tool contract.GitFilterToolPin) string {
	parts := append([]string{tool.Path}, tool.Args...)
	for i := range parts {
		parts[i] = "'" + strings.ReplaceAll(parts[i], "'", "'\\''") + "'"
	}
	return strings.Join(parts, " ")
}

func filterGitEnv() []string {
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0"}
}

func filterGitConfigArgs(driver string) []string {
	return []string{"--no-replace-objects", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false", "-c", "filter." + driver + ".clean=", "-c", "filter." + driver + ".smudge=", "-c", "filter." + driver + ".process=", "-c", "filter." + driver + ".required=false", "-c", "filter." + driver + ".delay=false"}
}

func OpenFilterGit(ctx context.Context, repoRoot string, manifest contract.GitFilterManifestV1) (*FilterGitContext, error) {
	if err := manifest.Validate(); err != nil {
		return nil, fail(CodeInvalidInput, "filter git manifest", err)
	}
	repoRoot = CanonicalPath(repoRoot)
	repoIdentity, err := directoryIdentity(repoRoot)
	if err != nil {
		return nil, err
	}
	g := &FilterGitContext{repoRoot: repoRoot, repoIdentity: repoIdentity, manifest: manifest.Clone(), attributes: map[string]filterPinnedFile{}}
	if g.git, err = pinFilterFile(manifest.Git.Path, manifest.Git.SHA256, 32<<20); err != nil {
		return nil, err
	}
	if g.clean, err = pinFilterFile(manifest.Clean.Path, manifest.Clean.SHA256, 32<<20); err != nil {
		return nil, err
	}
	if g.smudge, err = pinFilterFile(manifest.Smudge.Path, manifest.Smudge.SHA256, 32<<20); err != nil {
		return nil, err
	}
	private := context.WithValue(g.Context(ctx), filterGitPrivateKey{}, true)
	version, err := g.privateText(private, repoRoot, "--version")
	if err != nil || version != manifest.Git.Version {
		return nil, fail(CodeInvalidInput, "filter git pin", fmt.Errorf("version mismatch: %w", err))
	}
	common, err := g.privateText(private, repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	g.commonDir = CanonicalPath(common)
	g.commonIdentity, err = directoryIdentity(g.commonDir)
	if err != nil {
		return nil, err
	}
	if err := g.capture(private); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *FilterGitContext) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, filterGitContextKey{}, g)
}
func filterGitFromContext(ctx context.Context) *FilterGitContext {
	g, _ := ctx.Value(filterGitContextKey{}).(*FilterGitContext)
	return g
}

func (g *FilterGitContext) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	return nil
}
func (g *FilterGitContext) SHA256() string { g.mu.Lock(); defer g.mu.Unlock(); return g.digest }

func (g *FilterGitContext) verifyAfter(runErr error) error {
	if err := g.Verify(context.Background()); err != nil {
		return err
	}
	return runErr
}

func (g *FilterGitContext) Verify(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return fail(CodeInvalidInput, "filter git", fmt.Errorf("closed context"))
	}
	if actual, err := directoryIdentity(g.repoRoot); err != nil || actual != g.repoIdentity {
		return fail(CodeIdentityChanged, "filter git root", err)
	}
	if actual, err := directoryIdentity(g.commonDir); err != nil || actual != g.commonIdentity {
		return fail(CodeIdentityChanged, "filter git common", err)
	}
	private := context.WithValue(g.Context(ctx), filterGitPrivateKey{}, true)
	return g.verifyCaptured(private)
}

func (g *FilterGitContext) verifyDirectory(dir string) error {
	if CanonicalPath(dir) != g.repoRoot {
		return fail(CodeIdentityChanged, "filter git directory", fmt.Errorf("foreign repository"))
	}
	actual, err := directoryIdentity(dir)
	if err != nil || actual != g.repoIdentity {
		return fail(CodeIdentityChanged, "filter git directory", err)
	}
	return nil
}

func (g *FilterGitContext) capture(ctx context.Context) error {
	config, attrs, digest, err := g.readState(ctx)
	if err != nil {
		return err
	}
	g.config, g.attributes, g.digest = config, attrs, digest
	return nil
}
func (g *FilterGitContext) verifyCaptured(ctx context.Context) error {
	config, attrs, digest, err := g.readState(ctx)
	if err != nil {
		return err
	}
	if !sameIdentity(gitFilterIdentity(g.git), g.git.identity) || !sameIdentity(gitFilterIdentity(g.clean), g.clean.identity) || !sameIdentity(gitFilterIdentity(g.smudge), g.smudge.identity) || string(config) != string(g.config) || digest != g.digest || len(attrs) != len(g.attributes) {
		return fail(CodeIdentityChanged, "filter git", fmt.Errorf("context drift"))
	}
	for path, want := range g.attributes {
		got, ok := attrs[path]
		if !ok || got.sha256 != want.sha256 || !sameIdentity(got.identity, want.identity) {
			return fail(CodeIdentityChanged, "filter attributes", fmt.Errorf("context drift"))
		}
	}
	return nil
}

func gitFilterIdentity(p filterPinnedFile) FileIdentity {
	actual, err := pinFilterFile(p.path, p.sha256, 32<<20)
	if err != nil {
		return FileIdentity{}
	}
	return actual.identity
}

func (g *FilterGitContext) readState(ctx context.Context) ([]byte, map[string]filterPinnedFile, string, error) {
	config, err := g.privateOutput(ctx, g.repoRoot, nil, "config", "--local", "--no-includes", "--null", "--list")
	if err != nil {
		return nil, nil, "", err
	}
	if len(config) > filterConfigMaximumBytes {
		return nil, nil, "", fail(CodeInvalidInput, "filter config", fmt.Errorf("configuration too large"))
	}
	if err := g.validateConfig(config); err != nil {
		return nil, nil, "", err
	}
	attrs, err := g.readAttributes(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	h := sha256.New()
	_, _ = h.Write([]byte("filter-git-context-v1\x00"))
	_, _ = h.Write([]byte(g.repoRoot))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(g.commonDir))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(g.git.sha256))
	_, _ = h.Write([]byte(config))
	paths := make([]string, 0, len(attrs))
	for p := range attrs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		_, _ = h.Write([]byte("\x00" + p + "\x00" + attrs[p].sha256))
	}
	return []byte(config), attrs, hex.EncodeToString(h.Sum(nil)), nil
}

func (g *FilterGitContext) validateConfig(raw string) error {
	seen := map[string]bool{}
	for _, record := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return fail(CodeInvalidInput, "filter config", fmt.Errorf("invalid record"))
		}
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "include.") || strings.HasPrefix(lower, "includeif.") || strings.HasPrefix(lower, "filter.") && !strings.HasPrefix(lower, "filter."+strings.ToLower(g.manifest.DriverID)+".") || strings.HasPrefix(lower, "filter."+strings.ToLower(g.manifest.DriverID)+".") && lower != "filter."+strings.ToLower(g.manifest.DriverID)+".clean" && lower != "filter."+strings.ToLower(g.manifest.DriverID)+".smudge" && lower != "filter."+strings.ToLower(g.manifest.DriverID)+".required" || lower == "core.attributesfile" || lower == "core.hookspath" || lower == "core.fsmonitor" || lower == "commit.gpgsign" || strings.HasPrefix(lower, "gpg.") || lower == "diff.external" || strings.HasSuffix(lower, ".textconv") || strings.HasSuffix(lower, ".command") || strings.HasSuffix(lower, ".recentobjectshook") {
			return fail(CodeInvalidInput, "filter config", fmt.Errorf("unsupported %s", key))
		}
		prefix := "filter." + strings.ToLower(g.manifest.DriverID) + "."
		if strings.HasPrefix(lower, prefix) {
			seen[strings.TrimPrefix(lower, prefix)] = true
			want := map[string]string{"clean": filterShellCommand(g.manifest.Clean), "smudge": filterShellCommand(g.manifest.Smudge), "required": "false"}[strings.TrimPrefix(lower, prefix)]
			if value != want {
				return fail(CodeInvalidInput, "filter config", fmt.Errorf("unexpected %s", key))
			}
		}
	}
	if !seen["clean"] || !seen["smudge"] || !seen["required"] {
		return fail(CodeInvalidInput, "filter config", fmt.Errorf("incomplete manifest driver"))
	}
	if g.manifest.ConfigSHA256 != FilterConfigurationSHA256(g.manifest) {
		return fail(CodeInvalidInput, "filter config", fmt.Errorf("manifest digest mismatch"))
	}
	return nil
}

func (g *FilterGitContext) readAttributes(ctx context.Context) (map[string]filterPinnedFile, error) {
	listed, err := g.privateText(ctx, g.repoRoot, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	if len(listed) > filterAttributesMaximumBytes {
		return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("inventory too large"))
	}
	expected := map[string]string{}
	for _, p := range g.manifest.Attributes {
		expected[p.Path] = p.SHA256
	}
	actual := map[string]filterPinnedFile{}
	for _, p := range strings.Split(strings.TrimSuffix(listed, "\x00"), "\x00") {
		if filepath.Base(p) != ".gitattributes" {
			continue
		}
		hash, ok := expected[p]
		if !ok {
			return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("unlisted %s", p))
		}
		pin, err := pinFilterFile(filepath.Join(g.repoRoot, filepath.FromSlash(p)), hash, filterAttributeMaximumBytes)
		if err != nil {
			return nil, err
		}
		actual[p] = pin
	}
	if len(actual) != len(expected) {
		return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("declared source missing"))
	}
	for _, path := range g.manifest.Paths {
		out, err := g.privateInput(ctx, g.repoRoot, []byte(path+"\x00"), "check-attr", "--all", "-z", "--stdin")
		if err != nil {
			return nil, err
		}
		if err := g.validateAttributes(path, out); err != nil {
			return nil, err
		}
	}
	return actual, nil
}

func (g *FilterGitContext) validateAttributes(path, raw string) error {
	parts := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	if len(parts)%3 != 0 {
		return fail(CodeInvalidInput, "filter attributes", fmt.Errorf("invalid check-attr output"))
	}
	seenFilter := false
	for i := 0; i < len(parts); i += 3 {
		if parts[i] != path {
			return fail(CodeInvalidInput, "filter attributes", fmt.Errorf("unexpected attribute path"))
		}
		key, value := parts[i+1], parts[i+2]
		if key != "filter" || value != g.manifest.DriverID || seenFilter {
			return fail(CodeInvalidInput, "filter attributes", fmt.Errorf("unsupported attribute %s", key))
		}
		seenFilter = true
	}
	if !seenFilter {
		return fail(CodeInvalidInput, "filter attributes", fmt.Errorf("filter attribute missing"))
	}
	return nil
}

func (g *FilterGitContext) privateText(ctx context.Context, dir string, args ...string) (string, error) {
	raw, err := g.privateOutput(ctx, dir, nil, args...)
	return strings.TrimSpace(raw), err
}
func (g *FilterGitContext) privateInput(ctx context.Context, dir string, in []byte, args ...string) (string, error) {
	raw, err := g.privateOutput(ctx, dir, in, args...)
	return strings.TrimSpace(raw), err
}
func (g *FilterGitContext) privateOutput(ctx context.Context, dir string, in []byte, args ...string) (string, error) {
	r, err := candidateGitWithStart(ctx, dir, strings.NewReader(string(in)), process.Start, args...)
	return r.Stdout, err
}

// pinFilterFile accepts only a single regular, non-writable native file owned
// by the current user or root. The read/hash and identity are checked together.
func pinFilterFile(path, want string, limit int64) (filterPinnedFile, error) {
	return pinFilterFileWithOpenHook(path, want, limit, nil)
}

func pinFilterFileWithOpenHook(path, want string, limit int64, afterOpen func() error) (filterPinnedFile, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return filterPinnedFile{}, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Size() < 0 || st.Size() > limit || (int64(sys.Uid) == int64(os.Getuid()) && sys.Nlink != 1) || (int64(sys.Uid) != int64(os.Getuid()) && sys.Uid != 0) || st.Mode().Perm()&022 != 0 {
		return filterPinnedFile{}, fail(CodeInvalidInput, "filter pin", fmt.Errorf("untrusted native file"))
	}
	if afterOpen != nil {
		if err = afterOpen(); err != nil {
			return filterPinnedFile{}, err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || int64(len(b)) != st.Size() {
		return filterPinnedFile{}, fail(CodeIdentityChanged, "filter pin", err)
	}
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return filterPinnedFile{}, fail(CodeIdentityChanged, "filter pin", fmt.Errorf("digest mismatch"))
	}
	id, err := fileIdentity(path)
	if err != nil {
		return filterPinnedFile{}, err
	}
	return filterPinnedFile{path: path, sha256: got, identity: id}, nil
}
