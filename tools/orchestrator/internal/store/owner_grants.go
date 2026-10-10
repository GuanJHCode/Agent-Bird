package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"time"
)

type OwnerGrant struct {
	Version          int    `json:"version"`
	Kind             string `json:"kind"`
	ID               string `json:"grant_id"`
	Token            string `json:"token"`
	ControllerThread string `json:"controller_thread"`
	OriginPID        int    `json:"origin_pid"`
	OriginBirth      string `json:"origin_birth"`
	OriginContextID  string `json:"origin_context_id"`
	HostGeneration   string `json:"host_generation"`
}

// CreateOwnerGrant is retained for legacy in-process users. Public owner_bind
// must use PublishOwnerGrant so authority cannot precede the capability file.
func (d *DB) CreateOwnerGrant(ctx context.Context, controller string, pid int, birth string) (OwnerGrant, error) {
	g, _, err := d.createOwnerGrant(ctx, controller, pid, birth, nil)
	return g, err
}

// PublishOwnerGrant invokes a trusted publisher while holding DB.mu and the
// rooted transaction; publisher must not re-enter DB. rolledBack is live-only
// evidence for exact caller-owned file cleanup, never serialized authority.
// After any commit attempt it is false, including a failed/uncertain commit.
func (d *DB) PublishOwnerGrant(ctx context.Context, controller string, pid int, birth string, publish func(OwnerGrant, StoreRootAuthority) error) (grant OwnerGrant, rolledBack bool, err error) {
	if publish == nil {
		return OwnerGrant{}, false, CodeError("owner_publication_required")
	}
	return d.createOwnerGrant(ctx, controller, pid, birth, publish)
}

func (d *DB) createOwnerGrant(ctx context.Context, controller string, pid int, birth string, publish func(OwnerGrant, StoreRootAuthority) error) (OwnerGrant, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if controller == "" || len(controller) > 256 || pid <= 1 || birth == "" {
		return OwnerGrant{}, false, CodeError("owner_registration_invalid")
	}
	var owned *ownerTransaction
	var err error
	if publish == nil {
		owned, err = d.beginOwnerTransaction(ctx)
	} else {
		owned, err = d.beginRootedOwnerTransaction(ctx)
	}
	if err != nil {
		return OwnerGrant{}, false, err
	}
	defer owned.close()
	abort := func(err error) (OwnerGrant, bool, error) { return OwnerGrant{}, owned.rollback(), err }
	if err = validateOwnerRegistrationWith(ctx, owned.tx); err != nil {
		return abort(err)
	}
	id, err := newID("owner")
	if err != nil {
		return abort(err)
	}
	token, err := newID("owner-token")
	if err != nil {
		return abort(err)
	}
	grant := OwnerGrant{Version: 1, Kind: "local-owner", ID: id, Token: token, ControllerThread: controller, OriginPID: pid, OriginBirth: birth, OriginContextID: id, HostGeneration: id}
	if _, err = owned.tx.ExecContext(ctx, `INSERT INTO owner_grants(id,token_hash,controller_thread,origin_pid,origin_birth,created_at) VALUES(?,?,?,?,?,?)`, id, runtimeHash(token), controller, pid, birth, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return abort(err)
	}
	if publish != nil {
		if err = publish(grant, *owned.rootAuthority); err != nil {
			return abort(err)
		}
		if err = owned.rootAuthority.Validate(); err != nil {
			return abort(err)
		}
	}
	if err = owned.commit(); err != nil {
		return OwnerGrant{}, false, err
	}
	return grant, false, nil
}

func (d *DB) ValidateOwnerGrant(ctx context.Context, g OwnerGrant) error {
	var hash, controller, birth string
	var pid int
	err := d.sql.QueryRowContext(ctx, `SELECT token_hash,controller_thread,origin_pid,origin_birth FROM owner_grants WHERE id=?`, g.ID).Scan(&hash, &controller, &pid, &birth)
	if err != nil || g.Version != 1 || g.Kind != "local-owner" || g.OriginContextID != g.ID || g.HostGeneration != g.ID || g.ControllerThread != controller || g.OriginPID != pid || g.OriginBirth != birth || subtle.ConstantTimeCompare([]byte(hash), []byte(runtimeHash(g.Token))) != 1 {
		return CodeError("owner_capability_mismatch")
	}
	return nil
}

// Registered Host ancestry closes the interval before the first spawned event.
// Historical identities remain excluded across rebind; PID reuse has a new birth.
func (d *DB) IsWorkerAncestry(ctx context.Context, ancestors map[int]string) (bool, error) {
	rows, err := d.sql.QueryContext(ctx, workerAncestrySQL)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var birth string
		if err := rows.Scan(&pid, &birth); err != nil {
			return false, err
		}
		if ancestors[pid] == birth {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ValidateOwnerRegistration guards enrollment paths without an existing owner grant.
func (d *DB) ValidateOwnerRegistration(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.validateOwnerRegistration(ctx)
}

// ValidateDispatchPeer preserves existing masters' control during startup,
// while an unidentified orphan cannot use a token to assume that role.
func (d *DB) ValidateDispatchPeer(ctx context.Context, ancestors map[int]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	err := d.validateOwnerRegistration(ctx)
	if err != CodeError("owner_registration_deferred") {
		return err
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT origin_pid,origin_birth FROM runs UNION SELECT origin_pid,origin_birth FROM owner_grants`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var birth string
		if err := rows.Scan(&pid, &birth); err != nil {
			return err
		}
		if birth != "" && ancestors[pid] == birth {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return CodeError("owner_registration_deferred")
}

func (d *DB) validateOwnerRegistration(ctx context.Context) error {
	return validateOwnerRegistrationWith(ctx, d.sql)
}

type ownerRegistrationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateOwnerRegistrationWith(ctx context.Context, db ownerRegistrationQuery) error {
	// A worker can outlive its Host before the spawned identity reaches SQLite.
	// In that interval ancestry alone cannot distinguish it from a new master.
	// Check each segment, under the same mutex as dispatch/event persistence;
	// another command on this Host may already have a durable identity.
	var unidentified bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM segment_runtime s
	 WHERE s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')
	 AND NOT EXISTS(SELECT 1 FROM runtime_events e WHERE e.segment_id=s.segment_id
	   AND json_extract(e.body_json,'$.process.pid')>1
	   AND COALESCE(json_extract(e.body_json,'$.process.birth'),'')!='')
	)`).Scan(&unidentified); err != nil {
		return err
	}
	if unidentified {
		return CodeError("owner_registration_deferred")
	}
	return nil
}
