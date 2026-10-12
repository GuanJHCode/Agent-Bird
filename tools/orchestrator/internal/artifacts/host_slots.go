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
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	HostTerminalSlotBytes    int64 = 1 << 20
	HostProgressSlotBytes    int64 = 64 << 10
	HostProgressSlotCount          = 21
	MaxHostSlotManifestBytes int64 = 1024
)

// HostSlotBinding identifies one Store-reserved physical slot. It is an
// authority descriptor, not a Store authorization check.
type HostSlotBinding struct {
	Version       int    `json:"version"`
	ReservationID string `json:"reservation_id"`
	ScopeSHA256   string `json:"scope_sha256"`
	Class         string `json:"class"`
	Index         int    `json:"index"`
}

func (b HostSlotBinding) Validate() error {
	if b.Version != 1 || !token(b.ReservationID) || len(b.ScopeSHA256) != sha256.Size*2 || strings.ToLower(b.ScopeSHA256) != b.ScopeSHA256 {
		return ErrUnsafe
	}
	if sum, err := hex.DecodeString(b.ScopeSHA256); err != nil || len(sum) != sha256.Size {
		return ErrUnsafe
	}
	if b.Class == "terminal" && b.Index == 0 || b.Class == "progress" && b.Index >= 0 && b.Index < HostProgressSlotCount {
		return nil
	}
	return ErrUnsafe
}

// ObjectID deliberately omits scope, kind and content: all attempts to write
// a Store reservation's same physical slot contend on one namespace.
func (b HostSlotBinding) ObjectID() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Version              int
		ReservationID, Class string
		Index                int
	}{b.Version, b.ReservationID, b.Class, b.Index})
	if err != nil {
		return "", ErrUnsafe
	}
	sum := sha256.Sum256(raw)
	return "host-slot-v1-" + hex.EncodeToString(sum[:]), nil
}

type HostSlotDescriptor struct {
	HostSlotBinding
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func (d HostSlotDescriptor) limit() int64 {
	if d.Class == "progress" {
		return HostProgressSlotBytes
	}
	return HostTerminalSlotBytes
}

func (d HostSlotDescriptor) Validate() error {
	if err := d.HostSlotBinding.Validate(); err != nil || d.Size < 0 || d.Size > d.limit() || len(d.SHA256) != sha256.Size*2 || strings.ToLower(d.SHA256) != d.SHA256 {
		return ErrUnsafe
	}
	if sum, err := hex.DecodeString(d.SHA256); err != nil || len(sum) != sha256.Size {
		return ErrUnsafe
	}
	if d.Class == "terminal" && (d.Kind == "terminal" || d.Kind == "prelaunch") || d.Class == "progress" && d.Kind == "progress" {
		return nil
	}
	return ErrUnsafe
}

func (d HostSlotDescriptor) Canonical() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d)
	if err != nil || int64(len(raw)) > MaxHostSlotManifestBytes {
		return nil, ErrUnsafe
	}
	return raw, nil
}

func MaterializeHostSlot(ctx context.Context, root string, d HostSlotDescriptor, body []byte) (proof Proof, err error) {
	return materializeHostSlot(ctx, root, d, body, nil)
}

func materializeHostSlot(ctx context.Context, root string, d HostSlotDescriptor, body []byte, step func(string) error) (proof Proof, err error) {
	raw, err := d.Canonical()
	if err != nil {
		return proof, err
	}
	if int64(len(body)) != d.Size {
		return proof, ErrContent
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != d.SHA256 {
		return proof, ErrContent
	}
	if err = ctx.Err(); err != nil {
		return proof, err
	}
	id, err := d.ObjectID()
	if err != nil {
		return proof, err
	}
	n, err := openHostObjectMode(root, id, true)
	if err != nil {
		return proof, err
	}
	defer func() {
		err = errors.Join(err, n.close())
		if err != nil {
			proof = Proof{}
		}
	}()
	n.step = step
	if err = n.lock(ctx); err != nil {
		return proof, err
	}
	if err = n.layout(d.limit()); err != nil {
		return proof, err
	}
	if err = n.ensureIntentLimit(ctx, raw, MaxHostSlotManifestBytes); err != nil {
		return proof, err
	}
	if _, statErr := n.stat("data.bin"); statErr == nil {
		if _, e := n.stat("data.stage"); !errors.Is(e, unix.ENOENT) {
			return proof, ErrUnsafe
		}
		if err = n.sync(); err != nil {
			return proof, err
		}
		return n.hostSlotProof(ctx, d, raw, id)
	} else if !errors.Is(statErr, unix.ENOENT) {
		return proof, statErr
	}
	ready, err := n.recoverHostStage(ctx, d)
	if err != nil && !errors.Is(err, ErrContent) {
		return proof, err
	}
	if errors.Is(err, ErrContent) {
		ready = false
	}
	if !ready {
		if err = n.writeHostStage(ctx, d, body); err != nil {
			return proof, err
		}
	}
	if err = n.publishHostFile(ctx, "data.stage", "data.bin", d.Size, d.SHA256, d.limit()); err != nil {
		return proof, err
	}
	return n.hostSlotProof(ctx, d, raw, id)
}

func VerifyHostSlotAndCommit(ctx context.Context, root string, d HostSlotDescriptor, want Proof, commit func() error) (err error) {
	raw, err := d.Canonical()
	if err != nil {
		return err
	}
	id, err := d.ObjectID()
	if err != nil {
		return err
	}
	n, err := openHostObjectMode(root, id, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, n.close()) }()
	if err = n.lock(ctx); err != nil {
		return err
	}
	if err = n.layout(d.limit()); err != nil {
		return err
	}
	for _, name := range []string{"intent.stage", "data.stage"} {
		if _, e := n.stat(name); !errors.Is(e, unix.ENOENT) {
			return errors.Join(ErrUnsafe, e)
		}
	}
	actual, err := n.hostSlotProof(ctx, d, raw, id)
	if err != nil {
		return err
	}
	if actual != want {
		return ErrUnsafe
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return commit()
}

// ReadVerifiedHostSlot reads a bounded, verified snapshot of an existing object.
// It never takes the writer lock, creates files, or recovers partial publication.
// Consumers may inspect these bytes while holding their own DB transaction, but
// must use VerifyHostSlotAndCommit for new ownership/ref commitments.
func ReadVerifiedHostSlot(ctx context.Context, root string, d HostSlotDescriptor, want Proof) (body []byte, err error) {
	raw, err := d.Canonical()
	if err != nil {
		return nil, err
	}
	id, err := d.ObjectID()
	if err != nil {
		return nil, err
	}
	n, err := openHostObjectMode(root, id, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, n.close())
		if err != nil {
			body = nil
		}
	}()
	if err = n.layout(d.limit()); err != nil {
		return nil, err
	}
	for _, name := range []string{"intent.stage", "data.stage"} {
		if _, e := n.stat(name); !errors.Is(e, unix.ENOENT) {
			return nil, errors.Join(ErrUnsafe, e)
		}
	}
	actual, body, err := n.hostSlotProofBody(ctx, d, raw, id)
	if err != nil {
		return nil, err
	}
	if actual != want {
		return nil, ErrUnsafe
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return body, nil
}

func LoadHostSlot(ctx context.Context, root string, binding HostSlotBinding) (d HostSlotDescriptor, proof Proof, body []byte, err error) {
	return loadHostSlot(ctx, root, binding, nil)
}

func loadHostSlot(ctx context.Context, root string, binding HostSlotBinding, step func(string) error) (d HostSlotDescriptor, proof Proof, body []byte, err error) {
	if err = binding.Validate(); err != nil {
		return d, proof, body, err
	}
	id, err := binding.ObjectID()
	if err != nil {
		return d, proof, body, err
	}
	n, err := openHostObjectMode(root, id, false)
	if err != nil {
		return d, proof, body, err
	}
	defer func() {
		err = errors.Join(err, n.close())
		if err != nil {
			d, proof, body = HostSlotDescriptor{}, Proof{}, nil
		}
	}()
	n.step = step
	if err = n.lock(ctx); err != nil {
		return d, proof, body, err
	}
	if _, e := n.stat("intent.stage"); !errors.Is(e, unix.ENOENT) {
		return d, proof, body, ErrUnsafe
	}
	raw, err := n.read("intent.json", MaxHostSlotManifestBytes)
	if err != nil {
		return d, proof, body, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&d); err != nil || dec.Decode(&struct{}{}) != io.EOF || d.Validate() != nil || d.HostSlotBinding != binding {
		return HostSlotDescriptor{}, Proof{}, nil, ErrUnsafe
	}
	canonical, err := d.Canonical()
	if err != nil || !bytes.Equal(canonical, raw) {
		return HostSlotDescriptor{}, Proof{}, nil, ErrUnsafe
	}
	if err = n.layout(d.limit()); err != nil {
		return HostSlotDescriptor{}, Proof{}, nil, err
	}
	if _, e := n.stat("data.bin"); errors.Is(e, unix.ENOENT) {
		ready, recoverErr := n.recoverHostStage(ctx, d)
		if recoverErr != nil || !ready {
			return HostSlotDescriptor{}, Proof{}, nil, errors.Join(ErrContent, recoverErr)
		}
		if err = n.publishHostFile(ctx, "data.stage", "data.bin", d.Size, d.SHA256, d.limit()); err != nil {
			return HostSlotDescriptor{}, Proof{}, nil, err
		}
	} else if e != nil {
		return HostSlotDescriptor{}, Proof{}, nil, e
	}
	if _, e := n.stat("data.stage"); !errors.Is(e, unix.ENOENT) {
		return HostSlotDescriptor{}, Proof{}, nil, ErrUnsafe
	}
	proof, body, err = n.hostSlotProofBody(ctx, d, canonical, id)
	if err != nil {
		return HostSlotDescriptor{}, Proof{}, nil, err
	}
	return d, proof, body, nil
}

func (n *object) ensureIntentLimit(ctx context.Context, body []byte, limit int64) (err error) {
	got, err := n.read("intent.json", limit)
	if err == nil {
		if !bytes.Equal(got, body) {
			return ErrConflict
		}
		if _, e := n.stat("intent.stage"); !errors.Is(e, unix.ENOENT) {
			return ErrUnsafe
		}
		return nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	for _, name := range []string{"data.bin", "data.stage"} {
		if _, e := n.stat(name); !errors.Is(e, unix.ENOENT) {
			return ErrUnsafe
		}
	}
	f, before, err := n.regular("intent.stage", unix.O_RDWR, limit, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = n.point("intent.stage:before-truncate"); err != nil {
		return err
	}
	if err = n.verify(f, "intent.stage", before, false, limit); err != nil {
		return err
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = n.point("intent.stage:after-write"); err != nil {
		return err
	}
	if err = n.verify(f, "intent.stage", before, false, limit); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = n.point("intent.stage:after-sync"); err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	return n.publishHostFile(ctx, "intent.stage", "intent.json", int64(len(body)), hex.EncodeToString(digest[:]), limit)
}

func (n *object) publishHostFile(ctx context.Context, from, to string, size int64, digest string, limit int64) error {
	return n.publishChecked(from, to, limit, func(f *os.File, before unix.Stat_t) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if before.Size != size {
			return ErrContent
		}
		h := sha256.New()
		if _, err := io.Copy(h, io.LimitReader(f, size+1)); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != digest {
			return ErrContent
		}
		return ctx.Err()
	})
}

func (n *object) writeHostStage(ctx context.Context, d HostSlotDescriptor, body []byte) (err error) {
	f, before, err := n.regular("data.stage", unix.O_RDWR, d.limit(), true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = n.point("data.stage:before-truncate"); err != nil {
		return err
	}
	if err = n.verify(f, "data.stage", before, false, d.limit()); err != nil {
		return err
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = n.point("data.stage:after-write"); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = n.verify(f, "data.stage", before, false, d.limit()); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return n.point("data.stage:after-sync")
}

func (n *object) recoverHostStage(ctx context.Context, d HostSlotDescriptor) (ready bool, err error) {
	before, err := n.verifyHostData(ctx, "data.stage", d)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f, held, err := n.regular("data.stage", unix.O_RDONLY, d.limit(), false)
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			ready = false
		}
	}()
	if !state(before, held) || n.verify(f, "data.stage", before, true, d.limit()) != nil {
		return false, ErrUnsafe
	}
	if err = n.point("data.stage:before-recover-sync"); err != nil {
		return false, err
	}
	if err = f.Sync(); err != nil {
		return false, err
	}
	if err = n.point("data.stage:after-recover-sync"); err != nil {
		return false, err
	}
	if err = n.verify(f, "data.stage", before, true, d.limit()); err != nil {
		return false, err
	}
	return true, nil
}

func (n *object) verifyHostData(ctx context.Context, name string, d HostSlotDescriptor) (before unix.Stat_t, err error) {
	f, before, err := n.regular(name, unix.O_RDONLY, d.limit(), false)
	if err != nil {
		return before, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if before.Size != d.Size {
		return before, ErrContent
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, d.Size+1)); err != nil {
		return before, err
	}
	if err = ctx.Err(); err != nil {
		return before, err
	}
	if hex.EncodeToString(h.Sum(nil)) != d.SHA256 {
		return before, ErrContent
	}
	if err = n.verify(f, name, before, true, d.limit()); err != nil {
		return before, err
	}
	return before, nil
}

func (n *object) hostSlotProof(ctx context.Context, d HostSlotDescriptor, raw []byte, id string) (Proof, error) {
	proof, _, err := n.hostSlotProofBody(ctx, d, raw, id)
	return proof, err
}

// hostSlotProofBody reads the exact file handle whose post-read namespace
// identity is used in Proof. Load must not verify one opening and return bytes
// from a later opening: an attacker could replace the named file between them.
func (n *object) hostSlotProofBody(ctx context.Context, d HostSlotDescriptor, raw []byte, id string) (Proof, []byte, error) {
	intentBefore, err := n.stat("intent.json")
	if err != nil {
		return Proof{}, nil, err
	}
	got, err := n.read("intent.json", MaxHostSlotManifestBytes)
	if err != nil || !bytes.Equal(got, raw) {
		return Proof{}, nil, ErrConflict
	}
	before, body, err := n.readHostData(ctx, "data.bin", d)
	if err != nil {
		return Proof{}, nil, err
	}
	intentAfter, err := n.stat("intent.json")
	if err != nil || !state(intentBefore, intentAfter) {
		return Proof{}, nil, ErrUnsafe
	}
	if err = n.verify(n.lockFile, "writer.lock", n.lockID, true, 0); err != nil {
		return Proof{}, nil, err
	}
	digest := sha256.Sum256(raw)
	return Proof{IntakeID: id, DescriptorSHA256: hex.EncodeToString(digest[:]), Path: n.path + "/data.bin", SHA256: d.SHA256, Size: d.Size, Device: uint64(before.Dev), Inode: uint64(before.Ino), RootDevice: uint64(n.ids[0].Dev), RootInode: uint64(n.ids[0].Ino), ParentDevice: uint64(n.ids[1].Dev), ParentInode: uint64(n.ids[1].Ino), DirectoryDevice: uint64(n.ids[2].Dev), DirectoryInode: uint64(n.ids[2].Ino), IntentDevice: uint64(intentBefore.Dev), IntentInode: uint64(intentBefore.Ino), LockDevice: uint64(n.lockID.Dev), LockInode: uint64(n.lockID.Ino)}, body, nil
}

func (n *object) readHostData(ctx context.Context, name string, d HostSlotDescriptor) (before unix.Stat_t, body []byte, err error) {
	f, before, err := n.regular(name, unix.O_RDONLY, d.limit(), false)
	if err != nil {
		return before, nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if before.Size != d.Size {
		return before, nil, ErrContent
	}
	body = make([]byte, d.Size)
	if _, err = io.ReadFull(f, body); err != nil {
		return before, nil, err
	}
	if err = n.point(name + ":after-read"); err != nil {
		return before, nil, err
	}
	if err = n.verify(f, name, before, true, d.limit()); err != nil {
		return before, nil, err
	}
	if err = ctx.Err(); err != nil {
		return before, nil, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != d.SHA256 {
		return before, nil, ErrContent
	}
	return before, body, nil
}
