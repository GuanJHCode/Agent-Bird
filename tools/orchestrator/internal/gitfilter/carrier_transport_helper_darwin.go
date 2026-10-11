//go:build darwin

package gitfilter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"golang.org/x/sys/unix"
)

func helperID(id carrierSocketIdentity) CarrierHelperNamedIdentity {
	return CarrierHelperNamedIdentity{id.Device, id.Inode, id.Nlink, id.UID, id.Mode}
}
func (tr *carrierTransport) helperRoute(ctx context.Context, pin CarrierHelperExecutableExpectation) (CarrierHelperRoute, error) {
	if err := tr.verify(ctx); err != nil {
		return CarrierHelperRoute{}, err
	}
	host, err := process.CaptureHostPeerExpectation(ctx)
	if err != nil {
		return CarrierHelperRoute{}, err
	}
	deadline, _ := tr.ctx.Deadline()
	s := tr.socket.state
	return CarrierHelperRoute{Domain: carrierHelperDomain, Host: host, Socket: s.socket, Parent: helperID(s.parentID), Directory: helperID(s.childID), SocketID: helperID(s.socketID), Helper: pin, Deadline: deadline}, nil
}
func helperNamed(r CarrierHelperRoute) error {
	dir := filepath.Dir(r.Socket)
	base := filepath.Base(dir)
	if filepath.Dir(dir) != carrierSocketParent || filepath.Base(r.Socket) != carrierSocketName || len(base) != len("codex-carrier-")+24 || !strings.HasPrefix(base, "codex-carrier-") || !canonical(r.Socket) {
		return ErrCarrierHelper
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(base, "codex-carrier-")); err != nil {
		return ErrCarrierHelper
	}
	parent, pid, err := carrierHold(carrierSocketParent)
	if err != nil {
		return err
	}
	defer parent.Close()
	pinnedParent := r.Parent
	pinnedParent.Nlink = pid.Nlink
	if helperID(pid) != pinnedParent || pid.UID != 0 || os.FileMode(pid.Mode) != os.ModeDir|os.ModeSticky|0777 {
		return ErrCarrierHelper
	}
	namedDir, err := carrierNamedAt(parent, base)
	pinnedDirectory := r.Directory
	pinnedDirectory.Nlink = namedDir.Nlink
	if err != nil || helperID(namedDir) != pinnedDirectory {
		return ErrCarrierHelper
	}
	child, cid, err := carrierHold(dir)
	if err != nil {
		return err
	}
	defer child.Close()
	pinnedDirectory.Nlink = cid.Nlink
	if helperID(cid) != pinnedDirectory || cid.UID != uint32(os.Geteuid()) || os.FileMode(cid.Mode) != os.ModeDir|0700 {
		return ErrCarrierHelper
	}
	sock, err := carrierNamedAt(child, carrierSocketName)
	if err != nil || helperID(sock) != r.SocketID || sock.UID != uint32(os.Geteuid()) || sock.Nlink != 1 || os.FileMode(sock.Mode) != os.ModeSocket|0600 {
		return ErrCarrierHelper
	}
	entries, err := child.ReadDir(2)
	if err != nil && err != io.EOF || len(entries) != 1 || entries[0].Name() != carrierSocketName {
		return ErrCarrierHelper
	}
	again, err := carrierNamedAt(parent, base)
	pinnedDirectory.Nlink = again.Nlink
	if err != nil || helperID(again) != pinnedDirectory {
		return ErrCarrierHelper
	}
	return nil
}
func helperExecutable(ctx context.Context, pin CarrierHelperExecutableExpectation) error {
	current, err := os.Executable()
	if err != nil {
		return err
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil || current != pin.Path || !canonical(pin.Path) || !exactDigest(pin.SHA256) {
		return ErrCarrierHelper
	}
	fd, err := unix.Open(pin.Path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), pin.Path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 256<<20 {
		return ErrCarrierHelper
	}
	hash := sha256.New()
	n, err := io.Copy(hash, &helperContextReader{ctx: ctx, reader: io.LimitReader(f, info.Size()+1)})
	after, e := f.Stat()
	named, e2 := os.Lstat(pin.Path)
	if err != nil || e != nil || e2 != nil || n != info.Size() || !os.SameFile(info, after) || !os.SameFile(info, named) || info.Size() != after.Size() || info.ModTime() != after.ModTime() || hex.EncodeToString(hash.Sum(nil)) != pin.SHA256 {
		return ErrCarrierHelper
	}
	return nil
}
func helperPipe(f *os.File) (*os.File, error) {
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, ErrCarrierHelper
	}
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	if err = unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "git-filter-helper-pipe"), nil
}
func runCarrierHelper(ctx context.Context, r CarrierHelperRoute, input, output *os.File) error {
	if err := helperExecutable(ctx, r.Helper); err != nil {
		return errors.Join(ErrCarrierHelper, errors.New("helper_executable_expectation"), err)
	}
	if err := helperNamed(r); err != nil {
		return errors.Join(ErrCarrierHelper, errors.New("helper_named_expectation"), err)
	}
	dial := net.Dialer{}
	endpoint, err := dial.DialContext(ctx, "unix", r.Socket)
	if err != nil {
		return errors.Join(ErrCarrierHelper, err)
	}
	conn, ok := endpoint.(*net.UnixConn)
	if !ok {
		endpoint.Close()
		return ErrCarrierHelper
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	var verifyMu sync.Mutex
	verify := func() error {
		verifyMu.Lock()
		defer verifyMu.Unlock()
		if !carrierSocketContext(ctx) {
			return ErrCarrierHelper
		}
		if err := helperNamed(r); err != nil {
			return err
		}
		pid, err := ipc.PeerPID(conn)
		if err != nil {
			return err
		}
		if err = process.VerifyHostPeerExpectation(ctx, pid, r.Host); err != nil {
			return err
		}
		return helperNamed(r)
	}
	if err = verify(); err != nil {
		return errors.Join(ErrCarrierHelper, errors.New("helper_host_expectation"), err)
	}
	in, err := helperPipe(input)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := helperPipe(output)
	if err != nil {
		return err
	}
	defer out.Close()
	if err = in.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err = out.SetWriteDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close(); in.Close(); out.Close() })
	defer stop()
	type result struct {
		input bool
		err   error
	}
	done := make(chan result, 2)
	copyVerified := func(dst io.Writer, src io.Reader) error {
		buffer := make([]byte, 32<<10)
		for {
			if e := verify(); e != nil {
				return e
			}
			n, e := src.Read(buffer)
			if n > 0 {
				if err := verify(); err != nil {
					return err
				}
				written, err := dst.Write(buffer[:n])
				if err != nil {
					return err
				}
				if written != n {
					return io.ErrShortWrite
				}
				if err = verify(); err != nil {
					return err
				}
			}
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
		}
	}
	go func() {
		e := copyVerified(conn, in)
		if e == nil {
			e = conn.CloseWrite()
		}
		done <- result{true, e}
	}()
	go func() { done <- result{false, copyVerified(out, conn)} }()
	first := <-done
	if first.err != nil || !first.input {
		conn.Close()
		in.Close()
		out.Close()
	}
	second := <-done
	if !first.input && second.input && errors.Is(second.err, os.ErrClosed) {
		second.err = nil
	}
	if ctx.Err() != nil {
		return errors.Join(ErrCarrierHelper, ctx.Err())
	}
	return errors.Join(first.err, second.err)
}

// A regular executable read stays bounded by size and the original deadline.
type helperContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *helperContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
