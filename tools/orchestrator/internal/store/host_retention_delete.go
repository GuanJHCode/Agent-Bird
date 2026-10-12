package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"strconv"
	"time"
)

type hostDeletion struct {
	row                                                              hostRetentionRow
	request, binding, facts, rootJSON, namespaceJSON, created, phase string
	root                                                             StoreRootAuthority
	cost                                                             int64
}

func (x hostDeletion) controlBytes() int64 {
	n := int64(4096 + x.row.slots*hostSlotRetirementPrepaid)
	for _, s := range []string{x.row.id, x.row.run, x.row.task, x.row.attempt, x.row.segment, x.row.producer, x.row.class, x.row.scope, x.request, x.binding, x.facts, x.rootJSON, x.namespaceJSON, x.row.mode, x.created} {
		n += int64(len(s))
	}
	return n
}
func hostDeleteRequestID(p HostArtifactRetentionPlan, i HostArtifactRetentionItem) string {
	return "host-delete-" + runtimeHash(p.RunID+"\x00"+p.RequestID+"\x00"+p.OwnerBinding+"\x00"+i.ReservationID)
}
func authorizeHostPlan(ctx context.Context, tx *sql.Tx, controller, token string, p HostArtifactRetentionPlan) error {
	if e := authorizeHostRetention(ctx, tx, p.RunID, controller, token); e != nil {
		return e
	}
	if e := requirePublishedOwnerTx(ctx, tx, p.RunID); e != nil {
		return e
	}
	b, e := diagnosticsGCBinding(ctx, tx, p.RunID)
	if e != nil {
		return e
	}
	if b != p.OwnerBinding {
		return CodeError("host_retention_owner_changed")
	}
	return nil
}
func loadHostDeletion(ctx context.Context, tx *sql.Tx, row hostRetentionRow) (x hostDeletion, e error) {
	x.row = row
	var run, task, attempt, segment, producer, class, scope, mode string
	var slots int
	e = tx.QueryRowContext(ctx, `SELECT run_id,task_id,attempt_id,segment_id,producer_id,class,scope_sha256,slot_count,discovery_mode,request_id,binding_hash,facts_sha256,root_json,namespace_json,created_at,phase,control_bytes FROM host_artifact_deletions WHERE reservation_id=?`, row.id).Scan(&run, &task, &attempt, &segment, &producer, &class, &scope, &slots, &mode, &x.request, &x.binding, &x.facts, &x.rootJSON, &x.namespaceJSON, &x.created, &x.phase, &x.cost)
	if e != nil {
		return
	}
	var nodes StoreRootNodes
	namespace, _ := json.Marshal(row.namespace)
	if run != row.run || task != row.task || attempt != row.attempt || segment != row.segment || producer != row.producer || class != row.class || scope != row.scope || slots != row.slots || mode != row.mode || string(namespace) != x.namespaceJSON || json.Unmarshal([]byte(x.rootJSON), &nodes) != nil || x.cost != x.controlBytes() || !(x.phase == "prepared" && row.state == "deleting" || x.phase == "removed" && row.state == "released") {
		return x, CodeError("host_retention_state_invalid")
	}
	x.root = StoreRootAuthority{nodes: nodes}
	raw, _ := json.Marshal(nodes)
	if string(raw) != x.rootJSON {
		return x, CodeError("host_retention_state_invalid")
	}
	return
}

// Commit the prepaid reservation header before any empty slot directory/lock
// creation. No filesystem lease is held while entering this step.
func (d *DB) prepareHostDeletion(ctx context.Context, controller, token string, p HostArtifactRetentionPlan, i HostArtifactRetentionItem, now time.Time) (x hostDeletion, e error) {
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return x, e
	}
	defer o.close()
	if e = authorizeHostPlan(ctx, o.tx, controller, token, p); e != nil {
		return x, e
	}
	row, e := d.loadHostRetention(ctx, o.tx, p.RunID, i.ReservationID)
	if e != nil {
		return x, e
	}
	x, e = loadHostDeletion(ctx, o.tx, row)
	if e == nil {
		if x.request != hostDeleteRequestID(p, i) || x.binding != p.OwnerBinding || x.facts != i.FactsSHA256 || x.root.Nodes() != o.rootAuthority.Nodes() {
			return x, ErrConflict
		}
		return x, o.finishInitialRead()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return x, e
	}
	if row.state != "active" && row.state != "reserved" {
		return x, CodeError("host_artifact_unavailable")
	}
	facts, e := hostRetentionFacts(ctx, o.tx, row, p.OwnerBinding, now)
	if e != nil {
		return x, e
	}
	if facts != i.FactsSHA256 {
		return x, ErrConflict
	}
	rootJSON, _ := json.Marshal(o.rootAuthority.Nodes())
	namespace, _ := json.Marshal(row.namespace)
	x = hostDeletion{row: row, request: hostDeleteRequestID(p, i), binding: p.OwnerBinding, facts: facts, root: *o.rootAuthority, rootJSON: string(rootJSON), namespaceJSON: string(namespace), created: now.UTC().Format(time.RFC3339Nano), phase: "prepared"}
	x.cost = x.controlBytes()
	if e = d.storageAdmission(ctx, o.tx, row.run, x.cost, false); e != nil {
		return x, e
	}
	if e = d.managedArtifactAdmission(ctx, o.tx, 0, x.cost); e != nil {
		return x, e
	}
	_, e = o.tx.ExecContext(ctx, `INSERT INTO host_artifact_deletions(reservation_id,run_id,task_id,attempt_id,segment_id,producer_id,class,scope_sha256,slot_count,discovery_mode,request_id,binding_hash,facts_sha256,root_json,namespace_json,created_at,phase,control_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'prepared',?)`, row.id, row.run, row.task, row.attempt, row.segment, row.producer, row.class, row.scope, row.slots, row.mode, x.request, x.binding, x.facts, x.rootJSON, x.namespaceJSON, x.created, x.cost)
	if e != nil {
		return x, mapConflict(e)
	}
	if _, e = o.tx.ExecContext(ctx, `UPDATE managed_artifact_reservations SET state='deleting' WHERE id=?`, row.id); e != nil {
		return x, e
	}
	if e = o.commit(); e != nil {
		return x, e
	}
	x.row.state = "deleting"
	return x, nil
}

type hostSlotDeleteLease interface {
	Plan() []byte
	MarkerBytes() int64
	RemovedBytes() int64
	Remove(context.Context) error
	VerifyRemoved(context.Context) error
	Close() error
}
type openHostDeletion func(context.Context, hostDeletion, int, []byte) (hostSlotDeleteLease, int64, error)

func openManagedHostDeletion(ctx context.Context, x hostDeletion, index int, saved []byte) (hostSlotDeleteLease, int64, error) {
	request := "host-slot-delete-" + runtimeHash(x.request+"\x00"+strconv.Itoa(index))
	if ref, ok := x.row.refs[index]; ok {
		lease, e := artifacts.OpenHostSlotDeletion(ctx, x.row.root, ref.descriptor, ref.proof, request, saved)
		return lease, ref.proof.Size, e
	}
	binding := artifacts.HostSlotBinding{Version: 1, ReservationID: x.row.id, ScopeSHA256: x.row.scope, Class: x.row.class, Index: index}
	lease, plan, e := artifacts.OpenHostSlotBindingDeletion(ctx, x.row.root, binding, x.row.namespace, artifacts.HostSlotDiscoveryMode(x.row.mode), request, saved)
	return lease, plan.PlannedRemovedBytes, e
}
func (d *DB) ApplyHostArtifactRetention(ctx context.Context, controller, token string, p HostArtifactRetentionPlan) (HostArtifactRetentionResult, error) {
	return d.applyHostArtifactRetentionAt(ctx, controller, token, p, time.Now().UTC(), openManagedHostDeletion)
}
func (d *DB) applyHostArtifactRetentionAt(ctx context.Context, controller, token string, p HostArtifactRetentionPlan, now time.Time, open openHostDeletion) (out HostArtifactRetentionResult, e error) {
	raw, e := json.Marshal(p)
	if e != nil {
		return
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return
	}
	out = HostArtifactRetentionResult{Version: 1, RunID: p.RunID, RequestID: p.RequestID}
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e == nil {
		e = authorizeHostPlan(ctx, o.tx, controller, token, p)
		if e == nil {
			e = o.finishInitialRead()
		}
		o.close()
	}
	d.mu.Unlock()
	if e != nil {
		return out, e
	}
	for _, item := range p.Items {
		x, err := d.prepareHostDeletion(ctx, controller, token, p, item, now)
		if err != nil {
			return out, err
		}
		if x.phase == "removed" {
			out.Deleted++
			continue
		}
		for index := 0; index < x.row.slots; index++ {
			if e = d.deleteHostSlot(ctx, controller, token, p, x, index, now, open); e != nil {
				return out, e
			}
		}
		if e = d.finishHostDeletion(ctx, controller, token, p, x, now); e != nil {
			return out, e
		}
		out.Deleted++
	}
	return
}
func (d *DB) validateHostDeletionTx(ctx context.Context, o *ownerTransaction, controller, token string, p HostArtifactRetentionPlan, x hostDeletion, now time.Time) (hostDeletion, error) {
	if e := authorizeHostPlan(ctx, o.tx, controller, token, p); e != nil {
		return hostDeletion{}, e
	}
	if o.rootAuthority == nil || o.rootAuthority.Nodes() != x.root.Nodes() {
		return hostDeletion{}, CodeError("host_retention_store_root_invalid")
	}
	row, e := d.loadHostRetention(ctx, o.tx, x.row.run, x.row.id)
	if e != nil {
		return hostDeletion{}, e
	}
	current, e := loadHostDeletion(ctx, o.tx, row)
	if e != nil {
		return current, e
	}
	if current.request != x.request || current.binding != x.binding || current.facts != x.facts || current.rootJSON != x.rootJSON || current.namespaceJSON != x.namespaceJSON {
		return current, ErrConflict
	}
	facts, e := hostRetentionFacts(ctx, o.tx, row, x.binding, now)
	if e != nil {
		return current, e
	}
	if facts != x.facts {
		return current, ErrConflict
	}
	return current, nil
}
func loadHostDeletionSlot(ctx context.Context, tx *sql.Tx, x hostDeletion, index int) (plan []byte, phase string, removed int64, e error) {
	var id string
	e = tx.QueryRowContext(ctx, `SELECT object_id,plan_json,phase,removed_bytes FROM host_artifact_deletion_slots WHERE reservation_id=? AND slot=?`, x.row.id, index).Scan(&id, &plan, &phase, &removed)
	if e != nil {
		return
	}
	binding := artifacts.HostSlotBinding{Version: 1, ReservationID: x.row.id, ScopeSHA256: x.row.scope, Class: x.row.class, Index: index}
	expected, err := binding.ObjectID()
	if err != nil || expected != id || len(plan) < 1 || len(plan) > hostSlotPlanLimit {
		return nil, "", 0, CodeError("host_retention_state_invalid")
	}
	return
}
func (d *DB) deleteHostSlot(ctx context.Context, controller, token string, p HostArtifactRetentionPlan, x hostDeletion, index int, now time.Time, open openHostDeletion) (e error) {
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	o, e := d.beginRootedOwnerTransaction(ctx)
	var saved []byte
	if e == nil {
		_, e = d.validateHostDeletionTx(ctx, o, controller, token, p, x, now)
		if e == nil {
			saved, _, _, e = loadHostDeletionSlot(ctx, o.tx, x, index)
			if errors.Is(e, sql.ErrNoRows) {
				e = nil
			}
		}
		if e == nil {
			e = o.finishInitialRead()
		}
		o.close()
	}
	d.mu.Unlock()
	if e != nil {
		return
	}
	// No DB mutex or write lock is held while acquiring the slot writer lease.
	lease, removed, e := open(ctx, x, index, saved)
	if e != nil {
		return
	}
	defer func() { e = errors.Join(e, lease.Close()) }()
	plan := lease.Plan()
	if len(plan) < 1 || len(plan) > hostSlotPlanLimit || removed < 0 || removed > 2*hostSlotClassLimit(x.row.class) {
		return CodeError("host_retention_state_invalid")
	}
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	o, e = d.beginRootedOwnerTransaction(ctx)
	if e == nil {
		_, e = d.validateHostDeletionTx(ctx, o, controller, token, p, x, now)
		if e == nil {
			old, _, size, err := loadHostDeletionSlot(ctx, o.tx, x, index)
			if errors.Is(err, sql.ErrNoRows) {
				binding := artifacts.HostSlotBinding{Version: 1, ReservationID: x.row.id, ScopeSHA256: x.row.scope, Class: x.row.class, Index: index}
				id, _ := binding.ObjectID()
				_, e = o.tx.ExecContext(ctx, `INSERT INTO host_artifact_deletion_slots(reservation_id,slot,object_id,plan_json,removed_bytes,phase) VALUES(?,?,?,?,?,'prepared')`, x.row.id, index, id, string(plan), removed)
			} else if err != nil {
				e = err
			} else if string(old) != string(plan) || size != removed {
				e = ErrConflict
			}
		}
		if e == nil {
			e = o.commit()
		}
		o.close()
	}
	d.mu.Unlock()
	if e != nil {
		return
	}
	// The child plan is durable before Remove. Follow FS -> DB lock order and
	// hold SQLite write ownership through final reauthorization and physical IO.
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	defer d.mu.Unlock()
	o, e = d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return
	}
	defer o.close()
	if _, e = o.tx.ExecContext(ctx, `UPDATE host_artifact_deletions SET phase=phase WHERE reservation_id=?`, x.row.id); e != nil {
		return
	}
	if _, e = d.validateHostDeletionTx(ctx, o, controller, token, p, x, now); e != nil {
		return
	}
	old, phase, size, e := loadHostDeletionSlot(ctx, o.tx, x, index)
	if e != nil {
		return
	}
	if string(old) != string(plan) || size != removed {
		return ErrConflict
	}
	if phase == "removed" {
		if e = lease.VerifyRemoved(ctx); e != nil {
			return
		}
		return o.finishInitialRead()
	}
	if e = lease.Remove(ctx); e != nil {
		return
	}
	if e = lease.VerifyRemoved(ctx); e != nil {
		return
	}
	if lease.MarkerBytes() != int64(len(plan)) {
		return CodeError("host_retention_state_invalid")
	}
	if e = o.rootAuthority.Validate(); e != nil {
		return
	}
	if _, e = o.tx.ExecContext(ctx, `UPDATE host_artifact_deletion_slots SET phase='removed' WHERE reservation_id=? AND slot=?`, x.row.id, index); e != nil {
		return
	}
	return o.commit()
}
func hostSlotClassLimit(class string) int64 {
	if class == "progress" {
		return artifacts.HostProgressSlotBytes
	}
	return artifacts.HostTerminalSlotBytes
}
func (d *DB) finishHostDeletion(ctx context.Context, controller, token string, p HostArtifactRetentionPlan, x hostDeletion, now time.Time) (e error) {
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return
	}
	defer o.close()
	if _, e = o.tx.ExecContext(ctx, `UPDATE host_artifact_deletions SET phase=phase WHERE reservation_id=?`, x.row.id); e != nil {
		return
	}
	current, e := d.validateHostDeletionTx(ctx, o, controller, token, p, x, now)
	if e != nil {
		return
	}
	if current.phase == "removed" {
		return o.finishInitialRead()
	}
	var count int
	if e = o.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_artifact_deletion_slots WHERE reservation_id=? AND phase='removed'`, x.row.id).Scan(&count); e != nil {
		return
	}
	if count != x.row.slots {
		return CodeError("host_retention_incomplete")
	}
	if _, e = o.tx.ExecContext(ctx, `UPDATE host_artifact_deletions SET phase='removed' WHERE reservation_id=?`, x.row.id); e != nil {
		return
	}
	if _, e = o.tx.ExecContext(ctx, `UPDATE managed_artifact_reservations SET state='released',retained_bytes=0 WHERE id=?`, x.row.id); e != nil {
		return
	}
	return o.commit()
}
