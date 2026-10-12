//go:build darwin

package gitfilter

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"golang.org/x/sys/unix"
)

// The original held file pins the inode that was hashed at admission. Metadata
// drift rejects permanently; no request may reopen/rebaseline/re-hash the path.
type carrierExecutableIdentity struct {
	Device, Inode, Nlink                           uint64
	UID, Mode                                      uint32
	Size, MtimeSec, MtimeNano, CtimeSec, CtimeNano int64
}

func carrierExecutableID(info os.FileInfo) (carrierExecutableIdentity, error) {
	if info == nil {
		return carrierExecutableIdentity{}, process.ErrProcessTreeUnknown
	}
	raw, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return carrierExecutableIdentity{}, process.ErrProcessTreeUnknown
	}
	return carrierExecutableIdentity{uint64(raw.Dev), raw.Ino, uint64(raw.Nlink), raw.Uid, uint32(info.Mode()), info.Size(), raw.Mtimespec.Sec, raw.Mtimespec.Nsec, raw.Ctimespec.Sec, raw.Ctimespec.Nsec}, nil
}

type carrierExecutablePeer struct {
	self          *carrierExecutablePeer
	live          *process.LiveDescendant
	pid           int
	pin           CarrierHelperExecutableExpectation
	file          *os.File
	id            carrierExecutableIdentity
	mu            sync.Mutex
	ready, closed bool
	once          sync.Once
	closeErr      error
}

func newCarrierExecutablePeer(ctx context.Context, live *process.LiveDescendant, pid int, pin CarrierHelperExecutableExpectation, hash func(context.Context, *os.File, int64) (string, error)) (*carrierExecutablePeer, error) {
	p := &carrierExecutablePeer{live: live, pid: pid, pin: pin}
	p.self = p
	if live == nil || hash == nil || !carrierSocketContext(ctx) {
		return p, process.ErrProcessTreeUnknown
	}
	if err := live.Verify(ctx); err != nil {
		return p, err
	}
	path, err := process.ExecutablePath(pid)
	if err != nil {
		return p, errors.Join(process.ErrProcessTreeUnknown, err)
	}
	if path != pin.Path {
		again, e := process.ExecutablePath(pid)
		if e != nil {
			return p, errors.Join(process.ErrProcessTreeUnknown, e)
		}
		if e = live.Verify(ctx); e != nil {
			return p, e
		}
		if again != path {
			return p, process.ErrProcessTreeUnknown
		}
		return p, errCarrierPeerExecutable
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return p, errors.Join(process.ErrProcessTreeUnknown, err)
	}
	p.file = os.NewFile(uintptr(fd), path)
	info, err := p.file.Stat()
	if err != nil {
		return p, errors.Join(process.ErrProcessTreeUnknown, err)
	}
	p.id, err = carrierExecutableID(info)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || p.id.Nlink == 0 || p.id.Size < 1 || p.id.Size > 512<<20 {
		return p, process.ErrProcessTreeUnknown
	}
	if err = p.check(ctx, false); err != nil {
		return p, err
	}
	digest, err := hash(ctx, p.file, p.id.Size)
	if err != nil {
		return p, errors.Join(process.ErrProcessTreeUnknown, err)
	}
	if err = p.check(ctx, false); err != nil {
		return p, err
	}
	if digest != pin.SHA256 {
		return p, errCarrierPeerExecutable
	}
	p.mu.Lock()
	p.ready = true
	p.mu.Unlock()
	return p, nil
}
func (p *carrierExecutablePeer) check(ctx context.Context, requireReady bool) error {
	if p == nil || p.self != p || p.live == nil || !carrierSocketContext(ctx) {
		return process.ErrProcessTreeUnknown
	}
	p.mu.Lock()
	usable := !p.closed && p.file != nil && (!requireReady || p.ready)
	p.mu.Unlock()
	if !usable {
		return process.ErrProcessTreeUnknown
	}
	if err := p.live.Verify(ctx); err != nil {
		return err
	}
	path, err := process.ExecutablePath(p.pid)
	if err != nil {
		return errors.Join(process.ErrProcessTreeUnknown, err)
	}
	// Even a known executable mismatch is bracketed by the frozen raw tuple.
	if path != p.pin.Path {
		again, e := process.ExecutablePath(p.pid)
		if e != nil {
			return errors.Join(process.ErrProcessTreeUnknown, e)
		}
		if e = p.live.Verify(ctx); e != nil {
			return e
		}
		if again != path {
			return process.ErrProcessTreeUnknown
		}
		return errCarrierPeerExecutable
	}
	held, err := p.file.Stat()
	if err != nil {
		return errors.Join(process.ErrProcessTreeUnknown, err)
	}
	heldID, err := carrierExecutableID(held)
	if err != nil {
		return err
	}
	named, err := os.Lstat(p.pin.Path)
	if err != nil {
		return errors.Join(process.ErrProcessTreeUnknown, err)
	}
	namedID, err := carrierExecutableID(named)
	if err != nil {
		return err
	}
	mismatch := heldID != p.id || namedID != p.id
	again, err := process.ExecutablePath(p.pid)
	if err != nil {
		return errors.Join(process.ErrProcessTreeUnknown, err)
	}
	if err = p.live.Verify(ctx); err != nil {
		return err
	}
	if again != path {
		return process.ErrProcessTreeUnknown
	}
	if mismatch {
		return errCarrierPeerExecutable
	}
	return nil
}
func (p *carrierExecutablePeer) verify(ctx context.Context) error { return p.check(ctx, true) }
func (p *carrierExecutablePeer) close() error {
	if p == nil || p.self != p {
		return process.ErrProcessTreeUnknown
	}
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		var err error
		if p.file != nil {
			err = p.file.Close()
		}
		p.mu.Lock()
		p.closeErr = err
		p.mu.Unlock()
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeErr
}
