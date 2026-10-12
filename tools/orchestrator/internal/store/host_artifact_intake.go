package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

var errHostArtifactVerification = errors.New("host_artifact_verification_required")

type hostArtifactCommit struct {
	descriptor    artifacts.HostSlotDescriptor
	proof         artifacts.Proof
	root, receipt string
	manifestBytes int64
}
type hostArtifactGate struct {
	item       *hostArtifactCommit
	verified   bool
	validation *validationRetryDecision
}

func freezeHostEvent(e contract.Event) (contract.Event, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return contract.Event{}, err
	}
	var frozen contract.Event
	err = json.Unmarshal(body, &frozen)
	return frozen, err
}

// The first transaction only authenticates and binds the claimed object. It
// releases the DB mutex before filesystem work; the callback reauthorizes and
// commits under the original object's lock. Durable replay returns before IO.
func (d *DB) CommitHostEvent(ctx context.Context, event contract.Event) (contract.DurableAck, error) {
	event, err := freezeHostEvent(event)
	if err != nil {
		return contract.DurableAck{}, err
	}
	gate := &hostArtifactGate{}
	ack, err := d.commitHostEvent(ctx, event, gate)
	if !errors.Is(err, errHostArtifactVerification) {
		return ack, err
	}
	if event.Failure.ValidValidationStart() {
		gate.validation, err = d.prepareValidationRetry(ctx, event, gate.item)
		if err != nil {
			return contract.DurableAck{}, err
		}
	}
	err = artifacts.VerifyHostSlotAndCommit(ctx, gate.item.root, gate.item.descriptor, gate.item.proof, func() error {
		gate.verified = true
		var commitErr error
		ack, commitErr = d.commitHostEvent(ctx, event, gate)
		return commitErr
	})
	if err != nil {
		return contract.DurableAck{}, hostArtifactError(err)
	}
	return ack, nil
}

func (d *DB) CommitHostProgress(ctx context.Context, event contract.Event, launchID, token string, pid int, birth string) error {
	event, err := freezeHostEvent(event)
	if err != nil {
		return err
	}
	gate := &hostArtifactGate{}
	err = d.commitHostProgress(ctx, event, launchID, token, pid, birth, gate)
	if !errors.Is(err, errHostArtifactVerification) {
		return err
	}
	err = artifacts.VerifyHostSlotAndCommit(ctx, gate.item.root, gate.item.descriptor, gate.item.proof, func() error {
		gate.verified = true
		return d.commitHostProgress(ctx, event, launchID, token, pid, birth, gate)
	})
	if err != nil {
		return hostArtifactError(err)
	}
	return nil
}

func hostArtifactError(err error) error {
	var code CodeError
	if errors.As(err, &code) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrHostRejected) || errors.Is(err, ErrConflict) || errors.Is(err, ErrEventSequence) {
		return err
	}
	return CodeError("host_artifact_unavailable")
}

func authorizeHostArtifact(ctx context.Context, tx *sql.Tx, database string, e contract.Event, progress bool) (*hostArtifactCommit, error) {
	if e.Artifact == nil {
		return nil, nil
	}
	bad := CodeError("host_artifact_invalid")
	g := contract.LaunchCommand{RunID: e.RunID, TaskID: e.TaskID, AttemptID: e.AttemptID, SegmentID: e.SegmentID, HostID: e.ProducerID, CommandID: e.CommandID, WorkRevision: e.WorkRevision}
	target := "terminal"
	if progress {
		target = "progress"
	}
	if err := bindHostArtifactTarget(ctx, tx, &g, false, target); err != nil {
		return nil, err
	}
	r := e.Artifact.HostArtifact
	if g.HostArtifacts == nil {
		// Historical claims carry no managed filesystem authority.
		if r != nil {
			return nil, bad
		}
		return nil, nil
	}
	if r == nil || r.Validate() != nil {
		return nil, bad
	}
	var desc artifacts.HostSlotDescriptor
	var proof artifacts.Proof
	if json.Unmarshal(r.Descriptor, &desc) != nil || json.Unmarshal(r.Proof, &proof) != nil {
		return nil, bad
	}
	canonical, err := desc.Canonical()
	if err != nil || !bytes.Equal(canonical, r.Descriptor) {
		return nil, bad
	}
	proofBytes, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(proofBytes, r.Proof) {
		return nil, bad
	}
	reservation, class, index := g.HostArtifacts.TerminalReservationID, "terminal", 0
	if progress {
		reservation, class, index = g.HostArtifacts.ProgressReservationID, "progress", int(e.Sequence-1)
	}
	if desc.ReservationID != reservation || desc.ScopeSHA256 != g.HostArtifacts.ScopeSHA256 || desc.Class != class || desc.Index != index {
		return nil, bad
	}
	if progress {
		if desc.Kind != "progress" || e.Artifact.ID != "provider-progress" {
			return nil, bad
		}
	} else {
		switch e.Kind {
		case contract.EventLaunchRejected:
			if desc.Kind != "prelaunch" || e.Artifact.ID != "launch-failure" {
				return nil, bad
			}
		case contract.EventUnknown:
			if desc.Kind == "prelaunch" && e.Artifact.ID != "launch-failure" || desc.Kind == "terminal" && e.Artifact.ID != "result-output.bin" {
				return nil, bad
			}
		case contract.EventResult, contract.EventQuestion, contract.EventFailed, contract.EventStopped, contract.EventExited:
			if desc.Kind != "terminal" || e.Artifact.ID != "result-output.bin" {
				return nil, bad
			}
		default:
			return nil, bad
		}
	}
	id, err := desc.ObjectID()
	if err != nil {
		return nil, bad
	}
	root := filepath.Join(filepath.Dir(database), "host-spool", e.ProducerID)
	expected := filepath.Join(root, "managed-artifacts", id, "data.bin")
	if proof.IntakeID != id || proof.DescriptorSHA256 != runtimeHash(string(canonical)) || proof.Path != expected || e.Artifact.Path != expected || proof.SHA256 != desc.SHA256 || e.Artifact.SHA256 != desc.SHA256 || proof.Size != desc.Size || e.Artifact.Size != desc.Size {
		return nil, bad
	}
	if e.Kind != contract.EventExited && e.PayloadHash != desc.SHA256 {
		return nil, bad
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > contract.HostArtifactReceiptLimit {
		return nil, bad
	}
	return &hostArtifactCommit{descriptor: desc, proof: proof, root: root, receipt: string(raw), manifestBytes: int64(len(canonical))}, nil
}

func (d *DB) retainHostArtifact(ctx context.Context, tx *sql.Tx, e contract.Event, gate *hostArtifactGate, progress bool) error {
	item, err := authorizeHostArtifact(ctx, tx, d.path, e, progress)
	if err != nil {
		return err
	}
	if item == nil {
		if gate.verified {
			return CodeError("host_artifact_invalid")
		}
		return nil
	}
	if !gate.verified {
		gate.item = item
		return errHostArtifactVerification
	}
	if gate.item == nil || *gate.item != *item {
		return CodeError("host_artifact_invalid")
	}
	slot := strconv.Itoa(item.descriptor.Index)
	var saved string
	var metadata int64
	err = tx.QueryRowContext(ctx, `SELECT proof_json,manifest_bytes FROM managed_artifact_refs WHERE reservation_id=? AND slot=?`, item.descriptor.ReservationID, slot).Scan(&saved, &metadata)
	if err == nil {
		if saved != item.receipt || metadata != item.manifestBytes {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_artifact_refs(id,reservation_id,run_id,slot,intake_id,proof_json,manifest_bytes) VALUES(?,?,?,?,NULL,?,?)`, item.proof.IntakeID, item.descriptor.ReservationID, e.RunID, slot, item.receipt, item.manifestBytes); err != nil {
		return mapConflict(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE managed_artifact_reservations SET state='active',retained_bytes=retained_bytes+? WHERE id=? AND state IN ('reserved','active') AND retained_bytes<=reserved_bytes-?`, item.proof.Size, item.descriptor.ReservationID, item.proof.Size)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}

func (d *DB) hostArtifactCommitAdmission(ctx context.Context, tx *sql.Tx, run string, before int64, progress bool) error {
	var after int64
	if err := tx.QueryRowContext(ctx, `SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&after); err != nil {
		return err
	}
	if err := d.storageAdmission(ctx, tx, run, 0, progress); err != nil {
		return err
	}
	control := after - before
	if control < 0 {
		control = 0
	}
	if progress {
		return d.managedArtifactAdmission(ctx, tx, 0, control)
	}
	return d.managedReportArtifactAdmission(ctx, tx, 0, control)
}
