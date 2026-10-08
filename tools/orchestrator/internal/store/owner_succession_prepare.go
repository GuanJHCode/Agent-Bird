package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"math"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// AuthorizationID is supplied by the Coordinator's private successor session,
// not by the public HostHello or OwnerPrepareRequest decoder.
type PrepareSuccessorOwnerSpec struct {
	PrepareOriginalOwnerSpec
	AuthorizationID string
}

type successorOwnerPreview struct {
	scope         contract.OriginalOwnerScope
	authorization OriginalOwnerSuccessionAuthorization
	current       *originalOwnerRow
}

func (d *DB) PreviewSuccessorOwnerScope(ctx context.Context, host contract.HostHello, epoch uint64, digest, authorizationID string) (contract.OriginalOwnerScope, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "authorization", authorizationID, false)
	if err != nil {
		return contract.OriginalOwnerScope{}, err
	}
	defer owned.close()
	tx := owned.tx
	preview, err := successorOwnerPreviewTx(ctx, tx, host, epoch, digest, authorizationID)
	if err == nil {
		err = owned.finishInitialRead()
	}
	return preview.scope, err
}

func successorOwnerPredecessor(ctx context.Context, tx *sql.Tx, a OriginalOwnerSuccessionAuthorization) (originalOwnerRow, error) {
	p, err := loadOriginalOwner(ctx, tx, a.PredecessorOwnerID)
	if err != nil {
		return p, err
	}
	s := p.state.Intent.Scope
	hash, err := contract.OriginalOwnerScopeSHA256(s)
	if err != nil || p.state.IsCurrent || p.state.Phase != "retired" || p.state.Binding == nil || p.state.CurrentHost == nil || s.RunID != a.RunID || s.LaunchID != a.LaunchID || s.Incarnation != a.PredecessorIncarnation || s.Incarnation >= math.MaxInt64 || p.state.ControlRevision != a.PredecessorControlRevision || hash != a.PredecessorScopeSHA256 || p.owner.PID != a.Owner.PID || p.owner.Birth != a.Owner.Birth || *p.state.CurrentHost != a.Host || p.state.CoordinatorEpoch > a.CoordinatorEpoch {
		return p, originalOwnerRejected()
	}
	if a.AbandonedOwnerID == "" {
		if a.InitialProofID != "" {
			var valid int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_owners p JOIN owner_succession_authorizations a ON a.authorization_id=? WHERE p.owner_id=? AND `+initialPredecessorSQL, a.AuthorizationID, p.state.Intent.Scope.OwnerID).Scan(&valid); err != nil {
				return p, err
			}
			if valid != 1 {
				return p, originalOwnerRejected()
			}
		} else if p.state.AppliedControlRevision != p.state.ControlRevision {
			return p, originalOwnerRejected()
		}
	} else {
		var valid int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_owners p JOIN owner_succession_authorizations a ON a.authorization_id=? WHERE p.owner_id=? AND `+abandonedPredecessorSQL, a.AuthorizationID, p.state.Intent.Scope.OwnerID).Scan(&valid); err != nil {
			return p, err
		}
		if valid != 1 {
			return p, originalOwnerRejected()
		}
	}
	return p, nil
}

func successorOwnerReservation(ctx context.Context, tx *sql.Tx, a OriginalOwnerSuccessionAuthorization, producer string) error {
	if a.InitialProofID != "" {
		return initialOwnerReservation(ctx, tx, a, producer)
	}
	var valid int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id WHERE t.id=? AND t.run_id=? AND t.status='resume_queued' AND tr.work_revision=? AND tr.host_launch_id=? AND tr.ready_sequence=?`, a.TaskID, a.RunID, a.WorkRevision, a.LaunchID, a.ReadySequence).Scan(&valid); err != nil {
		return err
	}
	if valid != 1 {
		return originalOwnerRejected()
	}
	return ownerSuccessionQuiescentExceptQueued(ctx, tx, a.RunID, a.LaunchID, producer, a.TaskID)
}

func validateSuccessorOwnerBinding(ctx context.Context, tx *sql.Tx, row originalOwnerRow) error {
	a, err := loadOwnerSuccessionAuthorization(ctx, tx, row.state.SuccessionAuthorizationID)
	if err != nil {
		return err
	}
	p, err := successorOwnerPredecessor(ctx, tx, a)
	if err != nil {
		return err
	}
	target, err := effectiveSuccessorTargetTx(ctx, tx, a)
	if err != nil {
		return err
	}
	s := row.state.Intent.Scope
	wanted := p.state.Intent.Scope
	wanted.OwnerID, wanted.Incarnation = s.OwnerID, p.state.Intent.Scope.Incarnation+1
	wanted.OriginalOriginContextID, wanted.OriginalOriginPID, wanted.OriginalOriginBirth = target.Origin.OriginContextID, target.Origin.OriginPID, target.Origin.OriginBirth
	wanted.HostGeneration, wanted.Executable, wanted.ExecutableSHA256 = target.Generation, a.SuccessorExecutable, a.SuccessorExecutableSHA256
	if s != wanted || row.state.Intent.FirstHost == a.Host || row.state.Intent.CoordinatorEpoch < a.CoordinatorEpoch || row.state.Intent.CoordinatorEpoch < target.DerivationEpoch {
		return originalOwnerRejected()
	}
	return successorOwnerReservation(ctx, tx, a, s.ProducerID)
}

func successorOwnerPreviewTx(ctx context.Context, tx *sql.Tx, h contract.HostHello, epoch uint64, digest, authorizationID string) (successorOwnerPreview, error) {
	var result successorOwnerPreview
	a, err := loadOwnerSuccessionAuthorization(ctx, tx, authorizationID)
	if err != nil {
		return result, err
	}
	result.authorization = a
	target, err := effectiveSuccessorTargetTx(ctx, tx, a)
	if err != nil {
		return result, err
	}
	p, err := successorOwnerPredecessor(ctx, tx, a)
	if err != nil {
		return result, err
	}
	var token, mode, launchStatus, hostStatus, producer, hostOrigin, hostGeneration, hostExecutable string
	var currentEpoch uint64
	var physical contract.OriginalOwnerTransport
	var origin RunOwnerBinding
	var generation, executable string
	err = tx.QueryRowContext(ctx, `SELECT l.token_hash,l.execution_owner_mode,l.status,h.status,h.id,h.origin_context_id,h.host_generation,h.executable,h.pid,h.birth,r.origin_context_id,r.origin_pid,r.origin_birth,l.host_generation,l.executable,(SELECT epoch FROM coordinator WHERE id=1)
 FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN runs r ON r.id=t.run_id JOIN host_launches l ON l.id=tr.host_launch_id JOIN runtime_hosts h ON h.id=l.host_id AND h.launch_id=l.id
 WHERE t.id=? AND r.id=? AND l.id=? AND l.origin_context_id=r.origin_context_id`, a.TaskID, a.RunID, a.LaunchID).Scan(&token, &mode, &launchStatus, &hostStatus, &producer, &hostOrigin, &hostGeneration, &hostExecutable, &physical.PID, &physical.Birth, &origin.OriginContextID, &origin.OriginPID, &origin.OriginBirth, &generation, &executable, &currentEpoch)
	if err != nil {
		return result, err
	}
	wantedHost := contract.OriginalOwnerTransport{PID: h.PID, Birth: h.Birth}
	if epoch == 0 || epoch != currentEpoch || epoch < a.CoordinatorEpoch || epoch < target.DerivationEpoch || mode != "supervised-v1" || launchStatus == "released" || producer != p.state.Intent.Scope.ProducerID || h.LaunchID != a.LaunchID || h.LaunchToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(runtimeHash(h.LaunchToken))) != 1 || origin != target.Origin || h.OriginContextID != origin.OriginContextID || h.OriginPID != origin.OriginPID || h.OriginBirth != origin.OriginBirth || h.HostGeneration != generation || generation != target.Generation || h.Executable != executable || executable != a.SuccessorExecutable || digest != a.SuccessorExecutableSHA256 || hostOrigin != origin.OriginContextID || hostGeneration != generation || hostExecutable != executable || wantedHost.Validate() != nil || wantedHost == a.Host {
		return result, originalOwnerRejected()
	}
	s := p.state.Intent.Scope
	s.OwnerID, s.Incarnation = "", s.Incarnation+1
	s.OriginalOriginContextID, s.OriginalOriginPID, s.OriginalOriginBirth = origin.OriginContextID, origin.OriginPID, origin.OriginBirth
	s.HostGeneration, s.Executable, s.ExecutableSHA256 = generation, executable, digest
	result.scope = s
	var currentID string
	err = tx.QueryRowContext(ctx, `SELECT owner_id FROM execution_owners WHERE launch_id=? AND is_current=1`, a.LaunchID).Scan(&currentID)
	if err == nil {
		current, err := loadOriginalOwner(ctx, tx, currentID)
		if err != nil {
			return result, err
		}
		s.OwnerID = currentID
		if current.state.SuccessionAuthorizationID != a.AuthorizationID || current.state.Intent.Scope != s || physical != wantedHost || current.state.Intent.FirstHost != wantedHost {
			return result, ErrConflict
		}
		result.scope, result.current = s, &current
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if physical != a.Host || launchStatus != "offline" || hostStatus != "offline" {
		return result, originalOwnerRejected()
	}
	var maxIncarnation uint64
	var consumed int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(incarnation),0),(SELECT COUNT(*) FROM execution_owners WHERE succession_authorization_id=?) FROM execution_owners WHERE launch_id=?`, a.AuthorizationID, a.LaunchID).Scan(&maxIncarnation, &consumed); err != nil {
		return result, err
	}
	if maxIncarnation != a.PredecessorIncarnation || consumed != 0 {
		return result, ErrConflict
	}
	if err = successorOwnerReservation(ctx, tx, a, producer); err != nil {
		return result, err
	}
	return result, nil
}

func (d *DB) PrepareSuccessorOwner(ctx context.Context, spec PrepareSuccessorOwnerSpec) (contract.OriginalOwnerIntent, error) {
	return d.prepareSuccessorOwner(ctx, spec, func() (string, string, error) {
		id, err := newID("execution-owner")
		return id, originalOwnerNow(), err
	})
}

func (d *DB) prepareSuccessorOwner(ctx context.Context, spec PrepareSuccessorOwnerSpec, allocate func() (string, string, error)) (contract.OriginalOwnerIntent, error) {
	var zero contract.OriginalOwnerIntent
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "authorization", spec.AuthorizationID, false)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	preview, err := successorOwnerPreviewTx(ctx, tx, spec.Host, spec.Epoch, spec.ExecutableSHA256, spec.AuthorizationID)
	if err != nil {
		return zero, err
	}
	scope := preview.scope
	now := ""
	if preview.current == nil {
		scope.OwnerID, now, err = allocate()
		if err != nil {
			return zero, err
		}
	}
	i := contract.OriginalOwnerIntent{Scope: scope, BootstrapID: spec.BootstrapID, BootstrapTokenSHA256: spec.BootstrapTokenSHA256, CoordinatorKeyID: spec.CoordinatorKeyID, RequestID: spec.RequestID, RequestSHA256: spec.RequestSHA256, StartupDeadlineUnixMS: spec.StartupDeadlineUnixMS, FirstHost: contract.OriginalOwnerTransport{PID: spec.Host.PID, Birth: spec.Host.Birth}, CoordinatorEpoch: spec.Epoch}
	if err = i.Validate(); err != nil {
		return zero, err
	}
	if preview.current != nil {
		if i != preview.current.state.Intent {
			return zero, ErrConflict
		}
		return i, nil
	}
	body, err := originalOwnerBody(i)
	if err != nil {
		return zero, err
	}
	row := originalOwnerRow{state: OriginalOwnerState{Intent: i, SuccessionAuthorizationID: spec.AuthorizationID, Phase: "intended", IsCurrent: true, CoordinatorEpoch: spec.Epoch}, intentJSON: body, created: now, updated: now}
	if err = d.storageAdmission(ctx, tx, scope.RunID, originalOwnerRowBytes(row), false); err != nil {
		return zero, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_owners(owner_id,launch_id,run_id,producer_id,incarnation,bootstrap_id,bootstrap_token_hash,coordinator_key_id,request_id,request_hash,intent_json,phase,is_current,control_revision,coordinator_epoch,created_at,updated_at,succession_authorization_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,'intended',1,0,?,?,?,?)`, scope.OwnerID, scope.LaunchID, scope.RunID, scope.ProducerID, scope.Incarnation, i.BootstrapID, i.BootstrapTokenSHA256, i.CoordinatorKeyID, i.RequestID, i.RequestSHA256, body, i.CoordinatorEpoch, now, now, spec.AuthorizationID)
	if err != nil {
		return zero, mapConflict(err)
	}
	if err = moveOwnerTransport(ctx, tx, scope, preview.authorization.Host, i.FirstHost, spec.Epoch); err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return i, nil
}
