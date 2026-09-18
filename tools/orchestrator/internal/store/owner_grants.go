package store

import (
	"context"
	"crypto/subtle"
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

func (d *DB) CreateOwnerGrant(ctx context.Context, controller string, pid int, birth string) (OwnerGrant, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if controller == "" || len(controller) > 256 || pid <= 1 || birth == "" {
		return OwnerGrant{}, CodeError("owner_registration_invalid")
	}
	if err := d.validateOwnerRegistration(ctx); err != nil {
		return OwnerGrant{}, err
	}
	id, err := newID("owner")
	if err != nil {
		return OwnerGrant{}, err
	}
	token, err := newID("owner-token")
	if err != nil {
		return OwnerGrant{}, err
	}
	grant := OwnerGrant{Version: 1, Kind: "local-owner", ID: id, Token: token, ControllerThread: controller, OriginPID: pid, OriginBirth: birth, OriginContextID: id, HostGeneration: id}
	_, err = d.sql.ExecContext(ctx, `INSERT INTO owner_grants(id,token_hash,controller_thread,origin_pid,origin_birth,created_at) VALUES(?,?,?,?,?,?)`, id, runtimeHash(token), controller, pid, birth, time.Now().UTC().Format(time.RFC3339Nano))
	return grant, err
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
	rows, err := d.sql.QueryContext(ctx, `SELECT pid,birth FROM runtime_hosts UNION SELECT DISTINCT json_extract(body_json,'$.process.pid'),json_extract(body_json,'$.process.birth') FROM runtime_events WHERE json_extract(body_json,'$.process.pid') IS NOT NULL`)
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
	// A worker can outlive its Host before the spawned identity reaches SQLite.
	// In that interval ancestry alone cannot distinguish it from a new master.
	// Check each segment, under the same mutex as dispatch/event persistence;
	// another command on this Host may already have a durable identity.
	var unidentified bool
	if err := d.sql.QueryRowContext(ctx, `SELECT EXISTS(
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
