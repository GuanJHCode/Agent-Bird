package store

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type OriginalOwnerResumeRequest struct {
	TaskID           string `json:"task_id"`
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	WorkRevision     int    `json:"work_revision"`
	CommandID        string `json:"command_id"`
}

// Coordinator-only kernel observations. The caller must hold registration
// serialization and the verified predecessor recovery-file lock through commit.
// No public request decoder may produce these facts. An exit tuple is not a
// child Wait receipt and does not authorize clearing a pending recovery start.
type OriginalOwnerSuccessionObservation struct {
	OwnerID          string                          `json:"-"`
	Owner            contract.OriginalOwnerTransport `json:"-"`
	Host             contract.OriginalOwnerTransport `json:"-"`
	Origin           RunOwnerBinding                 `json:"-"`
	Epoch            uint64                          `json:"-"`
	ExecutableSHA256 string                          `json:"-"`
	OwnerExitedAt    string                          `json:"-"`
	HostExitedAt     string                          `json:"-"`
}

type OriginalOwnerSuccessionAuthorization struct {
	AuthorizationID            string                          `json:"authorization_id"`
	CommandID                  string                          `json:"command_id"`
	RequestSHA256              string                          `json:"request_sha256"`
	RunID                      string                          `json:"run_id"`
	TaskID                     string                          `json:"task_id"`
	WorkRevision               int                             `json:"work_revision"`
	LaunchID                   string                          `json:"launch_id"`
	PredecessorOwnerID         string                          `json:"predecessor_owner_id"`
	PredecessorIncarnation     uint64                          `json:"predecessor_incarnation"`
	PredecessorControlRevision uint64                          `json:"predecessor_control_revision"`
	PredecessorScopeSHA256     string                          `json:"predecessor_scope_sha256"`
	Owner                      contract.OriginalOwnerTransport `json:"owner"`
	OwnerExitedAt              string                          `json:"owner_exited_at"`
	Host                       contract.OriginalOwnerTransport `json:"host"`
	HostExitedAt               string                          `json:"host_exited_at"`
	SuccessorOrigin            RunOwnerBinding                 `json:"successor_origin"`
	SuccessorHostGeneration    string                          `json:"successor_host_generation"`
	SuccessorExecutable        string                          `json:"successor_executable"`
	SuccessorExecutableSHA256  string                          `json:"successor_executable_sha256"`
	CoordinatorEpoch           uint64                          `json:"coordinator_epoch"`
	ReadySequence              int64                           `json:"ready_sequence"`
	CreatedAt                  string                          `json:"created_at"`
}

func originalOwnerResumeHash(r OriginalOwnerResumeRequest) (string, error) {
	if r.TaskID == "" || r.ControllerThread == "" || r.ControlToken == "" || r.WorkRevision < 1 || r.CommandID == "" || len(r.CommandID) > 128 || !utf8.ValidString(r.CommandID) || strings.ContainsRune(r.CommandID, 0) || strings.TrimSpace(r.CommandID) != r.CommandID {
		return "", CodeError("owner_resume_invalid")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return originalOwnerHash(append([]byte("original-owner-resume-v1\x00"), body...)), nil
}

func authenticateOwnerResumeTx(ctx context.Context, tx *sql.Tx, r OriginalOwnerResumeRequest) error {
	var controller, hash string
	err := tx.QueryRowContext(ctx, `SELECT r.controller_thread,c.token_hash FROM runs r JOIN tasks t ON t.run_id=r.id JOIN run_control c ON c.run_id=r.id WHERE t.id=?`, r.TaskID).Scan(&controller, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if controller != r.ControllerThread || subtle.ConstantTimeCompare([]byte(hash), []byte(runtimeHash(r.ControlToken))) != 1 {
		return CodeError("owner_mismatch")
	}
	return nil
}

func successionObservationTime(value string) bool {
	v, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !v.IsZero() && v.UTC().Format(time.RFC3339Nano) == value
}

// ResumeOriginalOwner retires only a proved-dead, fully applied bound owner.
// A stable command is checked before mutable admission gates, so a lost reply
// cannot consume another ready sequence, authorization or execution allowance.
func (d *DB) ResumeOriginalOwner(ctx context.Context, request OriginalOwnerResumeRequest, observed OriginalOwnerSuccessionObservation, key ed25519.PublicKey) (OriginalOwnerSuccessionAuthorization, error) {
	return d.resumeOriginalOwner(ctx, request, observed, key, func() (string, string, error) {
		id, err := newID("owner-succession")
		return id, originalOwnerNow(), err
	})
}

// The private allocator permits deterministic quota-boundary tests without
// global mutable clocks, environment switches or a public caller-chosen ID.
func (d *DB) resumeOriginalOwner(ctx context.Context, request OriginalOwnerResumeRequest, observed OriginalOwnerSuccessionObservation, key ed25519.PublicKey, allocate func() (string, string, error)) (OriginalOwnerSuccessionAuthorization, error) {
	var zero OriginalOwnerSuccessionAuthorization
	hash, err := originalOwnerResumeHash(request)
	if err != nil {
		return zero, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Keep the physical lease until commit/rollback is resolved. A failed
	// SQLite COMMIT can leave its transaction active after sql.Tx is done.
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	if err = authenticateOwnerResumeTx(ctx, tx, request); err != nil {
		return zero, err
	}
	prior, err := loadOwnerSuccessionCommand(ctx, tx, request.CommandID)
	if err == nil {
		if prior.RequestSHA256 != hash {
			return zero, ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return zero, err
	}
	if len(key) != ed25519.PublicKeySize || observed.OwnerID == "" || observed.Owner.Validate() != nil || observed.Host.Validate() != nil || !successionObservationTime(observed.OwnerExitedAt) || !successionObservationTime(observed.HostExitedAt) {
		return zero, originalOwnerRejected()
	}
	row, err := loadOriginalOwner(ctx, tx, observed.OwnerID)
	if err != nil {
		return zero, err
	}
	i := row.state.Intent
	if !row.state.IsCurrent || row.state.Phase != "bound" || row.state.Binding == nil || row.state.CurrentHost == nil || row.state.AppliedControlRevision != row.state.ControlRevision || observed.Owner != (contract.OriginalOwnerTransport{PID: row.owner.PID, Birth: row.owner.Birth}) || observed.Host != *row.state.CurrentHost || observed.ExecutableSHA256 != i.Scope.ExecutableSHA256 || contract.VerifyOriginalOwnerBinding(*row.state.Binding, key) != nil {
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
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(i.Scope)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(row.owner)
	if permit.OwnerID != observed.OwnerID || permit.ControlRevision != row.state.ControlRevision || permit.ScopeSHA256 != scopeHash || permit.OwnerIdentitySHA256 != identityHash || permit.NewHost != observed.Host || permit.CoordinatorEpoch != row.state.CoordinatorEpoch || permit.CoordinatorKeyID != i.CoordinatorKeyID {
		return zero, originalOwnerRejected()
	}
	a, err := ownerSuccessionTarget(ctx, tx, request, observed, row)
	if err != nil {
		return zero, err
	}
	if err = ownerSuccessionQuiescent(ctx, tx, a.RunID, a.LaunchID, i.Scope.ProducerID); err != nil {
		return zero, err
	}
	a.AuthorizationID, a.CreatedAt, err = allocate()
	if err != nil {
		return zero, err
	}
	a.CommandID, a.RequestSHA256 = request.CommandID, hash
	a.PredecessorOwnerID, a.PredecessorIncarnation, a.PredecessorControlRevision, a.PredecessorScopeSHA256 = observed.OwnerID, i.Scope.Incarnation, row.state.ControlRevision, scopeHash
	a.Owner, a.OwnerExitedAt, a.Host, a.HostExitedAt = observed.Owner, observed.OwnerExitedAt, observed.Host, observed.HostExitedAt
	oldBytes := originalOwnerRowBytes(row)
	row.state.Phase, row.state.IsCurrent, row.updated = "retired", false, a.CreatedAt
	incoming := ownerSuccessionAuthorizationBytes(a) + max(int64(0), originalOwnerRowBytes(row)-oldBytes)
	if err = d.storageAdmission(ctx, tx, a.RunID, incoming, false); err != nil {
		return zero, err
	}
	a.ReadySequence, err = d.queueResumeTx(ctx, tx, request.TaskID, request.WorkRevision)
	if err != nil {
		return zero, err
	}
	if err = insertOwnerSuccessionAuthorization(ctx, tx, a); err != nil {
		return zero, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE execution_owners SET phase='retired',is_current=0,updated_at=? WHERE owner_id=? AND is_current=1 AND phase='bound' AND control_revision=? AND applied_control_revision=control_revision AND current_host_pid=? AND current_host_birth=?`, a.CreatedAt, observed.OwnerID, row.state.ControlRevision, observed.Host.PID, observed.Host.Birth)
	if err = ownerOneRow(r, err); err != nil {
		return zero, err
	}
	r, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='offline' WHERE id=? AND launch_id=? AND pid=? AND birth=?`, i.Scope.ProducerID, a.LaunchID, observed.Host.PID, observed.Host.Birth)
	if err = ownerOneRow(r, err); err != nil {
		return zero, err
	}
	r, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='offline' WHERE id=? AND host_id=? AND execution_owner_mode='supervised-v1' AND status!='released'`, a.LaunchID, i.Scope.ProducerID)
	if err = ownerOneRow(r, err); err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return a, nil
}

func ownerSuccessionTarget(ctx context.Context, tx *sql.Tx, request OriginalOwnerResumeRequest, observed OriginalOwnerSuccessionObservation, row originalOwnerRow) (OriginalOwnerSuccessionAuthorization, error) {
	var a OriginalOwnerSuccessionAuthorization
	var producer, mode, status, hostOrigin, hostGeneration, hostExecutable string
	var host contract.OriginalOwnerTransport
	err := tx.QueryRowContext(ctx, `SELECT t.run_id,tr.work_revision,l.id,h.id,l.execution_owner_mode,l.status,r.origin_context_id,r.origin_pid,r.origin_birth,l.host_generation,l.executable,h.pid,h.birth,h.origin_context_id,h.host_generation,h.executable,(SELECT epoch FROM coordinator WHERE id=1)
 FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN runs r ON r.id=t.run_id JOIN host_launches l ON l.id=tr.host_launch_id JOIN runtime_hosts h ON h.id=l.host_id AND h.launch_id=l.id WHERE t.id=? AND l.origin_context_id=r.origin_context_id`, request.TaskID).Scan(&a.RunID, &a.WorkRevision, &a.LaunchID, &producer, &mode, &status, &a.SuccessorOrigin.OriginContextID, &a.SuccessorOrigin.OriginPID, &a.SuccessorOrigin.OriginBirth, &a.SuccessorHostGeneration, &a.SuccessorExecutable, &host.PID, &host.Birth, &hostOrigin, &hostGeneration, &hostExecutable, &a.CoordinatorEpoch)
	s := row.state.Intent.Scope
	if err != nil {
		return a, err
	}
	if a.WorkRevision != request.WorkRevision || a.RunID != s.RunID || a.LaunchID != s.LaunchID || producer != s.ProducerID || mode != "supervised-v1" || status == "released" || host != observed.Host || a.SuccessorOrigin != observed.Origin || observed.Epoch == 0 || a.CoordinatorEpoch != observed.Epoch || a.CoordinatorEpoch < row.state.CoordinatorEpoch || a.SuccessorExecutable != s.Executable || a.SuccessorOrigin.OriginContextID != s.OriginalOriginContextID || hostOrigin != a.SuccessorOrigin.OriginContextID || hostGeneration != a.SuccessorHostGeneration || hostExecutable != a.SuccessorExecutable {
		return a, originalOwnerRejected()
	}
	if a.SuccessorHostGeneration == s.HostGeneration {
		if a.SuccessorOrigin.OriginPID != s.OriginalOriginPID || a.SuccessorOrigin.OriginBirth != s.OriginalOriginBirth {
			return a, originalOwnerRejected()
		}
	} else {
		if err := ownerSuccessionRebindChain(ctx, tx, a.RunID, a.SuccessorHostGeneration, s.HostGeneration, a.SuccessorOrigin); err != nil {
			return a, originalOwnerRejected()
		}
	}
	a.TaskID, a.SuccessorExecutableSHA256 = request.TaskID, observed.ExecutableSHA256
	return a, nil
}

func ownerSuccessionRebindChain(ctx context.Context, tx *sql.Tx, run, current, predecessor string, origin RunOwnerBinding) error {
	seen := map[string]bool{current: true}
	for hop := 0; hop < 64; hop++ {
		var old, birth, proof string
		var pid int
		err := tx.QueryRowContext(ctx, `SELECT old_host_generation,origin_pid,origin_birth,attachment_proof_sha256 FROM owner_rebinds WHERE run_id=? AND host_generation=?`, run, current).Scan(&old, &pid, &birth, &proof)
		if err != nil {
			return originalOwnerRejected()
		}
		if old == "" || seen[old] || pid <= 1 || birth == "" || !validDigest(proof) || (hop == 0 && (pid != origin.OriginPID || birth != origin.OriginBirth)) {
			return originalOwnerRejected()
		}
		if old == predecessor {
			return nil
		}
		seen[old], current = true, old
	}
	return CodeError("owner_succession_chain_limit")
}

func ownerSuccessionQuiescent(ctx context.Context, tx *sql.Tx, run, launch, producer string) error {
	return ownerSuccessionQuiescentExceptQueued(ctx, tx, run, launch, producer, "")
}

func ownerSuccessionQuiescentExceptQueued(ctx context.Context, tx *sql.Tx, run, launch, producer, queuedTask string) error {
	var unsafe int
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status NOT IN ('exited','not_started')) +
 (SELECT COUNT(*) FROM tasks WHERE run_id=? AND status IN ('ready','resume_queued','running','stopping','waiting_question') AND NOT(id=? AND status='resume_queued')) +
 (SELECT COUNT(*) FROM stop_runtime WHERE host_id=? AND status='pending') +
 (SELECT COUNT(*) FROM runtime_questions q JOIN tasks t ON t.id=q.task_id WHERE t.run_id=? AND q.status IN ('open','answer_committed','resume_queued','resume_started')) +
 (SELECT COUNT(*) FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE t.run_id=? AND tr.host_launch_id!=?)`, run, run, queuedTask, producer, run, run, launch).Scan(&unsafe)
	if err != nil {
		return err
	}
	if unsafe != 0 {
		return CodeError("owner_succession_not_quiescent")
	}
	return nil
}
