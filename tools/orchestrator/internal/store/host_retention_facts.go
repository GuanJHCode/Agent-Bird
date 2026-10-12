package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type hostRetentionRef struct {
	id, raw    string
	descriptor artifacts.HostSlotDescriptor
	proof      artifacts.Proof
}
type hostRetentionRow struct {
	id, run, task, attempt, segment, producer, class, scope, state, created, root, mode string
	slots                                                                               int
	reserved, retained                                                                  int64
	refs                                                                                map[int]hostRetentionRef
	namespace                                                                           artifacts.HostSlotNamespaceProof
}

func decodeHostRetentionRef(raw string) (x hostRetentionRef, e error) {
	x.raw = raw
	var receipt contract.HostArtifactReceipt
	if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.Validate() != nil || json.Unmarshal(receipt.Descriptor, &x.descriptor) != nil || json.Unmarshal(receipt.Proof, &x.proof) != nil {
		return x, CodeError("host_retention_state_invalid")
	}
	a, _ := json.Marshal(receipt)
	b, _ := json.Marshal(x.proof)
	c, err := x.descriptor.Canonical()
	id, idErr := x.descriptor.ObjectID()
	if err != nil || idErr != nil || string(a) != raw || string(b) != string(receipt.Proof) || string(c) != string(receipt.Descriptor) || x.proof.IntakeID != id || x.proof.DescriptorSHA256 != runtimeHash(string(c)) || x.proof.SHA256 != x.descriptor.SHA256 || x.proof.Size != x.descriptor.Size {
		return x, CodeError("host_retention_state_invalid")
	}
	x.id = id
	return
}
func (d *DB) loadHostRetention(ctx context.Context, tx *sql.Tx, run, id string) (r hostRetentionRow, e error) {
	e = tx.QueryRowContext(ctx, `SELECT id,run_id,task_id,attempt_id,scope_key,producer_id,class,namespace_sha256,state,created_at,reserved_bytes,retained_bytes FROM managed_artifact_reservations WHERE id=? AND run_id=? AND class IN ('terminal','progress') AND policy_sha256=''`, id, run).Scan(&r.id, &r.run, &r.task, &r.attempt, &r.segment, &r.producer, &r.class, &r.scope, &r.state, &r.created, &r.reserved, &r.retained)
	if errors.Is(e, sql.ErrNoRows) {
		return r, CodeError("host_artifact_unavailable")
	}
	if e != nil {
		return
	}
	r.slots = 1
	expected := int64(contract.HostTerminalArtifactBytes)
	if r.class == "progress" {
		r.slots = contract.HostProgressArtifactSlots
		expected = int64(contract.HostProgressArtifactSlots * contract.HostProgressArtifactBytes)
	}
	if r.reserved != expected || !validDigest(r.scope) || r.retained < 0 || r.retained > expected {
		return r, CodeError("host_retention_state_invalid")
	}
	r.root = filepath.Join(filepath.Dir(d.path), "host-spool", r.producer)
	var authorityJSON string
	e = tx.QueryRowContext(ctx, `SELECT namespace_json FROM host_artifact_cleanup_authorities WHERE reservation_id=? AND run_id=? AND version=1 AND scope_sha256=? AND class=? AND slot_count=?`, r.id, r.run, r.scope, r.class, r.slots).Scan(&authorityJSON)
	r.mode = "absent_only"
	if e == nil {
		if json.Unmarshal([]byte(authorityJSON), &r.namespace) != nil {
			return r, CodeError("host_retention_state_invalid")
		}
		raw, _ := json.Marshal(r.namespace)
		if string(raw) != authorityJSON {
			return r, CodeError("host_retention_state_invalid")
		}
		r.mode = "schema40_managed"
	} else if !errors.Is(e, sql.ErrNoRows) {
		return r, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id,slot,proof_json,manifest_bytes FROM managed_artifact_refs WHERE reservation_id=? AND run_id=? AND intake_id IS NULL ORDER BY slot`, r.id, r.run)
	if e != nil {
		return r, e
	}
	r.refs = map[int]hostRetentionRef{}
	sum := int64(0)
	for rows.Next() {
		var object, slot, raw string
		var manifest int64
		if e = rows.Scan(&object, &slot, &raw, &manifest); e != nil {
			break
		}
		x, err := decodeHostRetentionRef(raw)
		index, ie := strconv.Atoi(slot)
		if err != nil || ie != nil || strconv.Itoa(index) != slot || index < 0 || index >= r.slots || object != x.id || x.descriptor.ReservationID != r.id || x.descriptor.ScopeSHA256 != r.scope || x.descriptor.Class != r.class || x.descriptor.Index != index || x.proof.Path != filepath.Join(r.root, "managed-artifacts", object, "data.bin") {
			e = CodeError("host_retention_state_invalid")
			break
		}
		b, _ := x.descriptor.Canonical()
		if int64(len(b)) != manifest {
			e = CodeError("host_retention_state_invalid")
			break
		}
		r.refs[index] = x
		sum += x.proof.Size
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return r, e
	}
	if r.state == "released" && r.retained != 0 || r.state != "released" && sum != r.retained {
		return r, CodeError("host_retention_state_invalid")
	}
	if r.mode == "absent_only" {
		// Historical objects retain only their saved registered namespace proof.
		// Migration and retention never mint creation authority for old rows.
		var anchorRaw string
		e = tx.QueryRowContext(ctx, `SELECT f.proof_json FROM managed_artifact_refs f JOIN managed_artifact_reservations r ON r.id=f.reservation_id WHERE r.producer_id=? AND r.class IN ('terminal','progress') AND f.intake_id IS NULL AND f.manifest_bytes>0 ORDER BY f.id LIMIT 1`, r.producer).Scan(&anchorRaw)
		if errors.Is(e, sql.ErrNoRows) {
			return r, CodeError("host_retention_namespace_unverified")
		}
		if e != nil {
			return r, e
		}
		anchor, err := decodeHostRetentionRef(anchorRaw)
		if err != nil {
			return r, err
		}
		if anchor.proof.Path != filepath.Join(r.root, "managed-artifacts", anchor.id, "data.bin") {
			return r, CodeError("host_retention_state_invalid")
		}
		r.namespace = artifacts.HostSlotNamespaceProof{Version: 1, RootPath: r.root, RootDevice: anchor.proof.RootDevice, RootInode: anchor.proof.RootInode, ParentDevice: anchor.proof.ParentDevice, ParentInode: anchor.proof.ParentInode}
	}
	if r.namespace.Version != 1 || r.namespace.RootPath != r.root || r.namespace.RootDevice == 0 || r.namespace.RootInode == 0 || r.namespace.ParentDevice == 0 || r.namespace.ParentInode == 0 {
		return r, CodeError("host_retention_state_invalid")
	}
	for _, ref := range r.refs {
		if ref.proof.RootDevice != r.namespace.RootDevice || ref.proof.RootInode != r.namespace.RootInode || ref.proof.ParentDevice != r.namespace.ParentDevice || ref.proof.ParentInode != r.namespace.ParentInode {
			return r, CodeError("host_retention_state_invalid")
		}
	}
	return
}

func hostRetentionTime(raw string) (time.Time, error) {
	t, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil || raw != t.UTC().Format(time.RFC3339Nano) && raw != t.UTC().Format("2006-01-02T15:04:05.000000000Z") {
		return time.Time{}, CodeError("host_retention_state_invalid")
	}
	return t, nil
}
func hostRetentionExclusion(e error) bool {
	var c CodeError
	if !errors.As(e, &c) {
		return false
	}
	switch c {
	case "host_artifact_unavailable", "host_retention_pinned", "host_retention_not_terminal", "host_retention_unconsumed", "host_retention_unresolved", "host_retention_not_expired", "host_retention_terminal_unverified", "host_retention_namespace_unverified", "host_retention_legacy_unsealed":
		return true
	}
	return false
}

type hostTerminalFacts struct {
	event              contract.Event
	received, consumed time.Time
	vector             string
	success            bool
}

func loadHostTerminalFacts(ctx context.Context, tx *sql.Tx, r hostRetentionRow) (f hostTerminalFacts, e error) {
	var status, outcome, command, budget, segmentStatus, taskStatus, completion string
	var revision, currentRevision int
	e = tx.QueryRowContext(ctx, `SELECT sr.status,sr.outcome,sr.command_id,b.status,s.status,c.work_revision,tr.work_revision,t.status,tr.completion_policy FROM segment_runtime sr JOIN segments s ON s.id=sr.segment_id JOIN attempts a ON a.id=sr.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime tr ON tr.task_id=t.id JOIN budget_runtime b ON b.id=sr.reservation_id JOIN report_capabilities c ON c.segment_id=s.id WHERE sr.segment_id=? AND sr.attempt_id=? AND sr.host_id=? AND t.id=? AND t.run_id=?`, r.segment, r.attempt, r.producer, r.task, r.run).Scan(&status, &outcome, &command, &budget, &segmentStatus, &revision, &currentRevision, &taskStatus, &completion)
	if e != nil {
		return
	}
	if status != segmentStatus || status != "exited" && status != "not_started" || budget != "settled" {
		return f, CodeError("host_retention_not_terminal")
	}
	rows, e := tx.QueryContext(ctx, `SELECT event_id,producer_id,sequence,event_revision,action_slot,payload_hash,created_at,body_json FROM runtime_events WHERE segment_id=? ORDER BY sequence LIMIT 513`, r.segment)
	if e != nil {
		return f, e
	}
	var vector []string
	var terminal, exit *contract.Event
	var terminalAt time.Time
	for rows.Next() {
		var id, producer, slot, hash, at, raw string
		var sequence, eventRevision int64
		if e = rows.Scan(&id, &producer, &sequence, &eventRevision, &slot, &hash, &at, &raw); e != nil {
			break
		}
		var ev contract.Event
		received, re := hostRetentionTime(at)
		if len(vector) >= 512 || len(raw) > 65536 || re != nil || contract.DecodeOwnerRuntimeStrict([]byte(raw), &ev) != nil || ev.Version != 1 || ev.EventID != id || ev.ProducerID != producer || producer != r.producer || ev.Sequence != sequence || sequence != eventRevision || ev.TaskID != r.task || ev.AttemptID != r.attempt || ev.SegmentID != r.segment || ev.RunID != r.run || ev.CommandID != command || ev.WorkRevision != revision || ev.ExecutionEpoch == 0 || ev.PayloadHash != hash || !validDigest(hash) || ev.EventRevision != 0 && ev.EventRevision != eventRevision || ev.ActionSlot != "" && ev.ActionSlot != slot {
			e = CodeError("host_retention_terminal_unverified")
			break
		}
		if contract.ValidateTemporaryWorkspaceCleanupEvent(ev) != nil {
			e = CodeError("host_retention_terminal_unverified")
			break
		}
		vector = append(vector, raw+"\x00"+at+"\x00"+slot)
		switch ev.Kind {
		case contract.EventResult, contract.EventFailed, contract.EventStopped, contract.EventQuestion, contract.EventLaunchRejected:
			if terminal != nil || exit != nil {
				e = CodeError("host_retention_terminal_unverified")
				break
			}
			copy := ev
			copy.EventRevision, copy.ActionSlot = eventRevision, slot
			terminal = &copy
			terminalAt = received
		case contract.EventExited:
			if exit != nil || terminal == nil {
				e = CodeError("host_retention_terminal_unverified")
				break
			}
			copy := ev
			exit = &copy
			f.received = received
		case contract.EventUnknown:
			e = CodeError("host_retention_terminal_unverified")
		case contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventSession, contract.EventTemporaryWorkspaceCleanup:
		default:
			e = CodeError("host_retention_terminal_unverified")
		}
		if e != nil {
			break
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return f, e
	}
	if terminal == nil || terminal.Kind != outcome {
		return f, CodeError("host_retention_terminal_unverified")
	}
	if outcome == contract.EventLaunchRejected {
		if status != "not_started" || exit != nil || terminal.Failure == nil || !terminal.Failure.ValidPrelaunchProbe() && !terminal.Failure.ValidValidationStart() {
			return f, CodeError("host_retention_terminal_unverified")
		}
		f.received = terminalAt
	} else if status != "exited" || exit == nil {
		return f, CodeError("host_retention_terminal_unverified")
	}
	f.event = *terminal
	exitOK := exit != nil && exit.ExitCode != nil && *exit.ExitCode == 0
	if outcome == contract.EventResult && exit != nil && exit.ExitCode == nil && revision == currentRevision {
		target, err := filterTargetTask(ctx, tx, r.task)
		if err != nil {
			return f, err
		}
		exitOK = target && exit.GitFilterCleanup != nil && exit.GitFilterCleanup.Validate() == nil && exit.ActiveMS > 0
	}
	if outcome == contract.EventResult && exitOK && taskStatus == "completed" && revision == currentRevision {
		f.success = true
		if completion == "owner_review" {
			var accepted int
			e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_decisions WHERE task_id=? AND event_id=? AND event_revision=? AND event_hash=? AND action_slot=? AND decision='accept'`, r.task, terminal.EventID, terminal.EventRevision, terminal.PayloadHash, terminal.ActionSlot).Scan(&accepted)
			if e != nil {
				return
			}
			f.success = accepted == 1
		}
	}
	// Exact handled delivery consumes this segment's progress as superseded by
	// its terminal outcome. Progress itself does not acquire a synthetic ACK.
	var consumed string
	e = tx.QueryRowContext(ctx, `SELECT d.created_at FROM runtime_events e JOIN runtime_decisions d ON d.event_id=e.event_id AND d.event_revision=e.event_revision AND d.event_hash=e.payload_hash AND d.action_slot=e.action_slot AND d.decision='handled' WHERE e.event_id=? AND e.delivery_status='acked'`, terminal.EventID).Scan(&consumed)
	if errors.Is(e, sql.ErrNoRows) && terminal.Kind == contract.EventQuestion {
		// waiting_user alone is a receipt, not consumption. An exact claimed
		// continuation whose target has ended proves the answer was consumed.
		e = tx.QueryRowContext(ctx, `SELECT max(d.created_at,c.created_at) FROM runtime_events e JOIN runtime_decisions d ON d.event_id=e.event_id AND d.event_revision=e.event_revision AND d.event_hash=e.payload_hash AND d.action_slot=e.action_slot AND d.decision='waiting_user' JOIN continuation_decisions c ON c.source_kind='host' AND c.source_event_id=e.event_id AND c.source_event_revision=e.event_revision AND c.source_event_sha256=e.payload_hash AND c.action_slot=e.action_slot AND c.source_segment_id=e.segment_id AND c.attempt_id=e.attempt_id AND c.task_id=e.task_id JOIN segment_runtime s ON s.segment_id=c.target_segment_id JOIN budget_runtime b ON b.segment_id=s.segment_id WHERE e.event_id=? AND e.delivery_status='acked' AND c.status='claimed' AND s.status IN ('exited','not_started') AND b.status='settled'`, terminal.EventID).Scan(&consumed)
	}
	if errors.Is(e, sql.ErrNoRows) {
		return f, CodeError("host_retention_unconsumed")
	}
	if e != nil {
		return
	}
	f.consumed, e = hostRetentionTime(consumed)
	if e != nil {
		return
	}
	raw, _ := json.Marshal(vector)
	f.vector = runtimeHash(string(raw))
	return
}

func hostRetentionFacts(ctx context.Context, tx *sql.Tx, r hostRetentionRow, binding string, now time.Time) (string, error) {
	// Legacy unregistered slots have no creation authority. Do not prepare a
	// permanent run fence before knowing every slot has a saved owned proof.
	if r.mode == "absent_only" && len(r.refs) != r.slots {
		return "", CodeError("host_retention_legacy_unsealed")
	}
	var unsafe int
	e := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM tasks WHERE run_id=? AND status NOT IN ('completed','failed','cancelled'))
 OR EXISTS(SELECT 1 FROM tasks t JOIN attempts a ON a.task_id=t.id JOIN segments s ON s.attempt_id=a.id LEFT JOIN segment_runtime sr ON sr.segment_id=s.id LEFT JOIN budget_runtime b ON b.segment_id=s.id WHERE t.run_id=? AND (s.status NOT IN ('exited','not_started') OR sr.status IS NULL OR sr.status!=s.status OR b.status IS NULL OR b.status!='settled'))
 OR EXISTS(SELECT 1 FROM runtime_questions q JOIN tasks t ON t.id=q.task_id WHERE t.run_id=? AND q.status IN ('open','resume_queued','resume_started'))
 OR EXISTS(SELECT 1 FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id=? AND e.delivery_status IN ('pending','pending_session'))
 OR EXISTS(SELECT 1 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.run_id=? AND e.delivery_status IN ('pending','pending_session'))
 OR EXISTS(SELECT 1 FROM managed_artifact_intakes WHERE run_id=? AND phase='prepared')
 OR EXISTS(SELECT 1 FROM continuation_decisions c LEFT JOIN segment_runtime s ON s.segment_id=c.target_segment_id WHERE c.run_id=? AND (c.status='queued' OR s.status IS NULL OR s.status NOT IN ('exited','not_started')))
 OR EXISTS(SELECT 1 FROM decision_attempt_bindings d JOIN tasks t ON t.id=d.task_id WHERE t.run_id=? AND d.attempt_id IS NULL AND d.retired=0)
 OR EXISTS(SELECT 1 FROM new_base_lineage l JOIN tasks t ON t.run_id=l.run_id WHERE l.source_run_id=? AND t.status NOT IN ('completed','failed','cancelled'))
 OR EXISTS(SELECT 1 FROM host_artifact_pins WHERE reservation_id=? AND state='active')`, r.run, r.run, r.run, r.run, r.run, r.run, r.run, r.run, r.run, r.id).Scan(&unsafe)
	if e != nil {
		return "", e
	}
	if unsafe != 0 {
		return "", CodeError("host_retention_pinned")
	}
	terminal, e := loadHostTerminalFacts(ctx, tx, r)
	if e != nil {
		return "", e
	}
	cutoff, e := hostRetentionTime(r.created)
	if e != nil {
		return "", e
	}
	if terminal.received.After(cutoff) {
		cutoff = terminal.received
	}
	if terminal.consumed.After(cutoff) {
		cutoff = terminal.consumed
	}
	// A consumer may finish well after the source. Start no earlier than the
	// final received lifecycle record of this now-quiescent run.
	rows, e := tx.QueryContext(ctx, `SELECT e.created_at FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id=? AND json_extract(e.body_json,'$.kind') IN ('exited','launch_rejected') ORDER BY e.producer_id,e.sequence LIMIT 4097`, r.run)
	if e != nil {
		return "", e
	}
	var runTerminals []string
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			break
		}
		var at time.Time
		at, e = hostRetentionTime(raw)
		if e != nil || len(runTerminals) >= 4096 {
			e = CodeError("host_retention_terminal_unverified")
			break
		}
		runTerminals = append(runTerminals, raw)
		if at.After(cutoff) {
			cutoff = at
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return "", e
	}
	ttl := 30 * 24 * time.Hour
	resolution := ""
	if !terminal.success {
		var resolved string
		e = tx.QueryRowContext(ctx, `SELECT json_array(controller,token_hash,binding_hash,request_id,reason,resolution,created_at),created_at FROM host_artifact_resolutions WHERE reservation_id=? AND run_id=? AND binding_hash=?`, r.id, r.run, binding).Scan(&resolution, &resolved)
		if errors.Is(e, sql.ErrNoRows) {
			return "", CodeError("host_retention_unresolved")
		}
		if e != nil {
			return "", e
		}
		at, e := hostRetentionTime(resolved)
		if e != nil {
			return "", e
		}
		if at.After(cutoff) {
			cutoff = at
		}
		ttl = 7 * 24 * time.Hour
	}
	if !cutoff.Add(ttl).Before(now) {
		return "", CodeError("host_retention_not_expired")
	}
	refs := make([]string, r.slots)
	for index, ref := range r.refs {
		refs[index] = ref.raw
	}
	raw, e := json.Marshal([]any{r.id, r.run, r.task, r.attempt, r.segment, r.producer, r.class, r.scope, r.created, r.mode, r.namespace, refs, terminal.vector, terminal.success, terminal.consumed.UTC().Format(time.RFC3339Nano), runTerminals, resolution})
	if e != nil {
		return "", e
	}
	return runtimeHash(string(raw)), nil
}
