//go:build darwin || linux

package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

const deletionPlanBytes = manifestBytes

// Deletion is an exclusive, durable operation which removes only data.bin.
// The immutable intake intent and writer lock remain as the recovery anchor.
type Deletion struct {
	n         *object
	intake    Intake
	expected  Proof
	requestID string
	plan      []byte
}

type deletionPlan struct {
	Version   int    `json:"version"`
	Intake    Intake `json:"intake"`
	Proof     Proof  `json:"proof"`
	RequestID string `json:"request_id"`
}

func OpenDeletion(ctx context.Context, stateDir string, s Intake, expected Proof, requestID string, savedPlan []byte) (d *Deletion, err error) {
	if err = s.Validate(); err != nil || !token(requestID) || ctx.Err() != nil {
		return nil, errors.Join(err, ctx.Err(), ErrUnsafe)
	}
	n, err := openObjectMode(stateDir, s.ID, false)
	if err != nil {
		return nil, err
	}
	d = &Deletion{n: n, intake: s, expected: expected, requestID: requestID}
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

func (d *Deletion) Plan() []byte { return append([]byte(nil), d.plan...) }

func (d *Deletion) MarkerBytes() int64 {
	if d == nil || d.n == nil {
		return 0
	}
	st, err := d.n.stat("deletion.marker")
	if err != nil {
		return 0
	}
	return st.Size
}

func (d *Deletion) RemovedBytes() int64 {
	if d == nil || d.n == nil {
		return 0
	}
	if _, err := d.n.stat("data.bin"); errors.Is(err, unix.ENOENT) {
		return d.intake.Size
	}
	return 0
}

func (d *Deletion) Remove(ctx context.Context) error {
	if d == nil || d.n == nil {
		return ErrUnsafe
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
	actual, err := d.n.proof(ctx, d.intake)
	if err != nil || actual != d.expected {
		return errors.Join(ErrUnsafe, err)
	}
	if err = unix.Unlinkat(int(d.n.last().Fd()), "data.bin", 0); err != nil {
		return err
	}
	return d.n.sync()
}

func (d *Deletion) VerifyRemoved(ctx context.Context) error {
	if d == nil || d.n == nil {
		return ErrUnsafe
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

func (d *Deletion) Close() error {
	if d == nil || d.n == nil {
		return nil
	}
	err := d.n.close()
	d.n = nil
	return err
}

func (d *Deletion) bind(ctx context.Context, saved []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.deletionLayout(); err != nil {
		return err
	}
	plan, err := canonicalDeletionPlan(saved, d.intake, d.expected, d.requestID)
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
	want, err := d.intake.canonical()
	if err != nil {
		return err
	}
	got, err := d.n.read("intent.json", manifestBytes)
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
		actual, proofErr := d.n.proof(ctx, d.intake)
		if proofErr != nil || actual != d.expected {
			return errors.Join(ErrUnsafe, proofErr)
		}
	}
	return nil
}

func canonicalDeletionPlan(saved []byte, s Intake, expected Proof, requestID string) ([]byte, error) {
	want := deletionPlan{Version: 1, Intake: s, Proof: expected, RequestID: requestID}
	canonical, err := json.Marshal(want)
	if err != nil || len(canonical) > deletionPlanBytes {
		return nil, ErrUnsafe
	}
	if saved == nil {
		return canonical, nil
	}
	var got deletionPlan
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

func (d *Deletion) deletionLayout() error {
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
		limit := d.intake.Size
		switch name {
		case "writer.lock":
			limit = 0
		case "intent.json":
			limit = manifestBytes
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

func (d *Deletion) ensureMarker(ctx context.Context) error {
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
