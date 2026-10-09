//go:build darwin || linux

package diagnostics

// This file deliberately does not use Writer's recovery path.  A deletion
// lease keeps both existing locks until its caller has made the matching store
// transition, and turns the namespace into a tombstone that ordinary readers
// and writers reject as an unknown layout entry.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"golang.org/x/sys/unix"
)

const (
	deletionMarker    = ".deletion.json"
	deletionPlanLimit = 64 << 10
)

type deletionIdentity struct {
	Dev  uint64 `json:"d"`
	Ino  uint64 `json:"i"`
	UID  uint32 `json:"u"`
	Mode uint32 `json:"o"`
	Size int64  `json:"z"`
	MSec int64  `json:"ms"`
	MNS  int64  `json:"mn"`
	CSec int64  `json:"cs"`
	CNS  int64  `json:"cn"`
}

type deletionFile struct {
	Name   string           `json:"n"`
	ID     deletionIdentity `json:"i"`
	SHA256 string           `json:"h"`
}

type deletionPlan struct {
	Version   int              `json:"v"`
	Scope     Scope            `json:"scope"`
	CommandID string           `json:"command_id"`
	Root      deletionIdentity `json:"root"`
	Directory deletionIdentity `json:"directory"`
	Writer    deletionIdentity `json:"writer"`
	Update    deletionIdentity `json:"update"`
	Stage     string           `json:"stage"`
	Files     []deletionFile   `json:"files"`
}

type deletionMarkerBody struct {
	Version   int    `json:"v"`
	ScopeHash string `json:"scope_hash"`
	CommandID string `json:"command_id"`
	PlanSHA   string `json:"plan_sha256"`
}

// DeletionLease owns the namespace writer/update locks.  Its plan is immutable
// evidence for the store and never reflects a later directory scan.
type DeletionLease struct {
	ns           *namespace
	scope        Scope
	plan         deletionPlan
	planRaw      []byte
	markerRaw    []byte
	buffer       []byte
	removed      int64
	writerUnlock func() error
	updateUnlock func() error
	closed       bool
}

func deletionID(st unix.Stat_t) deletionIdentity {
	return deletionIdentity{Dev: uint64(st.Dev), Ino: st.Ino, UID: st.Uid, Mode: uint32(st.Mode), Size: st.Size,
		MSec: st.Mtim.Sec, MNS: st.Mtim.Nsec, CSec: st.Ctim.Sec, CNS: st.Ctim.Nsec}
}

func (id deletionIdentity) matches(st unix.Stat_t) bool { return id == deletionID(st) }
func (id deletionIdentity) matchesNode(st unix.Stat_t) bool {
	return id.Dev == uint64(st.Dev) && id.Ino == st.Ino && id.UID == st.Uid && id.Mode == uint32(st.Mode)
}

func validDeletionStage(name string) bool {
	if len(name) != len(".delete-")+32+len(".stage") || name[:8] != ".delete-" || name[len(name)-6:] != ".stage" {
		return false
	}
	for _, c := range name[8 : len(name)-6] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func canonicalPlan(p deletionPlan) ([]byte, error) {
	if p.Version != 1 || !validScopeID(p.CommandID) || !validDeletionStage(p.Stage) || len(p.Files) == 0 || len(p.Files) > SlotCount+1 {
		return nil, ErrUnsafeLayout
	}
	if _, err := p.Scope.Hash(); err != nil {
		return nil, ErrUnsafeLayout
	}
	last := ""
	for _, f := range p.Files {
		if f.Name <= last || (f.Name != "manifest.json" && (!isSlot(f.Name) || !validPolicyHash(f.SHA256))) || (f.Name == "manifest.json" && !validPolicyHash(f.SHA256)) || f.ID.Size < 0 {
			return nil, ErrUnsafeLayout
		}
		last = f.Name
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > deletionPlanLimit {
		return nil, ErrUnsafeLayout
	}
	return raw, nil
}

func isSlot(name string) bool {
	_, ok := fileLimit(name)
	return ok && len(name) == 13 && name[:6] == "chunk-"
}

func decodeDeletionPlan(raw []byte) (deletionPlan, []byte, error) {
	var p deletionPlan
	if len(raw) == 0 || len(raw) > deletionPlanLimit {
		return p, nil, ErrUnsafeLayout
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, nil, ErrUnsafeLayout
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, nil, ErrUnsafeLayout
	}
	canonical, err := canonicalPlan(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return p, nil, ErrUnsafeLayout
	}
	return p, canonical, nil
}

func deletionMarkerFor(p deletionPlan, raw []byte) ([]byte, error) {
	hash, err := p.Scope.Hash()
	if err != nil {
		return nil, ErrUnsafeLayout
	}
	sum := sha256.Sum256(raw)
	b, err := json.Marshal(deletionMarkerBody{Version: 1, ScopeHash: hash, CommandID: p.CommandID, PlanSHA: hex.EncodeToString(sum[:])})
	if err != nil || len(b) > deletionPlanLimit {
		return nil, ErrUnsafeLayout
	}
	return b, nil
}

func lockDeletionWriter(ctx context.Context, n *namespace) (func() error, error) {
	if err := n.check(); err != nil {
		return nil, err
	}
	if err := unix.Flock(int(n.writer.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.Join(ErrLockBusy, err)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = unix.Flock(int(n.writer.Fd()), unix.LOCK_UN)
		return nil, err
	}
	return func() error { return unix.Flock(int(n.writer.Fd()), unix.LOCK_UN) }, nil
}

// OpenDeletion never creates a namespace or a lock.  An empty savedPlan makes
// a fresh, closed snapshot; a non-empty plan can only reopen the exact stored
// evidence after a durable delete intent exists.
func OpenDeletion(ctx context.Context, root string, scope Scope, commandID string, savedPlan []byte) (lease *DeletionLease, err error) {
	if !validScopeID(commandID) {
		return nil, ErrUnsafeLayout
	}
	hash, err := scope.Hash()
	if err != nil {
		return nil, err
	}
	n, _, err := openNamespace(ctx, root, hash, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = n.close()
			lease = nil
		}
	}()
	wunlock, err := lockDeletionWriter(ctx, n)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = wunlock()
		}
	}()
	uunlock, err := n.lockUpdate(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = uunlock()
		}
	}()

	l := &DeletionLease{ns: n, scope: scope, writerUnlock: wunlock, updateUnlock: uunlock, buffer: make([]byte, 0, ChunkBytes)}
	if len(savedPlan) == 0 {
		if err = l.captureFresh(ctx, commandID); err != nil {
			return nil, err
		}
	} else {
		p, canonical, parseErr := decodeDeletionPlan(savedPlan)
		if parseErr != nil || p.Scope != scope || p.CommandID != commandID {
			return nil, ErrUnsafeLayout
		}
		l.plan, l.planRaw = p, append([]byte(nil), canonical...)
		l.markerRaw, err = deletionMarkerFor(p, canonical)
		if err != nil {
			return nil, err
		}
		for _, f := range p.Files {
			if f.ID.Size > 0 && l.removed <= 21102592-f.ID.Size {
				l.removed += f.ID.Size
			} else {
				return nil, ErrUnsafeLayout
			}
		}
		if err = l.verifyPinned(true); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (l *DeletionLease) captureFresh(ctx context.Context, command string) error {
	if err := l.ns.checkLayout(); err != nil {
		return err
	}
	m, _, unclean, err := l.ns.loadStored(ctx, l.scope, make([]byte, ChunkBytes))
	if err != nil || unclean || !m.Closed {
		if err != nil {
			return err
		}
		return ErrUnsafeLayout
	}
	stageBytes := make([]byte, 16)
	if _, err = rand.Read(stageBytes); err != nil {
		return err
	}
	p := deletionPlan{Version: 1, Scope: l.scope, CommandID: command, Root: deletionID(l.ns.rootID), Directory: deletionID(l.ns.dirID), Writer: deletionID(l.ns.writerID), Update: deletionID(l.ns.updateID), Stage: ".delete-" + hex.EncodeToString(stageBytes) + ".stage"}
	names := []string{"manifest.json"}
	for i, d := range m.Slots {
		if d.Sequence != 0 {
			names = append(names, slotName(i))
		}
	}
	sort.Strings(names)
	for _, name := range names {
		raw, readErr := l.ns.readInto(name, fileLimitMust(name), l.buffer)
		if readErr != nil {
			return readErr
		}
		l.buffer = raw[:0]
		var st unix.Stat_t
		if statErr := unix.Fstatat(int(l.ns.dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
			return unsafe(statErr)
		}
		sum := sha256.Sum256(raw)
		p.Files = append(p.Files, deletionFile{Name: name, ID: deletionID(st), SHA256: hex.EncodeToString(sum[:])})
		if st.Size > 0 && l.removed <= 21102592-st.Size {
			l.removed += st.Size
		} else {
			return ErrUnsafeLayout
		}
	}
	raw, err := canonicalPlan(p)
	if err != nil {
		return err
	}
	l.plan, l.planRaw = p, raw
	l.markerRaw, err = deletionMarkerFor(p, raw)
	return err
}

func fileLimitMust(name string) int { n, _ := fileLimit(name); return n }

func (l *DeletionLease) Plan() []byte        { return append([]byte(nil), l.planRaw...) }
func (l *DeletionLease) MarkerBytes() int64  { return int64(len(l.markerRaw)) }
func (l *DeletionLease) RemovedBytes() int64 { return l.removed }

func (l *DeletionLease) namedStat(name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(l.ns.dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return st, err
	}
	if !privateStat(st, false) {
		return st, ErrUnsafeLayout
	}
	return st, nil
}

func (l *DeletionLease) planFile(name string) (deletionFile, bool) {
	for _, f := range l.plan.Files {
		if f.Name == name {
			return f, true
		}
	}
	return deletionFile{}, false
}

func (l *DeletionLease) exactMarker() (bool, error) {
	st, err := l.namedStat(deletionMarker)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil || st.Size != int64(len(l.markerRaw)) || st.Size > deletionPlanLimit {
		return false, ErrUnsafeLayout
	}
	f, err := openAt(l.ns.dir, deletionMarker, unix.O_RDONLY, 0)
	if err != nil {
		return false, unsafe(err)
	}
	defer f.Close()
	got := make([]byte, len(l.markerRaw))
	if _, err = io.ReadFull(f, got); err != nil {
		return false, err
	}
	var held unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &held); err != nil || !sameFileState(held, st) || !bytes.Equal(got, l.markerRaw) {
		return false, ErrUnsafeLayout
	}
	return true, nil
}

// verifyPinned rejects any inventory outside exactly the original files,
// persistent marker, and this plan's private stage.  missing may be true only
// after the durable marker is exact.
func (l *DeletionLease) verifyPinned(missing bool) error {
	if err := l.ns.check(); err != nil {
		return err
	}
	if !l.plan.Root.matchesNode(l.ns.rootID) || !l.plan.Directory.matchesNode(l.ns.dirID) || !l.plan.Writer.matchesNode(l.ns.writerID) || !l.plan.Update.matchesNode(l.ns.updateID) {
		return ErrUnsafeLayout
	}
	marker, err := l.exactMarker()
	if err != nil {
		return err
	}
	d, err := openAt(l.ns.dir, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(SlotCount + 8)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > SlotCount+7 {
		return ErrUnsafeLayout
	}
	for _, name := range names {
		if name == "writer.lock" || name == "update.lock" || name == deletionMarker || name == l.plan.Stage {
			continue
		}
		f, ok := l.planFile(name)
		if !ok {
			return ErrUnsafeLayout
		}
		st, statErr := l.namedStat(name)
		if statErr != nil || !f.ID.matches(st) {
			return ErrUnsafeLayout
		}
		opened, openErr := openAt(l.ns.dir, name, unix.O_RDONLY, 0)
		if openErr != nil {
			return unsafe(openErr)
		}
		if cap(l.buffer) < int(st.Size) {
			l.buffer = make([]byte, int(st.Size))
		} else {
			l.buffer = l.buffer[:int(st.Size)]
		}
		buf := l.buffer
		_, readErr := io.ReadFull(opened, buf)
		var held unix.Stat_t
		statErr = unix.Fstat(int(opened.Fd()), &held)
		_ = opened.Close()
		if readErr != nil || statErr != nil || !f.ID.matches(held) {
			return ErrUnsafeLayout
		}
		sum := sha256.Sum256(buf)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return ErrUnsafeLayout
		}
		l.buffer = buf[:0]
	}
	for _, f := range l.plan.Files {
		_, statErr := l.namedStat(f.Name)
		if errors.Is(statErr, unix.ENOENT) {
			if missing && marker {
				continue
			}
			return ErrUnsafeLayout
		}
		if statErr != nil {
			return ErrUnsafeLayout
		}
	}
	return l.ns.check()
}

func (l *DeletionLease) publishMarker() error {
	marker, err := l.exactMarker()
	if err != nil {
		return err
	}
	if marker {
		return nil
	}
	// A stale stage is acceptable only when it is exactly this marker body.
	stage, err := l.namedStat(l.plan.Stage)
	if errors.Is(err, unix.ENOENT) {
		f, openErr := openAt(l.ns.dir, l.plan.Stage, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		if _, err = f.Write(l.markerRaw); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return err
		}
		if err = unix.Fstat(int(f.Fd()), &stage); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if stage.Size != int64(len(l.markerRaw)) {
			return ErrUnsafeLayout
		}
		f, openErr := openAt(l.ns.dir, l.plan.Stage, unix.O_RDONLY, 0)
		if openErr != nil {
			return unsafe(openErr)
		}
		got := make([]byte, len(l.markerRaw))
		_, err = io.ReadFull(f, got)
		_ = f.Close()
		if err != nil || !bytes.Equal(got, l.markerRaw) {
			return ErrUnsafeLayout
		}
	}
	if !privateStat(stage, false) {
		return ErrUnsafeLayout
	}
	if err = renameAbsent(int(l.ns.dir.Fd()), l.plan.Stage, deletionMarker); err != nil {
		if exists, checkErr := l.exactMarker(); checkErr == nil && exists {
			return nil
		}
		return err
	}
	return l.ns.dir.Sync()
}

func (l *DeletionLease) unlinkPlanned(f deletionFile) error {
	st, err := l.namedStat(f.Name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || !f.ID.matches(st) {
		return ErrUnsafeLayout
	}
	opened, err := openAt(l.ns.dir, f.Name, unix.O_RDONLY, 0)
	if err != nil {
		return unsafe(err)
	}
	defer opened.Close()
	if cap(l.buffer) < int(st.Size) {
		l.buffer = make([]byte, int(st.Size))
	} else {
		l.buffer = l.buffer[:int(st.Size)]
	}
	buf := l.buffer
	if _, err = io.ReadFull(opened, buf); err != nil {
		return err
	}
	var held unix.Stat_t
	if err = unix.Fstat(int(opened.Fd()), &held); err != nil || !f.ID.matches(held) {
		return ErrUnsafeLayout
	}
	sum := sha256.Sum256(buf)
	if hex.EncodeToString(sum[:]) != f.SHA256 {
		return ErrUnsafeLayout
	}
	l.buffer = buf[:0]
	if err = unix.Unlinkat(int(l.ns.dir.Fd()), f.Name, 0); err != nil {
		return err
	}
	return nil
}

func (l *DeletionLease) Remove(ctx context.Context) error {
	if l.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.verifyPinned(true); err != nil {
		return err
	}
	if ok, err := l.exactMarker(); err != nil || !ok {
		if err != nil {
			return err
		}
		if err = l.verifyPinned(false); err != nil {
			return err
		}
		if err = l.publishMarker(); err != nil {
			return err
		}
		if err = l.ns.checkpoint("deletion", "after-marker"); err != nil {
			return err
		}
	}
	for _, f := range l.plan.Files {
		if f.Name == "manifest.json" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := l.unlinkPlanned(f); err != nil {
			return err
		}
		if err := l.ns.checkpoint("deletion", "after-unlink-"+f.Name); err != nil {
			return err
		}
	}
	if f, ok := l.planFile("manifest.json"); ok {
		if err := l.unlinkPlanned(f); err != nil {
			return err
		}
		if err := l.ns.checkpoint("deletion", "after-unlink-manifest.json"); err != nil {
			return err
		}
	}
	if err := l.ns.dir.Sync(); err != nil {
		return err
	}
	return l.VerifyRemoved(ctx)
}

func (l *DeletionLease) VerifyRemoved(ctx context.Context) error {
	if l.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.verifyPinned(true); err != nil {
		return err
	}
	for _, f := range l.plan.Files {
		if _, err := l.namedStat(f.Name); !errors.Is(err, unix.ENOENT) {
			return ErrUnsafeLayout
		}
	}
	ok, err := l.exactMarker()
	if err != nil || !ok {
		return ErrUnsafeLayout
	}
	return l.ns.dir.Sync()
}

func (l *DeletionLease) Close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	return errors.Join(l.updateUnlock(), l.writerUnlock(), l.ns.close())
}
