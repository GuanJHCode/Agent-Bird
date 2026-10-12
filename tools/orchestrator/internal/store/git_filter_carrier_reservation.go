package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"time"
)

func (d *DB) reserveGitFilterCarrier(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	if c.GitFilterCarrier != nil {
		return contract.ErrGitFilterContract
	}
	if !contract.IsGitFilterCarrierAction(c.AdapterPayload) {
		return nil
	}
	p, err := adapter.DecodeInvocationPayload(c.AdapterPayload)
	if err != nil || !adapter.IsFilterIntegrate(p) || c.GitFilter == nil {
		return contract.ErrGitFilterContract
	}
	scope, err := contract.GitFilterCarrierScopeSHA256(*c)
	if err != nil {
		return err
	}
	id, err := newID("git-filter-carrier")
	if err != nil {
		return err
	}
	g := contract.GitFilterCarrierGrant{Version: 1, ReservationID: id, ScopeSHA256: scope, FilterReservationID: c.GitFilter.ReservationID, FilterScopeSHA256: c.GitFilter.ScopeSHA256, ManifestSHA256: c.GitFilter.ManifestSHA256, MetadataBytes: contract.GitFilterCarrierMetadataBytes, RuntimeNamespace: contract.GitFilterCarrierNamespace, DirectorySlots: 1, SocketSlots: 1}
	body, err := json.Marshal(g)
	if err != nil || len(body) > 2048 {
		return contract.ErrGitFilterContract
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	control := int64(gitFilterCarrierFixedControlBytes)
	for _, s := range []string{id, g.FilterReservationID, c.RunID, c.SegmentID, scope, string(body), stamp, "reserved"} {
		control += int64(len(s))
	}
	if err = d.storageAdmission(ctx, tx, c.RunID, control, false); err != nil {
		return err
	}
	if err = d.managedArtifactAdmission(ctx, tx, 0, control); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO git_filter_carrier_reservations(id,filter_reservation_id,run_id,segment_id,scope_sha256,grant_json,metadata_bytes,created_at,state) VALUES(?,?,?,?,?,?,?,?,'reserved')`, id, g.FilterReservationID, c.RunID, c.SegmentID, scope, string(body), g.MetadataBytes, stamp)
	if err != nil {
		return mapConflict(err)
	}
	c.GitFilterCarrier = &g
	return contract.RequireGitFilterCarrier(*c)
}

func bindGitFilterCarrier(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, live bool) error {
	var present, version int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='git_filter_carrier_reservations'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		if !live && c.GitFilterCarrier == nil && !contract.IsGitFilterCarrierAction(c.AdapterPayload) && tx.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version) == nil && version < 38 {
			return nil
		}
		return contract.ErrGitFilterContract
	}
	var id, original, run, segment, scope, raw, state, cleanup string
	var metadata int64
	var release sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,filter_reservation_id,run_id,segment_id,scope_sha256,grant_json,metadata_bytes,state,release_event_id,cleanup_json FROM git_filter_carrier_reservations WHERE segment_id=?`, c.SegmentID).Scan(&id, &original, &run, &segment, &scope, &raw, &metadata, &state, &release, &cleanup)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.RequireGitFilterCarrier(*c)
	}
	var g contract.GitFilterCarrierGrant
	if err != nil || len(raw) > 2048 || json.Unmarshal([]byte(raw), &g) != nil || g.ReservationID != id || g.FilterReservationID != original || g.ScopeSHA256 != scope || g.MetadataBytes != metadata || run != c.RunID || segment != c.SegmentID || c.GitFilterCarrier != nil && *c.GitFilterCarrier != g {
		return contract.ErrGitFilterContract
	}
	canonical, _ := json.Marshal(g)
	if string(canonical) != raw {
		return contract.ErrGitFilterContract
	}
	if state == "reserved" {
		if release.Valid || cleanup != "" {
			return contract.ErrGitFilterContract
		}
	} else if state == "released" && !live {
		if !release.Valid || verifyGitFilterCarrierRelease(ctx, tx, g, release.String, cleanup) != nil {
			return contract.ErrGitFilterContract
		}
	} else {
		return contract.ErrGitFilterContract
	}
	c.GitFilterCarrier = &g
	return contract.RequireGitFilterCarrier(*c)
}

func verifyGitFilterCarrierRelease(ctx context.Context, tx *sql.Tx, g contract.GitFilterCarrierGrant, eventID, raw string) error {
	var r contract.GitFilterCarrierCleanup
	if len(raw) > gitFilterCarrierCleanupLimit || json.Unmarshal([]byte(raw), &r) != nil || r.ReservationID != g.ReservationID || r.ScopeSHA256 != g.ScopeSHA256 || r.FilterReservationID != g.FilterReservationID || r.ManifestSHA256 != g.ManifestSHA256 || r.RuntimeNamespace != g.RuntimeNamespace {
		return contract.ErrGitFilterContract
	}
	canonical, _ := json.Marshal(r)
	if string(canonical) != raw {
		return contract.ErrGitFilterContract
	}
	var body, originalState, originalRelease, originalCleanup string
	err := tx.QueryRowContext(ctx, `SELECT e.body_json,f.state,COALESCE(f.release_event_id,''),f.cleanup_json FROM git_filter_resource_reservations f JOIN runtime_events e ON e.event_id=? WHERE f.id=?`, eventID, g.FilterReservationID).Scan(&body, &originalState, &originalRelease, &originalCleanup)
	var e contract.Event
	if err != nil || originalState != "released" || originalRelease != eventID || json.Unmarshal([]byte(body), &e) != nil || e.GitFilterCarrierCleanup == nil || *e.GitFilterCarrierCleanup != r || e.GitFilterCleanup == nil || e.GitFilterCleanup.ReservationID != g.FilterReservationID || e.GitFilterCleanup.ScopeSHA256 != g.FilterScopeSHA256 {
		return contract.ErrGitFilterContract
	}
	encoded, _ := json.Marshal(e.GitFilterCleanup)
	if string(encoded) != originalCleanup {
		return contract.ErrGitFilterContract
	}
	return nil
}

// Called directly after original Broker release in the same Host-event
// transaction. A missing or invalid companion rolls both releases back.
func applyGitFilterCarrierRelease(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	var raw, state, cleanup string
	var release sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT grant_json,state,release_event_id,cleanup_json FROM git_filter_carrier_reservations WHERE segment_id=?`, e.SegmentID).Scan(&raw, &state, &release, &cleanup)
	if errors.Is(err, sql.ErrNoRows) {
		if e.GitFilterCarrierCleanup != nil {
			return contract.ErrGitFilterContract
		}
		return nil
	}
	if err != nil {
		return err
	}
	if e.Kind != contract.EventExited && e.Kind != contract.EventLaunchRejected {
		if e.GitFilterCarrierCleanup != nil {
			return contract.ErrGitFilterContract
		}
		return nil
	}
	var g contract.GitFilterCarrierGrant
	if e.GitFilterCarrierCleanup == nil || len(e.EventID) > gitFilterReleaseEventLimit || json.Unmarshal([]byte(raw), &g) != nil {
		return contract.ErrGitFilterContract
	}
	body, _ := json.Marshal(e.GitFilterCarrierCleanup)
	if err = verifyGitFilterCarrierRelease(ctx, tx, g, e.EventID, string(body)); err != nil {
		return err
	}
	if state == "released" {
		if !release.Valid || release.String != e.EventID || cleanup != string(body) {
			return contract.ErrGitFilterContract
		}
		return nil
	}
	if state != "reserved" || release.Valid || cleanup != "" {
		return contract.ErrGitFilterContract
	}
	result, err := tx.ExecContext(ctx, `UPDATE git_filter_carrier_reservations SET state='released',release_event_id=?,cleanup_json=? WHERE id=? AND state='reserved'`, e.EventID, string(body), g.ReservationID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return contract.ErrGitFilterContract
	}
	return nil
}
