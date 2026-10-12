package store

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"golang.org/x/sys/unix"
)

// Only this successful exclusive creation can authorize the operation journal.
// A rolled-back/uncertain transaction may leave empty metadata; it is never
// adopted on a later request or removed by a directory scan.
func createCandidateMaintenanceJournal(a StoreRootAuthority) (root string, identity gitopsworker.MaintenanceJournalIdentity, err error) {
	bad := CodeError("candidate_maintenance_journal_invalid")
	if err = a.Validate(); err != nil {
		return
	}
	n := a.Nodes()
	fd, e := unix.Open(n.StateRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		err = e
		return
	}
	parent := os.NewFile(uintptr(fd), n.StateRoot)
	defer parent.Close()
	st, e := parent.Stat()
	if e != nil {
		err = e
		return
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || uint64(s.Dev) != n.StateDevice || uint64(s.Ino) != n.StateInode || !st.IsDir() || st.Mode().Perm() != 0700 || s.Uid != uint32(os.Geteuid()) {
		err = bad
		return
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return
	}
	name := "candidate-maintenance-" + hex.EncodeToString(random[:])
	root = filepath.Join(n.StateRoot, name)
	if err = unix.Mkdirat(fd, name, 0700); err != nil {
		return
	}
	childFD, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		err = e
		return
	}
	directory := os.NewFile(uintptr(childFD), root)
	defer directory.Close()
	info, e := directory.Stat()
	if e != nil {
		err = e
		return
	}
	ds, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || ds.Uid != uint32(os.Geteuid()) {
		err = bad
		return
	}
	identity.Directory = gitops.DirectoryIdentity{Device: int64(ds.Dev), Inode: int64(ds.Ino), Mode: int64(info.Mode()), UID: int64(ds.Uid)}
	for _, entry := range []struct {
		name   string
		target *gitops.FileIdentity
	}{{"events.jsonl", &identity.Events}, {"events.lock", &identity.Lock}} {
		fileFD, e := unix.Openat(childFD, entry.name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if e != nil {
			err = e
			return
		}
		f := os.NewFile(uintptr(fileFD), filepath.Join(root, entry.name))
		fi, e := f.Stat()
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			err = e
			return
		}
		fs, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0600 || fs.Uid != uint32(os.Geteuid()) || fs.Nlink != 1 || fi.Size() != 0 {
			err = bad
			return
		}
		*entry.target = gitops.FileIdentity{Device: int64(fs.Dev), Inode: int64(fs.Ino), Mode: int64(fi.Mode()), Size: 0, ModUnixNano: fi.ModTime().UnixNano()}
	}
	if err = directory.Sync(); err != nil {
		return
	}
	if err = parent.Sync(); err != nil {
		return
	}
	named, e := os.Lstat(root)
	if e != nil || !os.SameFile(info, named) {
		err = bad
		return
	}
	err = a.Validate()
	return
}
