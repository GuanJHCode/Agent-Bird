//go:build darwin

package gitfilter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

const carrierSocketParent = "/private/tmp"
const carrierSocketName = "socket"

type carrierSocketState struct {
	mu                                                    sync.Mutex
	busy, unknown, closed, closeDone, removed, notCreated bool
	binding                                               carrierSocketBinding
	journal                                               carrierSocketJournal
	segmentPath, basename, directory, socket              string
	segment, parent, child                                *os.File
	segmentID, parentID, childID, socketID                carrierSocketIdentity
	listener                                              *net.UnixListener
}

func carrierID(f *os.File) (carrierSocketIdentity, error) {
	n, e := identity(f)
	return carrierSocketIdentity{n.Device, n.Inode, n.Nlink, n.UID, n.Mode}, e
}
func carrierNamedAt(f *os.File, name string) (carrierSocketIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(f.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return carrierSocketIdentity{}, err
	}
	mode := os.FileMode(st.Mode & 0777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= os.ModeDir
	case unix.S_IFSOCK:
		mode |= os.ModeSocket
	case unix.S_IFLNK:
		mode |= os.ModeSymlink
	default:
		mode |= os.ModeDevice // reject unsupported nodes rather than regular-file adoption
	}
	if st.Mode&unix.S_ISVTX != 0 {
		mode |= os.ModeSticky
	}
	if st.Mode&unix.S_ISUID != 0 {
		mode |= os.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		mode |= os.ModeSetgid
	}
	return carrierSocketIdentity{uint64(st.Dev), st.Ino, uint64(st.Nlink), st.Uid, uint32(mode)}, nil
}
func carrierDirectorySame(a, b carrierSocketIdentity) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.UID == b.UID && a.Mode == b.Mode && os.FileMode(a.Mode).IsDir()
}
func carrierHold(path string) (*os.File, carrierSocketIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, carrierSocketIdentity{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	id, err := carrierID(f)
	if err != nil {
		f.Close()
		return nil, id, err
	}
	return f, id, nil
}
func (s *carrierSocketState) checkRoots() error {
	for _, root := range []struct {
		f    *os.File
		path string
		id   carrierSocketIdentity
	}{{s.segment, s.segmentPath, s.segmentID}, {s.parent, carrierSocketParent, s.parentID}} {
		if root.f == nil {
			return errCarrierSocket
		}
		held, err := carrierID(root.f)
		n, ne := named(root.path)
		nm := carrierSocketIdentity{n.Device, n.Inode, n.Nlink, n.UID, n.Mode}
		if err != nil || ne != nil || !canonical(root.path) || !carrierDirectorySame(held, root.id) || !carrierDirectorySame(nm, root.id) {
			return errCarrierSocket
		}
	}
	return nil
}
func (s *carrierSocketState) checkChild(present bool, socket bool) error {
	if err := s.checkRoots(); err != nil {
		return err
	}
	nm, err := carrierNamedAt(s.parent, s.basename)
	if !present {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errCarrierSocket
	}
	if err != nil || s.child == nil {
		return errCarrierSocket
	}
	held, err := carrierID(s.child)
	if err != nil || !carrierDirectorySame(held, s.childID) || !carrierDirectorySame(nm, s.childID) {
		return errCarrierSocket
	}
	// A fresh descriptor gives an independent directory offset; it is never a new baseline.
	fd, err := unix.Openat(int(s.child.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), s.directory)
	entries, readErr := f.ReadDir(2)
	closeErr := f.Close()
	want := 0
	if socket {
		want = 1
	}
	if readErr != nil && readErr != io.EOF || closeErr != nil || len(entries) != want {
		return errCarrierSocket
	}
	if socket {
		if entries[0].Name() != carrierSocketName {
			return errCarrierSocket
		}
		id, e := carrierNamedAt(s.child, carrierSocketName)
		if e != nil || id != s.socketID {
			return errCarrierSocket
		}
	}
	return nil
}
func (o *carrierSocket) begin(ctx context.Context, allowClosed bool) error {
	if !o.original() || !carrierSocketContext(ctx) {
		return errCarrierSocket
	}
	s := o.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.removed || s.unknown || s.closed && !allowClosed {
		return errCarrierSocket
	}
	s.busy = true
	return nil
}
func (o *carrierSocket) end() { s := o.state; s.mu.Lock(); s.busy = false; s.mu.Unlock() }
func (o *carrierSocket) uncertain(err error) error {
	s := o.state
	s.mu.Lock()
	s.unknown = true
	s.mu.Unlock()
	if err == nil {
		return errCarrierSocket
	}
	return err
}
func (s *carrierSocketState) record(ctx context.Context, phase string) error {
	if !carrierSocketContext(ctx) {
		return errCarrierSocket
	}
	s.mu.Lock()
	r := carrierSocketRecord{Phase: phase, Directory: s.directory, Socket: s.socket, Basename: s.basename, Binding: s.binding, Segment: s.segmentID, Parent: s.parentID, Owned: s.childID, SocketID: s.socketID, Known: !s.unknown, Unknown: s.unknown}
	s.mu.Unlock()
	// No owner-state mutex spans arbitrary synchronous journal code.
	if err := s.journal.RecordCarrierSocket(ctx, r); err != nil {
		return err
	}
	if !carrierSocketContext(ctx) {
		return errCarrierSocket
	}
	return nil
}
func newCarrierSocket(ctx context.Context, segment string, binding carrierSocketBinding, journal carrierSocketJournal) (*carrierSocket, error) {
	if !carrierSocketContext(ctx) || !binding.valid() || journal == nil || !canonical(segment) || !canonical(carrierSocketParent) {
		return nil, errCarrierSocket
	}
	s := &carrierSocketState{binding: binding, journal: journal, segmentPath: segment, busy: true}
	var err error
	s.segment, s.segmentID, err = carrierHold(segment)
	if err != nil {
		return nil, err
	}
	beforeJournal := func(err error) (*carrierSocket, error) {
		if s.parent != nil {
			s.parent.Close()
		}
		s.segment.Close()
		return nil, err
	}
	if s.segmentID.UID != uint32(os.Getuid()) || os.FileMode(s.segmentID.Mode) != os.ModeDir|0700 {
		return beforeJournal(errCarrierSocket)
	}
	s.parent, s.parentID, err = carrierHold(carrierSocketParent)
	if err != nil {
		return beforeJournal(err)
	}
	if s.parentID.UID != 0 || os.FileMode(s.parentID.Mode) != os.ModeDir|os.ModeSticky|0777 || s.checkRoots() != nil {
		return beforeJournal(errCarrierSocket)
	}
	for i := 0; i < 8; i++ {
		var random [12]byte
		if _, err = rand.Read(random[:]); err != nil {
			return beforeJournal(err)
		}
		candidate := "codex-carrier-" + hex.EncodeToString(random[:])
		if _, e := carrierNamedAt(s.parent, candidate); errors.Is(e, os.ErrNotExist) {
			s.basename = candidate
			break
		} else if e != nil {
			return beforeJournal(e)
		}
	}
	if s.basename == "" {
		return beforeJournal(errCarrierSocket)
	}
	s.directory = filepath.Join(carrierSocketParent, s.basename)
	s.socket = filepath.Join(s.directory, carrierSocketName)
	if len([]byte(s.socket)) >= len(unix.RawSockaddrUnix{}.Path) {
		return beforeJournal(errCarrierSocket)
	}
	o := &carrierSocket{state: s}
	o.self = o
	defer o.end()
	fail := func(err error) (*carrierSocket, error) { return o, o.uncertain(err) }
	if err = s.record(ctx, "reserved"); err != nil {
		return fail(err)
	}
	// Recheck both roots before even attempting Mkdirat. A callback may commit and fail.
	if err = s.checkRoots(); err != nil {
		return fail(err)
	}
	if err = unix.Mkdirat(int(s.parent.Fd()), s.basename, 0700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return fail(err)
		}
		if e := s.checkRoots(); e != nil {
			return fail(e)
		}
		s.notCreated = true
		if e := s.record(ctx, "not_created"); e != nil {
			return fail(e)
		}
		if e := s.checkRoots(); e != nil {
			return fail(e)
		}
		return o, err // Proven no owned node; never touch the foreign entry.
	}
	fd, err := unix.Openat(int(s.parent.Fd()), s.basename, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	s.child = os.NewFile(uintptr(fd), s.directory)
	s.childID, err = carrierID(s.child)
	if err != nil || s.childID.UID != uint32(os.Getuid()) || os.FileMode(s.childID.Mode) != os.ModeDir|0700 {
		return fail(errCarrierSocket)
	}
	if err = s.checkChild(true, false); err != nil {
		return fail(err)
	}
	if err = s.parent.Sync(); err != nil {
		return fail(err)
	}
	if !carrierSocketContext(ctx) {
		return fail(errCarrierSocket)
	}
	// Darwin has no bindat: bracket the sole pathname bind with held/named checks.
	s.listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: s.socket, Net: "unix"})
	if s.listener != nil {
		s.listener.SetUnlinkOnClose(false)
	}
	if err != nil {
		return fail(err)
	}
	// Freeze the new node before chmod, then require the same socket afterwards.
	s.socketID, err = carrierNamedAt(s.child, carrierSocketName)
	if err != nil || s.socketID.UID != uint32(os.Getuid()) || s.socketID.Nlink != 1 || os.FileMode(s.socketID.Mode)&os.ModeType != os.ModeSocket {
		return fail(errCarrierSocket)
	}
	if err = s.checkChild(true, true); err != nil {
		return fail(err)
	}
	beforeChmod := s.socketID
	if err = unix.Fchmodat(int(s.child.Fd()), carrierSocketName, 0600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fail(err)
	}
	s.socketID, err = carrierNamedAt(s.child, carrierSocketName)
	if err != nil || s.socketID.Device != beforeChmod.Device || s.socketID.Inode != beforeChmod.Inode || s.socketID.UID != beforeChmod.UID || s.socketID.Nlink != 1 || os.FileMode(s.socketID.Mode) != os.ModeSocket|0600 {
		return fail(errCarrierSocket)
	}
	if err = s.checkChild(true, true); err != nil {
		return fail(err)
	}
	if err = s.child.Sync(); err != nil {
		return fail(err)
	}
	if err = s.record(ctx, "prepared"); err != nil {
		return fail(err)
	}
	if err = s.checkChild(true, true); err != nil {
		return fail(err)
	}
	return o, nil
}
func (o *carrierSocket) verify(ctx context.Context) error {
	if err := o.begin(ctx, false); err != nil {
		return err
	}
	defer o.end()
	s := o.state
	if s.notCreated {
		return errCarrierSocket
	}
	if err := s.checkChild(true, true); err != nil {
		return o.uncertain(err)
	}
	return nil
}
func (o *carrierSocket) close(ctx context.Context) error {
	if !o.original() {
		return errCarrierSocket
	}
	s := o.state
	s.mu.Lock()
	if s.busy || s.closed || s.removed {
		s.mu.Unlock()
		return errCarrierSocket
	}
	s.closed, s.busy = true, true
	listener := s.listener
	s.mu.Unlock()
	defer o.end()
	// Always attempt original-listener closure, including cancelled/Unknown owners.
	done := make(chan error, 1)
	go func() {
		var err error
		if listener != nil {
			err = listener.Close()
		}
		done <- err
	}()
	if !carrierSocketContext(ctx) {
		return o.uncertain(errCarrierSocket)
	}
	select {
	case err := <-done:
		s.mu.Lock()
		s.closeDone = err == nil
		unknown := s.unknown
		s.mu.Unlock()
		if err != nil || unknown {
			return o.uncertain(err)
		}
		return nil
	case <-ctx.Done():
		return o.uncertain(ctx.Err())
	}
}
func (o *carrierSocket) remove(ctx context.Context) error {
	if err := o.begin(ctx, true); err != nil {
		return err
	}
	defer o.end()
	s := o.state
	if !s.closed || !s.closeDone || s.notCreated {
		return errCarrierSocket
	}
	if err := s.checkChild(true, true); err != nil {
		return o.uncertain(err)
	}
	if err := s.record(ctx, "removing"); err != nil {
		return o.uncertain(err)
	}
	if err := s.checkChild(true, true); err != nil {
		return o.uncertain(err)
	}
	if !carrierSocketContext(ctx) {
		return o.uncertain(errCarrierSocket)
	}
	if err := unix.Unlinkat(int(s.child.Fd()), carrierSocketName, 0); err != nil {
		return o.uncertain(err)
	}
	if err := s.child.Sync(); err != nil {
		return o.uncertain(err)
	}
	if err := s.checkChild(true, false); err != nil {
		return o.uncertain(err)
	}
	if !carrierSocketContext(ctx) {
		return o.uncertain(errCarrierSocket)
	}
	if err := unix.Unlinkat(int(s.parent.Fd()), s.basename, unix.AT_REMOVEDIR); err != nil {
		return o.uncertain(err)
	}
	if err := s.parent.Sync(); err != nil {
		return o.uncertain(err)
	}
	if err := s.checkChild(false, false); err != nil {
		return o.uncertain(err)
	}
	if err := s.record(ctx, "removed"); err != nil {
		return o.uncertain(err)
	}
	if err := s.checkChild(false, false); err != nil {
		return o.uncertain(err)
	}
	if err := s.closeFDs(); err != nil {
		return o.uncertain(err)
	}
	s.mu.Lock()
	s.removed = true
	s.mu.Unlock()
	return nil
}

// Releasing original descriptors never deletes retained/foreign nodes. Failed
// owners remain live Unknown objects; there is no adoption or cleanup scan.
func (s *carrierSocketState) closeFDs() error {
	var errs []error
	for _, f := range []*os.File{s.child, s.parent, s.segment} {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	return errors.Join(errs...)
}
