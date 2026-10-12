//go:build darwin || linux

package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrHostSlotUnowned = errors.New("host_slot_unowned")

type HostSlotNamespaceProof struct {
	Version      int    `json:"version"`
	RootPath     string `json:"root_path"`
	RootDevice   uint64 `json:"root_device"`
	RootInode    uint64 `json:"root_inode"`
	ParentDevice uint64 `json:"parent_device"`
	ParentInode  uint64 `json:"parent_inode"`
}

type HostSlotDiscoveryMode string

const (
	HostSlotAbsentOnly      HostSlotDiscoveryMode = "absent_only"
	HostSlotDiscoverManaged HostSlotDiscoveryMode = "schema40_managed"
)

type HostSlotFileSnapshot struct {
	Name   string `json:"name"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}

// HostSlotDeletionPlan is the exact, durable facts record returned by binding
// discovery. Pointer fields are nil when the named artifact was absent.
type HostSlotDeletionPlan struct {
	Version             int                    `json:"version"`
	RequestID           string                 `json:"request_id"`
	Binding             HostSlotBinding        `json:"binding"`
	ObjectID            string                 `json:"object_id"`
	Namespace           HostSlotNamespaceProof `json:"namespace"`
	DiscoveryMode       HostSlotDiscoveryMode  `json:"discovery_mode"`
	Mode                string                 `json:"mode"`
	Descriptor          *HostSlotDescriptor    `json:"descriptor"`
	Proof               *Proof                 `json:"proof"`
	DirectoryDevice     uint64                 `json:"directory_device"`
	DirectoryInode      uint64                 `json:"directory_inode"`
	LockDevice          uint64                 `json:"lock_device"`
	LockInode           uint64                 `json:"lock_inode"`
	Intent              *HostSlotFileSnapshot  `json:"intent"`
	IntentStage         *HostSlotFileSnapshot  `json:"intent_stage"`
	Data                *HostSlotFileSnapshot  `json:"data"`
	DataStage           *HostSlotFileSnapshot  `json:"data_stage"`
	PlannedRemovedBytes int64                  `json:"planned_removed_bytes"`
}

// HostSlotDeletion is an exclusive, durable removal of a Host slot's data.bin.
// Its descriptor, intent and writer lock remain as the recovery anchor.
type HostSlotDeletion struct {
	n            *object
	descriptor   HostSlotDescriptor
	expected     Proof
	requestID    string
	plan         []byte
	bindingPlan  *HostSlotDeletionPlan
	removedBytes int64
}

type hostSlotDeletionPlan struct {
	Version    int                `json:"version"`
	Descriptor HostSlotDescriptor `json:"descriptor"`
	Proof      Proof              `json:"proof"`
	RequestID  string             `json:"request_id"`
}

func OpenHostSlotDeletion(ctx context.Context, root string, descriptor HostSlotDescriptor, expected Proof, requestID string, savedPlan []byte) (d *HostSlotDeletion, err error) {
	if err = descriptor.Validate(); err != nil || !token(requestID) || ctx.Err() != nil {
		return nil, errors.Join(err, ctx.Err(), ErrUnsafe)
	}
	id, err := descriptor.ObjectID()
	if err != nil {
		return nil, err
	}
	n, err := openHostObjectMode(root, id, false)
	if err != nil {
		return nil, err
	}
	d = &HostSlotDeletion{n: n, descriptor: descriptor, expected: expected, requestID: requestID}
	defer func() {
		if err != nil {
			_ = d.Close()
			d = nil
		}
	}()
	if err = n.lock(ctx); err != nil {
		return nil, err
	}
	if err = d.bind(ctx, savedPlan); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *HostSlotDeletion) Plan() []byte { return append([]byte(nil), d.plan...) }

func (d *HostSlotDeletion) MarkerBytes() int64 {
	if d == nil || d.n == nil {
		return 0
	}
	st, err := d.n.stat("deletion.marker")
	if err != nil {
		return 0
	}
	return st.Size
}

func (d *HostSlotDeletion) RemovedBytes() int64 {
	if d == nil || d.n == nil {
		return 0
	}
	if d.bindingPlan != nil {
		var removed int64
		for _, snapshot := range []*HostSlotFileSnapshot{d.bindingPlan.Data, d.bindingPlan.DataStage} {
			if snapshot != nil {
				if _, err := d.n.stat(snapshot.Name); errors.Is(err, unix.ENOENT) {
					removed += int64(snapshot.Size)
				}
			}
		}
		return removed
	}
	if _, err := d.n.stat("data.bin"); errors.Is(err, unix.ENOENT) {
		return d.descriptor.Size
	}
	return 0
}

func (d *HostSlotDeletion) Remove(ctx context.Context) error {
	if d == nil || d.n == nil {
		return ErrUnsafe
	}
	if d.bindingPlan != nil {
		return d.removeBinding(ctx)
	}
	if err := d.bind(ctx, d.plan); err != nil {
		return err
	}
	if err := d.ensureMarker(ctx); err != nil {
		return err
	}
	if _, err := d.n.stat("data.bin"); errors.Is(err, unix.ENOENT) {
		return d.VerifyRemoved(ctx)
	} else if err != nil {
		return err
	}
	raw, err := d.descriptor.Canonical()
	if err != nil {
		return err
	}
	actual, err := d.n.hostSlotProof(ctx, d.descriptor, raw, d.expected.IntakeID)
	if err != nil || actual != d.expected {
		return errors.Join(ErrUnsafe, err)
	}
	if err = unix.Unlinkat(int(d.n.last().Fd()), "data.bin", 0); err != nil {
		return err
	}
	return d.n.sync()
}

func (d *HostSlotDeletion) VerifyRemoved(ctx context.Context) error {
	if d == nil || d.n == nil {
		return ErrUnsafe
	}
	if d.bindingPlan != nil {
		return d.verifyBindingRemoved(ctx)
	}
	if err := d.bind(ctx, d.plan); err != nil {
		return err
	}
	got, err := d.n.read("deletion.marker", deletionPlanBytes)
	if err != nil || !bytes.Equal(got, d.plan) {
		return errors.Join(ErrUnsafe, err)
	}
	if _, err = d.n.stat("data.bin"); !errors.Is(err, unix.ENOENT) {
		return errors.Join(ErrUnsafe, err)
	}
	return d.n.sync()
}

func (d *HostSlotDeletion) Close() error {
	if d == nil || d.n == nil {
		return nil
	}
	err := d.n.close()
	d.n = nil
	return err
}

func (d *HostSlotDeletion) bind(ctx context.Context, saved []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.deletionLayout(); err != nil {
		return err
	}
	plan, err := canonicalHostSlotDeletionPlan(saved, d.descriptor, d.expected, d.requestID)
	if err != nil {
		return err
	}
	d.plan = plan
	intent, err := d.n.stat("intent.json")
	if err != nil || uint64(intent.Dev) != d.expected.IntentDevice || uint64(intent.Ino) != d.expected.IntentInode {
		return errors.Join(ErrUnsafe, err)
	}
	if uint64(d.n.lockID.Dev) != d.expected.LockDevice || uint64(d.n.lockID.Ino) != d.expected.LockInode ||
		uint64(d.n.ids[0].Dev) != d.expected.RootDevice || uint64(d.n.ids[0].Ino) != d.expected.RootInode ||
		uint64(d.n.ids[1].Dev) != d.expected.ParentDevice || uint64(d.n.ids[1].Ino) != d.expected.ParentInode ||
		uint64(d.n.ids[2].Dev) != d.expected.DirectoryDevice || uint64(d.n.ids[2].Ino) != d.expected.DirectoryInode {
		return ErrUnsafe
	}
	want, err := d.descriptor.Canonical()
	if err != nil {
		return err
	}
	got, err := d.n.read("intent.json", MaxHostSlotManifestBytes)
	if err != nil || !bytes.Equal(got, want) {
		return errors.Join(ErrConflict, err)
	}
	if marker, markerErr := d.n.read("deletion.marker", deletionPlanBytes); markerErr == nil {
		if !bytes.Equal(marker, d.plan) {
			return ErrUnsafe
		}
	} else if !errors.Is(markerErr, unix.ENOENT) {
		return markerErr
	}
	if _, stageErr := d.n.stat("deletion.stage"); !errors.Is(stageErr, unix.ENOENT) {
		return errors.Join(ErrUnsafe, stageErr)
	}
	if err := d.n.verify(d.n.lockFile, "writer.lock", d.n.lockID, true, 0); err != nil {
		return err
	}
	if saved == nil {
		actual, proofErr := d.n.hostSlotProof(ctx, d.descriptor, want, d.expected.IntakeID)
		if proofErr != nil || actual != d.expected {
			return errors.Join(ErrUnsafe, proofErr)
		}
	}
	return nil
}

func canonicalHostSlotDeletionPlan(saved []byte, descriptor HostSlotDescriptor, expected Proof, requestID string) ([]byte, error) {
	want := hostSlotDeletionPlan{Version: 1, Descriptor: descriptor, Proof: expected, RequestID: requestID}
	canonical, err := json.Marshal(want)
	if err != nil || len(canonical) > deletionPlanBytes {
		return nil, ErrUnsafe
	}
	if saved == nil {
		return canonical, nil
	}
	var got hostSlotDeletionPlan
	dec := json.NewDecoder(bytes.NewReader(saved))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&got); err != nil || got != want {
		return nil, ErrUnsafe
	}
	var trailing struct{}
	if err = dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrUnsafe
	}
	encoded, err := json.Marshal(got)
	if err != nil || !bytes.Equal(encoded, saved) {
		return nil, ErrUnsafe
	}
	return canonical, nil
}

func (d *HostSlotDeletion) deletionLayout() error {
	f, err := openAt(d.n.last(), ".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer f.Close()
	names, err := f.Readdirnames(7)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 6 {
		return ErrUnsafe
	}
	for _, name := range names {
		limit := d.descriptor.limit()
		switch name {
		case "writer.lock":
			limit = 0
		case "intent.json":
			limit = MaxHostSlotManifestBytes
		case "data.bin":
		case "deletion.marker", "deletion.stage":
			limit = deletionPlanBytes
		default:
			return ErrUnsafe
		}
		st, statErr := d.n.stat(name)
		if statErr != nil || !private(st, false) || st.Size > limit {
			return errors.Join(ErrUnsafe, statErr)
		}
	}
	return d.n.check()
}

func (d *HostSlotDeletion) ensureMarker(ctx context.Context) error {
	got, err := d.n.read("deletion.marker", deletionPlanBytes)
	if err == nil {
		if !bytes.Equal(got, d.plan) {
			return ErrConflict
		}
		if _, e := d.n.stat("deletion.stage"); !errors.Is(e, unix.ENOENT) {
			return ErrUnsafe
		}
		return d.n.sync()
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if _, e := d.n.stat("deletion.stage"); !errors.Is(e, unix.ENOENT) {
		return ErrUnsafe
	}
	f, before, err := d.n.regular("deletion.stage", unix.O_RDWR, deletionPlanBytes, true)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Write(d.plan); err != nil {
			return err
		}
		if err := d.n.verify(f, "deletion.stage", before, false, deletionPlanBytes); err != nil {
			return err
		}
		return f.Sync()
	}()
	if err = errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return d.n.publishChecked("deletion.stage", "deletion.marker", deletionPlanBytes, nil)
}

func (d *HostSlotDeletion) bindBindingPlan(ctx context.Context) error {
	if d.bindingPlan == nil || ctx.Err() != nil {
		return errors.Join(ErrUnsafe, ctx.Err())
	}
	p := d.bindingPlan
	if err := verifyHostSlotNamespace(p.Namespace.RootPath, p.Namespace); err != nil || uint64(d.n.ids[2].Dev) != p.DirectoryDevice || uint64(d.n.ids[2].Ino) != p.DirectoryInode || uint64(d.n.lockID.Dev) != p.LockDevice || uint64(d.n.lockID.Ino) != p.LockInode {
		return errors.Join(ErrUnsafe, err)
	}
	if err := hostSlotDeletionBindingLayout(d.n); err != nil {
		return err
	}
	marker, markerErr := d.n.read("deletion.marker", deletionPlanBytes)
	marked := markerErr == nil
	if marked && !bytes.Equal(marker, d.plan) {
		return ErrUnsafe
	}
	if !marked && !errors.Is(markerErr, unix.ENOENT) {
		return markerErr
	}
	if p.Mode == "published_orphan" {
		if p.Proof == nil || p.Proof.Path != d.n.path+"/data.bin" {
			return ErrUnsafe
		}
		if !marked {
			raw, err := p.Descriptor.Canonical()
			if err != nil {
				return err
			}
			actual, err := d.n.hostSlotProof(ctx, *p.Descriptor, raw, p.ObjectID)
			if err != nil || actual != *p.Proof {
				return errors.Join(ErrUnsafe, err)
			}
		}
	}
	for _, expected := range []*HostSlotFileSnapshot{p.Intent, p.IntentStage, p.Data, p.DataStage} {
		if expected != nil {
			if err := verifyHostSlotSnapshot(d.n, p.Binding, expected); err != nil && !(marked && (expected.Name == "intent.stage" || expected.Name == "data.bin" || expected.Name == "data.stage") && errors.Is(err, unix.ENOENT)) {
				return err
			}
		}
	}
	for _, name := range []string{"intent.json", "intent.stage", "data.bin", "data.stage"} {
		if bindingPlanSnapshot(p, name) == nil {
			if _, err := d.n.stat(name); !errors.Is(err, unix.ENOENT) {
				return errors.Join(ErrUnsafe, err)
			}
		}
	}
	return d.n.verify(d.n.lockFile, "writer.lock", d.n.lockID, true, 0)
}

func bindingPlanSnapshot(p *HostSlotDeletionPlan, name string) *HostSlotFileSnapshot {
	switch name {
	case "intent.json":
		return p.Intent
	case "intent.stage":
		return p.IntentStage
	case "data.bin":
		return p.Data
	case "data.stage":
		return p.DataStage
	}
	return nil
}

func hostSlotDeletionBindingLayout(n *object) error {
	f, err := openAt(n.last(), ".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer f.Close()
	names, err := f.Readdirnames(8)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 7 {
		return ErrUnsafe
	}
	for _, name := range names {
		switch name {
		case "writer.lock", "intent.json", "intent.stage", "data.bin", "data.stage", "deletion.marker", "deletion.stage":
		default:
			return ErrUnsafe
		}
	}
	return n.check()
}

func (d *HostSlotDeletion) removeBinding(ctx context.Context) error {
	if err := d.bindBindingPlan(ctx); err != nil {
		return err
	}
	if err := d.ensureMarker(ctx); err != nil {
		return err
	}
	for _, target := range []struct {
		name     string
		snapshot *HostSlotFileSnapshot
	}{{"data.bin", d.bindingPlan.Data}, {"data.stage", d.bindingPlan.DataStage}, {"intent.stage", d.bindingPlan.IntentStage}} {
		snapshot := target.snapshot
		if snapshot == nil {
			continue
		}
		if _, err := d.n.stat(target.name); errors.Is(err, unix.ENOENT) {
			continue
		} else if err != nil {
			return err
		}
		if err := verifyHostSlotSnapshot(d.n, d.bindingPlan.Binding, snapshot); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(d.n.last().Fd()), target.name, 0); err != nil {
			return err
		}
		if target.name == "data.bin" || target.name == "data.stage" {
			d.removedBytes += int64(snapshot.Size)
		}
	}
	return d.n.sync()
}

func (d *HostSlotDeletion) verifyBindingRemoved(ctx context.Context) error {
	if err := d.bindBindingPlan(ctx); err != nil {
		return err
	}
	marker, err := d.n.read("deletion.marker", deletionPlanBytes)
	if err != nil || !bytes.Equal(marker, d.plan) {
		return errors.Join(ErrUnsafe, err)
	}
	for _, name := range []string{"data.bin", "data.stage", "intent.stage"} {
		if _, err := d.n.stat(name); !errors.Is(err, unix.ENOENT) {
			return errors.Join(ErrUnsafe, err)
		}
	}
	return d.n.sync()
}

func verifyHostSlotSnapshot(n *object, binding HostSlotBinding, want *HostSlotFileSnapshot) error {
	got, err := hostSlotSnapshot(n, binding, want.Name)
	if err != nil || *got != *want {
		return errors.Join(ErrUnsafe, err)
	}
	return nil
}

// OpenHostSlotBindingDeletion opens a Store-authorized deletion lease for an
// otherwise unregistered binding. It never turns an existing unknown object
// into an owned slot: absent_only may create only a missing object, while
// schema40_managed may inspect the fixed Host publication layout.
func OpenHostSlotBindingDeletion(ctx context.Context, root string, binding HostSlotBinding, namespace HostSlotNamespaceProof, mode HostSlotDiscoveryMode, requestID string, savedPlan []byte) (d *HostSlotDeletion, plan HostSlotDeletionPlan, err error) {
	if err = binding.Validate(); err != nil || !token(requestID) || (mode != HostSlotAbsentOnly && mode != HostSlotDiscoverManaged) || ctx.Err() != nil {
		return nil, plan, errors.Join(err, ctx.Err(), ErrUnsafe)
	}
	id, err := binding.ObjectID()
	if err != nil {
		return nil, plan, err
	}
	if err = verifyHostSlotNamespace(root, namespace); err != nil {
		return nil, plan, err
	}
	var replay *HostSlotDeletionPlan
	if savedPlan != nil {
		decoded, decodeErr := decodeHostSlotBindingPlan(savedPlan)
		if decodeErr != nil || decoded.Binding != binding || decoded.Namespace != namespace || decoded.RequestID != requestID || decoded.DiscoveryMode != mode || decoded.Mode == "" {
			return nil, plan, errors.Join(ErrUnsafe, decodeErr)
		}
		replay = &decoded
	}
	exists, err := hostSlotObjectExists(root, id)
	if err != nil {
		return nil, plan, err
	}
	if exists && mode == HostSlotAbsentOnly && replay == nil {
		return nil, plan, ErrHostSlotUnowned
	}
	n, err := openHostObjectMode(root, id, !exists)
	if err != nil {
		return nil, plan, err
	}
	d = &HostSlotDeletion{n: n, requestID: requestID}
	defer func() {
		if err != nil {
			_ = d.Close()
			d = nil
		}
	}()
	if err = n.lock(ctx); err != nil {
		return nil, plan, err
	}
	if replay != nil {
		plan = *replay
	} else if !exists {
		plan = HostSlotDeletionPlan{Version: 1, RequestID: requestID, Binding: binding, ObjectID: id, Namespace: namespace, DiscoveryMode: mode, Mode: string(HostSlotAbsentOnly), DirectoryDevice: uint64(n.ids[2].Dev), DirectoryInode: uint64(n.ids[2].Ino), LockDevice: uint64(n.lockID.Dev), LockInode: uint64(n.lockID.Ino)}
	} else {
		plan, err = discoverManagedHostSlotDeletion(ctx, n, binding, namespace, id, requestID, mode)
		if err != nil {
			return nil, HostSlotDeletionPlan{}, err
		}
	}
	if err = validateHostSlotBindingPlan(plan, binding, namespace, id, requestID); err != nil {
		return nil, HostSlotDeletionPlan{}, err
	}
	encoded := savedPlan
	if replay == nil {
		encoded, err = canonicalHostSlotBindingPlan(nil, plan)
		if err != nil {
			return nil, HostSlotDeletionPlan{}, err
		}
	}
	d.plan, d.bindingPlan = encoded, &plan
	if err = d.bindBindingPlan(ctx); err != nil {
		return nil, HostSlotDeletionPlan{}, err
	}
	return d, plan, nil
}

func validateHostSlotBindingPlan(plan HostSlotDeletionPlan, binding HostSlotBinding, namespace HostSlotNamespaceProof, id, requestID string) error {
	if plan.Version != 1 || plan.RequestID != requestID || plan.Binding != binding || plan.ObjectID != id || plan.Namespace != namespace || (plan.DiscoveryMode != HostSlotAbsentOnly && plan.DiscoveryMode != HostSlotDiscoverManaged) ||
		(plan.Mode != string(HostSlotAbsentOnly) && plan.Mode != "published_orphan" && plan.Mode != "partial_orphan") ||
		plan.DirectoryDevice == 0 || plan.DirectoryInode == 0 || plan.LockDevice == 0 || plan.LockInode == 0 {
		return ErrUnsafe
	}
	wantNames := []struct {
		name string
		got  *HostSlotFileSnapshot
	}{{"intent.json", plan.Intent}, {"intent.stage", plan.IntentStage}, {"data.bin", plan.Data}, {"data.stage", plan.DataStage}}
	seen := make(map[string]bool, len(wantNames))
	for _, entry := range wantNames {
		if entry.got == nil {
			continue
		}
		if entry.got.Name != entry.name || seen[entry.got.Name] || entry.got.Device == 0 || entry.got.Inode == 0 || int64(entry.got.Size) < 0 || entry.got.Size > uint64(bindingLimit(binding)) || len(entry.got.SHA256) != sha256.Size*2 {
			return ErrUnsafe
		}
		if entry.name == "intent.json" || entry.name == "intent.stage" {
			if entry.got.Size > uint64(MaxHostSlotManifestBytes) {
				return ErrUnsafe
			}
		}
		if decoded, err := hex.DecodeString(entry.got.SHA256); err != nil || len(decoded) != sha256.Size || strings.ToLower(entry.got.SHA256) != entry.got.SHA256 {
			return ErrUnsafe
		}
		seen[entry.got.Name] = true
	}
	removed := int64(0)
	if plan.Data != nil {
		removed += int64(plan.Data.Size)
	}
	if plan.DataStage != nil {
		removed += int64(plan.DataStage.Size)
	}
	if plan.PlannedRemovedBytes != removed {
		return ErrUnsafe
	}
	switch plan.Mode {
	case string(HostSlotAbsentOnly):
		if plan.Descriptor != nil || plan.Proof != nil || plan.Intent != nil || plan.IntentStage != nil || plan.Data != nil || plan.DataStage != nil || removed != 0 {
			return ErrUnsafe
		}
	case "published_orphan":
		if plan.DiscoveryMode != HostSlotDiscoverManaged {
			return ErrUnsafe
		}
		if plan.Descriptor == nil || plan.Proof == nil || plan.Intent == nil || plan.Data == nil || plan.IntentStage != nil || plan.DataStage != nil || plan.Descriptor.HostSlotBinding != binding || plan.Descriptor.Validate() != nil {
			return ErrUnsafe
		}
		raw, err := plan.Descriptor.Canonical()
		if err != nil {
			return ErrUnsafe
		}
		digest := sha256.Sum256(raw)
		if plan.Intent.Size != uint64(len(raw)) || plan.Intent.SHA256 != hex.EncodeToString(digest[:]) || plan.Data.Size != uint64(plan.Descriptor.Size) || plan.Data.SHA256 != plan.Descriptor.SHA256 ||
			plan.Proof.IntakeID != id || plan.Proof.DescriptorSHA256 != hex.EncodeToString(digest[:]) || plan.Proof.Size != plan.Descriptor.Size || plan.Proof.SHA256 != plan.Descriptor.SHA256 || plan.Proof.Device != plan.Data.Device || plan.Proof.Inode != plan.Data.Inode || plan.Proof.RootDevice != namespace.RootDevice || plan.Proof.RootInode != namespace.RootInode || plan.Proof.ParentDevice != namespace.ParentDevice || plan.Proof.ParentInode != namespace.ParentInode || plan.Proof.IntentDevice != plan.Intent.Device || plan.Proof.IntentInode != plan.Intent.Inode || plan.Proof.DirectoryDevice != plan.DirectoryDevice || plan.Proof.DirectoryInode != plan.DirectoryInode || plan.Proof.LockDevice != plan.LockDevice || plan.Proof.LockInode != plan.LockInode {
			return ErrUnsafe
		}
	case "partial_orphan":
		if plan.DiscoveryMode != HostSlotDiscoverManaged || plan.Descriptor != nil || plan.Proof != nil || (plan.Intent == nil && plan.IntentStage == nil && plan.Data == nil && plan.DataStage == nil) {
			return ErrUnsafe
		}
	}
	return nil
}

func verifyHostSlotNamespace(root string, p HostSlotNamespaceProof) error {
	if p.Version != 1 || p.RootPath != root {
		return ErrUnsafe
	}
	c, err := openAnchor(root, true)
	if err != nil {
		return err
	}
	defer c.close()
	if err = c.child("managed-artifacts", false); err != nil {
		return err
	}
	if uint64(c.ids[0].Dev) != p.RootDevice || uint64(c.ids[0].Ino) != p.RootInode || uint64(c.ids[1].Dev) != p.ParentDevice || uint64(c.ids[1].Ino) != p.ParentInode {
		return ErrUnsafe
	}
	return nil
}

func hostSlotObjectExists(root, id string) (bool, error) {
	c, err := openAnchor(root, true)
	if err != nil {
		return false, err
	}
	defer c.close()
	if err = c.child("managed-artifacts", false); err != nil {
		return false, err
	}
	var st unix.Stat_t
	err = unix.Fstatat(int(c.last().Fd()), id, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil || !private(st, true) {
		return false, errors.Join(ErrUnsafe, err)
	}
	return true, nil
}

func canonicalHostSlotBindingPlan(saved []byte, want HostSlotDeletionPlan) ([]byte, error) {
	encoded, err := json.Marshal(want)
	if err != nil || len(encoded) > deletionPlanBytes {
		return nil, ErrUnsafe
	}
	if saved == nil {
		return encoded, nil
	}
	var got HostSlotDeletionPlan
	dec := json.NewDecoder(bytes.NewReader(saved))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&got); err != nil || got != want {
		return nil, ErrUnsafe
	}
	var tail struct{}
	if err = dec.Decode(&tail); !errors.Is(err, io.EOF) {
		return nil, ErrUnsafe
	}
	if !bytes.Equal(saved, encoded) {
		return nil, ErrUnsafe
	}
	return encoded, nil
}

func decodeHostSlotBindingPlan(saved []byte) (HostSlotDeletionPlan, error) {
	var plan HostSlotDeletionPlan
	dec := json.NewDecoder(bytes.NewReader(saved))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return HostSlotDeletionPlan{}, ErrUnsafe
	}
	var tail struct{}
	if err := dec.Decode(&tail); !errors.Is(err, io.EOF) {
		return HostSlotDeletionPlan{}, ErrUnsafe
	}
	encoded, err := json.Marshal(plan)
	if err != nil || len(encoded) > deletionPlanBytes || !bytes.Equal(encoded, saved) {
		return HostSlotDeletionPlan{}, ErrUnsafe
	}
	return plan, nil
}

func discoverManagedHostSlotDeletion(ctx context.Context, n *object, binding HostSlotBinding, namespace HostSlotNamespaceProof, id, requestID string, discoveryMode HostSlotDiscoveryMode) (HostSlotDeletionPlan, error) {
	plan := HostSlotDeletionPlan{Version: 1, RequestID: requestID, Binding: binding, ObjectID: id, Namespace: namespace, DiscoveryMode: discoveryMode, Mode: "partial_orphan", DirectoryDevice: uint64(n.ids[2].Dev), DirectoryInode: uint64(n.ids[2].Ino), LockDevice: uint64(n.lockID.Dev), LockInode: uint64(n.lockID.Ino)}
	for _, spec := range []struct {
		name string
		to   **HostSlotFileSnapshot
	}{{"intent.json", &plan.Intent}, {"intent.stage", &plan.IntentStage}, {"data.bin", &plan.Data}, {"data.stage", &plan.DataStage}} {
		s, err := hostSlotSnapshot(n, binding, spec.name)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return HostSlotDeletionPlan{}, err
		}
		*spec.to = s
	}
	if err := hostSlotManagedLayout(n); err != nil {
		return HostSlotDeletionPlan{}, err
	}
	if plan.Intent == nil && plan.IntentStage == nil && plan.Data == nil && plan.DataStage == nil {
		return HostSlotDeletionPlan{}, ErrHostSlotUnowned
	}
	if plan.Intent != nil {
		var descriptor HostSlotDescriptor
		dec := json.NewDecoder(bytes.NewReader(mustHostSlotRead(n, "intent.json")))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&descriptor); err != nil || dec.Decode(&struct{}{}) != io.EOF || descriptor.Validate() != nil || descriptor.HostSlotBinding != binding {
			return HostSlotDeletionPlan{}, ErrUnsafe
		}
		raw, err := descriptor.Canonical()
		if err != nil || !bytes.Equal(raw, mustHostSlotRead(n, "intent.json")) {
			return HostSlotDeletionPlan{}, ErrUnsafe
		}
		if plan.Data != nil && plan.IntentStage == nil && plan.DataStage == nil {
			proof, err := n.hostSlotProof(ctx, descriptor, raw, id)
			if err != nil {
				return HostSlotDeletionPlan{}, err
			}
			plan.Mode, plan.Descriptor, plan.Proof = "published_orphan", &descriptor, &proof
		}
	}
	if plan.Data != nil {
		plan.PlannedRemovedBytes += int64(plan.Data.Size)
	}
	if plan.DataStage != nil {
		plan.PlannedRemovedBytes += int64(plan.DataStage.Size)
	}
	return plan, nil
}

func mustHostSlotRead(n *object, name string) []byte {
	b, _ := n.read(name, deletionPlanBytes)
	return b
}

func hostSlotManagedLayout(n *object) error {
	f, err := openAt(n.last(), ".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer f.Close()
	names, err := f.Readdirnames(7)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 5 {
		return ErrUnsafe
	}
	for _, name := range names {
		switch name {
		case "writer.lock", "intent.json", "intent.stage", "data.bin", "data.stage":
		default:
			return ErrUnsafe
		}
	}
	return n.check()
}

func hostSlotSnapshot(n *object, binding HostSlotBinding, name string) (*HostSlotFileSnapshot, error) {
	st, err := n.stat(name)
	if err != nil {
		return nil, err
	}
	limit := bindingLimit(binding)
	if name == "intent.json" || name == "intent.stage" {
		limit = MaxHostSlotManifestBytes
	}
	if !private(st, false) || st.Size > limit {
		return nil, ErrUnsafe
	}
	body, err := n.read(name, limit)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	return &HostSlotFileSnapshot{Name: name, Device: uint64(st.Dev), Inode: uint64(st.Ino), Size: uint64(st.Size), SHA256: hex.EncodeToString(sum[:])}, nil
}

func bindingLimit(binding HostSlotBinding) int64 {
	if binding.Class == "progress" {
		return HostProgressSlotBytes
	}
	return HostTerminalSlotBytes
}
