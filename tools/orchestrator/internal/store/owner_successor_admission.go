package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// SuccessorOwnerAdmission stays inside the Coordinator. The public Hello only
// carries a stale-start selector; it can never choose an authorization ID.
type SuccessorOwnerAdmission struct {
	AuthorizationID, CommandID, OwnerPhase string
	Scope                                  contract.OriginalOwnerScope
}

func validHostResumeSelector(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && !strings.ContainsRune(id, 0) && strings.TrimSpace(id) == id
}

// ownerSuccessorSelectionTx derives the only incoming authorization of the
// current owner, or the unconsumed authorization of the latest retired owner.
// Returning a current initial owner with ErrNotFound is useful for presence.
func ownerSuccessorSelectionTx(ctx context.Context, tx *sql.Tx, launch string) (OriginalOwnerSuccessionAuthorization, *originalOwnerRow, error) {
	var zero OriginalOwnerSuccessionAuthorization
	var id string
	err := tx.QueryRowContext(ctx, `SELECT owner_id FROM execution_owners WHERE launch_id=? AND is_current=1`, launch).Scan(&id)
	if err == nil {
		row, err := loadOriginalOwner(ctx, tx, id)
		if err != nil {
			return zero, nil, err
		}
		if row.state.SuccessionAuthorizationID == "" {
			return zero, &row, ErrNotFound
		}
		a, err := loadOwnerSuccessionAuthorization(ctx, tx, row.state.SuccessionAuthorizationID)
		return a, &row, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT a.authorization_id FROM owner_succession_authorizations a JOIN execution_owners p ON p.owner_id=a.predecessor_owner_id
 WHERE a.launch_id=? AND p.phase='retired' AND p.is_current=0
 AND p.incarnation=(SELECT MAX(incarnation) FROM execution_owners WHERE launch_id=a.launch_id)
 AND NOT EXISTS(SELECT 1 FROM execution_owners WHERE succession_authorization_id=a.authorization_id)`, launch).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, nil, ErrNotFound
	}
	if err != nil {
		return zero, nil, err
	}
	a, err := loadOwnerSuccessionAuthorization(ctx, tx, id)
	return a, nil, err
}

func (d *DB) ResolveSuccessorOwnerAdmission(ctx context.Context, hello contract.HostHello, epoch uint64, digest string) (SuccessorOwnerAdmission, error) {
	var zero SuccessorOwnerAdmission
	if !validHostResumeSelector(hello.ResumeCommandID) {
		return zero, originalOwnerRejected()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	a, current, err := ownerSuccessorSelectionTx(ctx, owned.tx, hello.LaunchID)
	if err != nil {
		return zero, err
	}
	if a.CommandID != hello.ResumeCommandID {
		return zero, ErrConflict
	}
	preview, err := successorOwnerPreviewTx(ctx, owned.tx, hello, epoch, digest, a.AuthorizationID)
	if err != nil {
		return zero, err
	}
	phase := "succession_pending"
	if current != nil {
		phase = current.state.Phase
		if phase != "intended" && phase != "bound" {
			return zero, originalOwnerRejected()
		}
	}
	return SuccessorOwnerAdmission{AuthorizationID: a.AuthorizationID, CommandID: a.CommandID, OwnerPhase: phase, Scope: preview.scope}, nil
}

// Presence is only a launch-authenticated projection. A later HostHello still
// has to pass ResolveSuccessorOwnerAdmission and the committing Prepare CAS.
func ownerSuccessorPresenceTx(ctx context.Context, tx *sql.Tx, launch string) (string, string, error) {
	a, current, err := ownerSuccessorSelectionTx(ctx, tx, launch)
	if errors.Is(err, ErrNotFound) {
		if current != nil {
			return current.state.Phase, "", nil
		}
		return "none", "", nil
	}
	if err != nil {
		return "", "", err
	}
	p, err := successorOwnerPredecessor(ctx, tx, a)
	if err != nil {
		return "", "", err
	}
	if current == nil {
		if err = successorOwnerReservation(ctx, tx, a, p.state.Intent.Scope.ProducerID); err != nil {
			return "", "", err
		}
		return "succession_pending", a.CommandID, nil
	}
	if current.state.Intent.Scope.LaunchID != launch || current.state.Intent.Scope.Incarnation != p.state.Intent.Scope.Incarnation+1 || (current.state.Phase != "intended" && current.state.Phase != "bound") {
		return "", "", originalOwnerRejected()
	}
	return current.state.Phase, a.CommandID, nil
}
