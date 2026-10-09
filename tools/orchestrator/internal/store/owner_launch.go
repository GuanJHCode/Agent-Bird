package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func insertOwnerLaunchHead(ctx context.Context, tx *sql.Tx, owner string, attempt int, phase, hash, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_launch_heads(owner_id,attempt_no,phase,latest_receipt_hash,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(owner_id) DO UPDATE SET attempt_no=excluded.attempt_no,phase=excluded.phase,latest_receipt_hash=excluded.latest_receipt_hash,updated_at=excluded.updated_at`, owner, attempt, phase, hash, now)
	return mapConflict(err)
}

func supervisorPublic(key ed25519.PrivateKey) (string, error) {
	p, err := originalOwnerSigningPublic(key, "")
	if err == nil {
		return hex.EncodeToString(p), nil
	}
	if len(key) != ed25519.PrivateKeySize {
		return "", originalOwnerRejected()
	}
	return hex.EncodeToString(key.Public().(ed25519.PublicKey)), nil
}

// PreBindRetryAdmission is a credential-free, read-only selection.  The
// Coordinator fills Supervisor and Prepare only after authenticating both
// physical peers; Commit repeats every durable predicate.
type PreBindRetryAdmission struct {
	Recovery              HostRecovery
	OwnerID               string
	ExpectedAttemptSHA256 string
	OldFirstHost          contract.OriginalOwnerTransport
	NewHost               contract.HostHello
	Epoch                 uint64
	Supervisor            contract.OriginalOwnerLaunchSupervisor
	Prepare               PrepareOriginalOwnerSpec
}

func validPreBindReceiptChain(ctx context.Context, tx *sql.Tx, owner string, a contract.OriginalOwnerLaunchAttempt, latest contract.OriginalOwnerLaunchReceipt, key ed25519.PublicKey) error {
	rows, err := tx.QueryContext(ctx, `SELECT receipt_json,receipt_hash,phase FROM owner_launch_receipts WHERE owner_id=? AND attempt_hash=? ORDER BY receipt_no`, owner, a.AttemptSHA256)
	if err != nil {
		return err
	}
	defer rows.Close()
	var prior string
	var phases []string
	for rows.Next() {
		var body, hash, phase string
		if err = rows.Scan(&body, &hash, &phase); err != nil {
			return err
		}
		var r contract.OriginalOwnerLaunchReceipt
		if contract.DecodeOwnerRuntimeStrict([]byte(body), &r) != nil || r.ReceiptSHA256 != hash || r.Phase != phase || r.PriorReceiptSHA256 != prior || (key != nil && contract.VerifyOriginalOwnerLaunchReceipt(r, key, a) != nil) {
			return originalOwnerRejected()
		}
		prior = hash
		phases = append(phases, phase)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(phases) == 0 || prior != latest.ReceiptSHA256 {
		return originalOwnerRejected()
	}
	if latest.Phase == contract.OriginalOwnerLaunchPreparedNotStarted {
		if len(phases) != 1 {
			return originalOwnerRejected()
		}
		return nil
	}
	if latest.Phase == contract.OriginalOwnerLaunchNotStartedFinal {
		if len(phases) != 3 || phases[0] != contract.OriginalOwnerLaunchPreparedNotStarted || phases[1] != contract.OriginalOwnerLaunchStarting {
			return originalOwnerRejected()
		}
		return nil
	}
	if latest.Phase == contract.OriginalOwnerLaunchWaited {
		if len(phases) != 4 || phases[0] != contract.OriginalOwnerLaunchPreparedNotStarted || phases[1] != contract.OriginalOwnerLaunchStarting || phases[2] != contract.OriginalOwnerLaunchStartedWaitOwned {
			return originalOwnerRejected()
		}
		return nil
	}
	return originalOwnerRejected()
}

func validBindLaunchWitness(ctx context.Context, tx *sql.Tx, owner string, a contract.OriginalOwnerLaunchAttempt, latest contract.OriginalOwnerLaunchReceipt, child contract.OriginalOwnerTransport, key ed25519.PublicKey) error {
	rows, err := tx.QueryContext(ctx, `SELECT receipt_json,receipt_hash FROM owner_launch_receipts WHERE owner_id=? AND attempt_hash=? ORDER BY receipt_no`, owner, a.AttemptSHA256)
	if err != nil {
		return err
	}
	defer rows.Close()
	var prior string
	phases := []string{}
	var started *contract.OriginalOwnerLaunchReceipt
	for rows.Next() {
		var body, hash string
		if err = rows.Scan(&body, &hash); err != nil {
			return err
		}
		var r contract.OriginalOwnerLaunchReceipt
		if contract.DecodeOwnerRuntimeStrict([]byte(body), &r) != nil || r.ReceiptSHA256 != hash || r.PriorReceiptSHA256 != prior || contract.VerifyOriginalOwnerLaunchReceipt(r, key, a) != nil {
			return originalOwnerRejected()
		}
		prior = hash
		phases = append(phases, r.Phase)
		if r.Phase == contract.OriginalOwnerLaunchStartedWaitOwned {
			x := r
			started = &x
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if latest.Phase != contract.OriginalOwnerLaunchStartedWaitOwned && latest.Phase != contract.OriginalOwnerLaunchWaited {
		return originalOwnerRejected()
	}
	if prior != latest.ReceiptSHA256 || len(phases) < 3 || phases[0] != contract.OriginalOwnerLaunchPreparedNotStarted || phases[1] != contract.OriginalOwnerLaunchStarting || phases[2] != contract.OriginalOwnerLaunchStartedWaitOwned || started == nil || started.Child == nil || *started.Child != child || !started.HandoffClosed {
		return originalOwnerRejected()
	}
	return nil
}

func ownerLaunchRetrySnapshot(ctx context.Context, tx *sql.Tx, hello contract.HostHello, epoch uint64, digest string, key ed25519.PublicKey) (PreBindRetryAdmission, originalOwnerRow, contract.OriginalOwnerLaunchAttempt, contract.OriginalOwnerLaunchReceipt, error) {
	var out PreBindRetryAdmission
	var row originalOwnerRow
	var a contract.OriginalOwnerLaunchAttempt
	var r contract.OriginalOwnerLaunchReceipt
	if epoch == 0 || digest == "" {
		return out, row, a, r, originalOwnerRejected()
	}
	// A pending retry peer is intentionally not registered yet.  Compare all
	// stable launch fields/token against the currently registered predecessor.
	scope, physical, err := originalOwnerRuntimeScope(ctx, tx, hello, epoch, false)
	if err != nil {
		return out, row, a, r, err
	}
	if hello.PID == physical.PID && hello.Birth == physical.Birth {
		return out, row, a, r, originalOwnerRejected()
	}
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT owner_id FROM execution_owners WHERE launch_id=? AND is_current=1`, hello.LaunchID).Scan(&owner); err != nil {
		return out, row, a, r, err
	}
	row, err = loadOriginalOwner(ctx, tx, owner)
	if err != nil {
		return out, row, a, r, err
	}
	if !row.state.IsCurrent || row.state.Phase != "intended" || row.state.ControlRevision != 0 || row.state.Binding != nil || row.state.CurrentHost != nil || !originalOwnerSameScope(scope, row.state.Intent.Scope) || row.state.Intent.Scope.ExecutableSHA256 != digest {
		return out, row, a, r, originalOwnerRejected()
	}
	a, r, err = loadOwnerLaunch(ctx, tx, owner)
	if err != nil {
		return out, row, a, r, err
	}
	if a.Version != 2 || a.Attempt != 1 || a.BaseIntent != row.state.Intent || a.EffectiveIntent.FirstHost != physical || a.EffectiveIntent.Scope != row.state.Intent.Scope || a.EffectiveIntent.Scope.ExecutableSHA256 != digest || (key != nil && validPreBindReceiptChain(ctx, tx, owner, a, r, key) != nil) || (key == nil && validPreBindReceiptChain(ctx, tx, owner, a, r, nil) != nil) {
		return out, row, a, r, originalOwnerRejected()
	}
	var work int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE host_id=?`, row.state.Intent.Scope.ProducerID).Scan(&work); err != nil || work != 0 {
		return out, row, a, r, originalOwnerRejected()
	}
	out = PreBindRetryAdmission{Recovery: HostRecovery{RunID: scope.RunID, LaunchID: scope.LaunchID, OriginContextID: scope.OriginalOriginContextID, OriginPID: scope.OriginalOriginPID, OriginBirth: scope.OriginalOriginBirth, HostGeneration: scope.HostGeneration, Executable: scope.Executable, PID: physical.PID, Birth: physical.Birth}, OwnerID: owner, ExpectedAttemptSHA256: a.AttemptSHA256, OldFirstHost: physical, NewHost: hello, Epoch: epoch}
	return out, row, a, r, nil
}

func (d *DB) ResolvePreBindRetryAdmission(ctx context.Context, hello contract.HostHello, epoch uint64, executableSHA string) (PreBindRetryAdmission, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return PreBindRetryAdmission{}, err
	}
	defer tx.Rollback()
	a, _, _, _, err := ownerLaunchRetrySnapshot(ctx, tx, hello, epoch, executableSHA, nil)
	return a, err
}

func (d *DB) CommitPreBindRetryAdmission(ctx context.Context, admission PreBindRetryAdmission, oldFirstHostExited contract.OriginalOwnerTransport, key ed25519.PrivateKey) (contract.OwnerPrepared, error) {
	if admission.OldFirstHost != oldFirstHostExited || admission.Supervisor.Validate() != nil || admission.Supervisor.Epoch != admission.Epoch || admission.Supervisor.ExecutableSHA256 == "" {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	defer owned.close()
	pub, err := originalOwnerSigningPublic(key, admission.Prepare.CoordinatorKeyID)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	current, row, old, prior, err := ownerLaunchRetrySnapshot(ctx, owned.tx, admission.NewHost, admission.Epoch, admission.Supervisor.ExecutableSHA256, pub)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	if current.OwnerID != admission.OwnerID || current.ExpectedAttemptSHA256 != admission.ExpectedAttemptSHA256 || current.OldFirstHost != admission.OldFirstHost || current.Recovery != admission.Recovery || admission.Prepare.Host != admission.NewHost || admission.Prepare.Epoch != admission.Epoch || admission.Prepare.ExecutableSHA256 != admission.Supervisor.ExecutableSHA256 || admission.Prepare.CoordinatorKeyID != row.state.Intent.CoordinatorKeyID || admission.Prepare.BootstrapID == row.state.Intent.BootstrapID || admission.Prepare.RequestID == row.state.Intent.RequestID {
		return contract.OwnerPrepared{}, ErrConflict
	}
	effective := row.state.Intent
	effective.FirstHost = contract.OriginalOwnerTransport{PID: admission.NewHost.PID, Birth: admission.NewHost.Birth}
	effective.BootstrapID = admission.Prepare.BootstrapID
	effective.BootstrapTokenSHA256 = admission.Prepare.BootstrapTokenSHA256
	effective.RequestID = admission.Prepare.RequestID
	effective.RequestSHA256 = admission.Prepare.RequestSHA256
	effective.StartupDeadlineUnixMS = admission.Prepare.StartupDeadlineUnixMS
	effective.CoordinatorEpoch = admission.Epoch
	if effective.Validate() != nil {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	next, err := contract.FinalizeOriginalOwnerLaunchAttempt(contract.OriginalOwnerLaunchAttempt{Version: 2, Attempt: 2, BaseIntent: old.BaseIntent, EffectiveIntent: effective, PriorReceiptSHA256: prior.ReceiptSHA256, Supervisor: admission.Supervisor})
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	receipt, err := contract.SignOriginalOwnerLaunchReceipt(contract.OriginalOwnerLaunchReceipt{Version: 2, AttemptSHA256: next.AttemptSHA256, Phase: contract.OriginalOwnerLaunchPreparedNotStarted, Supervisor: admission.Supervisor}, key)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	body, err := originalOwnerBody(next)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	receiptBody, err := originalOwnerBody(receipt)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	now := originalOwnerNow()
	// Reserve this immutable attempt and every remaining receipt before a new
	// Start can be called.  The escrow is deliberately retained.
	escrow := int64(len(body) + len(receiptBody) + 4096 + 3*(contract.OriginalOwnerEnvelopeLimit+512))
	if err = d.storageAdmission(ctx, owned.tx, row.state.Intent.Scope.RunID, escrow, false); err != nil {
		return contract.OwnerPrepared{}, err
	}
	pubText, _ := supervisorPublic(key)
	if _, err = owned.tx.ExecContext(ctx, `INSERT INTO owner_launch_attempts(owner_id,run_id,attempt_no,attempt_hash,base_intent_json,attempt_json,supervisor_epoch,supervisor_pid,supervisor_birth,supervisor_nonce,supervisor_executable_hash,supervisor_public_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, admission.OwnerID, row.state.Intent.Scope.RunID, 2, next.AttemptSHA256, row.intentJSON, body, admission.Supervisor.Epoch, admission.Supervisor.PID, admission.Supervisor.Birth, admission.Supervisor.Nonce, admission.Supervisor.ExecutableSHA256, pubText, now); err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	if err = insertOwnerLaunchHead(ctx, owned.tx, admission.OwnerID, 2, receipt.Phase, receipt.ReceiptSHA256, now); err != nil {
		return contract.OwnerPrepared{}, err
	}
	var n int
	if err = owned.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_launch_receipts WHERE owner_id=?`, admission.OwnerID).Scan(&n); err != nil {
		return contract.OwnerPrepared{}, err
	}
	if _, err = owned.tx.ExecContext(ctx, `INSERT INTO owner_launch_receipts(owner_id,run_id,receipt_no,attempt_hash,phase,receipt_json,receipt_hash,created_at) VALUES(?,?,?,?,?,?,?,?)`, admission.OwnerID, row.state.Intent.Scope.RunID, n+1, next.AttemptSHA256, receipt.Phase, receiptBody, receipt.ReceiptSHA256, now); err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	if _, err = owned.tx.ExecContext(ctx, `INSERT INTO owner_launch_escrows(owner_id,attempt_no,run_id,reserved_bytes) VALUES(?,?,?,?)`, admission.OwnerID, 2, row.state.Intent.Scope.RunID, 3*(contract.OriginalOwnerEnvelopeLimit+512)); err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	if err = moveOwnerTransport(ctx, owned.tx, row.state.Intent.Scope, admission.OldFirstHost, effective.FirstHost, admission.Epoch); err != nil {
		return contract.OwnerPrepared{}, err
	}
	if err = owned.commit(); err != nil {
		return contract.OwnerPrepared{}, err
	}
	return contract.OwnerPrepared{Intent: effective, LaunchAttempt: &next}, nil
}

// PrepareSupervisedOriginalOwner creates the v2 intent, immutable attempt and
// its signed prepared receipt in one transaction. It never upgrades a legacy
// intended row: the caller must opt into protocol v2 before this call.
func (d *DB) PrepareSupervisedOriginalOwner(ctx context.Context, spec PrepareOriginalOwnerSpec, authorizationID string, supervisor contract.OriginalOwnerLaunchSupervisor, key ed25519.PrivateKey) (contract.OwnerPrepared, error) {
	if authorizationID != "" {
		return d.prepareSupervisedSuccessorOwner(ctx, spec, authorizationID, supervisor, key)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	defer owned.close()
	tx := owned.tx
	scope, host, err := originalOwnerRuntimeScope(ctx, tx, spec.Host, spec.Epoch, true)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	scope.ExecutableSHA256 = spec.ExecutableSHA256
	if supervisor.Validate() != nil || supervisor.Epoch != spec.Epoch || supervisor.ExecutableSHA256 != spec.ExecutableSHA256 {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT owner_id FROM execution_owners WHERE request_id=?`, spec.RequestID).Scan(&existing)
	if err == nil {
		a, r, e := loadOwnerLaunch(ctx, tx, existing)
		if e != nil {
			return contract.OwnerPrepared{}, e
		}
		if a.Supervisor != supervisor || a.BaseIntent.RequestSHA256 != spec.RequestSHA256 || r.Phase != contract.OriginalOwnerLaunchPreparedNotStarted {
			return contract.OwnerPrepared{}, ErrConflict
		}
		return contract.OwnerPrepared{Intent: a.EffectiveIntent, CoordinatorPublicKey: "", LaunchAttempt: &a}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return contract.OwnerPrepared{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_owners WHERE launch_id=?`, scope.LaunchID).Scan(&count); err != nil || count != 0 {
		return contract.OwnerPrepared{}, ErrConflict
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE host_id=?`, scope.ProducerID).Scan(&count); err != nil || count != 0 {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	scope.OwnerID, err = newID("execution-owner")
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	scope.Incarnation = 1
	i := contract.OriginalOwnerIntent{Scope: scope, BootstrapID: spec.BootstrapID, BootstrapTokenSHA256: spec.BootstrapTokenSHA256, CoordinatorKeyID: spec.CoordinatorKeyID, RequestID: spec.RequestID, RequestSHA256: spec.RequestSHA256, StartupDeadlineUnixMS: spec.StartupDeadlineUnixMS, FirstHost: host, CoordinatorEpoch: spec.Epoch}
	if i.Validate() != nil {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	a, err := contract.FinalizeOriginalOwnerLaunchAttempt(contract.OriginalOwnerLaunchAttempt{Version: 2, Attempt: 1, BaseIntent: i, EffectiveIntent: i, Supervisor: supervisor})
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	r, err := contract.SignOriginalOwnerLaunchReceipt(contract.OriginalOwnerLaunchReceipt{Version: 2, AttemptSHA256: a.AttemptSHA256, Phase: contract.OriginalOwnerLaunchPreparedNotStarted, Supervisor: supervisor}, key)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	if _, err = originalOwnerSigningPublic(key, i.CoordinatorKeyID); err != nil {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	body, err := originalOwnerBody(i)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	abody, err := originalOwnerBody(a)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	rbody, err := originalOwnerBody(r)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	now := originalOwnerNow()
	if err = d.storageAdmission(ctx, tx, scope.RunID, int64(len(body)+len(abody)+len(rbody)+512)+3*(contract.OriginalOwnerEnvelopeLimit+512), false); err != nil {
		return contract.OwnerPrepared{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_owners(owner_id,launch_id,run_id,producer_id,incarnation,bootstrap_id,bootstrap_token_hash,coordinator_key_id,request_id,request_hash,intent_json,phase,is_current,control_revision,coordinator_epoch,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'intended',1,0,?,?,?)`, scope.OwnerID, scope.LaunchID, scope.RunID, scope.ProducerID, scope.Incarnation, i.BootstrapID, i.BootstrapTokenSHA256, i.CoordinatorKeyID, i.RequestID, i.RequestSHA256, body, i.CoordinatorEpoch, now, now)
	if err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	pubText, _ := supervisorPublic(key)
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_launch_attempts(owner_id,run_id,attempt_no,attempt_hash,base_intent_json,attempt_json,supervisor_epoch,supervisor_pid,supervisor_birth,supervisor_nonce,supervisor_executable_hash,supervisor_public_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, scope.OwnerID, scope.RunID, 1, a.AttemptSHA256, body, abody, supervisor.Epoch, supervisor.PID, supervisor.Birth, supervisor.Nonce, supervisor.ExecutableSHA256, pubText, now)
	if err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	if err = insertOwnerLaunchHead(ctx, tx, scope.OwnerID, 1, r.Phase, r.ReceiptSHA256, now); err != nil {
		return contract.OwnerPrepared{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_launch_receipts(owner_id,run_id,receipt_no,attempt_hash,phase,receipt_json,receipt_hash,created_at) VALUES(?,?,?,?,?,?,?,?)`, scope.OwnerID, scope.RunID, 1, a.AttemptSHA256, r.Phase, rbody, r.ReceiptSHA256, now)
	if err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owner_launch_escrows(owner_id,attempt_no,run_id,reserved_bytes) VALUES(?,?,?,?)`, scope.OwnerID, 1, scope.RunID, 3*(contract.OriginalOwnerEnvelopeLimit+512)); err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	if err = owned.commit(); err != nil {
		return contract.OwnerPrepared{}, err
	}
	return contract.OwnerPrepared{Intent: i, LaunchAttempt: &a}, nil
}

func (d *DB) prepareSupervisedSuccessorOwner(ctx context.Context, spec PrepareOriginalOwnerSpec, authorizationID string, supervisor contract.OriginalOwnerLaunchSupervisor, key ed25519.PrivateKey) (contract.OwnerPrepared, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "authorization", authorizationID, false)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	defer owned.close()
	tx := owned.tx
	p, err := successorOwnerPreviewTx(ctx, tx, spec.Host, spec.Epoch, spec.ExecutableSHA256, authorizationID)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	if supervisor.Validate() != nil || supervisor.Epoch != spec.Epoch || supervisor.ExecutableSHA256 != spec.ExecutableSHA256 {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	if p.current != nil {
		a, r, e := loadOwnerLaunch(ctx, tx, p.current.state.Intent.Scope.OwnerID)
		if e != nil || a.Supervisor != supervisor || r.Phase != contract.OriginalOwnerLaunchPreparedNotStarted {
			return contract.OwnerPrepared{}, ErrConflict
		}
		return contract.OwnerPrepared{Intent: a.EffectiveIntent, LaunchAttempt: &a}, nil
	}
	s := p.scope
	s.OwnerID, err = newID("execution-owner")
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	now := originalOwnerNow()
	i := contract.OriginalOwnerIntent{Scope: s, BootstrapID: spec.BootstrapID, BootstrapTokenSHA256: spec.BootstrapTokenSHA256, CoordinatorKeyID: spec.CoordinatorKeyID, RequestID: spec.RequestID, RequestSHA256: spec.RequestSHA256, StartupDeadlineUnixMS: spec.StartupDeadlineUnixMS, FirstHost: contract.OriginalOwnerTransport{PID: spec.Host.PID, Birth: spec.Host.Birth}, CoordinatorEpoch: spec.Epoch}
	if i.Validate() != nil {
		return contract.OwnerPrepared{}, originalOwnerRejected()
	}
	a, err := contract.FinalizeOriginalOwnerLaunchAttempt(contract.OriginalOwnerLaunchAttempt{Version: 2, Attempt: 1, BaseIntent: i, EffectiveIntent: i, Supervisor: supervisor})
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	r, err := contract.SignOriginalOwnerLaunchReceipt(contract.OriginalOwnerLaunchReceipt{Version: 2, AttemptSHA256: a.AttemptSHA256, Phase: contract.OriginalOwnerLaunchPreparedNotStarted, Supervisor: supervisor}, key)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	if _, err = originalOwnerSigningPublic(key, i.CoordinatorKeyID); err != nil {
		return contract.OwnerPrepared{}, err
	}
	body, _ := originalOwnerBody(i)
	abody, _ := originalOwnerBody(a)
	rbody, _ := originalOwnerBody(r)
	if err = d.storageAdmission(ctx, tx, s.RunID, int64(len(body)+len(abody)+len(rbody)+512)+3*(contract.OriginalOwnerEnvelopeLimit+512), false); err != nil {
		return contract.OwnerPrepared{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_owners(owner_id,launch_id,run_id,producer_id,incarnation,bootstrap_id,bootstrap_token_hash,coordinator_key_id,request_id,request_hash,intent_json,phase,is_current,control_revision,coordinator_epoch,created_at,updated_at,succession_authorization_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,'intended',1,0,?,?,?,?)`, s.OwnerID, s.LaunchID, s.RunID, s.ProducerID, s.Incarnation, i.BootstrapID, i.BootstrapTokenSHA256, i.CoordinatorKeyID, i.RequestID, i.RequestSHA256, body, i.CoordinatorEpoch, now, now, authorizationID)
	if err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	pubText, _ := supervisorPublic(key)
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_launch_attempts(owner_id,run_id,attempt_no,attempt_hash,base_intent_json,attempt_json,supervisor_epoch,supervisor_pid,supervisor_birth,supervisor_nonce,supervisor_executable_hash,supervisor_public_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.OwnerID, s.RunID, 1, a.AttemptSHA256, body, abody, supervisor.Epoch, supervisor.PID, supervisor.Birth, supervisor.Nonce, supervisor.ExecutableSHA256, pubText, now)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	if err = insertOwnerLaunchHead(ctx, tx, s.OwnerID, 1, r.Phase, r.ReceiptSHA256, now); err != nil {
		return contract.OwnerPrepared{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_launch_receipts(owner_id,run_id,receipt_no,attempt_hash,phase,receipt_json,receipt_hash,created_at) VALUES(?,?,?,?,?,?,?,?)`, s.OwnerID, s.RunID, 1, a.AttemptSHA256, r.Phase, rbody, r.ReceiptSHA256, now)
	if err != nil {
		return contract.OwnerPrepared{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owner_launch_escrows(owner_id,attempt_no,run_id,reserved_bytes) VALUES(?,?,?,?)`, s.OwnerID, 1, s.RunID, 3*(contract.OriginalOwnerEnvelopeLimit+512)); err != nil {
		return contract.OwnerPrepared{}, mapConflict(err)
	}
	if err = moveOwnerTransport(ctx, tx, s, p.authorization.Host, i.FirstHost, spec.Epoch); err != nil {
		return contract.OwnerPrepared{}, err
	}
	if err = owned.commit(); err != nil {
		return contract.OwnerPrepared{}, err
	}
	return contract.OwnerPrepared{Intent: i, LaunchAttempt: &a}, nil
}

func loadOwnerLaunch(ctx context.Context, tx *sql.Tx, ownerID string) (contract.OriginalOwnerLaunchAttempt, contract.OriginalOwnerLaunchReceipt, error) {
	var body, base, ownerBase, hash, receiptBody, receiptHash, publicText string
	err := tx.QueryRowContext(ctx, `SELECT a.attempt_json,a.base_intent_json,o.intent_json,a.attempt_hash,(SELECT receipt_json FROM owner_launch_receipts r WHERE r.owner_id=a.owner_id AND r.attempt_hash=a.attempt_hash ORDER BY receipt_no DESC LIMIT 1),h.latest_receipt_hash,a.supervisor_public_key FROM owner_launch_heads h JOIN owner_launch_attempts a ON a.owner_id=h.owner_id AND a.attempt_no=h.attempt_no JOIN execution_owners o ON o.owner_id=a.owner_id WHERE a.owner_id=?`, ownerID).Scan(&body, &base, &ownerBase, &hash, &receiptBody, &receiptHash, &publicText)
	if errors.Is(err, sql.ErrNoRows) {
		var facts int
		if e := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM owner_launch_attempts WHERE owner_id=?)+(SELECT COUNT(*) FROM owner_launch_heads WHERE owner_id=?)+(SELECT COUNT(*) FROM owner_launch_receipts WHERE owner_id=?)+(SELECT COUNT(*) FROM owner_launch_escrows WHERE owner_id=?)`, ownerID, ownerID, ownerID, ownerID).Scan(&facts); e != nil {
			return contract.OriginalOwnerLaunchAttempt{}, contract.OriginalOwnerLaunchReceipt{}, e
		}
		if facts != 0 {
			return contract.OriginalOwnerLaunchAttempt{}, contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
		}
		return contract.OriginalOwnerLaunchAttempt{}, contract.OriginalOwnerLaunchReceipt{}, ErrNotFound
	}
	if err != nil {
		return contract.OriginalOwnerLaunchAttempt{}, contract.OriginalOwnerLaunchReceipt{}, err
	}
	var a contract.OriginalOwnerLaunchAttempt
	if contract.DecodeOwnerRuntimeStrict([]byte(body), &a) != nil || a.Validate() != nil || a.AttemptSHA256 != hash {
		return a, contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	baseBody, _ := originalOwnerBody(a.BaseIntent)
	if baseBody != base || base != ownerBase {
		return a, contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	var r contract.OriginalOwnerLaunchReceipt
	pub, decodeErr := hex.DecodeString(publicText)
	if decodeErr != nil || len(pub) != ed25519.PublicKeySize {
		return a, contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	key := ed25519.PublicKey(pub)
	id, idErr := contract.OriginalOwnerKeyID(key)
	if idErr != nil || id != a.BaseIntent.CoordinatorKeyID || contract.DecodeOwnerRuntimeStrict([]byte(receiptBody), &r) != nil || r.Validate() != nil || r.ReceiptSHA256 != receiptHash || contract.VerifyOriginalOwnerLaunchReceipt(r, key, a) != nil {
		return a, r, originalOwnerRejected()
	}
	rows, e := tx.QueryContext(ctx, `SELECT receipt_json,receipt_hash FROM owner_launch_receipts WHERE owner_id=? AND attempt_hash=? ORDER BY receipt_no`, ownerID, a.AttemptSHA256)
	if e != nil {
		return a, r, e
	}
	defer rows.Close()
	prior := ""
	for rows.Next() {
		var raw, got string
		if e = rows.Scan(&raw, &got); e != nil {
			return a, r, e
		}
		var each contract.OriginalOwnerLaunchReceipt
		if contract.DecodeOwnerRuntimeStrict([]byte(raw), &each) != nil || each.ReceiptSHA256 != got || each.PriorReceiptSHA256 != prior || contract.VerifyOriginalOwnerLaunchReceipt(each, key, a) != nil {
			return a, r, originalOwnerRejected()
		}
		prior = got
	}
	if e = rows.Err(); e != nil || prior != receiptHash {
		if e != nil {
			return a, r, e
		}
		return a, r, originalOwnerRejected()
	}
	return a, r, nil
}

func (d *DB) InspectOriginalOwnerLaunch(ctx context.Context, ownerID string) (OriginalOwnerState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	defer tx.Rollback()
	row, err := loadOriginalOwner(ctx, tx, ownerID)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	a, r, err := loadOwnerLaunch(ctx, tx, ownerID)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	row.state.BaseIntent = &a.BaseIntent
	row.state.Intent = a.EffectiveIntent
	row.state.LaunchAttempt = &a
	row.state.LaunchReceipt = &r
	return row.state, nil
}

func (d *DB) recordOwnerLaunch(ctx context.Context, ownerID, expected string, supervisor contract.OriginalOwnerLaunchSupervisor, phase string, child *contract.OriginalOwnerTransport, handoff bool, exit OriginalOwnerLaunchExit, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, err
	}
	defer owned.close()
	row, err := loadOriginalOwner(ctx, owned.tx, ownerID)
	if err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, err
	}
	a, prior, err := loadOwnerLaunch(ctx, owned.tx, ownerID)
	if err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, err
	}
	if expected == "" || expected != a.AttemptSHA256 || supervisor != a.Supervisor || (phase != contract.OriginalOwnerLaunchWaited && row.state.Phase != "intended") || (phase == contract.OriginalOwnerLaunchWaited && row.state.Phase != "intended" && row.state.Phase != "bound" && row.state.Phase != "retired") {
		return contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	if _, err := originalOwnerSigningPublic(key, a.EffectiveIntent.CoordinatorKeyID); err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	allowed := map[string]string{contract.OriginalOwnerLaunchStarting: contract.OriginalOwnerLaunchPreparedNotStarted, contract.OriginalOwnerLaunchStartedWaitOwned: contract.OriginalOwnerLaunchStarting, contract.OriginalOwnerLaunchWaited: contract.OriginalOwnerLaunchStartedWaitOwned, contract.OriginalOwnerLaunchNotStartedFinal: contract.OriginalOwnerLaunchStarting}
	if allowed[phase] != prior.Phase {
		return contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	r, err := contract.SignOriginalOwnerLaunchReceipt(contract.OriginalOwnerLaunchReceipt{Version: 2, AttemptSHA256: a.AttemptSHA256, Phase: phase, Supervisor: supervisor, Child: child, HandoffClosed: handoff, ExitCode: exit.Code, ExitSignal: exit.Signal, PriorReceiptSHA256: prior.ReceiptSHA256}, key)
	if err != nil {
		return r, err
	}
	body, err := originalOwnerBody(r)
	if err != nil {
		return r, err
	}
	now := originalOwnerNow()
	var n int
	if err = owned.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_launch_receipts WHERE owner_id=?`, ownerID).Scan(&n); err != nil {
		return r, err
	}
	result, err := owned.tx.ExecContext(ctx, `UPDATE owner_launch_heads SET phase=?,latest_receipt_hash=?,updated_at=? WHERE owner_id=? AND attempt_no=(SELECT attempt_no FROM owner_launch_attempts WHERE owner_id=? AND attempt_hash=?) AND phase=?`, phase, r.ReceiptSHA256, now, ownerID, ownerID, expected, prior.Phase)
	if err != nil {
		return r, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return r, ErrConflict
	}
	if _, err = owned.tx.ExecContext(ctx, `INSERT INTO owner_launch_receipts(owner_id,run_id,receipt_no,attempt_hash,phase,receipt_json,receipt_hash,created_at) VALUES(?,?,?,?,?,?,?,?)`, ownerID, row.state.Intent.Scope.RunID, n+1, expected, phase, body, r.ReceiptSHA256, now); err != nil {
		return r, mapConflict(err)
	}
	if err = owned.commit(); err != nil {
		return r, err
	}
	return r, nil
}
func (d *DB) RecordOriginalOwnerLaunchStartIntent(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchStarting, nil, false, OriginalOwnerLaunchExit{}, key)
}
func (d *DB) RecordOriginalOwnerLaunchStarted(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, child contract.OriginalOwnerTransport, handoff bool, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchStartedWaitOwned, &child, handoff, OriginalOwnerLaunchExit{}, key)
}
func (d *DB) RecordOriginalOwnerLaunchWait(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, child contract.OriginalOwnerTransport, exit OriginalOwnerLaunchExit, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchWaited, &child, true, exit, key)
}
func (d *DB) RecordOriginalOwnerLaunchNoStart(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchNotStartedFinal, nil, false, OriginalOwnerLaunchExit{}, key)
}
