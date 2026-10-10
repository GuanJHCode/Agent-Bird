package gitops

import (
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// filterTargetIndexGuard is source-owned evidence. It intentionally does not
// share the observer descriptors: closing an observation cannot weaken source
// process boundaries.
type filterTargetIndexGuard struct {
	gitDir, index *os.File
	gitIdentity   DirectoryIdentity
	indexIdentity FileIdentity
	indexSHA      string
	gitPath       string
}

func openFilterTargetIndexGuard(g *FilterGitContext) (*filterTargetIndexGuard, error) {
	guard := &filterTargetIndexGuard{gitPath: g.repository.gitDir}
	var err error
	guard.gitDir, err = os.OpenFile(guard.gitPath, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	failOpen := func(err error) (*filterTargetIndexGuard, error) { _ = guard.Close(); return nil, err }
	guard.gitIdentity, err = filterTargetDirectory(guard.gitDir)
	if err != nil || guard.gitIdentity != g.repository.gitIdentity {
		return failOpen(fail(CodeIdentityChanged, "filter target git directory", err))
	}
	guard.index, err = filterOpenAt(guard.gitDir, "index", unix.O_RDONLY, 0)
	if err != nil {
		return failOpen(err)
	}
	if err = guard.checkMetadata(); err != nil {
		return failOpen(err)
	}
	var raw []byte
	raw, guard.indexIdentity, err = guard.readIndex()
	if err != nil {
		return failOpen(err)
	}
	if err = filterTargetIndexHeader(raw); err != nil {
		return failOpen(err)
	}
	guard.indexSHA = filterTargetHash(raw)
	return guard, nil
}

func (g *filterTargetIndexGuard) Close() error {
	var errs []error
	for _, file := range []**os.File{&g.index, &g.gitDir} {
		if *file != nil {
			errs = append(errs, (*file).Close())
			*file = nil
		}
	}
	return errors.Join(errs...)
}

func (g *filterTargetIndexGuard) Verify() error {
	if g == nil || g.gitDir == nil || g.index == nil {
		return fail(CodeIdentityChanged, "filter target index guard closed", nil)
	}
	held, err := filterTargetDirectory(g.gitDir)
	named, namedErr := directoryIdentity(g.gitPath)
	if err != nil || namedErr != nil || held != g.gitIdentity || named != held || CanonicalPath(g.gitPath) != g.gitPath {
		return fail(CodeIdentityChanged, "filter target git directory", errors.Join(err, namedErr))
	}
	if err = g.checkMetadata(); err != nil {
		return err
	}
	raw, id, err := g.readIndex()
	if err != nil || id != g.indexIdentity || filterTargetHash(raw) != g.indexSHA {
		return fail(CodeIdentityChanged, "filter target index changed", err)
	}
	return filterTargetIndexHeader(raw)
}

func (g *filterTargetIndexGuard) readIndex() ([]byte, FileIdentity, error) {
	id, err := filterTargetRegular(g.index)
	if err != nil || (id.Mode != 0600 && id.Mode != 0644) {
		return nil, id, fail(CodeInvalidInput, "filter target index mode", err)
	}
	if _, err = g.index.Seek(0, io.SeekStart); err != nil {
		return nil, id, err
	}
	raw, err := io.ReadAll(io.LimitReader(g.index, 16<<20+1))
	after, afterErr := filterTargetRegular(g.index)
	named, openErr := filterOpenAt(g.gitDir, "index", unix.O_RDONLY, 0)
	if openErr != nil {
		return nil, id, openErr
	}
	namedID, namedErr := filterTargetRegular(named)
	closeErr := named.Close()
	if err != nil || afterErr != nil || namedErr != nil || closeErr != nil || id != after || id != namedID || int64(len(raw)) != id.Size {
		return nil, id, fail(CodeIdentityChanged, "filter target index identity", errors.Join(err, afterErr, namedErr, closeErr))
	}
	return raw, id, nil
}

func (g *filterTargetIndexGuard) checkMetadata() error {
	if _, err := g.gitDir.Seek(0, io.SeekStart); err != nil {
		return err
	}
	count := 0
	for {
		entries, err := g.gitDir.ReadDir(128)
		count += len(entries)
		if count > 4096 {
			return fail(CodeInvalidInput, "filter target git directory bounds", nil)
		}
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			if strings.HasPrefix(name, "sharedindex.") {
				return fail(CodeInvalidInput, "filter target split index", nil)
			}
			switch name {
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
	info, err := filterOpenAt(g.gitDir, "info", unix.O_RDONLY|unix.O_DIRECTORY, 0)
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
