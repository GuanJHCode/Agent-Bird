package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// OriginalOwnerLiveObservation is private Coordinator evidence obtained while
// holding host-registration serialization. Public requests cannot supply it.
type OriginalOwnerLiveObservation struct {
	OwnerID          string                          `json:"-"`
	Owner            contract.OriginalOwnerTransport `json:"-"`
	Origin           RunOwnerBinding                 `json:"-"`
	Epoch            uint64                          `json:"-"`
	ExecutableSHA256 string                          `json:"-"`
	OwnerAliveAt     string                          `json:"-"`
}

// OriginalOwnerResumeReceipt permanently identifies the first admitted mode.
// Exact authenticated replay never changes mode after the observed owner dies.
type OriginalOwnerResumeReceipt struct {
	Mode             string `json:"mode"`
	CommandID        string `json:"command_id"`
	RequestSHA256    string `json:"request_sha256"`
	RunID            string `json:"run_id"`
	TaskID           string `json:"task_id"`
	LaunchID         string `json:"launch_id"`
	OwnerID          string `json:"owner_id"`
	AuthorizationID  string `json:"authorization_id,omitempty"`
	WorkRevision     int    `json:"work_revision"`
	ControlRevision  uint64 `json:"control_revision"`
	CoordinatorEpoch uint64 `json:"coordinator_epoch"`
	ReadySequence    int64  `json:"ready_sequence"`
	CreatedAt        string `json:"created_at"`
}

type existingOwnerResumeRow struct {
	OriginalOwnerResumeReceipt
	segmentCount  int64
	lastSegmentID string
}

func loadExistingOwnerResume(ctx context.Context, tx *sql.Tx, command string) (existingOwnerResumeRow, error) {
	r := existingOwnerResumeRow{OriginalOwnerResumeReceipt: OriginalOwnerResumeReceipt{Mode: "existing_owner"}}
	err := tx.QueryRowContext(ctx, `SELECT command_id,request_hash,run_id,task_id,launch_id,owner_id,last_segment_id,created_at,work_revision,control_revision,coordinator_epoch,ready_sequence,segment_count FROM existing_owner_resume_commands WHERE command_id=?`, command).Scan(&r.CommandID, &r.RequestSHA256, &r.RunID, &r.TaskID, &r.LaunchID, &r.OwnerID, &r.lastSegmentID, &r.CreatedAt, &r.WorkRevision, &r.ControlRevision, &r.CoordinatorEpoch, &r.ReadySequence, &r.segmentCount)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return r, err
}

func ownerSuccessionResumeReceipt(a OriginalOwnerSuccessionAuthorization) OriginalOwnerResumeReceipt {
	return OriginalOwnerResumeReceipt{Mode: "successor", CommandID: a.CommandID, RequestSHA256: a.RequestSHA256, RunID: a.RunID, TaskID: a.TaskID, LaunchID: a.LaunchID, OwnerID: a.PredecessorOwnerID, AuthorizationID: a.AuthorizationID, WorkRevision: a.WorkRevision, ControlRevision: a.PredecessorControlRevision, CoordinatorEpoch: a.CoordinatorEpoch, ReadySequence: a.ReadySequence, CreatedAt: a.CreatedAt}
}

func loadOriginalOwnerResume(ctx context.Context, tx *sql.Tx, command string) (OriginalOwnerResumeReceipt, bool, error) {
	var zero OriginalOwnerResumeReceipt
	live, liveErr := loadExistingOwnerResume(ctx, tx, command)
	if liveErr != nil && !errors.Is(liveErr, ErrNotFound) {
		return zero, false, liveErr
	}
	dead, deadErr := loadOwnerSuccessionCommand(ctx, tx, command)
	if deadErr != nil && !errors.Is(deadErr, ErrNotFound) {
		return zero, false, deadErr
	}
	if liveErr == nil && deadErr == nil {
		return zero, false, ErrConflict
	}
	if liveErr == nil {
		return live.OriginalOwnerResumeReceipt, true, nil
	}
	if deadErr == nil {
		return ownerSuccessionResumeReceipt(dead), true, nil
	}
	return zero, false, nil
}

// LookupOriginalOwnerResume authenticates against the current task capability
// before looking up immutable history, independently of mutable resume gates.
func (d *DB) LookupOriginalOwnerResume(ctx context.Context, request OriginalOwnerResumeRequest) (OriginalOwnerResumeReceipt, bool, error) {
	var zero OriginalOwnerResumeReceipt
	hash, err := originalOwnerResumeHash(request)
	if err != nil {
		return zero, false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return zero, false, err
	}
	defer owned.close()
	if err = authenticateOwnerResumeTx(ctx, owned.tx, request); err != nil {
		return zero, false, err
	}
	r, found, err := loadOriginalOwnerResume(ctx, owned.tx, request.CommandID)
	if err != nil {
		return zero, false, err
	}
	if found && r.RequestSHA256 != hash {
		return zero, false, ErrConflict
	}
	return r, found, nil
}

func (d *DB) ResumeExistingOriginalOwner(ctx context.Context, request OriginalOwnerResumeRequest, observed OriginalOwnerLiveObservation, key ed25519.PublicKey) (OriginalOwnerResumeReceipt, error) {
	return d.resumeExistingOriginalOwner(ctx, request, observed, key, originalOwnerNow)
}

func (d *DB) resumeExistingOriginalOwner(ctx context.Context, request OriginalOwnerResumeRequest, observed OriginalOwnerLiveObservation, key ed25519.PublicKey, now func() string) (OriginalOwnerResumeReceipt, error) {
	var zero OriginalOwnerResumeReceipt
	hash, err := originalOwnerResumeHash(request)
	if err != nil {
		return zero, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	if err = authenticateOwnerResumeTx(ctx, tx, request); err != nil {
		return zero, err
	}
	prior, found, err := loadOriginalOwnerResume(ctx, tx, request.CommandID)
	if err != nil {
		return zero, err
	}
	if found {
		if prior.Mode != "existing_owner" || prior.RequestSHA256 != hash {
			return zero, ErrConflict
		}
		return prior, nil
	}
	if len(key) != ed25519.PublicKeySize || observed.OwnerID == "" || observed.Owner.Validate() != nil || !successionObservationTime(observed.OwnerAliveAt) {
		return zero, originalOwnerRejected()
	}
	row, err := loadOriginalOwner(ctx, tx, observed.OwnerID)
	if err != nil {
		return zero, err
	}
	s := row.state.Intent.Scope
	if !row.state.IsCurrent || row.state.Phase != "bound" || row.state.Binding == nil || row.state.CurrentHost == nil || row.state.AppliedControlRevision != row.state.ControlRevision || observed.Owner != (contract.OriginalOwnerTransport{PID: row.owner.PID, Birth: row.owner.Birth}) || observed.ExecutableSHA256 != s.ExecutableSHA256 || contract.VerifyOriginalOwnerBinding(*row.state.Binding, key) != nil {
		return zero, originalOwnerRejected()
	}
	var permitRequest string
	if err = tx.QueryRowContext(ctx, `SELECT request_id FROM owner_control_changes WHERE owner_id=? AND revision=? AND applied_hash IS NOT NULL`, observed.OwnerID, row.state.ControlRevision).Scan(&permitRequest); err != nil {
		return zero, originalOwnerRejected()
	}
	permit, err := loadOriginalOwnerPermit(ctx, tx, permitRequest, key)
	if err != nil {
		return zero, err
	}
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(s)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(row.owner)
	if permit.OwnerID != observed.OwnerID || permit.ControlRevision != row.state.ControlRevision || permit.ScopeSHA256 != scopeHash || permit.OwnerIdentitySHA256 != identityHash || permit.NewHost != *row.state.CurrentHost || permit.CoordinatorEpoch != row.state.CoordinatorEpoch || permit.CoordinatorKeyID != row.state.Intent.CoordinatorKeyID {
		return zero, originalOwnerRejected()
	}
	// The existing signer retains its immutable origin and host generation.
	// Unlike succession, a rebind chain cannot authorize retargeting a live owner.
	target, err := ownerSuccessionTarget(ctx, tx, request, OriginalOwnerSuccessionObservation{Host: *row.state.CurrentHost, Origin: observed.Origin, Epoch: observed.Epoch, ExecutableSHA256: observed.ExecutableSHA256}, row)
	if err != nil {
		return zero, err
	}
	if target.SuccessorHostGeneration != s.HostGeneration || observed.Origin != (RunOwnerBinding{OriginContextID: s.OriginalOriginContextID, OriginPID: s.OriginalOriginPID, OriginBirth: s.OriginalOriginBirth}) {
		return zero, originalOwnerRejected()
	}
	var hostEpoch uint64
	var hostStatus string
	if err = tx.QueryRowContext(ctx, `SELECT coordinator_epoch,status FROM runtime_hosts WHERE id=? AND launch_id=?`, s.ProducerID, s.LaunchID).Scan(&hostEpoch, &hostStatus); err != nil {
		return zero, err
	}
	if hostEpoch != observed.Epoch || (hostStatus != "ready" && hostStatus != "reconciling" && hostStatus != "offline") {
		return zero, originalOwnerRejected()
	}
	r := existingOwnerResumeRow{OriginalOwnerResumeReceipt: OriginalOwnerResumeReceipt{Mode: "existing_owner", CommandID: request.CommandID, RequestSHA256: hash, RunID: s.RunID, TaskID: request.TaskID, LaunchID: s.LaunchID, OwnerID: observed.OwnerID, WorkRevision: request.WorkRevision, ControlRevision: row.state.ControlRevision, CoordinatorEpoch: observed.Epoch, CreatedAt: now()}}
	if !successionObservationTime(r.CreatedAt) {
		return zero, originalOwnerRejected()
	}
	r.segmentCount, r.lastSegmentID, err = ownerResumeSegmentSnapshot(ctx, tx, request.TaskID)
	if err != nil {
		return zero, err
	}
	if err = d.storageAdmission(ctx, tx, r.RunID, existingOwnerResumeBytes(r), false); err != nil {
		return zero, err
	}
	r.ReadySequence, err = d.queueResumeTx(ctx, tx, request.TaskID, request.WorkRevision)
	if err != nil {
		return zero, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO existing_owner_resume_commands(command_id,request_hash,run_id,task_id,launch_id,owner_id,last_segment_id,created_at,work_revision,control_revision,coordinator_epoch,ready_sequence,segment_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.CommandID, r.RequestSHA256, r.RunID, r.TaskID, r.LaunchID, r.OwnerID, r.lastSegmentID, r.CreatedAt, r.WorkRevision, r.ControlRevision, r.CoordinatorEpoch, r.ReadySequence, r.segmentCount)
	if err != nil {
		return zero, mapConflict(err)
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return r.OriginalOwnerResumeReceipt, nil
}

func ownerResumeSegmentSnapshot(ctx context.Context, tx *sql.Tx, task string) (int64, string, error) {
	var count int64
	var last string
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE((SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1),'') FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=?`, task, task).Scan(&count, &last)
	return count, last, err
}

func existingOwnerResumeBytes(r existingOwnerResumeRow) int64 {
	cost := int64(40)
	for _, value := range []string{r.CommandID, r.RequestSHA256, r.RunID, r.TaskID, r.LaunchID, r.OwnerID, r.lastSegmentID, r.CreatedAt} {
		cost += int64(len(value))
	}
	return cost
}
