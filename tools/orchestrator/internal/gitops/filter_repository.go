package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

const filterGitLinkMaximumBytes = 8192

type filterRepositoryAnchor struct {
	gitDir              string
	gitIdentity         DirectoryIdentity
	gitLink, commonLink *filterPinnedFile
}

func readFilterGitLink(path string) (filterPinnedFile, string, error) {
	pin, err := pinFilterFileKind(path, "", filterGitLinkMaximumBytes, false, nil)
	if err != nil {
		return pin, "", err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return pin, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return pin, "", err
	}
	id, err := identityFromInfo(st)
	if err != nil || id != pin.identity {
		return pin, "", fail(CodeIdentityChanged, "git link", err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, filterGitLinkMaximumBytes+1))
	if err != nil || int64(len(raw)) != id.Size || !verifyFilterPin(pin) {
		return pin, "", fail(CodeIdentityChanged, "git link", err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != pin.sha256 {
		return pin, "", fail(CodeIdentityChanged, "git link", nil)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" || !utf8.ValidString(text) || strings.ContainsAny(text, "\x00\r\n") {
		return pin, "", fail(CodeInvalidInput, "git link", nil)
	}
	return pin, text, nil
}

func filterGitLinkTarget(base, link string) (string, error) {
	if link == "" {
		return "", fail(CodeInvalidInput, "git link target", nil)
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(base, link)
	}
	link = filepath.Clean(link)
	if CanonicalPath(link) != link {
		return "", fail(CodeIdentityChanged, "git link target", nil)
	}
	return link, nil
}

func (g *FilterGitContext) captureRepository(ctx context.Context) error {
	gitDir, err := g.privateText(ctx, g.repoRoot, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(gitDir) || filepath.Clean(gitDir) != gitDir || CanonicalPath(gitDir) != gitDir {
		return fail(CodeInvalidInput, "filter git directory", nil)
	}
	anchor := filterRepositoryAnchor{gitDir: gitDir}
	anchor.gitIdentity, err = directoryIdentity(gitDir)
	if err != nil {
		return err
	}
	marker := filepath.Join(g.repoRoot, ".git")
	st, err := os.Lstat(marker)
	if err != nil {
		return err
	}
	if st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
		if marker != gitDir {
			return fail(CodeIdentityChanged, "filter git directory", nil)
		}
	} else if st.Mode().IsRegular() {
		pin, content, e := readFilterGitLink(marker)
		if e != nil {
			return e
		}
		link, ok := strings.CutPrefix(content, "gitdir: ")
		target, e := filterGitLinkTarget(g.repoRoot, link)
		if !ok || e != nil || target != gitDir {
			return fail(CodeIdentityChanged, "filter git link", e)
		}
		anchor.gitLink = &pin
	} else {
		return fail(CodeInvalidInput, "filter git marker", nil)
	}
	commonLink := filepath.Join(gitDir, "commondir")
	if _, e := os.Lstat(commonLink); e == nil {
		pin, content, e := readFilterGitLink(commonLink)
		if e != nil {
			return e
		}
		target, e := filterGitLinkTarget(gitDir, content)
		if e != nil || target != g.commonDir {
			return fail(CodeIdentityChanged, "filter common link", e)
		}
		anchor.commonLink = &pin
	} else if !os.IsNotExist(e) || gitDir != g.commonDir {
		return fail(CodeIdentityChanged, "filter common link", e)
	}
	g.repository = anchor
	return g.verifyRepository(ctx)
}

func (g *FilterGitContext) verifyRepositoryFiles() error {
	a := g.repository
	if a.gitDir == "" || CanonicalPath(a.gitDir) != a.gitDir {
		return fail(CodeIdentityChanged, "filter git directory", nil)
	}
	actual, err := directoryIdentity(a.gitDir)
	if err != nil || actual != a.gitIdentity {
		return fail(CodeIdentityChanged, "filter git directory", err)
	}
	if a.gitLink != nil {
		if !verifyFilterPin(*a.gitLink) {
			return fail(CodeIdentityChanged, "filter git link", nil)
		}
	} else if filepath.Join(g.repoRoot, ".git") != a.gitDir {
		return fail(CodeIdentityChanged, "filter git marker", nil)
	}
	if a.commonLink != nil {
		if !verifyFilterPin(*a.commonLink) {
			return fail(CodeIdentityChanged, "filter common link", nil)
		}
	} else if _, err := os.Lstat(filepath.Join(a.gitDir, "commondir")); !os.IsNotExist(err) || a.gitDir != g.commonDir {
		return fail(CodeIdentityChanged, "filter common link", err)
	}
	for _, dir := range []string{a.gitDir, g.commonDir} {
		for _, name := range []string{"config.worktree", "info/attributes", "info/grafts"} {
			if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				return fail(CodeInvalidInput, "filter git metadata", fmt.Errorf("unsupported %s", name))
			}
		}
	}
	return nil
}

func (g *FilterGitContext) verifyRepository(ctx context.Context) error {
	if err := g.verifyRepositoryFiles(); err != nil {
		return err
	}
	gitDir, err := g.privateText(ctx, g.repoRoot, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	common, err := g.privateText(ctx, g.repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if gitDir != g.repository.gitDir || CanonicalPath(common) != g.commonDir {
		return fail(CodeIdentityChanged, "filter repository resolution", nil)
	}
	return g.verifyRepositoryFiles()
}

func (a filterRepositoryAnchor) digest() string {
	type link struct {
		Path, SHA256 string
		Identity     FileIdentity
		UID, Links   uint64
	}
	view := func(p *filterPinnedFile) *link {
		if p == nil {
			return nil
		}
		return &link{p.path, p.sha256, p.identity, p.uid, p.links}
	}
	raw, _ := json.Marshal(struct {
		GitDir              string
		GitIdentity         DirectoryIdentity
		GitLink, CommonLink *link
	}{a.gitDir, a.gitIdentity, view(a.gitLink), view(a.commonLink)})
	h := sha256.Sum256(append([]byte("filter-repository-v1\x00"), raw...))
	return hex.EncodeToString(h[:])
}
