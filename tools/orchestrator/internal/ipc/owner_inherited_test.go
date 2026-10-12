package ipc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func ownerInheritedDup(t *testing.T, f *os.File) int {
	t.Helper()
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return fd // ownership passes to the receiving helper
}

func ownerInheritedClosed(t *testing.T, fd int) {
	t.Helper()
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, syscall.EBADF) {
		t.Error("original inherited descriptor was not closed", err)
		_ = syscall.Close(fd)
	}
}

func ownerInheritedCloexec(t *testing.T, fd uintptr) {
	t.Helper()
	flags, err := unix.FcntlInt(fd, unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Error("control descriptor can cross worker exec", err)
	}
}

func TestOwnerInheritedBootstrapPipeIsBoundedAndOwned(t *testing.T) {
	for _, n := range []int{0, 17, 64 * 1024, 64*1024 + 1} {
		t.Run("bytes_"+strconv.Itoa(n), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			fd := ownerInheritedDup(t, r)
			_ = r.Close()
			payload := bytes.Repeat([]byte("x"), n)
			done := make(chan struct{})
			go func() { _, _ = w.Write(payload); _ = w.Close(); close(done) }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			got, err := ReadOwnerBootstrapPipe(ctx, fd)
			cancel()
			ownerInheritedClosed(t, fd)
			_ = w.Close()
			<-done
			if n > 0 && n <= 64*1024 {
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatal("valid bounded bootstrap not consumed", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("empty or oversized bootstrap accepted")
			}
		})
	}
}

func TestOwnerInheritedBootstrapRejectsFileAndDeadlineClosesPipe(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-pipe")
	if err != nil {
		t.Fatal(err)
	}
	fd := ownerInheritedDup(t, f)
	_ = f.Close()
	if _, err = ReadOwnerBootstrapPipe(context.Background(), fd); err == nil {
		t.Fatal("regular file was accepted as inherited bootstrap")
	}
	ownerInheritedClosed(t, fd)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	fd = ownerInheritedDup(t, r)
	_ = r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err = ReadOwnerBootstrapPipe(ctx, fd); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("unclosed bootstrap writer escaped its original deadline", err)
	}
	ownerInheritedClosed(t, fd)
}

func TestOwnerInheritedControlPairUsesFramingAndClosesOnExec(t *testing.T) {
	parent, child, err := NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	defer child.Close()
	ownerInheritedCloexec(t, child.Fd())
	fd := ownerInheritedDup(t, child)
	_ = child.Close()
	peer, err := TakeOwnerControlFD(fd)
	if err != nil {
		ownerInheritedClosed(t, fd)
		t.Fatal(err)
	}
	defer peer.Close()
	ownerInheritedClosed(t, fd)
	for _, conn := range []*net.UnixConn{parent, peer} {
		if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		raw, err := conn.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		if err = raw.Control(func(fd uintptr) { ownerInheritedCloexec(t, fd) }); err != nil {
			t.Fatal(err)
		}
	}
	message := Envelope{Version: Version, Kind: KindOwnerCommand, RequestID: "forward-only", Epoch: 7, Payload: []byte(`{"opaque":"signed-body"}`)}
	if err = Write(parent, message); err != nil {
		t.Fatal(err)
	}
	got, err := Read(peer)
	if err != nil || got.Kind != message.Kind || !bytes.Equal(got.Payload, message.Payload) || got.Epoch != message.Epoch {
		t.Fatal("private control changed forwarded frame", err)
	}
	if err = peer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(parent); !errors.Is(err, io.EOF) {
		t.Fatal("control EOF was not observable", err)
	}
}

func TestOwnerInheritedControlRejectsOtherDescriptorKinds(t *testing.T) {
	for _, kind := range []string{"pipe", "file", "datagram", "listener"} {
		t.Run(kind, func(t *testing.T) {
			var f *os.File
			var err error
			switch kind {
			case "pipe":
				var writer *os.File
				f, writer, err = os.Pipe()
				if writer != nil {
					defer writer.Close()
				}
			case "file":
				f, err = os.CreateTemp(t.TempDir(), "not-socket")
			case "datagram":
				var pair [2]int
				pair, err = syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
				if err == nil {
					f = os.NewFile(uintptr(pair[0]), "not-stream")
					defer syscall.Close(pair[1])
				}
			case "listener":
				var listener *net.UnixListener
				listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "control.sock"), Net: "unix"})
				if err == nil {
					defer listener.Close()
					f, err = listener.File()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			fd := ownerInheritedDup(t, f)
			_ = f.Close()
			if conn, err := TakeOwnerControlFD(fd); err == nil || conn != nil {
				if conn != nil {
					_ = conn.Close()
				}
				t.Fatal("non-pair descriptor accepted")
			}
			ownerInheritedClosed(t, fd)
		})
	}
}
