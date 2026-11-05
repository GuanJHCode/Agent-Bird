package store

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

// A single upload owns at most one quarantine directory. Its durable namespace
// prefix prevents repeated GC from accumulating directories. Existing entries
// are never adopted or traversed: owner resolution is required before retry.
type reworkQuarantine struct {
	source, dir *os.File
	root        *os.Root
}

const quarantinePending CodeError = "rework_cleanup_quarantine_awaiting_cleanup_decision"

func existingReworkQuarantine(root *os.Root, upload string) (bool, error) {
	f, e := root.Open(".")
	if e != nil {
		return false, e
	}
	defer f.Close()
	entries, e := f.ReadDir(-1)
	if e != nil {
		return false, e
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gc-"+upload+"-") {
			return true, nil
		}
	}
	return false, nil
}

func newReworkQuarantine(root *os.Root, upload string) (*reworkQuarantine, error) {
	source, e := root.Open(".")
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*reworkQuarantine, error) { source.Close(); return nil, e }
	entries, e := source.ReadDir(-1)
	if e != nil {
		return fail(e)
	}
	prefix := ".gc-" + upload + "-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			return fail(quarantinePending)
		}
	}
	var nonce [16]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return fail(e)
	}
	name := prefix + hex.EncodeToString(nonce[:])
	if e = root.Mkdir(name, 0700); e != nil {
		return fail(e)
	}
	if e = source.Sync(); e != nil {
		return fail(e)
	}
	private, e := root.OpenRoot(name)
	if e != nil {
		return fail(e)
	}
	dir, e := private.Open(".")
	if e != nil {
		private.Close()
		return fail(e)
	}
	info, e := dir.Stat()
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !storeOwned(info) {
		dir.Close()
		private.Close()
		return fail(CodeError("rework_cleanup_identity_unknown"))
	}
	return &reworkQuarantine{source: source, dir: dir, root: private}, nil
}
func (q *reworkQuarantine) Close() { q.dir.Close(); q.root.Close(); q.source.Close() }

// The source name may change after preflight. Atomically capture whichever
// entry currently occupies it, then validate the captured inode inside our
// private held directory. Never unlink a source path after checking its inode.
func (q *reworkQuarantine) captureAndRemove(name string, r adapter.ReworkObjectRef, links int, cut func(string) error) (result error) {
	defer func() {
		if result != nil {
			result = quarantinePending
		}
	}()
	if e := reworkCaptureNoReplace(q.source, q.dir, name); e != nil {
		return e
	}
	if e := q.source.Sync(); e != nil {
		return e
	}
	if e := q.dir.Sync(); e != nil {
		return e
	}
	if e := cut("gc_after_capture"); e != nil {
		return e
	}
	bad := CodeError("rework_cleanup_identity_unknown")
	info, e := q.root.Lstat(name)
	if e != nil {
		return e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || !storeOwned(info) || info.Mode().Perm() != 0600 || st.Ino != r.Inode || uint64(st.Dev) != r.Device || int(st.Nlink) != links {
		return bad
	}
	// The private namespace is not exposed through any request or worker grant.
	// A no-follow held FD verifies the captured entry before its sole writer unlinks it.
	held, e := q.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return e
	}
	defer held.Close()
	opened, e := held.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return bad
	}
	if e = q.root.Remove(name); e != nil {
		return e
	}
	return q.dir.Sync()
}
