package store

// Recovery-sequence matrix. Expectations are the decisions.go QueueResume /
// scheduling.go dispatch contract plus docs/runtime-protocols.md ("协调器重启
// 后 Host 必须先用相同 launch_id + token + generation 重连并补发持久化 spool,
// owner 再显式 resume"; recovery is per interruption round, not lifetime-once).
//
// Coverage map (required behavior -> test):
//  - late events from an offline Host are rejected without ledger change:
//    mapped -> TestLateHostEventsRejectedAfterOfflineAndEpochAdvance
//  - events stamped with an old coordinator epoch are rejected:
//    mapped -> same test
//  - QueueResume is single-shot per round (while queued or dispatched):
//    mapped -> TestQueueResumeIsSingleShot; re-asserted inline below as the
//    refusal path around each allowed round
//  - profile_timeout interruption is never resumable, candidate review
//    interruption refuses plain resume: mapped ->
//    TestProfileInterruptionNeverOffersOrQueuesResume and
//    TestCandidateReviewResumeRejectedBeforeQueue
//  - after a resume was dispatched and the new segment is interrupted again,
//    the next explicit resume round is allowed: NEW ->
//    TestRecoveryMatrixNextExplicitRoundAllowedAfterReinterruption
//  - recovery flow interrupted by a coordinator restart after the resume
//    dispatch: PendingLaunches replays the same grant without a second
//    reservation, resume stays guarded, and the next round is allowed once
//    the replayed segment is interrupted again: NEW ->
//    TestRecoveryMatrixCrashAfterResumeDispatchReplaysGuardedRound
//  - an unknown segment (stop never confirmed) is never implicitly
//    resumable: NEW refusal assertion in
//    TestRecoveryMatrixUnknownIsNotResumable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type resumeMatrix struct {
	db   *DB
	host string
	path string
	seq  int64
}

func newResumeMatrix(t *testing.T) *resumeMatrix {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: []byte(`{}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner", HostGeneration: "gen", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	m := &resumeMatrix{db: db, host: host, path: path}
	// Double close is harmless; this only guards early t.Fatal leaks.
	t.Cleanup(func() { _ = m.db.Close() })
	return m
}

// nextSequence re-reads the durable producer watermark so a restarted
// coordinator continues where the pre-crash spool stopped.
func (m *resumeMatrix) nextSequence(ctx context.Context, t *testing.T) {
	t.Helper()
	var last int64
	if err := m.db.sql.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM runtime_events WHERE producer_id=?`, m.host).Scan(&last); err != nil {
		t.Fatal(err)
	}
	m.seq = last
}

func (m *resumeMatrix) send(ctx context.Context, t *testing.T, grant contract.LaunchCommand, kind string, activeMS int64) {
	t.Helper()
	m.seq++
	e := contract.Event{Version: 1, ProducerID: m.host, EventID: fmt.Sprintf("r-%d", m.seq), RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, Sequence: m.seq, Kind: kind, PayloadHash: fmt.Sprintf("rhash-%d", m.seq), ActiveMS: activeMS}
	wire, _ := json.Marshal(e)
	var decoded contract.Event
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.CommitHostEvent(ctx, decoded); err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
}

// interruptRound drives one segment through stopped+exited with a hard-coded
// measured usage and asserts the segment-level settlement contract value.
func (m *resumeMatrix) interruptRound(ctx context.Context, t *testing.T, grant contract.LaunchCommand, measured int64) {
	t.Helper()
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventStopped} {
		m.send(ctx, t, grant, kind, 0)
	}
	m.send(ctx, t, grant, contract.EventExited, measured)
	var status string
	var used, granted int64
	if err := m.db.sql.QueryRow(`SELECT status,used_ms,granted_ms FROM budget_runtime WHERE segment_id=?`, grant.SegmentID).Scan(&status, &used, &granted); err != nil {
		t.Fatal(err)
	}
	if status != "settled" || used != measured || granted != 1000 {
		t.Fatalf("round settlement: status=%s used=%d granted=%d want settled/%d/1000", status, used, granted, measured)
	}
}

func (m *resumeMatrix) taskStatus(ctx context.Context, t *testing.T) string {
	t.Helper()
	var status string
	if err := m.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// Required gap: interruption after a dispatched resume does not exhaust the
// recovery contract. Each explicit round guards its own dispatch (single-shot
// per round) and a fresh QueueResume is allowed only after the previous round
// is interrupted again, with per-segment ledgers.
func TestRecoveryMatrixNextExplicitRoundAllowedAfterReinterruption(t *testing.T) {
	ctx := context.Background()
	m := newResumeMatrix(t)
	defer m.db.Close()
	first, err := m.db.ClaimReady(ctx, m.host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	m.interruptRound(ctx, t, first, 500)
	if got := m.taskStatus(ctx, t); got != "interrupted" {
		t.Fatalf("status=%s", got)
	}
	if err = m.db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatal(err)
	}
	// Guarded refusal while this round is only queued.
	if err = m.db.QueueResume(ctx, "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued-round resume err=%v", err)
	}
	second, err := m.db.ClaimReady(ctx, m.host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.SegmentID == first.SegmentID || second.AttemptID != first.AttemptID {
		t.Fatalf("resume dispatch changed segment/attempt identity: %+v", second)
	}
	// Guarded refusal while the resumed round is live.
	if err = m.db.QueueResume(ctx, "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("dispatched-round resume err=%v", err)
	}
	m.interruptRound(ctx, t, second, 600)
	// The contract allows the next explicit round after a new interruption.
	if err = m.db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatalf("second explicit resume round rejected: %v", err)
	}
	if got := m.taskStatus(ctx, t); got != "resume_queued" {
		t.Fatalf("status=%s want resume_queued", got)
	}
	third, err := m.db.ClaimReady(ctx, m.host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if third.SegmentID == second.SegmentID || third.AttemptID != first.AttemptID {
		t.Fatalf("third round identity: %+v", third)
	}
	// Independent hard-coded ledger expectations for the three segments.
	want := []struct {
		status string
		used   int64
	}{{"settled", 500}, {"settled", 600}, {"reserved", 0}}
	got := []struct {
		status string
		used   int64
	}{}
	for _, segmentID := range []string{first.SegmentID, second.SegmentID, third.SegmentID} {
		var status string
		var used int64
		if err = m.db.sql.QueryRow(`SELECT status,used_ms FROM budget_runtime WHERE segment_id=?`, segmentID).Scan(&status, &used); err != nil {
			t.Fatal(err)
		}
		got = append(got, struct {
			status string
			used   int64
		}{status, used})
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment %d ledger=%+v want %+v", i, got[i], want[i])
		}
	}
	var rows int
	if err = m.db.sql.QueryRow(`SELECT COUNT(*) FROM budget_runtime WHERE budget_group_id='task'`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("reservation rows=%d err=%v want exactly one per segment", rows, err)
	}
}

// Required gap: the recovery flow itself is interrupted by a coordinator
// restart right after the resume was dispatched. The restart must replay the
// same durable grant without a second reservation, keep refusing another
// resume while the round is live, and still allow the next explicit round
// once the replayed segment is interrupted again.
func TestRecoveryMatrixCrashAfterResumeDispatchReplaysGuardedRound(t *testing.T) {
	ctx := context.Background()
	m := newResumeMatrix(t)
	first, err := m.db.ClaimReady(ctx, m.host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	m.interruptRound(ctx, t, first, 500)
	if err = m.db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatal(err)
	}
	second, err := m.db.ClaimReady(ctx, m.host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Simulated coordinator crash between dispatch and any host event.
	if err = m.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(m.path)
	if err != nil {
		t.Fatal(err)
	}
	m.db = db
	defer db.Close()
	m.nextSequence(ctx, t)
	// The dispatched round survives: one reservation, still reserved.
	var rows int
	var status string
	var used int64
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM budget_runtime WHERE segment_id=?`, second.SegmentID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("restarted reservations=%d err=%v", rows, err)
	}
	if err = db.sql.QueryRow(`SELECT status,used_ms FROM budget_runtime WHERE segment_id=?`, second.SegmentID).Scan(&status, &used); err != nil || status != "reserved" || used != 0 {
		t.Fatalf("restarted reservation: status=%s used=%d err=%v", status, used, err)
	}
	replay, err := db.PendingLaunches(ctx, m.host, 1)
	if err != nil || len(replay) != 1 || replay[0].SegmentID != second.SegmentID || replay[0].CommandID != second.CommandID || replay[0].LaunchIntentHash != second.LaunchIntentHash {
		t.Fatalf("dispatched resume did not replay intact: %+v err=%v", replay, err)
	}
	if err = db.QueueResume(ctx, "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("resume during replayed live round err=%v", err)
	}
	m.interruptRound(ctx, t, second, 250)
	if got := m.taskStatus(ctx, t); got != "interrupted" {
		t.Fatalf("status=%s", got)
	}
	if err = db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatalf("explicit resume after recovered round rejected: %v", err)
	}
	// Hard-coded ledger after the recovered round: two settled segments; the
	// freshly queued round has no reservation yet.
	if status, used, granted := ledgerBudgetRow(t, db, first.SegmentID); status != "settled" || used != 500 || granted != 1000 {
		t.Fatalf("round-1 row: status=%s used=%d granted=%d", status, used, granted)
	}
	if status, used, granted := ledgerBudgetRow(t, db, second.SegmentID); status != "settled" || used != 250 || granted != 1000 {
		t.Fatalf("recovered row: status=%s used=%d granted=%d", status, used, granted)
	}
	if got := ledgerGroupCharged(t, db, "task"); got != 750 {
		t.Fatalf("group charged=%d want %d (queued round still holds no reservation)", got, 750)
	}
}

// Product-unsupported path asserted as refusal: a stop whose terminal
// observation is unknown never becomes silently resumable; QueueResume keeps
// refusing until a real exited settles the round.
func TestRecoveryMatrixUnknownIsNotResumable(t *testing.T) {
	ctx := context.Background()
	m := newResumeMatrix(t)
	defer m.db.Close()
	grant, err := m.db.ClaimReady(ctx, m.host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
		m.send(ctx, t, grant, kind, 0)
	}
	if _, err = m.db.RequestStop(ctx, "task", 1, "owner_stop_requested"); err != nil {
		t.Fatal(err)
	}
	if err = m.db.MarkStoppingUnknown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.taskStatus(ctx, t); got != "unknown" {
		t.Fatalf("status=%s want unknown", got)
	}
	if err = m.db.QueueResume(ctx, "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown resume err=%v want refusal", err)
	}
	if status, used, granted := ledgerBudgetRow(t, m.db, grant.SegmentID); status != "reserved" || used != 0 || granted != 1000 {
		t.Fatalf("unknown round ledger: status=%s used=%d granted=%d", status, used, granted)
	}
	// Only a genuine exited closes the round; then recovery becomes possible.
	m.send(ctx, t, grant, contract.EventExited, 400)
	if status, used, granted := ledgerBudgetRow(t, m.db, grant.SegmentID); status != "settled" || used != 400 || granted != 1000 {
		t.Fatalf("post-exit ledger: status=%s used=%d granted=%d", status, used, granted)
	}
	if err = m.db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatalf("resume after real settlement rejected: %v", err)
	}
}
