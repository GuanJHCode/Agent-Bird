package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func filterTargetDirectory(f *os.File) (DirectoryIdentity, error) {
	st, err := f.Stat()
	if err != nil {
		return DirectoryIdentity{}, err
	}
	id, err := filterDirectoryInfo(st)
	if err != nil || (st.Mode() != os.ModeDir|0700 && st.Mode() != os.ModeDir|0755) {
		return DirectoryIdentity{}, fail(CodeInvalidInput, "filter target directory permissions", err)
	}
	return id, nil
}
func filterTargetRegular(f *os.File) (FileIdentity, error) {
	id, err := filterRegularIdentity(f)
	if err != nil {
		return id, err
	}
	mode := os.FileMode(id.Mode)
	if (mode != 0600 && mode != 0644 && mode != 0700 && mode != 0755) || id.Size < 0 || id.Size > 16<<20 {
		return id, fail(CodeInvalidInput, "filter target file permissions/size", nil)
	}
	return id, nil
}
func (m *FilterTargetObservation) open() error {
	p := m.request.Worktree
	if CanonicalPath(p) != p || filepath.Clean(p) != p || !filepath.IsAbs(p) || p == "/" {
		return fail(CodeInvalidInput, "filter target path", nil)
	}
	if err := noSymlinkComponents(filepath.Dir(p)); err != nil {
		return err
	}
	var err error
	m.parent, err = os.OpenFile(filepath.Dir(p), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	m.parentIdentity, err = filterTargetDirectory(m.parent)
	if err != nil {
		return err
	}
	m.root, err = filterOpenAt(m.parent, filepath.Base(p), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	m.rootIdentity, err = filterTargetDirectory(m.root)
	if err != nil {
		return err
	}
	if m.rootIdentity != m.source.repoIdentity {
		return fail(CodeIdentityChanged, "filter target root", nil)
	}
	gitPath := m.source.repository.gitDir
	if err = noSymlinkComponents(gitPath); err != nil {
		return err
	}
	m.gitDir, err = os.OpenFile(gitPath, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	m.gitIdentity, err = filterTargetDirectory(m.gitDir)
	if err != nil {
		return err
	}
	if m.gitIdentity != m.source.repository.gitIdentity {
		return fail(CodeIdentityChanged, "filter target git directory", nil)
	}
	m.index, err = filterOpenAt(m.gitDir, "index", unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	return m.verifyAnchors()
}
func (m *FilterTargetObservation) verifyAnchors() error {
	for _, entry := range []struct {
		file *os.File
		path string
		want DirectoryIdentity
	}{
		{m.parent, filepath.Dir(m.request.Worktree), m.parentIdentity},
		{m.root, m.request.Worktree, m.rootIdentity},
		{m.gitDir, m.source.repository.gitDir, m.gitIdentity},
	} {
		if entry.file == nil {
			return fail(CodeIdentityChanged, "filter target closed", nil)
		}
		held, err := filterTargetDirectory(entry.file)
		if err != nil {
			return err
		}
		named, err := directoryIdentity(entry.path)
		if err != nil || held != entry.want || named != held || CanonicalPath(entry.path) != entry.path {
			return fail(CodeIdentityChanged, "filter target directory changed", err)
		}
	}
	return nil
}
func (m *FilterTargetObservation) readIndex() ([]byte, FileIdentity, error) {
	if err := m.verifyAnchors(); err != nil {
		return nil, FileIdentity{}, err
	}
	if m.index == nil {
		return nil, FileIdentity{}, fail(CodeIdentityChanged, "filter target index closed", nil)
	}
	id, err := filterTargetRegular(m.index)
	if err != nil || (id.Mode != 0600 && id.Mode != 0644) {
		return nil, id, fail(CodeInvalidInput, "filter target index mode", err)
	}
	if _, err = m.index.Seek(0, io.SeekStart); err != nil {
		return nil, id, err
	}
	raw, err := io.ReadAll(io.LimitReader(m.index, 16<<20+1))
	if err != nil {
		return nil, id, err
	}
	after, err := filterTargetRegular(m.index)
	named, openErr := filterOpenAt(m.gitDir, "index", unix.O_RDONLY, 0)
	if openErr != nil {
		return nil, id, openErr
	}
	namedID, namedErr := filterTargetRegular(named)
	closeErr := named.Close()
	if err != nil || namedErr != nil || closeErr != nil || id != after || id != namedID || int64(len(raw)) != id.Size {
		return nil, id, fail(CodeIdentityChanged, "filter target index identity", errors.Join(err, namedErr, closeErr))
	}
	return raw, id, nil
}
func (m *FilterTargetObservation) checkMetadata() error {
	if err := m.verifyAnchors(); err != nil {
		return err
	}
	// Reject sidecars before any command can load an external split index.
	if _, err := m.gitDir.Seek(0, io.SeekStart); err != nil {
		return err
	}
	count := 0
	for {
		entries, err := m.gitDir.ReadDir(128)
		count += len(entries)
		if count > 4096 {
			return fail(CodeInvalidInput, "filter target git directory bounds", nil)
		}
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if strings.HasPrefix(n, "sharedindex.") {
				return fail(CodeInvalidInput, "filter target split index", nil)
			}
			switch n {
			case "merge_head", "cherry_pick_head", "revert_head", "rebase-apply", "rebase-merge", "sequencer", "bisect_log", "index.lock":
				return fail(CodeTargetDirty, "filter target operation in progress", nil)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	info, err := filterOpenAt(m.gitDir, "info", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer info.Close()
	if _, err = filterTargetDirectory(info); err != nil {
		return err
	}
	var st unix.Stat_t
	err = unix.Fstatat(int(info.Fd()), "sparse-checkout", &st, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.ENOENT) {
		return fail(CodeInvalidInput, "filter target sparse metadata", err)
	}
	return nil
}
func (m *FilterTargetObservation) checkGit(ctx context.Context, tree string) ([]byte, error) {
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--shared-index-path"}, ""},
		{[]string{"symbolic-ref", "-q", "HEAD"}, m.request.Ref},
		{[]string{"rev-parse", "--verify", "HEAD"}, m.request.BaseOID},
		{[]string{"rev-parse", "--verify", m.request.Ref}, m.request.BaseOID},
		{[]string{"rev-parse", "--verify", "HEAD^{tree}"}, tree},
	} {
		got, err := run(ctx, m.request.Worktree, check.args...)
		if err != nil {
			return nil, err
		}
		if got.Stdout != check.want {
			return nil, fail(CodeTargetDrift, "filter target ref/base", nil)
		}
	}
	raw, err := filterBoundedQuery(ctx, m.source, nil, "ls-files", "--stage", "-v", "-z", "--full-name", "--sparse")
	if err != nil {
		return nil, err
	}
	return raw, filterTargetIndexVector(raw, m.base)
}
func (m *FilterTargetObservation) snapshot(ctx context.Context) (map[string]filterTargetEntry, error) {
	if err := m.verifyAnchors(); err != nil {
		return nil, err
	}
	out := map[string]filterTargetEntry{}
	count, pathBytes, total := 0, 0, int64(0)
	var walk func(*os.File, string) error
	walk = func(parent *os.File, prefix string) error {
		if _, err := parent.Seek(0, io.SeekStart); err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, readErr := parent.ReadDir(128)
			for _, entry := range entries {
				rel := entry.Name()
				if prefix != "" {
					rel = prefix + "/" + rel
				}
				if rel == ".git" {
					continue
				} // source repository anchor authenticates this marker.
				count++
				pathBytes += len(rel)
				want, ok := m.expected[rel]
				if !ok || count > 4096 || pathBytes > 256<<10 || !filterSafePath(rel) {
					return fail(CodeCollision, "filter target inventory", nil)
				}
				flags := unix.O_RDONLY
				if want.Mode == "directory" {
					flags |= unix.O_DIRECTORY
				}
				f, err := filterOpenAt(parent, entry.Name(), flags, 0)
				if err != nil {
					return err
				}
				err = func() error {
					defer f.Close()
					if want.Mode == "directory" {
						if _, err := filterTargetDirectory(f); err != nil {
							return err
						}
						st, err := f.Stat()
						if err != nil {
							return err
						}
						id, err := identityFromInfo(st)
						if err != nil {
							return err
						}
						if err = walk(f, rel); err != nil {
							return err
						}
						final, err := f.Stat()
						if err != nil {
							return err
						}
						after, err := identityFromInfo(final)
						if err != nil || after != id {
							return fail(CodeIdentityChanged, "filter target directory changed", err)
						}
						named, err := filterOpenAt(parent, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY, 0)
						if err != nil {
							return err
						}
						namedInfo, statErr := named.Stat()
						var namedID FileIdentity
						if statErr == nil {
							namedID, statErr = identityFromInfo(namedInfo)
						}
						closeErr := named.Close()
						if statErr != nil || closeErr != nil || namedID != id {
							return fail(CodeIdentityChanged, "filter target named directory", errors.Join(statErr, closeErr))
						}
						out[rel] = filterTargetEntry{Identity: id}
						return nil
					}
					id, err := filterTargetRegular(f)
					if err != nil {
						return err
					}
					if (id.Mode&0111 != 0) != (want.Mode == "100755") || id.Size != want.Size {
						return fail(CodeTargetDirty, "filter target rendered mode/size", nil)
					}
					total += id.Size
					if total > filterWorkspaceMaximumBytes {
						return fail(CodeInvalidInput, "filter target total size", nil)
					}
					hash := sha256.New()
					n, err := io.Copy(hash, io.LimitReader(f, id.Size+1))
					if err != nil {
						return err
					}
					sha := hex.EncodeToString(hash.Sum(nil))
					after, err := filterTargetRegular(f)
					if err != nil || after != id || n != id.Size || sha != want.SHA256 {
						return fail(CodeTargetDirty, "filter target rendered content", err)
					}
					// Reopen relative to the same parent to reject same-name replacement.
					named, err := filterOpenAt(parent, entry.Name(), unix.O_RDONLY, 0)
					if err != nil {
						return err
					}
					namedID, err := filterTargetRegular(named)
					closeErr := named.Close()
					if err != nil || closeErr != nil || namedID != id {
						return fail(CodeIdentityChanged, "filter target named file", errors.Join(err, closeErr))
					}
					out[rel] = filterTargetEntry{Identity: id, SHA256: sha}
					return nil
				}()
				if err != nil {
					return err
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		return nil
	}
	if err := walk(m.root, ""); err != nil {
		return nil, err
	}
	if len(out) != len(m.expected) {
		return nil, fail(CodeTargetDirty, "filter target missing entry", nil)
	}
	if err := m.verifyAnchors(); err != nil {
		return nil, err
	}
	return out, nil
}

// Pin loose and packed resolution inputs as well as HEAD. Final non-Git checks
// catch a journal callback changing refs after an earlier Git observation.
func (m *FilterTargetObservation) refFiles(capture bool) error {
	paths := []string{filepath.Join(m.source.repository.gitDir, "HEAD"), filepath.Join(m.source.commonDir, filepath.FromSlash(m.request.Ref)), filepath.Join(m.source.commonDir, "packed-refs"), filepath.Join(m.source.commonDir, filepath.FromSlash(m.candidate.PrivateRef))}
	for _, p := range paths {
		if err := noSymlinkComponents(filepath.Dir(p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		st, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			if p == paths[0] || !capture && m.refs[p] != nil {
				return fail(CodeIdentityChanged, "filter target ref disappeared", err)
			}
			if capture {
				m.refs[p] = nil
			}
			continue
		}
		if err != nil {
			return err
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() || (st.Mode() != 0600 && st.Mode() != 0644) {
			return fail(CodeInvalidInput, "filter target ref mode", nil)
		}
		pin, err := pinFilterFileKind(p, "", 1<<20, false, nil)
		if err != nil {
			return err
		}
		if capture {
			m.refs[p] = &pin
		} else if m.refs[p] == nil || !reflect.DeepEqual(*m.refs[p], pin) {
			return fail(CodeIdentityChanged, "filter target ref changed", nil)
		}
	}
	if m.refs[paths[0]] == nil {
		return fail(CodeIdentityChanged, "filter target HEAD missing", nil)
	}
	// Support direct branches only. A symbolic branch would introduce an
	// unpinned referent even though HEAD and the requested loose ref stay fixed.
	for _, check := range []struct{ path, text string }{
		{paths[0], "ref: " + m.request.Ref + "\n"},
		{paths[1], m.request.BaseOID + "\n"},
		{paths[3], m.candidate.CandidateOID + "\n"},
	} {
		if m.refs[check.path] == nil {
			continue
		} // packed-only resolution is pinned above.
		f, err := os.OpenFile(check.path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 8193))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(raw) > 8192 || !bytes.Equal(raw, []byte(check.text)) || !verifyFilterPin(*m.refs[check.path]) {
			return fail(CodeTargetDrift, "filter target direct ref", errors.Join(readErr, closeErr))
		}
	}
	return nil
}

// Finish without another Git command (and hence without another journal
// callback). The full Git/context checks ran above; these original file pins
// reject a callback changing their last observed source after its final query.
func (m *FilterTargetObservation) verifySourcePins() error {
	g := m.source
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || !g.verifySources() || !verifyFilterPin(m.configPin) {
		return fail(CodeIdentityChanged, "filter target source pins", nil)
	}
	if err := g.verifyRepositoryFiles(); err != nil {
		return err
	}
	for _, pin := range g.attributes {
		if !verifyFilterPin(pin) {
			return fail(CodeIdentityChanged, "filter target source attributes", nil)
		}
	}
	return nil
}
