package store

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"database/sql"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// These inputs are trusted in-process observations, not public JSON assertions.
// B2 must obtain them under registration serialization from the original peer.
type OriginalOwnerAuthentication struct {
	Owner            contract.OriginalOwnerIdentity
	OriginPID        int
	OriginBirth      string
	ExecutableSHA256 string
}

type PrepareOriginalOwnerSpec struct {
	Host                  contract.HostHello
	Epoch                 uint64
	ExecutableSHA256      string
	BootstrapID           string
	BootstrapTokenSHA256  string
	CoordinatorKeyID      string
	RequestID             string
	RequestSHA256         string
	StartupDeadlineUnixMS int64
}

type BindOriginalOwnerSpec struct {
	Hello         contract.OriginalOwnerHello
	Host          contract.HostHello
	Epoch         uint64
	Authenticated OriginalOwnerAuthentication
}

type OriginalOwnerState struct {
	Intent                    contract.OriginalOwnerIntent
	SuccessionAuthorizationID string
	Phase                     string
	IsCurrent                 bool
	ControlRevision           uint64
	AppliedControlRevision    uint64
	CurrentHost               *contract.OriginalOwnerTransport
	CoordinatorEpoch          uint64
	Binding                   *contract.OriginalOwnerBinding
}

type OriginalOwnerBindResult struct {
	Binding contract.OriginalOwnerBinding
	Permit  contract.OwnerControlPermit
}

type OriginalOwnerAttachSpec struct {
	OwnerID                 string
	ScopeSHA256             string
	OwnerIdentitySHA256     string
	CoordinatorKeyID        string
	ExpectedControlRevision uint64
	OldHost                 contract.OriginalOwnerTransport
	NewHost                 contract.OriginalOwnerTransport
	Epoch                   uint64
	RequestID               string
	RequestSHA256           string
	Authentication          OriginalOwnerAttachAuthentication `json:"-"`
}

// These actual observations are separate from the immutable request/permit.
// No public decoder or handler produces them in B1.
type OriginalOwnerAttachAuthentication struct {
	Owner OriginalOwnerAuthentication
	Host  contract.HostHello
	Epoch uint64
	// Exact positive old-peer observation; nil means no death proof.
	ExitedHost *contract.OriginalOwnerTransport
}

func (d *DB) CommitOriginalOwnerAttach(ctx context.Context, spec OriginalOwnerAttachSpec, key ed25519.PrivateKey) (contract.OwnerControlPermit, error) {
	prototype := contract.OwnerControlPermit{Version: 1, Kind: "attach", OwnerID: spec.OwnerID, ScopeSHA256: spec.ScopeSHA256, OwnerIdentitySHA256: spec.OwnerIdentitySHA256, CoordinatorKeyID: spec.CoordinatorKeyID, CoordinatorEpoch: spec.Epoch, ExpectedControlRevision: spec.ExpectedControlRevision, ControlRevision: spec.ExpectedControlRevision + 1, OldHost: &spec.OldHost, NewHost: spec.NewHost, RequestID: spec.RequestID, RequestSHA256: spec.RequestSHA256, PermitID: "validation-only"}
	if prototype.Validate() != nil || spec.ExpectedControlRevision == 0 {
		return contract.OwnerControlPermit{}, originalOwnerRejected()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.OwnerControlPermit{}, err
	}
	defer tx.Rollback()
	row, err := loadOriginalOwner(ctx, tx, spec.OwnerID)
	if err != nil {
		return contract.OwnerControlPermit{}, err
	}
	i := row.state.Intent
	if !row.state.IsCurrent || row.state.Phase != "bound" || row.state.CurrentHost == nil || !originalOwnerMatchesAuthentication(i.Scope, row.owner, spec.Authentication.Owner) {
		return contract.OwnerControlPermit{}, originalOwnerRejected()
	}
	scope, runtimeHost, err := originalOwnerRuntimeScope(ctx, tx, spec.Authentication.Host, spec.Authentication.Epoch, false)
	if err != nil || !originalOwnerSameScope(scope, i.Scope) {
		return contract.OwnerControlPermit{}, originalOwnerRejected()
	}
	authHost := contract.OriginalOwnerTransport{PID: spec.Authentication.Host.PID, Birth: spec.Authentication.Host.Birth}
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(i.Scope)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(row.owner)
	if spec.ScopeSHA256 != scopeHash || spec.OwnerIdentitySHA256 != identityHash || spec.CoordinatorKeyID != i.CoordinatorKeyID || authHost != spec.NewHost {
		return contract.OwnerControlPermit{}, originalOwnerRejected()
	}
	pub, err := originalOwnerSigningPublic(key, i.CoordinatorKeyID)
	if err != nil || contract.VerifyOriginalOwnerBinding(*row.state.Binding, pub) != nil {
		return contract.OwnerControlPermit{}, originalOwnerRejected()
	}
	if err = requireOwnerRecoveryHandoffAppliedTx(ctx, tx, row); err != nil {
		return contract.OwnerControlPermit{}, err
	}
	replay, err := loadOriginalOwnerPermit(ctx, tx, spec.RequestID, pub)
	if err == nil {
		if replay.OwnerID != spec.OwnerID || replay.ScopeSHA256 != spec.ScopeSHA256 || replay.OwnerIdentitySHA256 != spec.OwnerIdentitySHA256 || replay.CoordinatorKeyID != spec.CoordinatorKeyID || replay.CoordinatorEpoch != spec.Epoch || replay.ExpectedControlRevision != spec.ExpectedControlRevision || replay.RequestSHA256 != spec.RequestSHA256 || replay.OldHost == nil || *replay.OldHost != spec.OldHost || replay.NewHost != spec.NewHost || *row.state.CurrentHost != authHost {
			return contract.OwnerControlPermit{}, ErrConflict
		}
		// Historical committed state only: no revision/epoch/lease mutation.
		return replay, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return contract.OwnerControlPermit{}, err
	}
	if row.state.ControlRevision != spec.ExpectedControlRevision || *row.state.CurrentHost != spec.OldHost || runtimeHost != spec.OldHost || spec.Epoch != spec.Authentication.Epoch || spec.Epoch < row.state.CoordinatorEpoch || spec.Epoch < i.CoordinatorEpoch {
		return contract.OwnerControlPermit{}, ErrConflict
	}
	if spec.OldHost == spec.NewHost {
		if spec.Epoch == row.state.CoordinatorEpoch {
			return contract.OwnerControlPermit{}, ErrConflict
		}
	} else if spec.Authentication.ExitedHost == nil || *spec.Authentication.ExitedHost != spec.OldHost {
		return contract.OwnerControlPermit{}, originalOwnerRejected()
	}
	id, err := newID("owner-permit")
	if err != nil {
		return contract.OwnerControlPermit{}, err
	}
	prototype.PermitID = id
	p, err := contract.SignOwnerControlPermit(prototype, key)
	if err != nil {
		return contract.OwnerControlPermit{}, err
	}
	body, err := originalOwnerBody(p)
	if err != nil {
		return contract.OwnerControlPermit{}, err
	}
	hash, now := originalOwnerHash([]byte(body)), originalOwnerNow()
	oldBytes := originalOwnerRowBytes(row)
	row.state.CurrentHost = &spec.NewHost
	row.updated = now
	growth := originalOwnerRowBytes(row) - oldBytes
	if growth < 0 {
		growth = 0
	}
	growth += originalOwnerPermitBytes(i, p, body, hash, now)
	if err = d.storageAdmission(ctx, tx, i.Scope.RunID, growth, false); err != nil {
		return contract.OwnerControlPermit{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_owners SET control_revision=?,current_host_pid=?,current_host_birth=?,coordinator_epoch=?,updated_at=? WHERE owner_id=? AND phase='bound' AND is_current=1 AND control_revision=? AND current_host_pid=? AND current_host_birth=?`, p.ControlRevision, spec.NewHost.PID, spec.NewHost.Birth, spec.Epoch, now, spec.OwnerID, spec.ExpectedControlRevision, spec.OldHost.PID, spec.OldHost.Birth)
	if err != nil {
		return contract.OwnerControlPermit{}, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return contract.OwnerControlPermit{}, ErrConflict
	}
	if err = moveOwnerTransport(ctx, tx, i.Scope, spec.OldHost, spec.NewHost, spec.Epoch); err != nil {
		return contract.OwnerControlPermit{}, err
	}
	if err = insertOriginalOwnerPermit(ctx, tx, i, p, body, hash, now); err != nil {
		return contract.OwnerControlPermit{}, err
	}
	if err = tx.Commit(); err != nil {
		return contract.OwnerControlPermit{}, err
	}
	return p, nil
}

func (d *DB) PrepareOriginalOwner(ctx context.Context, spec PrepareOriginalOwnerSpec) (contract.OriginalOwnerIntent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	defer tx.Rollback()
	scope, host, err := originalOwnerRuntimeScope(ctx, tx, spec.Host, spec.Epoch, true)
	if err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	scope.ExecutableSHA256 = spec.ExecutableSHA256
	var id string
	err = tx.QueryRowContext(ctx, `SELECT owner_id FROM execution_owners WHERE request_id=?`, spec.RequestID).Scan(&id)
	if err == nil {
		row, e := loadOriginalOwner(ctx, tx, id)
		if e != nil {
			return contract.OriginalOwnerIntent{}, e
		}
		scope.OwnerID, scope.Incarnation = row.state.Intent.Scope.OwnerID, row.state.Intent.Scope.Incarnation
		wanted := contract.OriginalOwnerIntent{Scope: scope, BootstrapID: spec.BootstrapID, BootstrapTokenSHA256: spec.BootstrapTokenSHA256, CoordinatorKeyID: spec.CoordinatorKeyID, RequestID: spec.RequestID, RequestSHA256: spec.RequestSHA256, StartupDeadlineUnixMS: spec.StartupDeadlineUnixMS, FirstHost: host, CoordinatorEpoch: spec.Epoch}
		if !row.state.IsCurrent || wanted != row.state.Intent {
			return contract.OriginalOwnerIntent{}, ErrConflict
		}
		return row.state.Intent, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return contract.OriginalOwnerIntent{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_owners WHERE launch_id=?`, scope.LaunchID).Scan(&count); err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	if count != 0 {
		return contract.OriginalOwnerIntent{}, ErrConflict
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE host_id=?`, scope.ProducerID).Scan(&count); err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	if count != 0 {
		return contract.OriginalOwnerIntent{}, originalOwnerRejected()
	}
	scope.OwnerID, err = newID("execution-owner")
	if err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	scope.Incarnation = 1
	i := contract.OriginalOwnerIntent{Scope: scope, BootstrapID: spec.BootstrapID, BootstrapTokenSHA256: spec.BootstrapTokenSHA256, CoordinatorKeyID: spec.CoordinatorKeyID, RequestID: spec.RequestID, RequestSHA256: spec.RequestSHA256, StartupDeadlineUnixMS: spec.StartupDeadlineUnixMS, FirstHost: host, CoordinatorEpoch: spec.Epoch}
	if err = i.Validate(); err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	body, err := originalOwnerBody(i)
	if err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	now := originalOwnerNow()
	row := originalOwnerRow{state: OriginalOwnerState{Intent: i, Phase: "intended", IsCurrent: true, CoordinatorEpoch: spec.Epoch}, intentJSON: body, created: now, updated: now}
	if err = d.storageAdmission(ctx, tx, scope.RunID, originalOwnerRowBytes(row), false); err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_owners(owner_id,launch_id,run_id,producer_id,incarnation,bootstrap_id,bootstrap_token_hash,coordinator_key_id,request_id,request_hash,intent_json,phase,is_current,control_revision,coordinator_epoch,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'intended',1,0,?,?,?)`, scope.OwnerID, scope.LaunchID, scope.RunID, scope.ProducerID, scope.Incarnation, i.BootstrapID, i.BootstrapTokenSHA256, i.CoordinatorKeyID, i.RequestID, i.RequestSHA256, body, i.CoordinatorEpoch, now, now)
	if err != nil {
		return contract.OriginalOwnerIntent{}, mapConflict(err)
	}
	if err = tx.Commit(); err != nil {
		return contract.OriginalOwnerIntent{}, err
	}
	return i, nil
}
func (d *DB) BindOriginalOwner(ctx context.Context, spec BindOriginalOwnerSpec, key ed25519.PrivateKey) (OriginalOwnerBindResult, error) {
	if spec.Hello.Validate() != nil {
		return OriginalOwnerBindResult{}, originalOwnerRejected()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	defer owned.close()
	tx := owned.tx
	row, err := loadOriginalOwner(ctx, tx, spec.Hello.Scope.OwnerID)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	i := row.state.Intent
	scope, host, err := originalOwnerRuntimeScope(ctx, tx, spec.Host, spec.Epoch, true)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	tokenHash, _ := contract.OriginalOwnerBootstrapSHA256(spec.Hello.BootstrapToken)
	if !row.state.IsCurrent || !originalOwnerSameScope(scope, i.Scope) || spec.Hello.Scope != i.Scope || spec.Hello.BootstrapID != i.BootstrapID || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(i.BootstrapTokenSHA256)) != 1 || spec.Hello.FirstHost != i.FirstHost || host != i.FirstHost || spec.Hello.CoordinatorKeyID != i.CoordinatorKeyID || !originalOwnerMatchesAuthentication(i.Scope, spec.Hello.Owner, spec.Authenticated) {
		return OriginalOwnerBindResult{}, originalOwnerRejected()
	}
	pub, err := originalOwnerSigningPublic(key, i.CoordinatorKeyID)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	if row.state.Phase == "bound" {
		if row.owner != spec.Hello.Owner || contract.VerifyOriginalOwnerBinding(*row.state.Binding, pub) != nil {
			return OriginalOwnerBindResult{}, ErrConflict
		}
		p, err := loadOriginalOwnerPermit(ctx, tx, i.RequestID, pub)
		if err != nil {
			return OriginalOwnerBindResult{}, err
		}
		return OriginalOwnerBindResult{Binding: *row.state.Binding, Permit: p}, nil
	}
	if row.state.Phase != "intended" || spec.Epoch < i.CoordinatorEpoch {
		return OriginalOwnerBindResult{}, originalOwnerRejected()
	}
	if row.state.SuccessionAuthorizationID != "" {
		if err = validateSuccessorOwnerBinding(ctx, tx, row); err != nil {
			return OriginalOwnerBindResult{}, err
		}
	} else {
		var segments int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE host_id=?`, i.Scope.ProducerID).Scan(&segments); err != nil {
			return OriginalOwnerBindResult{}, err
		}
		if segments != 0 {
			return OriginalOwnerBindResult{}, originalOwnerRejected()
		}
	}
	b, err := contract.SignOriginalOwnerBinding(contract.OriginalOwnerBinding{Scope: i.Scope, Owner: spec.Hello.Owner, CoordinatorKeyID: i.CoordinatorKeyID, FirstHost: i.FirstHost, BindingRevision: 1}, key)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(i.Scope)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(b.Owner)
	permitID, err := newID("owner-permit")
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	p, err := contract.SignOwnerControlPermit(contract.OwnerControlPermit{Version: 1, Kind: "attach", OwnerID: i.Scope.OwnerID, ScopeSHA256: scopeHash, OwnerIdentitySHA256: identityHash, CoordinatorKeyID: i.CoordinatorKeyID, CoordinatorEpoch: spec.Epoch, ControlRevision: 1, NewHost: i.FirstHost, RequestID: i.RequestID, RequestSHA256: i.RequestSHA256, PermitID: permitID}, key)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	body, err := originalOwnerBody(b)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	permitBody, err := originalOwnerBody(p)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	bindingHash, permitHash := originalOwnerHash([]byte(body)), originalOwnerHash([]byte(permitBody))
	now := originalOwnerNow()
	oldBytes := originalOwnerRowBytes(row)
	row.state.Phase = "bound"
	row.state.ControlRevision = 1
	row.state.CoordinatorEpoch = spec.Epoch
	row.state.CurrentHost = &i.FirstHost
	row.owner = b.Owner
	row.bindingJSON, row.bindingHash = body, bindingHash
	row.updated = now
	growth := originalOwnerRowBytes(row) - oldBytes
	if growth < 0 {
		growth = 0
	}
	growth += originalOwnerPermitBytes(i, p, permitBody, permitHash, now)
	if err = d.storageAdmission(ctx, tx, i.Scope.RunID, growth, false); err != nil {
		return OriginalOwnerBindResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_owners SET phase='bound',owner_pid=?,owner_birth=?,owner_public_key=?,control_revision=1,current_host_pid=?,current_host_birth=?,original_binding_json=?,original_binding_hash=?,updated_at=?,coordinator_epoch=? WHERE owner_id=? AND phase='intended' AND control_revision=0 AND is_current=1`, b.Owner.PID, b.Owner.Birth, b.Owner.PublicKey, i.FirstHost.PID, i.FirstHost.Birth, body, bindingHash, now, spec.Epoch, i.Scope.OwnerID)
	if err != nil {
		return OriginalOwnerBindResult{}, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return OriginalOwnerBindResult{}, ErrConflict
	}
	if err = insertOriginalOwnerPermit(ctx, tx, i, p, permitBody, permitHash, now); err != nil {
		return OriginalOwnerBindResult{}, err
	}
	if err = owned.commit(); err != nil {
		return OriginalOwnerBindResult{}, err
	}
	return OriginalOwnerBindResult{Binding: b, Permit: p}, nil
}
