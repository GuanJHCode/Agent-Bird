package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

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
	source                       *FilterGitContext
	bindingBacklink              *filterPinnedFile
	repository                   filterRepositoryAnchor
	repoRoot, commonDir          string
	repoIdentity, commonIdentity DirectoryIdentity
	manifest                     contract.GitFilterManifestV1
	git, clean, smudge           filterPinnedFile
	config                       []byte
	attributes                   map[string]filterPinnedFile
	digest                       string
	targetIndex                  *filterTargetIndexGuard
	mu                           sync.Mutex
	closed                       bool
}

type filterPinnedFile struct {
	path, sha256 string
	identity     FileIdentity
	uid, links   uint64
	native       bool
	limit        int64
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
	return openFilterGit(ctx, repoRoot, manifest, false, nil)
}

// OpenFilterTargetGit additionally retains target-index evidence before any
// filtered Git command can consult that index.
func OpenFilterTargetGit(ctx context.Context, repoRoot string, manifest contract.GitFilterManifestV1) (*FilterGitContext, error) {
	return openFilterGit(ctx, repoRoot, manifest, true, nil)
}

func openFilterGit(ctx context.Context, repoRoot string, manifest contract.GitFilterManifestV1, target bool, source *FilterGitContext) (g *FilterGitContext, err error) {
	if err := manifest.Validate(); err != nil {
		return nil, fail(CodeInvalidInput, "filter git manifest", err)
	}
	repoRoot = CanonicalPath(repoRoot)
	repoIdentity, err := directoryIdentity(repoRoot)
	if err != nil {
		return nil, err
	}
	g = &FilterGitContext{source: source, repoRoot: repoRoot, repoIdentity: repoIdentity, manifest: manifest.Clone(), attributes: map[string]filterPinnedFile{}}
	opened := g
	defer func() {
		if err != nil {
			_ = opened.Close()
		}
	}()
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
	if err := g.captureRepository(private); err != nil {
		return nil, err
	}
	if target {
		if err := g.installTargetIndexGuard(); err != nil {
			return nil, err
		}
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
	if g.targetIndex != nil {
		err := g.targetIndex.Close()
		g.targetIndex = nil
		return err
	}
	return nil
}
func (g *FilterGitContext) SHA256() string { g.mu.Lock(); defer g.mu.Unlock(); return g.digest }

func (g *FilterGitContext) verifyAfter(runErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(runErr, g.Verify(ctx))
}

func (g *FilterGitContext) Verify(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return fail(CodeInvalidInput, "filter git", fmt.Errorf("closed context"))
	}
	if g.targetIndex != nil {
		if err := g.targetIndex.Verify(); err != nil {
			return err
		}
	}
	if actual, err := directoryIdentity(g.repoRoot); err != nil || actual != g.repoIdentity {
		return fail(CodeIdentityChanged, "filter git root", err)
	}
	if actual, err := directoryIdentity(g.commonDir); err != nil || actual != g.commonIdentity {
		return fail(CodeIdentityChanged, "filter git common", err)
	}
	if g.source != nil {
		if err := g.source.Verify(ctx); err != nil {
			return err
		}
		if g.bindingBacklink == nil || !verifyFilterPin(*g.bindingBacklink) {
			return fail(CodeIdentityChanged, "filter worktree backlink", nil)
		}
	}
	private := context.WithValue(g.Context(ctx), filterGitPrivateKey{}, true)
	if err := g.verifyCaptured(private); err != nil {
		return err
	}
	if g.source != nil {
		if !verifyFilterPin(*g.bindingBacklink) {
			return fail(CodeIdentityChanged, "filter worktree backlink", nil)
		}
		return g.source.Verify(ctx)
	}
	return nil
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
	if string(config) != string(g.config) || digest != g.digest || len(attrs) != len(g.attributes) {
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

func (g *FilterGitContext) verifySources() bool {
	r, re := directoryIdentity(g.repoRoot)
	c, ce := directoryIdentity(g.commonDir)
	return re == nil && ce == nil && r == g.repoIdentity && c == g.commonIdentity && verifyFilterPin(g.git) && verifyFilterPin(g.clean) && verifyFilterPin(g.smudge)
}

func (g *FilterGitContext) installTargetIndexGuard() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.source != nil {
		return fail(CodeInvalidInput, "filter target index guard", nil)
	}
	if g.targetIndex != nil {
		return g.targetIndex.Verify()
	}
	guard, err := openFilterTargetIndexGuard(g)
	if err != nil {
		return err
	}
	g.targetIndex = guard
	return nil
}

func (g *FilterGitContext) verifyTargetIndexGuard() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.verifyTargetIndexGuardHeld()
}

// Private Git runs with the current context locked (or still unpublished).
// A child's parent is independently owned and must acquire its own lock.
func (g *FilterGitContext) verifyTargetIndexGuardHeld() error {
	if g.closed {
		return fail(CodeInvalidInput, "filter git", fmt.Errorf("closed context"))
	}
	if g.source != nil {
		if err := g.source.verifyTargetIndexGuard(); err != nil {
			return err
		}
	}
	if g.targetIndex != nil {
		return g.targetIndex.Verify()
	}
	return nil
}

func (g *FilterGitContext) readState(ctx context.Context) ([]byte, map[string]filterPinnedFile, string, error) {
	if err := g.verifyRepository(ctx); err != nil {
		return nil, nil, "", err
	}
	if !g.verifySources() {
		return nil, nil, "", fail(CodeIdentityChanged, "filter sources", nil)
	}
	configPin, err := pinFilterFileKind(filepath.Join(g.commonDir, "config"), "", filterConfigMaximumBytes, false, nil)
	if err != nil {
		return nil, nil, "", err
	}
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
	if !verifyFilterPin(configPin) || !g.verifySources() {
		return nil, nil, "", fail(CodeIdentityChanged, "filter config", fmt.Errorf("source changed during read"))
	}
	if err := g.verifyRepository(ctx); err != nil {
		return nil, nil, "", err
	}
	type pinned struct {
		Path, SHA256 string
		Identity     FileIdentity
		UID, Links   uint64
	}
	view := func(p filterPinnedFile) pinned { return pinned{p.path, p.sha256, p.identity, p.uid, p.links} }
	manifestSHA, _ := g.manifest.Digest()
	state := struct {
		Version                      int
		ManifestSHA256, Root, Common string
		RootIdentity, CommonIdentity DirectoryIdentity
		Git, Clean, Smudge, Config   pinned
		ConfigOutput                 string
		Attributes                   []pinned
		RepositorySHA256             string
	}{1, manifestSHA, g.repoRoot, g.commonDir, g.repoIdentity, g.commonIdentity, view(g.git), view(g.clean), view(g.smudge), view(configPin), config, []pinned{}, g.repository.digest()}
	paths := make([]string, 0, len(attrs))
	for p := range attrs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		state.Attributes = append(state.Attributes, view(attrs[p]))
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, nil, "", err
	}
	h := sha256.Sum256(append([]byte("filter-git-context-v1\x00"), encoded...))
	return []byte(config), attrs, hex.EncodeToString(h[:]), nil
}

func (g *FilterGitContext) validateConfig(raw string) error {
	seen := map[string]bool{}
	seenKeys := map[string]bool{}
	for _, record := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return fail(CodeInvalidInput, "filter config", fmt.Errorf("invalid record"))
		}
		lower := strings.ToLower(key)
		if seenKeys[key] {
			return fail(CodeInvalidInput, "filter config", fmt.Errorf("duplicate key"))
		}
		seenKeys[key] = true
		prefix := "filter." + g.manifest.DriverID + "."
		if strings.HasPrefix(key, prefix) {
			name := strings.TrimPrefix(key, prefix)
			want, allowed := map[string]string{"clean": filterShellCommand(g.manifest.Clean), "smudge": filterShellCommand(g.manifest.Smudge), "required": "false"}[name]
			if !allowed || value != want {
				return fail(CodeInvalidInput, "filter config", fmt.Errorf("unexpected %s", key))
			}
			seen[name] = true
			continue
		}
		// A closed local allowlist also excludes partial-clone/promisor lazy
		// fetching, credentials, aliases, worktree config and future extensions.
		allowed := false
		switch lower {
		case "core.repositoryformatversion":
			allowed = value == "0"
		case "core.bare":
			allowed = value == "false"
		case "core.filemode", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "core.symlinks":
			allowed = value == "true" || value == "false"
		case "user.name", "user.email":
			allowed = len(value) <= 4096
		}
		if !allowed {
			return fail(CodeInvalidInput, "filter config", fmt.Errorf("unsupported %s", key))
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

func (g *FilterGitContext) noInfoAttributes() error {
	info := filepath.Join(g.commonDir, "info")
	if st, err := os.Lstat(info); err == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fail(CodeInvalidInput, "filter info", fmt.Errorf("unsafe info directory"))
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(info, "attributes")); !os.IsNotExist(err) {
		return fail(CodeInvalidInput, "filter attributes", fmt.Errorf("info attributes unsupported"))
	}
	return nil
}

func (g *FilterGitContext) attributeSources(ctx context.Context) ([]string, error) {
	listed, err := g.privateOutput(ctx, g.repoRoot, nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	if len(listed) > filterAttributesMaximumBytes {
		return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("inventory too large"))
	}
	ignored, err := g.privateOutput(ctx, g.repoRoot, nil, "ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	if len(listed)+len(ignored) > filterAttributesMaximumBytes {
		return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("inventory too large"))
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, path := range strings.Split(strings.TrimSuffix(listed+ignored, "\x00"), "\x00") {
		name := filepath.Base(path)
		if !strings.EqualFold(name, ".gitattributes") {
			continue
		}
		if name != ".gitattributes" || seen[path] {
			return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("ambiguous source"))
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (g *FilterGitContext) readAttributes(ctx context.Context) (map[string]filterPinnedFile, error) {
	if err := g.noInfoAttributes(); err != nil {
		return nil, err
	}
	paths, err := g.attributeSources(ctx)
	if err != nil {
		return nil, err
	}
	expected := map[string]string{}
	for _, p := range g.manifest.Attributes {
		expected[p.Path] = p.SHA256
	}
	if len(paths) != len(expected) {
		return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("source set changed"))
	}
	actual := map[string]filterPinnedFile{}
	var total int64
	for _, path := range paths {
		hash, ok := expected[path]
		if !ok {
			return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("unlisted source"))
		}
		pin, err := pinFilterFileKind(filepath.Join(g.repoRoot, filepath.FromSlash(path)), hash, filterAttributeMaximumBytes, false, nil)
		if err != nil {
			return nil, err
		}
		total += pin.identity.Size
		if total > filterAttributesMaximumBytes {
			return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("sources too large"))
		}
		actual[path] = pin
	}
	// One bounded native query for the exact sorted manifest path vector.
	out, err := g.privateInput(ctx, g.repoRoot, []byte(strings.Join(g.manifest.Paths, "\x00")+"\x00"), "check-attr", "--all", "-z", "--stdin")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(out) > filterAttributesMaximumBytes || len(parts) != 3*len(g.manifest.Paths) {
		return nil, fail(CodeInvalidInput, "filter attributes", fmt.Errorf("unexpected attribute vector"))
	}
	for i, path := range g.manifest.Paths {
		if err := g.validateAttributes(path, strings.Join(parts[i*3:i*3+3], "\x00")+"\x00"); err != nil {
			return nil, err
		}
	}
	if err := g.noInfoAttributes(); err != nil {
		return nil, err
	}
	after, err := g.attributeSources(ctx)
	if err != nil {
		return nil, err
	}
	if strings.Join(after, "\x00") != strings.Join(paths, "\x00") {
		return nil, fail(CodeIdentityChanged, "filter attributes", fmt.Errorf("inventory changed"))
	}
	for _, pin := range actual {
		if !verifyFilterPin(pin) {
			return nil, fail(CodeIdentityChanged, "filter attributes", fmt.Errorf("source changed during query"))
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
	return strings.TrimSuffix(raw, "\n"), err
}
func (g *FilterGitContext) privateInput(ctx context.Context, dir string, in []byte, args ...string) (string, error) {
	raw, err := g.privateOutput(ctx, dir, in, args...)
	return raw, err
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
	return pinFilterFileKind(path, want, limit, true, afterOpen)
}

func pinFilterFileKind(path, want string, limit int64, native bool, afterOpen func() error) (filterPinnedFile, error) {
	invalid := func(err error) (filterPinnedFile, error) {
		return filterPinnedFile{}, fail(CodeIdentityChanged, "filter pin", err)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || CanonicalPath(path) != path || limit < 1 {
		return invalid(fmt.Errorf("noncanonical source"))
	}
	st, err := os.Lstat(path)
	if err != nil {
		return filterPinnedFile{}, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Size() < 0 || st.Size() > limit || sys.Nlink != 1 || (int64(sys.Uid) != int64(os.Getuid()) && sys.Uid != 0) || st.Mode().Perm()&022 != 0 || native && st.Mode().Perm()&0111 == 0 {
		return filterPinnedFile{}, fail(CodeInvalidInput, "filter pin", fmt.Errorf("untrusted native file"))
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return invalid(err)
	}
	defer f.Close()
	id, err := identityFromInfo(st)
	if err != nil {
		return invalid(err)
	}
	same := func(info os.FileInfo) bool {
		other, e := identityFromInfo(info)
		s, ok := info.Sys().(*syscall.Stat_t)
		return e == nil && other == id && ok && s.Uid == sys.Uid && s.Nlink == sys.Nlink
	}
	held, err := f.Stat()
	if err != nil || !same(held) {
		return invalid(errors.Join(err, fmt.Errorf("opened source changed")))
	}
	if afterOpen != nil {
		if err = afterOpen(); err != nil {
			return filterPinnedFile{}, err
		}
	}
	if native && !process.NativeExecutableFile(f, st.Size()) {
		return invalid(fmt.Errorf("non-native executable"))
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) != st.Size() {
		return filterPinnedFile{}, fail(CodeIdentityChanged, "filter pin", err)
	}
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	if want != "" && got != want {
		return filterPinnedFile{}, fail(CodeIdentityChanged, "filter pin", fmt.Errorf("digest mismatch"))
	}
	held, err = f.Stat()
	if err != nil || !same(held) {
		return invalid(errors.Join(err, fmt.Errorf("held source changed")))
	}
	named, err := os.Lstat(path)
	if err != nil || !same(named) || CanonicalPath(path) != path {
		return invalid(errors.Join(err, fmt.Errorf("named source changed")))
	}
	return filterPinnedFile{path: path, sha256: got, identity: id, uid: uint64(sys.Uid), links: uint64(sys.Nlink), native: native, limit: limit}, nil
}

func verifyFilterPin(p filterPinnedFile) bool {
	now, err := pinFilterFileKind(p.path, p.sha256, p.limit, p.native, nil)
	return err == nil && now.identity == p.identity && now.uid == p.uid && now.links == p.links
}
