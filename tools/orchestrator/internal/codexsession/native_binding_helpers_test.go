//go:build darwin

package codexsession

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These helpers are acceptance-test only. Pure selections below have no
// TestMain, exec, process launcher or access to source auth/config references.
type bindingWriteSnapshot struct {
	Methods         map[string]int
	Invalid, Closed bool
}
type bindingObservedStream struct {
	io.ReadWriteCloser
	mu                sync.Mutex
	pending           []byte
	seen              map[int]string
	completed, writes int
	invalid, closed   bool
}

// Only observations take mu; native I/O and Close never hold it. Counting does
// not change n, error, payload, or Close semantics, including a partial Write.
func (s *bindingObservedStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	if s.closed || s.writes > 256 || len(s.pending)+len(p) > 1<<20 {
		s.invalid = true
	}
	if !s.invalid {
		candidate := append(append([]byte(nil), s.pending...), p...)
		index := s.completed
		for len(candidate) > 0 {
			end := bytes.IndexByte(candidate, '\n')
			if end < 0 {
				break
			}
			var frame map[string]json.RawMessage
			var method string
			if index >= 16 || json.Unmarshal(candidate[:end], &frame) != nil || json.Unmarshal(frame["method"], &method) != nil {
				s.invalid = true
				break
			}
			switch method {
			case "initialize", "initialized", "config/read", "thread/start", "turn/start":
			default:
				method = "unexpected"
				s.invalid = true
			}
			if s.seen == nil {
				s.seen = make(map[int]string)
			}
			if previous, exists := s.seen[index]; exists && previous != method {
				s.invalid = true
				break
			}
			s.seen[index] = method
			index++
			candidate = candidate[end+1:]
		}
	}
	s.mu.Unlock()
	n, err := s.ReadWriteCloser.Write(p)
	s.mu.Lock()
	if n < 0 || n > len(p) {
		s.invalid = true
	} else if !s.invalid {
		s.pending = append(s.pending, p[:n]...)
		for {
			end := bytes.IndexByte(s.pending, '\n')
			if end < 0 {
				break
			}
			s.completed++
			s.pending = s.pending[end+1:]
		}
		if len(s.pending) == 0 {
			s.pending = nil
		}
	}
	s.mu.Unlock()
	return n, err
}
func (s *bindingObservedStream) Close() error {
	err := s.ReadWriteCloser.Close()
	s.mu.Lock()
	s.closed = true
	s.invalid = s.invalid || len(s.pending) > 0 || len(s.seen) != s.completed
	s.pending = nil
	s.mu.Unlock()
	return err
}
func (s *bindingObservedStream) snapshot() bindingWriteSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := bindingWriteSnapshot{Methods: make(map[string]int), Invalid: s.invalid, Closed: s.closed}
	for _, method := range s.seen {
		out.Methods[method]++
	}
	return out
}

type bindingFileIdentity struct {
	Device, Inode, UID, Links uint64
	Mode                      uint32
	Size, Mtime, Ctime        int64
}

var bindingReferenceError = errors.New("native_binding: source_reference_invalid")

func bindingStat(info os.FileInfo) bindingFileIdentity {
	s := info.Sys().(*syscall.Stat_t)
	return bindingFileIdentity{Device: uint64(s.Dev), Inode: s.Ino, UID: uint64(s.Uid), Links: uint64(s.Nlink), Mode: uint32(info.Mode().Perm()), Size: info.Size(), Mtime: s.Mtimespec.Nano(), Ctime: s.Ctimespec.Nano()}
}
func bindingOpenReference(path string) (*os.File, bindingFileIdentity, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || canonical != path {
		return nil, bindingFileIdentity{}, bindingReferenceError
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, bindingFileIdentity{}, bindingReferenceError
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, bindingFileIdentity{}, bindingReferenceError
	}
	id := bindingStat(info)
	if id.UID != uint64(os.Geteuid()) || id.Links != 1 || id.Mode&022 != 0 {
		f.Close()
		return nil, bindingFileIdentity{}, bindingReferenceError
	}
	return f, id, nil
}
func bindingReferenceIdentity(path string) (bindingFileIdentity, error) {
	f, id, err := bindingOpenReference(path)
	if err != nil {
		return id, err
	}
	defer f.Close()
	info, err := os.Lstat(path)
	if err != nil || bindingStat(info) != id {
		return bindingFileIdentity{}, bindingReferenceError
	}
	return id, nil
}
func bindingPrivateRead(path string, limit int64) ([]byte, bindingFileIdentity, error) {
	f, before, err := bindingOpenReference(path)
	if err != nil {
		return nil, before, err
	}
	defer f.Close()
	if limit < 1 || limit > 1<<20 || before.Size < 1 || before.Size > limit {
		return nil, before, bindingReferenceError
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(body)) != before.Size {
		return nil, before, bindingReferenceError
	}
	after, err := f.Stat()
	if err != nil || bindingStat(after) != before {
		return nil, before, bindingReferenceError
	}
	current, err := os.Lstat(path)
	if err != nil || bindingStat(current) != before {
		return nil, before, bindingReferenceError
	}
	return body, before, nil
}

type bindingFragmentStream struct {
	net.Conn
	limit   int
	entered chan struct{}
}

func (s *bindingFragmentStream) Write(p []byte) (int, error) {
	if s.entered != nil {
		close(s.entered)
		s.entered = nil
	}
	if s.limit > 0 && len(p) > s.limit {
		p = p[:s.limit]
	}
	return s.Conn.Write(p)
}

func TestNativeBindingObserverPreservesPartialWriteAndClose(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	wire := []byte("{\"method\":\"initialize\",\"params\":{\"secret_sentinel\":\"not-exported\"}}\n{\"method\":\"initialized\"}\n")
	stream := &bindingObservedStream{ReadWriteCloser: &bindingFragmentStream{Conn: local, limit: 7}}
	done := make(chan []byte, 1)
	go func() { body, _ := io.ReadAll(peer); done <- body }()
	for offset := 0; offset < len(wire); {
		n, err := stream.Write(wire[offset:])
		if err != nil || n <= 0 || n > 7 {
			t.Fatalf("partial write changed: n=%d err=%v", n, err)
		}
		offset += n
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-done:
		if !bytes.Equal(body, wire) {
			t.Fatal("transport bytes changed")
		}
	case <-time.After(time.Second):
		t.Fatal("original peer did not join")
	}
	got := stream.snapshot()
	if got.Invalid || !got.Closed || !reflect.DeepEqual(got.Methods, map[string]int{"initialize": 1, "initialized": 1}) {
		t.Fatalf("method-only observation missing: %#v", got)
	}
	got.Methods["turn/start"] = 1
	if stream.snapshot().Methods["turn/start"] != 0 {
		t.Fatal("snapshot aliases observer")
	}
}

func TestNativeBindingObserverCloseInterruptsOriginalBlockedWrite(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	entered := make(chan struct{})
	stream := &bindingObservedStream{ReadWriteCloser: &bindingFragmentStream{Conn: local, entered: entered}}
	done := make(chan error, 1)
	go func() { _, err := stream.Write([]byte("{\"method\":\"initialize\"}\n")); done <- err }()
	<-entered
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("original error changed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close retained blocked Write")
	}
}

func TestNativeBindingPrivateReadChecksOwnedBoundedRegularInode(t *testing.T) {
	directory := t.TempDir()
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonical, "config-fixture.toml")
	if err := os.WriteFile(path, []byte("model = \"synthetic-model\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body, before, err := bindingPrivateRead(path, 1024)
	if err != nil || string(body) != "model = \"synthetic-model\"\n" || before.Inode == 0 {
		t.Fatalf("bounded regular read missing: %v", err)
	}
	_, stable, err := bindingPrivateRead(path, 1024)
	if err != nil || before != stable {
		t.Fatal("stable original file identity changed")
	}
	for _, mode := range []os.FileMode{0622, 0666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, _, err := bindingPrivateRead(path, 1024); err == nil {
			t.Fatal("untrusted writable reference accepted")
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindingPrivateRead(path, 1); err == nil {
		t.Fatal("oversized source accepted")
	}
	link := filepath.Join(canonical, "symlink.toml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindingPrivateRead(link, 1024); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, _, err := bindingPrivateRead(canonical, 1024); err == nil {
		t.Fatal("directory accepted")
	}
	fifo := filepath.Join(canonical, "input-fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindingPrivateRead(fifo, 1024); err == nil {
		t.Fatal("FIFO accepted")
	}
	hard := filepath.Join(canonical, "hard-link")
	if err := os.Link(path, hard); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindingPrivateRead(path, 1024); err == nil {
		t.Fatal("multiply linked source accepted")
	}
}

func TestNativeBindingObserverRejectsForbiddenAndUnboundedObservations(t *testing.T) {
	for _, body := range []string{"{\"id\":1,\"result\":{}}\n", "{\"method\":\"account/login/start\"}\n", string(bytes.Repeat([]byte{'x'}, (1<<20)+1))} {
		local, peer := net.Pipe()
		stream := &bindingObservedStream{ReadWriteCloser: local}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, peer); peer.Close(); close(done) }()
		n, err := stream.Write([]byte(body))
		if err != nil || n != len(body) {
			t.Fatal("observation changed original write")
		}
		stream.Close()
		<-done
		if !stream.snapshot().Invalid {
			t.Fatal("unbounded or unauthorized observation accepted")
		}
	}
	local, peer := net.Pipe()
	stream := &bindingObservedStream{ReadWriteCloser: local}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, peer); peer.Close(); close(done) }()
	stream.Write([]byte("{\"method\":\"turn/start\"}\n"))
	stream.Close()
	<-done
	if stream.snapshot().Methods["turn/start"] != 1 {
		t.Fatal("turn attempt not observable")
	}
}
