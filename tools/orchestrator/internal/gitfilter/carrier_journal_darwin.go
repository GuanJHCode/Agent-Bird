//go:build darwin

package gitfilter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

const carrierJournalName = "git-filter-carrier.json"

type carrierJournalSnapshot struct {
	Version                                                int
	Ordinal                                                int64
	PreviousSHA256                                         string
	RunID, TaskID, AttemptID, SegmentID, HostID, CommandID string
	WorkRevision, PlanRevision                             int
	Grant                                                  contract.GitFilterCarrierGrant
	Filter                                                 contract.GitFilterResourceGrant
	Socket                                                 *carrierJournalSocket `json:",omitempty"`
	Transform                                              *ExactTransformRecord `json:",omitempty"`
}
type carrierJournalSocket struct {
	Phase, Basename                    string
	Segment, Parent, Directory, Socket carrierSocketIdentity
	Known, Unknown                     bool
}
type carrierJournal struct {
	self           *carrierJournal
	mu             sync.Mutex
	spool          *events.Spool
	segment        *os.Root
	fd             *os.File
	segmentID      node
	path           string
	binding        carrierSocketBinding
	current        carrierJournalSnapshot
	currentID      node
	body           []byte
	failed, closed bool
}

func newCarrierJournal(g contract.LaunchCommand, sp *events.Spool) (*carrierJournal, error) {
	if sp == nil || contract.RequireGitFilter(g) != nil || g.GitFilterCarrier == nil {
		return nil, contract.ErrGitFilterContract
	}
	dir := sp.Directory()
	if !canonical(dir) || filepath.Base(dir) != g.SegmentID || filepath.Base(filepath.Dir(dir)) != g.AttemptID {
		return nil, errCarrierSocket
	}
	fd, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	id, err := identity(fd)
	if err != nil || id.UID != uint32(os.Getuid()) || os.FileMode(id.Mode) != os.ModeDir|0700 {
		fd.Close()
		return nil, errCarrierSocket
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		fd.Close()
		return nil, err
	}
	j := &carrierJournal{spool: sp, segment: root, fd: fd, segmentID: id, path: dir, binding: carrierSocketBinding{Original: StreamBinding{g.GitFilter.ManifestSHA256, g.GitFilter.ScopeSHA256, g.GitFilter.ReservationID, g.AttemptID, g.PlanRevision}, ReservationID: g.GitFilterCarrier.ReservationID, Scope: g.GitFilterCarrier.ScopeSHA256, RuntimeNamespace: g.GitFilterCarrier.RuntimeNamespace, DirectorySlots: 1, SocketSlots: 1}, current: carrierJournalSnapshot{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, HostID: g.HostID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision, Grant: *g.GitFilterCarrier, Filter: *g.GitFilter}}
	j.self = j
	if err = j.verify(); err != nil {
		_ = j.close()
		return nil, err
	}
	return j, nil
}

func (j *carrierJournal) roots() error {
	if j == nil || j.self != j || j.closed || j.failed || j.fd == nil || j.segment == nil || !canonical(j.path) {
		return errCarrierSocket
	}
	held, err := identity(j.fd)
	nm, ne := named(j.path)
	if err != nil || ne != nil || !brokerDirectorySame(held, j.segmentID) || !brokerDirectorySame(nm, j.segmentID) {
		return errCarrierSocket
	}
	f, err := j.segment.Open(".")
	if err != nil {
		return err
	}
	actual, err := identity(f)
	closeErr := f.Close()
	if err != nil || closeErr != nil || !brokerDirectorySame(actual, j.segmentID) {
		return errCarrierSocket
	}
	return nil
}
func (j *carrierJournal) read() ([]byte, node, error) {
	f, err := j.segment.OpenFile(carrierJournalName, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, node{}, err
	}
	defer f.Close()
	id, err := identity(f)
	if err != nil || id.Mode != 0600 || id.UID != uint32(os.Getuid()) || id.Nlink != 1 || id.Size < 1 || id.Size > contract.GitFilterJournalLimit+1 {
		return nil, node{}, errCarrierSocket
	}
	body, err := io.ReadAll(io.LimitReader(f, contract.GitFilterJournalLimit+2))
	after, ae := identity(f)
	info, ne := j.segment.Lstat(carrierJournalName)
	if err != nil || ae != nil || ne != nil || after != id || int64(len(body)) != id.Size {
		return nil, node{}, errCarrierSocket
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != id.Device || stat.Ino != id.Inode || info.Mode() != 0600 {
		return nil, node{}, errCarrierSocket
	}
	return body, id, nil
}
func (j *carrierJournal) verify() error {
	if err := j.roots(); err != nil {
		return err
	}
	if _, err := j.segment.Lstat("." + carrierJournalName + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		return errCarrierSocket
	}
	if len(j.body) == 0 {
		if _, err := j.segment.Lstat(carrierJournalName); !errors.Is(err, os.ErrNotExist) {
			return errCarrierSocket
		}
		return nil
	}
	body, id, err := j.read()
	if err != nil || id != j.currentID || !bytes.Equal(body, j.body) {
		return errCarrierSocket
	}
	return nil
}
func (j *carrierJournal) write(ctx context.Context, next carrierJournalSnapshot) error {
	if !carrierSocketContext(ctx) || j.current.Ordinal >= 4096 {
		return errCarrierSocket
	}
	if err := j.verify(); err != nil {
		j.failed = true
		return err
	}
	next.Ordinal = j.current.Ordinal + 1
	h := sha256.Sum256(j.body)
	next.PreviousSHA256 = hex.EncodeToString(h[:])
	raw, err := json.Marshal(next)
	if err != nil || len(raw) > contract.GitFilterJournalLimit {
		return errCarrierSocket
	}
	if err = j.spool.WriteMetadata(carrierJournalName, next); err != nil {
		j.failed = true
		return err
	}
	if err = j.roots(); err != nil {
		j.failed = true
		return err
	}
	body, id, err := j.read()
	if err != nil || !bytes.Equal(body, append(raw, '\n')) || !carrierSocketContext(ctx) {
		j.failed = true
		return errCarrierSocket
	}
	j.current, j.body, j.currentID = next, body, id
	return nil
}
func (j *carrierJournal) RecordCarrierSocket(ctx context.Context, r carrierSocketRecord) error {
	if j == nil || j.self != j {
		return errCarrierSocket
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if r.Binding != j.binding || r.Unknown || !r.Known || !carrierDirectorySame(r.Segment, carrierSocketIdentity{j.segmentID.Device, j.segmentID.Inode, j.segmentID.Nlink, j.segmentID.UID, j.segmentID.Mode}) {
		return errCarrierSocket
	}
	prior := ""
	if j.current.Socket != nil {
		prior = j.current.Socket.Phase
	}
	if !(prior == "" && r.Phase == "reserved" || prior == "reserved" && (r.Phase == "prepared" || r.Phase == "not_created") || prior == "prepared" && r.Phase == "removing" || prior == "removing" && r.Phase == "removed") {
		return errCarrierSocket
	}
	next := j.current
	next.Socket = &carrierJournalSocket{r.Phase, r.Basename, r.Segment, r.Parent, r.Owned, r.SocketID, r.Known, r.Unknown}
	return j.write(ctx, next)
}
func (j *carrierJournal) RecordExactTransform(ctx context.Context, r ExactTransformRecord) error {
	if j == nil || j.self != j {
		return errCarrierSocket
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	b := j.binding.Original
	if r.ManifestSHA256 != b.ManifestSHA256 || r.ScopeSHA256 != b.ScopeSHA256 || r.ReservationID != b.ReservationID || r.AttemptID != b.AttemptID || r.PlanRevision != b.PlanRevision || !exactDigest(r.PermitsSHA256) || j.current.Socket == nil || j.current.Socket.Phase != "prepared" {
		return errCarrierSocket
	}
	if j.current.Transform != nil && j.current.Transform.PermitsSHA256 != r.PermitsSHA256 {
		return errCarrierSocket
	}
	next := j.current
	copy := r
	next.Transform = &copy
	return j.write(ctx, next)
}

// Call only after transport drain, original Git Wait/tree, session close and
// original socket removal. No persisted record is ever accepted as proof.
func (j *carrierJournal) cleanup(o *carrierSocket) (*contract.GitFilterCarrierCleanup, error) {
	if j == nil || j.self != j {
		return nil, errCarrierSocket
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.verify(); err != nil {
		return nil, err
	}
	g := j.current.Grant
	r := &contract.GitFilterCarrierCleanup{Version: 1, ReservationID: g.ReservationID, ScopeSHA256: g.ScopeSHA256, FilterReservationID: g.FilterReservationID, ManifestSHA256: g.ManifestSHA256, RuntimeNamespace: g.RuntimeNamespace}
	if o == nil && j.current.Ordinal == 0 {
		r.Outcome = "not_created"
		h := sha256.Sum256(nil)
		r.JournalSHA256 = hex.EncodeToString(h[:])
		return r, r.Validate()
	}
	if !o.original() || o.state.binding != j.binding {
		return nil, errCarrierSocket
	}
	o.state.mu.Lock()
	notCreated := o.state.notCreated
	known := (o.state.removed || notCreated) && !o.state.unknown && o.state.closeDone
	o.state.mu.Unlock()
	last := j.current.Socket
	if !known || last == nil || (last.Phase != "removed" && !(notCreated && last.Phase == "not_created")) || last.Unknown || !last.Known {
		return nil, errCarrierSocket
	}
	convert := func(v carrierSocketIdentity) contract.GitFilterCarrierIdentity {
		return contract.GitFilterCarrierIdentity{Device: v.Device, Inode: v.Inode, Nlink: v.Nlink, UID: v.UID, Mode: v.Mode}
	}
	h := sha256.Sum256(j.body)
	r.Outcome, r.Basename = last.Phase, last.Basename
	r.Parent, r.Directory, r.Socket = convert(last.Parent), convert(last.Directory), convert(last.Socket)
	r.FinalOrdinal = j.current.Ordinal
	r.RetainedBytes = int64(len(j.body))
	r.JournalSHA256 = hex.EncodeToString(h[:])
	return r, r.Validate()
}
func (j *carrierJournal) close() error {
	if j == nil || j.self != j {
		return errCarrierSocket
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var err error
	if j.segment != nil {
		err = j.segment.Close()
	}
	if j.fd != nil {
		err = errors.Join(err, j.fd.Close())
	}
	return err
}
