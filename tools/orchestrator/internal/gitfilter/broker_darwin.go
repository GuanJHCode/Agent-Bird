//go:build darwin

package gitfilter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func OpenBroker(ctx context.Context, req BrokerRequest) (ResourceBroker, error) {
	b, err := NewBroker(ctx, req)
	if b == nil {
		return nil, err
	}
	return b, err // Preserve a partially initialized live owner on failure.
}

type brokerFile struct {
	Name     string `json:"name"`
	Identity node   `json:"identity"`
	SHA256   string `json:"sha256,omitempty"`
	file     *os.File
}

type brokerJournal struct {
	Version                 int          `json:"version"`
	ReservationID           string       `json:"reservation_id"`
	ScopeSHA256             string       `json:"scope_sha256"`
	ManifestSHA256          string       `json:"manifest_sha256"`
	Phase                   string       `json:"phase"`
	EverStartAttempted      bool         `json:"ever_start_attempted"`
	StepOrdinal             uint64       `json:"step_ordinal"`
	ResourceInventorySHA256 string       `json:"resource_inventory_sha256"`
	Inventory               []brokerFile `json:"inventory"`
}

// Broker owns exactly two sealed tools and three reusable slots. It never
// reconstructs a live object from disk. Close retains files; Cleanup is explicit.
type Broker struct {
	mu                             sync.Mutex
	req                            BrokerRequest
	segment, root                  *os.Root
	segmentFD                      *os.File
	segmentID                      node
	nodes                          map[string]*brokerFile
	slots                          [3]*os.File
	initialized                    [3]bool
	resource                       string
	journal                        brokerJournal
	journalID                      node
	journalBytes                   []byte
	state                          BrokerState
	ready, failed, removed, closed bool
	streamWriteAt                  func(*os.File, []byte, int64) (int, error) // test seam; nil uses File.WriteAt
}

var errStreamWriterRevoked = errors.New("filter_stream_writer_revoked")

type streamSlotIOError struct{ err error }

func (e *streamSlotIOError) Error() string { return e.err.Error() }
func (e *streamSlotIOError) Unwrap() error { return e.err }

// streamSlotWriter is intentionally not an os.File: a producer can only append
// bounded bytes to the one slot selected by its owning broker call.
type streamSlotWriter struct {
	mu      sync.Mutex
	ctx     context.Context
	file    *os.File
	writeAt func(*os.File, []byte, int64) (int, error)
	offset  int64
	err     error
	revoked bool
}

func (w *streamSlotWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if w.revoked {
		return 0, errStreamWriterRevoked
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return 0, err
	}
	if int64(len(p)) > MaximumBytes-w.offset {
		w.err = errors.New("filter_stream_input_limit")
		return 0, w.err
	}
	writeAt := func(p []byte, off int64) (int, error) { return w.file.WriteAt(p, off) }
	if w.writeAt != nil {
		writeAt = func(p []byte, off int64) (int, error) { return w.writeAt(w.file, p, off) }
	}
	n, err := writeAt(p, w.offset)
	if err != nil {
		w.err = &streamSlotIOError{err}
		return n, w.err
	}
	if n != len(p) {
		w.err = &streamSlotIOError{io.ErrShortWrite}
		return n, w.err
	}
	w.offset += int64(n)
	return n, nil
}

func (w *streamSlotWriter) revoke() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.revoked = true
	return w.err
}

var errBroker = errors.New("git_filter_broker_invalid")

func NewBroker(ctx context.Context, req BrokerRequest) (*Broker, error) {
	req.Manifest = req.Manifest.Clone()
	manifest, err := req.Manifest.Digest()
	scope, scopeErr := contract.GitFilterResourceScopeSHA256(req.Launch, manifest)
	g := req.Grant
	if err != nil || scopeErr != nil || ctx.Err() != nil || contract.RequireHostArtifacts(req.Launch) != nil || g.Validate() != nil || g.ManifestSHA256 != manifest || g.ScopeSHA256 != scope || g.CleanToolSHA256 != req.Manifest.Clean.SHA256 || g.SmudgeToolSHA256 != req.Manifest.Smudge.SHA256 || g.ReservationID == req.Launch.HostArtifacts.TerminalReservationID || g.ReservationID == req.Launch.HostArtifacts.ProgressReservationID {
		return nil, errBroker
	}
	if req.Launch.ValidationTool != nil && (g.ReservationID == req.Launch.ValidationTool.ReservationID || contract.RequireValidationTool(req.Launch, req.Launch.ValidationTool.ToolSHA256) != nil) {
		return nil, errBroker
	}
	if req.Spool == nil || req.Spool.Directory() != req.SegmentDirectory || !canonical(req.SegmentDirectory) || filepath.Base(req.SegmentDirectory) != req.Launch.SegmentID || filepath.Base(filepath.Dir(req.SegmentDirectory)) != req.Launch.AttemptID {
		return nil, errBroker
	}
	namedSegment, err := named(req.SegmentDirectory)
	if err != nil || namedSegment.UID != uint32(os.Getuid()) || os.FileMode(namedSegment.Mode) != os.ModeDir|0700 {
		return nil, errBroker
	}
	segment, err := os.OpenRoot(req.SegmentDirectory)
	if err != nil {
		return nil, err
	}
	segmentFD, err := segment.Open(".")
	if err != nil {
		segment.Close()
		return nil, err
	}
	held, err := identity(segmentFD)
	if err != nil || !brokerDirectorySame(held, namedSegment) {
		segmentFD.Close()
		segment.Close()
		return nil, errBroker
	}
	b := &Broker{req: req, segment: segment, segmentFD: segmentFD, segmentID: held, nodes: map[string]*brokerFile{}, resource: filepath.Join(req.SegmentDirectory, ".git-filter")}
	// Refuse all cold state before a first write. No journal is an exit proof.
	for _, n := range []string{"git-filter.json", ".git-filter.json.tmp", ".git-filter"} {
		if _, e := segment.Lstat(n); !errors.Is(e, os.ErrNotExist) {
			b.Close()
			return nil, errBroker
		}
	}
	b.journal = brokerJournal{Version: 1, ReservationID: g.ReservationID, ScopeSHA256: g.ScopeSHA256, ManifestSHA256: g.ManifestSHA256, Phase: "reserved"}
	fail := func(err error) (*Broker, error) { b.state.Unknown = true; return b, err }
	if err = b.writeJournal("reserved"); err != nil {
		return fail(err)
	}
	if err = segment.Mkdir(".git-filter", 0700); err != nil {
		return fail(err)
	}
	if err = segmentFD.Sync(); err != nil {
		return fail(err)
	}
	b.root, err = segment.OpenRoot(".git-filter")
	if err != nil {
		return fail(err)
	}
	if err = b.addNode("."); err != nil {
		return fail(err)
	}
	if err = b.writeJournal("sealing"); err != nil {
		return fail(err)
	}
	for _, n := range []string{"clean", "smudge", "empty"} {
		if err = b.root.Mkdir(n, 0700); err != nil {
			return fail(err)
		}
		if err = b.addNode(n); err != nil {
			return fail(err)
		}
	}
	for i, pin := range []contract.GitFilterToolPin{req.Manifest.Clean, req.Manifest.Smudge} {
		n := []string{"clean/tool", "smudge/tool"}[i]
		if err = b.seal(ctx, n, pin); err != nil {
			return fail(err)
		}
	}
	for i := range b.slots {
		n := brokerSlot(i)
		fd, e := b.root.OpenFile(n, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return fail(e)
		}
		b.slots[i] = fd
		if err = b.recordFile(ctx, n, fd); err != nil {
			return fail(err)
		}
		if err = fd.Sync(); err != nil {
			return fail(err)
		}
	}
	for _, n := range []string{"clean", "smudge", "empty"} {
		if err = b.nodes[n].file.Chmod(0500); err != nil {
			return fail(err)
		}
		if err = b.nodes[n].file.Sync(); err != nil {
			return fail(err)
		}
		b.nodes[n].Identity, err = identity(b.nodes[n].file)
		if err != nil {
			return fail(err)
		}
	}
	if err = b.nodes["."].file.Sync(); err != nil {
		return fail(err)
	}
	if err = b.checkAll(ctx); err != nil {
		return fail(err)
	}
	if err = b.writeJournal("ready"); err != nil {
		return fail(err)
	}
	b.ready = true
	return b, nil
}

func brokerDirectorySame(a, b node) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.UID == b.UID && a.Mode == b.Mode && os.FileMode(a.Mode).IsDir()
}
func brokerSlot(i int) string { return []string{"slot-0", "slot-1", "slot-2"}[i] }
func (b *Broker) addNode(n string) error {
	f, err := b.root.OpenFile(n, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	id, err := identity(f)
	if err != nil {
		f.Close()
		return err
	}
	b.nodes[n] = &brokerFile{Name: n, Identity: id, file: f}
	return nil
}
func (b *Broker) recordFile(ctx context.Context, n string, f *os.File) error {
	id, err := identity(f)
	if err != nil {
		return err
	}
	if os.FileMode(id.Mode)&os.ModeType != 0 || id.UID != uint32(os.Getuid()) || id.Nlink != 1 {
		return errBroker
	}
	sha, err := hashFD(ctx, f, id.Size)
	if err != nil {
		return err
	}
	b.nodes[n] = &brokerFile{Name: n, Identity: id, SHA256: sha, file: f}
	return nil
}
func (b *Broker) seal(ctx context.Context, n string, pin contract.GitFilterToolPin) error {
	if !canonical(pin.Path) {
		return errBroker
	}
	for _, name := range []string{"env", "sh", "bash", "dash", "zsh", "ksh", "csh", "tcsh", "fish", "busybox", "toybox"} {
		if filepath.Base(pin.Path) == name {
			return errBroker
		}
	}
	src, err := os.OpenFile(pin.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	before, err := identity(src)
	if err != nil || os.FileMode(before.Mode)&os.ModeType != 0 || os.FileMode(before.Mode)&0111 == 0 || before.Size < 4 || before.Size > MaximumToolBytes {
		return errBroker
	}
	// Native format and known dispatcher checks are also repeated by Execute.
	if !process.NativeExecutableFile(src, before.Size) {
		return errBroker
	}
	out, err := b.root.OpenFile(n, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0500)
	if err != nil {
		return err
	}
	b.nodes[n] = &brokerFile{Name: n, file: out}
	if err = brokerCopy(ctx, src, out, before.Size); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	after, err := identity(src)
	namedSource, ne := named(pin.Path)
	if err != nil || ne != nil || before != after || before != namedSource {
		return errBroker
	}
	if err = b.recordFile(ctx, n, out); err != nil {
		return err
	}
	sourceSHA, err := hashFD(ctx, src, before.Size)
	if err != nil || sourceSHA != pin.SHA256 || b.nodes[n].SHA256 != pin.SHA256 {
		return errBroker
	}
	return nil
}

func brokerCopy(ctx context.Context, src, dst *os.File, size int64) error {
	buf := make([]byte, 32768)
	for off := int64(0); off < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if size-off < want {
			want = size - off
		}
		n, err := src.ReadAt(buf[:want], off)
		if n > 0 {
			w, e := dst.WriteAt(buf[:n], off)
			if e != nil {
				return e
			}
			if w != n {
				return io.ErrShortWrite
			}
			off += int64(n)
		}
		if err != nil && !(err == io.EOF && off == size) {
			return err
		}
		if n == 0 && off < size {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func (b *Broker) checkSegment() error {
	if b.closed || b.segment == nil || b.segmentFD == nil {
		return errBroker
	}
	held, e := identity(b.segmentFD)
	nm, ne := named(b.req.SegmentDirectory)
	if e != nil || ne != nil || !brokerDirectorySame(held, b.segmentID) || !brokerDirectorySame(nm, b.segmentID) {
		return errBroker
	}
	return nil
}
func (b *Broker) checkNode(ctx context.Context, n *brokerFile) error {
	held, e := identity(n.file)
	info, ne := b.root.Lstat(n.Name)
	if e != nil || ne != nil {
		return errBroker
	}
	raw, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(raw.Dev) != held.Device || raw.Ino != held.Inode {
		return errBroker
	}
	if os.FileMode(n.Identity.Mode).IsDir() {
		if !brokerDirectorySame(held, n.Identity) || info.Mode() != os.FileMode(n.Identity.Mode) {
			return errBroker
		}
	} else {
		if held != n.Identity || info.Mode() != os.FileMode(n.Identity.Mode) || uint64(raw.Nlink) != 1 || raw.Uid != uint32(os.Getuid()) || info.Size() != held.Size || info.ModTime().UnixNano() != held.Modified {
			return errBroker
		}
		sha, err := hashFD(ctx, n.file, held.Size)
		if err != nil || sha != n.SHA256 {
			return errBroker
		}
	}
	return nil
}
func (b *Broker) checkAll(ctx context.Context) error {
	if err := b.checkSegment(); err != nil {
		return err
	}
	if b.root == nil || b.nodes["."] == nil {
		return errBroker
	}
	rootNamed, e := b.segment.Lstat(".git-filter")
	if e != nil {
		return errBroker
	}
	raw, ok := rootNamed.Sys().(*syscall.Stat_t)
	rootID := b.nodes["."].Identity
	if !ok || uint64(raw.Dev) != rootID.Device || raw.Ino != rootID.Inode || rootNamed.Mode() != os.ModeDir|0700 || raw.Uid != uint32(os.Getuid()) {
		return errBroker
	}
	for _, n := range b.nodes {
		if err := b.checkNode(ctx, n); err != nil {
			return err
		}
	}
	for _, dir := range []string{".", "clean", "smudge", "empty"} {
		expected := map[string]bool{}
		for name := range b.nodes {
			if name != "." && filepath.Dir(name) == dir {
				expected[filepath.Base(name)] = true
			}
		}
		fd, e := b.root.Open(dir)
		if e != nil {
			return e
		}
		entries, e := fd.ReadDir(len(expected) + 1)
		fd.Close()
		if e != nil && e != io.EOF || len(entries) != len(expected) {
			return errBroker
		}
		for _, entry := range entries {
			if !expected[entry.Name()] {
				return errBroker
			}
		}
	}
	return nil
}
func (b *Broker) readJournal() ([]byte, node, error) {
	fd, err := b.segment.OpenFile("git-filter.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, node{}, err
	}
	defer fd.Close()
	id, err := identity(fd)
	if err != nil || id.UID != uint32(os.Getuid()) || id.Nlink != 1 || os.FileMode(id.Mode) != 0600 || id.Size < 1 || id.Size > 65537 {
		return nil, node{}, errBroker
	}
	body, err := io.ReadAll(io.LimitReader(fd, 65538))
	if err != nil || int64(len(body)) != id.Size {
		return nil, node{}, errBroker
	}
	info, err := b.segment.Lstat("git-filter.json")
	if err != nil {
		return nil, node{}, err
	}
	raw, ok := info.Sys().(*syscall.Stat_t)
	after, e := identity(fd)
	if !ok || e != nil || after != id || uint64(raw.Dev) != id.Device || raw.Ino != id.Inode {
		return nil, node{}, errBroker
	}
	return body, id, nil
}
func (b *Broker) writeJournal(phase string) error {
	if err := b.checkSegment(); err != nil {
		return err
	}
	if _, err := b.segment.Lstat(".git-filter.json.tmp"); !errors.Is(err, os.ErrNotExist) {
		return errBroker
	}
	if len(b.journalBytes) == 0 {
		if _, err := b.segment.Lstat("git-filter.json"); !errors.Is(err, os.ErrNotExist) {
			return errBroker
		}
	} else {
		body, id, err := b.readJournal()
		if err != nil || id != b.journalID || !bytes.Equal(body, b.journalBytes) {
			return errBroker
		}
	}
	next := b.journal
	next.Phase = phase
	names := make([]string, 0, len(b.nodes))
	for name := range b.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	next.Inventory = make([]brokerFile, 0, len(names))
	for _, name := range names {
		next.Inventory = append(next.Inventory, *b.nodes[name])
	}
	inventory, _ := json.Marshal(next.Inventory)
	hash := sha256.Sum256(inventory)
	next.ResourceInventorySHA256 = hex.EncodeToString(hash[:])
	raw, err := json.Marshal(next)
	if err != nil || len(raw) > 65536 {
		return errBroker
	}
	if err = b.req.Spool.WriteMetadata("git-filter.json", next); err != nil {
		return err
	}
	if err = b.checkSegment(); err != nil {
		return err
	}
	body, id, err := b.readJournal()
	if err != nil || !bytes.Equal(body, append(raw, '\n')) {
		return errBroker
	}
	b.journal, b.journalBytes, b.journalID = next, body, id
	return nil
}
func (b *Broker) usable() bool {
	return b != nil && b.ready && !b.failed && !b.removed && !b.closed && !b.state.Unknown
}
func (b *Broker) uncertain(err error) error { b.state.Unknown = true; return err }
func (b *Broker) refreshSlot(ctx context.Context, i int) error {
	n := b.nodes[brokerSlot(i)]
	id, err := identity(n.file)
	if err != nil || id.Device != n.Identity.Device || id.Inode != n.Identity.Inode || id.Nlink != 1 || id.UID != n.Identity.UID || id.Mode != n.Identity.Mode || id.Size < 0 || id.Size > MaximumBytes {
		return errBroker
	}
	return b.recordFile(ctx, n.Name, n.file)
}

func (b *Broker) Load(ctx context.Context, i int, src *os.File) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.usable() || i < 0 || i >= 3 || src == nil {
		return errBroker
	}
	before, err := identity(src)
	target := b.nodes[brokerSlot(i)].Identity
	if err != nil || os.FileMode(before.Mode)&os.ModeType != 0 || before.Size < 0 || before.Size > MaximumBytes || before.Device == target.Device && before.Inode == target.Inode {
		return errBroker
	}
	if err = b.checkAll(ctx); err != nil {
		return b.uncertain(err)
	}
	b.initialized[i] = false
	if err = b.slots[i].Truncate(0); err != nil {
		return b.uncertain(err)
	}
	if err = brokerCopy(ctx, src, b.slots[i], before.Size); err != nil {
		return b.uncertain(err)
	}
	if err = b.slots[i].Sync(); err != nil {
		return b.uncertain(err)
	}
	if err = b.refreshSlot(ctx, i); err != nil {
		return b.uncertain(err)
	}
	after, err := identity(src)
	sha, se := hashFD(ctx, src, before.Size)
	if err != nil || se != nil || after != before || sha != b.nodes[brokerSlot(i)].SHA256 {
		return b.uncertain(errBroker)
	}
	if err = b.checkAll(ctx); err != nil {
		return b.uncertain(err)
	}
	if err = b.writeJournal("ready"); err != nil {
		return b.uncertain(err)
	}
	b.initialized[i] = true
	return nil
}

func (b *Broker) LoadStream(ctx context.Context, i int, producer func(io.Writer) error) (StreamLoadResult, error) {
	return b.loadStream(ctx, i, producer, nil)
}

func (b *Broker) loadStream(ctx context.Context, i int, producer func(io.Writer) error, afterLoading func()) (result StreamLoadResult, retErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.usable() || i < 0 || i >= len(b.slots) || producer == nil {
		return result, errBroker
	}
	if _, ok := ctx.Deadline(); !ok {
		return result, errBroker
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := b.streamInventory(-1); err != nil {
		return result, b.uncertain(err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := b.writeJournal("loading"); err != nil {
		return result, b.uncertain(err)
	}
	if afterLoading != nil {
		afterLoading()
	}
	b.initialized[i] = false
	if err := b.slots[i].Truncate(0); err != nil {
		return result, b.uncertain(err)
	}
	if err := b.slots[i].Sync(); err != nil {
		return result, b.uncertain(err)
	}
	if err := b.streamInventory(i); err != nil {
		return result, b.uncertain(err)
	}
	if err := ctx.Err(); err != nil {
		return result, b.finishStreamFailure(err, i)
	}
	w := &streamSlotWriter{ctx: ctx, file: b.slots[i], writeAt: b.streamWriteAt}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = w.revoke()
			b.state.Unknown = true
			panic(recovered)
		}
	}()
	producerErr := producer(w)
	writerErr := w.revoke()
	retErr = errors.Join(producerErr, writerErr, ctx.Err())
	if retErr != nil {
		var slotIO *streamSlotIOError
		if errors.Is(retErr, process.ErrProcessTreeUnknown) || errors.As(retErr, &slotIO) {
			return result, b.uncertain(retErr)
		}
		return result, b.finishStreamFailure(retErr, i)
	}
	if err := b.slots[i].Sync(); err != nil {
		return result, b.uncertain(err)
	}
	if err := b.streamInventory(i); err != nil {
		return result, b.uncertain(err)
	}
	if err := ctx.Err(); err != nil {
		return result, b.finishStreamFailure(err, i)
	}
	if err := b.writeJournal("ready"); err != nil {
		return result, b.uncertain(err)
	}
	if err := ctx.Err(); err != nil {
		return result, b.finishStreamFailure(err, i)
	}
	b.initialized[i] = true
	n := b.nodes[brokerSlot(i)]
	return StreamLoadResult{SHA256: n.SHA256, Bytes: n.Identity.Size}, nil
}

// Integrity checks use a fresh bounded context so caller cancellation cannot be
// confused with a damaged inventory. Actual I/O/identity failures stay UNKNOWN.
func (b *Broker) streamInventory(slot int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if slot >= 0 {
		if err := b.refreshSlot(ctx, slot); err != nil {
			return err
		}
	}
	return b.checkAll(ctx)
}

func (b *Broker) finishStreamFailure(original error, i int) error {
	finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.slots[i].Truncate(0); err != nil {
		return b.uncertain(errors.Join(original, err))
	}
	if err := b.slots[i].Sync(); err != nil {
		return b.uncertain(errors.Join(original, err))
	}
	if err := b.refreshSlot(finalCtx, i); err != nil {
		return b.uncertain(errors.Join(original, err))
	}
	if err := b.checkAll(finalCtx); err != nil {
		return b.uncertain(errors.Join(original, err))
	}
	if err := b.writeJournal("terminal"); err != nil {
		return b.uncertain(errors.Join(original, err))
	}
	b.failed = true
	return original
}
func (b *Broker) Transform(ctx context.Context, op string, src, dst int) (Result, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.usable() || src < 0 || src >= 3 || dst < 0 || dst >= 3 || src == dst || !b.initialized[src] || op != "clean" && op != "smudge" || b.journal.StepOrdinal >= 4096 {
		return Result{}, errBroker
	}
	if err := b.checkAll(ctx); err != nil {
		return Result{}, b.uncertain(err)
	}
	b.initialized[dst] = false
	if err := b.slots[dst].Truncate(0); err != nil {
		return Result{}, b.uncertain(err)
	}
	if err := b.slots[dst].Sync(); err != nil {
		return Result{}, b.uncertain(err)
	}
	if err := b.refreshSlot(ctx, dst); err != nil {
		return Result{}, b.uncertain(err)
	}
	b.journal.EverStartAttempted = true
	b.journal.StepOrdinal++
	if err := b.writeJournal("starting"); err != nil {
		return Result{}, b.uncertain(err)
	}
	pin := b.req.Manifest.Clean
	if op == "smudge" {
		pin = b.req.Manifest.Smudge
	}
	tool := b.nodes[op+"/tool"]
	result, err := Execute(ctx, Request{Operation: op, ScopeSHA256: b.req.Grant.ScopeSHA256, Tool: Tool{Path: filepath.Join(b.resource, op, "tool"), SHA256: pin.SHA256, Size: tool.Identity.Size}, Args: pin.Args, Input: b.slots[src], Output: b.slots[dst], EmptyDirectory: filepath.Join(b.resource, "empty")})
	if result.Started {
		b.state.EverStarted = true
		if b.state.StartedAt.IsZero() {
			b.state.StartedAt = result.StartedAt
		}
	}
	if result.Unknown || result.Started && (!result.WaitProved || !result.TreeProved || result.StartedAt.IsZero() || result.TerminalAt.IsZero()) {
		return result, b.uncertain(errors.Join(err, errBroker))
	}
	if result.Started {
		b.state.TerminalAt = result.TerminalAt
	}
	// Caller cancellation cannot suppress the durable original terminal record.
	finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := b.refreshSlot(finalCtx, dst); e != nil {
		return result, b.uncertain(errors.Join(err, e))
	}
	if e := b.checkAll(finalCtx); e != nil {
		return result, b.uncertain(errors.Join(err, e))
	}
	if e := b.writeJournal("terminal"); e != nil {
		return result, b.uncertain(errors.Join(err, e))
	}
	b.failed = err != nil || !result.Accepted
	b.initialized[dst] = !b.failed
	return result, err
}
func (b *Broker) ReadSlot(ctx context.Context, i int, w io.Writer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.usable() || i < 0 || i >= 3 || !b.initialized[i] || w == nil {
		return errBroker
	}
	if err := b.checkAll(ctx); err != nil {
		return b.uncertain(err)
	}
	n := b.nodes[brokerSlot(i)]
	buf := make([]byte, 32768)
	for off := int64(0); off < n.Identity.Size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if n.Identity.Size-off < want {
			want = n.Identity.Size - off
		}
		read, err := n.file.ReadAt(buf[:want], off)
		if err != nil && err != io.EOF {
			return b.uncertain(err)
		}
		if read == 0 {
			return b.uncertain(io.ErrUnexpectedEOF)
		}
		wrote, e := w.Write(buf[:read])
		if e != nil {
			return e
		}
		if wrote != read {
			return io.ErrShortWrite
		}
		off += int64(read)
	}
	if err := b.checkNode(ctx, n); err != nil {
		return b.uncertain(err)
	}
	return nil
}
func (b *Broker) State() BrokerState { b.mu.Lock(); defer b.mu.Unlock(); return b.state }

// Cleanup proves only this broker's filters. The Host must separately prove its
// provider tree terminal before emitting any segment cleanup/release event.
func (b *Broker) Cleanup(ctx context.Context) (*contract.GitFilterCleanup, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ready || b.removed || b.closed || b.state.Unknown || b.journal.Phase != "ready" && b.journal.Phase != "terminal" {
		return nil, errBroker
	}
	if err := b.checkAll(ctx); err != nil {
		return nil, b.uncertain(err)
	}
	// Authenticate the last owned journal before any irreversible cleanup.
	body, id, err := b.readJournal()
	if err != nil || id != b.journalID || !bytes.Equal(body, b.journalBytes) {
		return nil, b.uncertain(errBroker)
	}
	if _, err = b.segment.Lstat(".git-filter.json.tmp"); !errors.Is(err, os.ErrNotExist) {
		return nil, b.uncertain(errBroker)
	}
	var removed int64
	for _, name := range []string{"clean", "smudge"} {
		if err = b.nodes[name].file.Chmod(0700); err != nil {
			return nil, b.uncertain(err)
		}
		b.nodes[name].Identity, err = identity(b.nodes[name].file)
		if err != nil {
			return nil, b.uncertain(err)
		}
	}
	for _, name := range []string{"clean/tool", "smudge/tool", "slot-0", "slot-1", "slot-2"} {
		n := b.nodes[name]
		if err = b.checkSegment(); err != nil {
			return nil, b.uncertain(err)
		}
		if err = b.checkNode(ctx, n); err != nil {
			return nil, b.uncertain(err)
		}
		if err = b.root.Remove(name); err != nil {
			return nil, b.uncertain(err)
		}
		// Unlinked-but-open files still occupy the raw resource budget. Close
		// every original descriptor before a release receipt can be produced.
		if err = n.file.Close(); err != nil {
			return nil, b.uncertain(err)
		}
		n.file = nil
		removed += n.Identity.Size
	}
	for _, name := range []string{"clean", "smudge", "empty"} {
		if err = b.checkNode(ctx, b.nodes[name]); err != nil {
			return nil, b.uncertain(err)
		}
		if err = b.root.Remove(name); err != nil {
			return nil, b.uncertain(err)
		}
		if err = b.nodes[name].file.Close(); err != nil {
			return nil, b.uncertain(err)
		}
		b.nodes[name].file = nil
	}
	if err = b.nodes["."].file.Sync(); err != nil {
		return nil, b.uncertain(err)
	}
	if err = b.checkSegment(); err != nil {
		return nil, b.uncertain(err)
	}
	info, err := b.segment.Lstat(".git-filter")
	raw, ok := func() (*syscall.Stat_t, bool) {
		if info == nil {
			return nil, false
		}
		r, o := info.Sys().(*syscall.Stat_t)
		return r, o
	}()
	if err != nil || !ok || uint64(raw.Dev) != b.nodes["."].Identity.Device || raw.Ino != b.nodes["."].Identity.Inode {
		return nil, b.uncertain(errBroker)
	}
	if err = b.segment.Remove(".git-filter"); err != nil {
		return nil, b.uncertain(err)
	}
	if err = b.nodes["."].file.Close(); err != nil {
		return nil, b.uncertain(err)
	}
	b.nodes["."].file = nil
	if err = b.root.Close(); err != nil {
		return nil, b.uncertain(err)
	}
	b.root = nil
	if err = b.writeJournal("removed"); err != nil {
		return nil, b.uncertain(err)
	}
	b.removed = true
	hash := sha256.Sum256(b.journalBytes)
	g := b.req.Grant
	receipt := &contract.GitFilterCleanup{Version: 1, ReservationID: g.ReservationID, ScopeSHA256: g.ScopeSHA256, ManifestSHA256: g.ManifestSHA256, CleanToolSHA256: g.CleanToolSHA256, SmudgeToolSHA256: g.SmudgeToolSHA256, JournalSHA256: hex.EncodeToString(hash[:]), RemovedBytes: removed}
	return receipt, receipt.Validate()
}
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var errs []error
	for _, n := range b.nodes {
		if n.file != nil {
			errs = append(errs, n.file.Close())
		}
	}
	// A failed slot creation can precede insertion into the inventory.
	for i, f := range b.slots {
		if f != nil && b.nodes[brokerSlot(i)] == nil {
			errs = append(errs, f.Close())
		}
	}
	if b.root != nil {
		errs = append(errs, b.root.Close())
	}
	if b.segmentFD != nil {
		errs = append(errs, b.segmentFD.Close())
	}
	if b.segment != nil {
		errs = append(errs, b.segment.Close())
	}
	return errors.Join(errs...)
}
