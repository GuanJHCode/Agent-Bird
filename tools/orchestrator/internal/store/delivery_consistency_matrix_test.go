package store

// Result-delivery and candidate-version consistency matrix (task C-1).
// All expectations in this file are independent contract values (error codes
// and ledger fingerprints) hardcoded here, never computed from the delivery
// implementation.
//
// Coverage map (required behavior -> test):
//
//  1. Sequencing spawn->result->exited->collect->ack->accept:
//     NEW -> TestDeliveryMatrixCollectSequencing (accept/ack refused before
//     the result settles, collect before the result mints no receipt, ack
//     before collect refused, accept requires the exited-settled
//     result_ready state).
//     - ack replay idempotency and conflicting-replay refusal: NEW (same
//     test, ack replay section). Conflicting decision on one slot is
//     additionally mapped -> TestRetryUsesOnlyPreauthorizedFallbackAndAcceptReleasesDependency.
//     - accept replay idempotency: NEW (same test). Old-accept replay after
//     rework is mapped -> TestReworkRevisesWholeCandidateChainAndReplaysWithoutRepeating
//     and re-asserted in TestDeliveryMatrixReworkedRevisionRejections.
//     - wire-level collect/ack/accept routes are mapped ->
//     coordinator.TestCollectionReceiptDoesNotAcceptBusinessResult and
//     coordinator.TestServerRoutesControlToBoundHostAndDurablyCollects.
//  2. Proof-mode / forged delivery / tampered bindings:
//     - collect-mode historyProof or malformed collectionProof ->
//     delivery_proof_mode_mismatch: NEW.
//     - native-mode collectionProof -> delivery_proof_mode_mismatch: NEW ->
//     TestDeliveryMatrixNativeProofMode.
//     - collect mode: a delivery id that is not a server-minted receipt
//     (unknown/forged), a tampered event hash, a dropped or extra binding ->
//     conflict: NEW. In native mode the delivery id is caller-minted
//     bookkeeping, so an id the ledger has never seen is not itself a
//     conflict; the binding that must hold there is id -> (task, history
//     proof, decisions hash), asserted by the re-affirm and tampered-hash
//     cases below.
//     - refusal leaves the ledger and every event byte-identical: NEW
//     (ledgerFingerprint equality).
//     - collect-mode receipt identity is the proof digest, not the id: a
//     well-formed wrong digest (either case) on a real receipt id is refused
//     and the digest comparison is case-insensitive both at the receipt and
//     at the durable delivery-ack layer: NEW ->
//     TestDeliveryMatrixProofDigestMismatchRejected.
//     - a decided action slot cannot be replayed as another task's
//     acceptance: the idempotent-replay branch must match task and event
//     identity, not only the slot: NEW ->
//     TestDeliveryMatrixCrossTaskSlotReplayIsRejected (task identity) and
//     TestDeliveryMatrixDecidedSlotRequiresExactEventIdentity (event
//     identity on the same task).
//     - wire-level forged receipt is mapped ->
//     coordinator.TestCollectionReceiptDoesNotAcceptBusinessResult.
//  3. Non-result events cannot be accepted as success (failed/exited/late
//     rows): NEW -> TestDeliveryMatrixCollectSequencing ("non-result and
//     post-completion acceptance") and TestDeliveryMatrixReworkedRevisionRejections.
//     Superseded host events: mapped ->
//     TestReworkFencesOldHostEventsButKeepsDurableReplay,
//     TestReworkLateOldHostEventIsHistoricalAndACKable.
//  4. Stale acceptance: wrong work_revision or already-completed task ->
//     conflict: NEW -> both tests above.
//  5. After rework, old artifact/old candidate/old segment references are
//     refused on the new revision: mapped ->
//     TestReworkRejectsUnsafeOrStaleChangesAtomically (stale_candidate,
//     tampered_artifact, old_revision), TestReworkDecisionCannotReplayAsOrdinaryRetry,
//     TestCandidateReviewRetryFencesEarlierSegmentOfRetiredAttempt.
//     Post-rework ack/accept bookkeeping on the new revision is the NEW gap
//     covered by TestDeliveryMatrixReworkedRevisionRejections.
//  6. Candidate version freeze / drift refusal is strongly covered in the
//     gitops package: mapped -> TestComposeCandidateFreezesOrderedDependencyComposition,
//     TestIntegrateTargetDriftIsUnknownAndDoesNotRetry,
//     TestPrivateRefRequiresManagedNamespaceAndCompareAndSwap,
//     TestIntegrateRequiresReviewBindingAndUsesProtectedFastForward.
//     Store-side candidate binding: mapped ->
//     TestCandidateInputBindsAcceptedHostResultAndReplay.
//  7. Late exited (fresh event id) after the task completed: the exit is
//     durable history the Host may re-spool (CommitHostEvent returns a durable
//     ACK, and an identical replay stays a no-op), yet it neither re-settles
//     the budget nor touches any delivery/acceptance row, is not collectible,
//     and can never be accepted or delivery-acked: NEW ->
//     TestDeliveryMatrixLateExitedIsHistoryWithoutAcceptanceChange. Its
//     settlement non-refund invariant is cross-checked against
//     TestLedgerMatrixLateOldAttemptExitIsHistoryAndCannotRefund, and the
//     "an exited row is never an acceptance target" half is asserted again in
//     TestDeliveryMatrixCollectSequencing and
//     TestDeliveryMatrixReworkedRevisionRejections.
//  8. Incomplete diagnostics are never accepted as success: with
//     completion_policy=artifact the settlement may only complete the task on
//     an exact digest match, and while the task still waits in result_ready
//     (artifact missing or mismatched) an owner *accept* is refused as a
//     conflict while *reject* stays available: NEW ->
//     TestDeliveryMatrixArtifactPolicyGate.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Hardcoded contract codes (verified against docs/runtime-protocols.md and
// the store package contracts, not copied from implementation helpers).
const (
	contractConflict      = "conflict"
	contractModeMismatch  = "delivery_proof_mode_mismatch"
	contractInvalidAck    = "invalid_ack"
	contractInvalidReview = "invalid_review"
	contractAccepted      = "accepted"
)

type deliveryFixture struct {
	db     *DB
	hostID string
	grant  contract.LaunchCommand
	seq    int64
}

// newDeliveryFixture submits a single-task run; each tune mutates that task's
// spec before the plan is submitted (completion policy, budget, ...).
func newDeliveryFixture(t *testing.T, mode string, tune ...func(*TaskSpec)) *deliveryFixture {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	spec := PlanSpec{
		Run:   RunSpec{DeliveryMode: mode, ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"},
		Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3}},
	}
	for _, apply := range tune {
		apply(&spec.Tasks[0])
	}
	receipt, err := db.SubmitPlan(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host-birth", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	return &deliveryFixture{db: db, hostID: hostID, grant: grant}
}

func (f *deliveryFixture) commit(t *testing.T, kind, eventID string) contract.Event {
	t.Helper()
	f.seq++
	e := contract.Event{Version: 1, ProducerID: f.hostID, EventID: eventID, RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.seq, Kind: kind, PayloadHash: fmt.Sprintf("%064x", f.seq)}
	if kind == contract.EventExited {
		e.ActiveMS = 1000
		// Merge adaptation carried from the candidate lineage: the store only
		// settles a durable result as result_ready for an explicitly zero exit
		// code, so a fixture that intends a clean exit must say so.
		zero := 0
		e.ExitCode = &zero
	}
	if _, err := f.db.CommitHostEvent(context.Background(), e); err != nil {
		t.Fatalf("commit %s: %v", kind, err)
	}
	return e
}

func (f *deliveryFixture) taskStatus(t *testing.T, taskID string) string {
	t.Helper()
	return reworkTaskStatus(t, f.db, taskID)
}

// ledgerFingerprint captures the whole delivery/acceptance ledger state. A
// refused command must leave this string byte-identical.
func (f *deliveryFixture) ledgerFingerprint(t *testing.T) string {
	t.Helper()
	var receipts, collectionRows, decisions, reviewRows int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM delivery_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM collection_receipts`).Scan(&collectionRows); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM runtime_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM review_decisions`).Scan(&reviewRows); err != nil {
		t.Fatal(err)
	}
	var events strings.Builder
	rows, err := f.db.sql.Query(`SELECT event_id,delivery_status FROM runtime_events ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err = rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&events, "%s=%s;", id, status)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("task=%s;receipts=%d;collections=%d;decisions=%d;reviews=%d;events=%s", f.taskStatus(t, "task"), receipts, collectionRows, decisions, reviewRows, events.String())
}

func deliveryErrCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
}

func reworkTaskStatus(t *testing.T, db *DB, taskID string) string {
	t.Helper()
	var status string
	if err := db.sql.QueryRow(`SELECT status FROM tasks WHERE id=?`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// eventLedgerRow reads the stored identity triple needed to ack/accept an
// event; the identity itself is ledger data, not an expectation.
func eventLedgerRow(t *testing.T, db *DB, eventID string) (contract.Event, int64, string) {
	t.Helper()
	var e contract.Event
	var revision int64
	var slot, body string
	if err := db.sql.QueryRow(`SELECT event_revision,action_slot,body_json FROM runtime_events WHERE event_id=?`, eventID).Scan(&revision, &slot, &body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatal(err)
	}
	return e, revision, slot
}

func TestDeliveryMatrixCollectSequencing(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t, "collect")
	const fingerprintSpawned = "task=running;receipts=0;collections=0;decisions=0;reviews=0;events="
	if got := f.ledgerFingerprint(t); got != fingerprintSpawned {
		t.Fatalf("spawned fingerprint=%s", got)
	}

	// 1. Before any result exists accept and ack have nothing to settle.
	_, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: "missing", EventRevision: 1, EventHash: strings.Repeat("a", 64), ActionSlot: "missing-slot", Decision: "accept", CommandID: "early-accept"})
	deliveryErrCode(t, err, contractConflict)
	err = f.db.AckDelivery(ctx, "task", "collection-forged", "", strings.Repeat("b", 64), []AckDecision{{EventID: "missing", EventRevision: 1, EventHash: strings.Repeat("a", 64), ActionSlot: "missing-slot", Decision: "handled", CommandID: "early-ack"}})
	deliveryErrCode(t, err, contractConflict)
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("pre-result collect=%+v err=%v", page, err)
	}
	id, proof, err := f.db.CollectionReceipt(ctx, "task", page.Events)
	if err != nil || id != "" || proof != "" {
		t.Fatalf("empty page must not mint a receipt: %q %q %v", id, proof, err)
	}
	if got := f.ledgerFingerprint(t); got != fingerprintSpawned {
		t.Fatalf("refused pre-result commands perturbed the ledger:\nwant %s\ngot  %s", fingerprintSpawned, got)
	}

	// 2. Result committed but not settled by exited: collectible, not
	//    acceptable (owner_review completes only via ReviewResult).
	result := f.commit(t, contract.EventResult, "event-result")
	if status := f.taskStatus(t, "task"); status != "running" {
		t.Fatalf("pre-exit task status=%s", status)
	}
	page, err = f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("pending after result=%+v err=%v", page, err)
	}
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "accept", CommandID: "pre-exit-accept"})
	deliveryErrCode(t, err, contractConflict)

	// A second actionable event grows the page; the receipt binds the exact
	// delivered page and nothing else.
	f.commit(t, contract.EventFailed, "event-failed")
	page, err = f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("pending page=%+v err=%v", page, err)
	}
	id, proof, err = f.db.CollectionReceipt(ctx, "task", page.Events)
	if err != nil || id == "" {
		t.Fatalf("receipt: %q %v", id, err)
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(proof) {
		t.Fatalf("collection proof is not a lowercase hex digest: %q", proof)
	}
	otherID, otherProof, err := f.db.CollectionReceipt(ctx, "task", []contract.Event{page.Events[0]})
	if err != nil || otherID == id || otherProof == proof {
		t.Fatalf("receipt not page-bound: %q/%q vs %q/%q", id, proof, otherID, otherProof)
	}
	replayID, replayProof, err := f.db.CollectionReceipt(ctx, "task", page.Events)
	if err != nil || replayID != id || replayProof != proof {
		t.Fatalf("receipt not deterministic for the same page: %q/%q %v", replayID, replayProof, err)
	}
	decisions := []AckDecision{
		{EventID: page.Events[0].EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "handled", CommandID: "ack-result"},
		{EventID: page.Events[1].EventID, EventRevision: page.Events[1].EventRevision, EventHash: page.Events[1].PayloadHash, ActionSlot: page.Events[1].ActionSlot, Decision: "handled", CommandID: "ack-failed"},
	}

	// 3. Proof-mode and binding refusals, each with a zero-perturbation check.
	//    The receipt still holds the two-event page here, but the ledger rows
	//    are: result(seq1) + failed(seq2) pending; collections=2 (page and
	//    half-page receipts minted above).
	before := f.ledgerFingerprint(t)
	err = f.db.AckDelivery(ctx, "task", id, strings.Repeat("c", 64), "", decisions)
	deliveryErrCode(t, err, contractModeMismatch)
	err = f.db.AckDelivery(ctx, "task", id, "", "not-a-digest", decisions)
	deliveryErrCode(t, err, contractModeMismatch)
	err = f.db.AckDelivery(ctx, "task", "collection-"+strings.Repeat("d", 64), "", proof, decisions)
	deliveryErrCode(t, err, contractConflict)
	tampered := append([]AckDecision(nil), decisions...)
	tampered[0].EventHash = strings.Repeat("0", 64)
	err = f.db.AckDelivery(ctx, "task", id, "", proof, tampered)
	deliveryErrCode(t, err, contractConflict)
	err = f.db.AckDelivery(ctx, "task", id, "", proof, decisions[:1])
	deliveryErrCode(t, err, contractConflict)
	forgedSlot := append([]AckDecision(nil), decisions...)
	forgedSlot[1].ActionSlot = "action-forged"
	err = f.db.AckDelivery(ctx, "task", id, "", proof, forgedSlot)
	deliveryErrCode(t, err, contractConflict)
	if got := f.ledgerFingerprint(t); got != before {
		t.Fatalf("refused acks perturbed the ledger:\nwant %s\ngot  %s", before, got)
	}

	// The valid ack still succeeds after every refusal: refusals do not
	// consume the receipt.
	if err = f.db.AckDelivery(ctx, "task", id, "", proof, decisions); err != nil {
		t.Fatalf("valid ack refused: %v", err)
	}
	if err = f.db.AckDelivery(ctx, "task", id, "", proof, decisions); err != nil {
		t.Fatalf("ack replay not idempotent: %v", err)
	}
	changed := append([]AckDecision(nil), decisions...)
	changed[1].CommandID = "ack-failed-renewed"
	err = f.db.AckDelivery(ctx, "task", id, "", proof, changed)
	deliveryErrCode(t, err, contractConflict)
	conflicting := append([]AckDecision(nil), decisions...)
	conflicting[1].Decision = "stale"
	err = f.db.AckDelivery(ctx, "task", id, "", proof, conflicting)
	deliveryErrCode(t, err, contractConflict)
	const fingerprintAcked = "task=running;receipts=1;collections=2;decisions=2;reviews=0;events=event-failed=acked;event-result=acked;"
	if got := f.ledgerFingerprint(t); got != fingerprintAcked {
		t.Fatalf("post-ack fingerprint:\nwant %s\ngot  %s", fingerprintAcked, got)
	}

	// 4. Exited settles the result; accept then completes the task exactly
	//    once, and identical replays are no-ops regardless of CommandID.
	f.commit(t, contract.EventExited, "event-exited")
	if status := f.taskStatus(t, "task"); status != "result_ready" {
		t.Fatalf("post-exit task status=%s", status)
	}
	status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "accept", CommandID: "accept-1"})
	if err != nil || status != contractAccepted {
		t.Fatalf("accept status=%q err=%v", status, err)
	}
	if status, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "accept", CommandID: "accept-renewed-command"}); err != nil || status != contractAccepted {
		t.Fatalf("accept replay status=%q err=%v", status, err)
	}
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "reject", CommandID: "accept-flip"})
	deliveryErrCode(t, err, contractConflict)
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision + 1, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "accept", CommandID: "accept-wrong-event-revision"})
	deliveryErrCode(t, err, contractConflict)
	// Contract: once a slot carries a decision, the slot identity
	// (event/hash/revision/decision) governs the replay; a differing
	// requested work_revision on the same decided slot is tolerated as an
	// idempotent no-op and never re-runs the transition. Genuine stale
	// work_revision refusals for undecided slots are asserted in
	// TestDeliveryMatrixReworkedRevisionRejections.
	if status, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 2, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "accept", CommandID: "accept-wrong-work-revision"}); err != nil || status != contractAccepted {
		t.Fatalf("decided-slot replay: %q %v", status, err)
	}
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: "action-forged", Decision: "accept", CommandID: "accept-wrong-slot"})
	deliveryErrCode(t, err, contractConflict)
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "complete", CommandID: "accept-bad-decision"})
	deliveryErrCode(t, err, contractInvalidReview)

	// 5. Non-result and post-completion rows can never be accepted as
	//    success. The exited ledger row, the failed event and a late result
	//    all stay outside the acceptance ledger.
	exitedEvent, exitedRevision, exitedSlot := eventLedgerRow(t, f.db, "event-exited")
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: exitedEvent.EventID, EventRevision: exitedRevision, EventHash: exitedEvent.PayloadHash, ActionSlot: exitedSlot, Decision: "accept", CommandID: "accept-exited"})
	deliveryErrCode(t, err, contractConflict)
	failedEvent, failedRevision, failedSlot := eventLedgerRow(t, f.db, "event-failed")
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: failedEvent.EventID, EventRevision: failedRevision, EventHash: failedEvent.PayloadHash, ActionSlot: failedSlot, Decision: "accept", CommandID: "accept-failed"})
	deliveryErrCode(t, err, contractConflict)
	late := f.commit(t, contract.EventResult, "event-late-result")
	lateEvent, lateRevision, lateSlot := eventLedgerRow(t, f.db, "event-late-result")
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: late.EventID, EventRevision: lateRevision, EventHash: lateEvent.PayloadHash, ActionSlot: lateSlot, Decision: "accept", CommandID: "accept-after-complete"})
	deliveryErrCode(t, err, contractConflict)

	const fingerprintFinal = "task=completed;receipts=1;collections=2;decisions=2;reviews=1;events=event-exited=internal;event-failed=acked;event-late-result=pending;event-result=acked;"
	if got := f.ledgerFingerprint(t); got != fingerprintFinal {
		t.Fatalf("final fingerprint:\nwant %s\ngot  %s", fingerprintFinal, got)
	}
}

func TestDeliveryMatrixNativeProofMode(t *testing.T) {
	// Contract: in native mode there is no server-minted collection receipt,
	// so CollectionReceipt stays empty and the ack is proved by the
	// caller-supplied history digest bound to the delivery id. A collection
	// proof on a native ack is refused as a mode mismatch, never silently
	// ignored.
	ctx := context.Background()
	f := newDeliveryFixture(t, "native")
	f.commit(t, contract.EventResult, "event-result")
	f.commit(t, contract.EventExited, "event-exited")
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("native pending=%+v err=%v", page, err)
	}
	id, proof, err := f.db.CollectionReceipt(ctx, "task", page.Events)
	if err != nil || id != "" || proof != "" {
		t.Fatalf("native mode must not mint collection receipts: %q %q %v", id, proof, err)
	}
	if status := f.taskStatus(t, "task"); status != "result_ready" {
		t.Fatalf("status=%s", status)
	}
	decisions := []AckDecision{{EventID: page.Events[0].EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "handled", CommandID: "native-ack"}}

	before := f.ledgerFingerprint(t)
	err = f.db.AckDelivery(ctx, "task", "delivery-native", strings.Repeat("a", 64), strings.Repeat("b", 64), decisions)
	deliveryErrCode(t, err, contractModeMismatch)
	err = f.db.AckDelivery(ctx, "task", "", strings.Repeat("a", 64), "", decisions)
	deliveryErrCode(t, err, contractInvalidAck)
	err = f.db.AckDelivery(ctx, "task", "delivery-native", "", "", decisions)
	deliveryErrCode(t, err, contractInvalidAck)
	err = f.db.AckDelivery(ctx, "task", "delivery-native", "zz", "", decisions)
	deliveryErrCode(t, err, contractInvalidAck)
	err = f.db.AckDelivery(ctx, "task", "delivery-native", strings.Repeat("a", 64), "", []AckDecision{})
	deliveryErrCode(t, err, contractInvalidAck)
	dup := append([]AckDecision(nil), decisions...)
	dup = append(dup, AckDecision{EventID: "other-event", EventRevision: 1, EventHash: strings.Repeat("c", 64), ActionSlot: decisions[0].ActionSlot, Decision: "stale", CommandID: "dup-slot"})
	err = f.db.AckDelivery(ctx, "task", "delivery-native", strings.Repeat("a", 64), "", dup)
	deliveryErrCode(t, err, contractInvalidAck)
	unknown := append([]AckDecision(nil), decisions...)
	unknown[0].Decision = "acknowledged"
	err = f.db.AckDelivery(ctx, "task", "delivery-native", strings.Repeat("a", 64), "", unknown)
	deliveryErrCode(t, err, contractInvalidAck)
	if got := f.ledgerFingerprint(t); got != before {
		t.Fatalf("refused native acks perturbed the ledger:\nwant %s\ngot  %s", before, got)
	}

	if err = f.db.AckDelivery(ctx, "task", "delivery-native", strings.Repeat("a", 64), "", decisions); err != nil {
		t.Fatalf("valid native ack refused: %v", err)
	}
	if err = f.db.AckDelivery(ctx, "task", "delivery-native", strings.Repeat("a", 64), "", decisions); err != nil {
		t.Fatalf("native ack replay not idempotent: %v", err)
	}
	err = f.db.AckDelivery(ctx, "task", "delivery-native", strings.Repeat("e", 64), "", decisions)
	deliveryErrCode(t, err, contractConflict)
	changed := append([]AckDecision(nil), decisions...)
	changed[0].EventHash = strings.Repeat("0", 64)
	err = f.db.AckDelivery(ctx, "task", "delivery-native-2", strings.Repeat("f", 64), "", changed)
	deliveryErrCode(t, err, contractConflict)
	// Re-affirming the identical decision under a fresh delivery id is a
	// durable-ack replay, not a second business effect.
	if err = f.db.AckDelivery(ctx, "task", "delivery-native-2", strings.Repeat("f", 64), "", decisions); err != nil {
		t.Fatalf("durable re-ack refused: %v", err)
	}
	const fingerprintNative = "task=result_ready;receipts=2;collections=0;decisions=1;reviews=0;events=event-exited=internal;event-result=acked;"
	if got := f.ledgerFingerprint(t); got != fingerprintNative {
		t.Fatalf("native fingerprint:\nwant %s\ngot  %s", fingerprintNative, got)
	}
}

func TestDeliveryMatrixAcceptIndependentOfAck(t *testing.T) {
	// Contract: the collection receipt is delivery bookkeeping, never a
	// business acceptance, and acceptance does not require the ack first.
	ctx := context.Background()
	f := newDeliveryFixture(t, "collect")
	result := f.commit(t, contract.EventResult, "event-result")
	f.commit(t, contract.EventExited, "event-exited")
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("pending=%+v err=%v", page, err)
	}
	status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "accept", CommandID: "accept-first"})
	if err != nil || status != contractAccepted {
		t.Fatalf("accept-before-ack status=%q err=%v", status, err)
	}
	const fingerprintAcceptedPending = "task=completed;receipts=0;collections=0;decisions=0;reviews=1;events=event-exited=internal;event-result=pending;"
	if got := f.ledgerFingerprint(t); got != fingerprintAcceptedPending {
		t.Fatalf("accept must not mark delivery acked:\nwant %s\ngot  %s", fingerprintAcceptedPending, got)
	}
	id, proof, err := f.db.CollectionReceipt(ctx, "task", page.Events)
	if err != nil || id == "" {
		t.Fatalf("receipt after accept: %q %v", id, err)
	}
	if err = f.db.AckDelivery(ctx, "task", id, "", proof, []AckDecision{{EventID: page.Events[0].EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, Decision: "handled", CommandID: "ack-after-accept"}}); err != nil {
		t.Fatalf("ack after accept refused: %v", err)
	}
}

func TestDeliveryMatrixReworkedRevisionRejections(t *testing.T) {
	ctx := context.Background()
	f := newReworkFixture(t)
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if next.WorkRevision != 2 || next.TaskID != "edit" {
		t.Fatalf("rev2 grant=%+v", next)
	}
	_, oldRevision, oldSlot := eventLedgerRow(t, f.db, f.result.EventID)

	// The revision-1 result was accepted before the rework: a byte-identical
	// replay stays a no-op under any revision number and cannot complete or
	// re-open revision 2.
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 2, EventID: f.result.EventID, EventRevision: oldRevision, EventHash: f.result.PayloadHash, ActionSlot: oldSlot, Decision: "accept", CommandID: "old-result-replay"}); err != nil || status != contractAccepted {
		t.Fatalf("idempotent old replay: %q %v", status, err)
	}
	if status := reworkTaskStatus(t, f.db, "edit"); status != "running" {
		t.Fatalf("old accept replay changed the new revision: %s", status)
	}
	// Flipping the old slot or changing its identity is refused.
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 2, EventID: f.result.EventID, EventRevision: oldRevision, EventHash: f.result.PayloadHash, ActionSlot: oldSlot, Decision: "reject", CommandID: "flip-old-result"})
	deliveryErrCode(t, err, contractConflict)
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 2, EventID: f.result.EventID, EventRevision: oldRevision + 100, EventHash: f.result.PayloadHash, ActionSlot: oldSlot, Decision: "accept", CommandID: "old-wrong-event-revision"})
	deliveryErrCode(t, err, contractConflict)
	// The review stage's failed row cannot be accepted as success at the old
	// revision (superseded) or at the current revision (task no longer in
	// result_ready).
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "review", WorkRevision: 1, EventID: f.spec.Review.EventID, EventRevision: f.spec.Review.EventRevision, EventHash: f.spec.Review.EventHash, ActionSlot: f.spec.ActionSlot, Decision: "accept", CommandID: "accept-stale-review"})
	deliveryErrCode(t, err, contractConflict)
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "review", WorkRevision: 2, EventID: f.spec.Review.EventID, EventRevision: f.spec.Review.EventRevision, EventHash: f.spec.Review.EventHash, ActionSlot: f.spec.ActionSlot, Decision: "accept", CommandID: "accept-old-review-new-revision"})
	deliveryErrCode(t, err, contractConflict)

	// Deliver revision 2 through the same collect->ack->accept pipeline.
	newResult := contract.Event{Version: 1, ProducerID: f.host, EventID: "rev2-result", RunID: "run", TaskID: "edit", AttemptID: next.AttemptID, SegmentID: next.SegmentID, WorkRevision: 2, ExecutionEpoch: 1, CommandID: next.CommandID, Sequence: 7, Kind: contract.EventResult, PayloadHash: strings.Repeat("1", 64)}
	if _, err = f.db.CommitHostEvent(ctx, newResult); err != nil {
		t.Fatal(err)
	}
	newExit := newResult
	newExit.EventID = "rev2-exited"
	newExit.Sequence = 8
	newExit.Kind = contract.EventExited
	newExit.PayloadHash = strings.Repeat("2", 64)
	newExit.ActiveMS = 500
	zero := 0
	newExit.ExitCode = &zero
	if _, err = f.db.CommitHostEvent(ctx, newExit); err != nil {
		t.Fatal(err)
	}
	if status := reworkTaskStatus(t, f.db, "edit"); status != "result_ready" {
		t.Fatalf("rev2 exit settlement status=%s", status)
	}

	page, err := f.db.CollectPending(ctx, "edit", "", 0, false)
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("rev2 pending page=%+v err=%v", page, err)
	}
	// The reworked run stays in native delivery mode, so the page is
	// acknowledged through the history-proof path (forged collection
	// receipts are exercised by TestDeliveryMatrixCollectSequencing).
	var oldPage, newPage *contract.Event
	for i := range page.Events {
		switch page.Events[i].EventID {
		case f.result.EventID:
			oldPage = &page.Events[i]
		case "rev2-result":
			newPage = &page.Events[i]
		}
	}
	if oldPage == nil || newPage == nil {
		t.Fatalf("page lost an actionable result: %+v", page)
	}

	// The internal exited row can never be acknowledged as delivered.
	exitEvent, exitRevision, exitSlot := eventLedgerRow(t, f.db, "rev2-exited")
	err = f.db.AckDelivery(ctx, "edit", "delivery-history", strings.Repeat("a", 64), "", []AckDecision{{EventID: exitEvent.EventID, EventRevision: exitRevision, EventHash: exitEvent.PayloadHash, ActionSlot: exitSlot, Decision: "handled", CommandID: "ack-exited"}})
	deliveryErrCode(t, err, contractConflict)
	// "handled" on the superseded revision-1 result is refused while its
	// task row belongs to revision 2 unless the exact acceptance is bound
	// to the slot; the fixture accepted it, so the bound disposition is the
	// stale marker below, never a fresh handled business effect.
	//
	// Deliver the full rev2 page: the superseded old result is disposed as
	// "stale", the current result as "handled".
	decisions := []AckDecision{
		{EventID: oldPage.EventID, EventRevision: oldPage.EventRevision, EventHash: oldPage.PayloadHash, ActionSlot: oldPage.ActionSlot, Decision: "stale", CommandID: "ack-old-stale"},
		{EventID: newPage.EventID, EventRevision: newPage.EventRevision, EventHash: newPage.PayloadHash, ActionSlot: newPage.ActionSlot, Decision: "handled", CommandID: "ack-new"},
	}
	if err = f.db.AckDelivery(ctx, "edit", "delivery-rev2", strings.Repeat("b", 64), "", decisions); err != nil {
		t.Fatalf("post-rework ack refused: %v", err)
	}

	// Acceptance of the rev2 result refuses a stale work_revision, then the
	// matching revision completes the task.
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: newPage.EventID, EventRevision: newPage.EventRevision, EventHash: newPage.PayloadHash, ActionSlot: newPage.ActionSlot, Decision: "accept", CommandID: "accept-rev2-as-rev1"})
	deliveryErrCode(t, err, contractConflict)
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 2, EventID: newPage.EventID, EventRevision: newPage.EventRevision, EventHash: newPage.PayloadHash, ActionSlot: newPage.ActionSlot, Decision: "accept", CommandID: "accept-rev2"}); err != nil || status != contractAccepted {
		t.Fatalf("rev2 accept status=%q err=%v", status, err)
	}
	if got := reworkTaskStatus(t, f.db, "edit"); got != "completed" {
		t.Fatalf("rev2 completion status=%s", got)
	}
	// After completion of revision 2, replaying the revision-1 acceptance is
	// still the same idempotent no-op and never re-opens or re-completes.
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "edit", WorkRevision: 2, EventID: f.result.EventID, EventRevision: oldRevision, EventHash: f.result.PayloadHash, ActionSlot: oldSlot, Decision: "accept", CommandID: "old-accept-replay"}); err != nil || status != contractAccepted {
		t.Fatalf("old replay after completion: %q %v", status, err)
	}
	if got := reworkTaskStatus(t, f.db, "edit"); got != "completed" {
		t.Fatalf("old replay perturbed completion: %s", got)
	}
}

// Behavior 7: a late exited event carrying a fresh event id, delivered after
// the segment already exited and the task is completed, is durable history
// only. The mechanism is the settlement guard in the host-event commit path:
// once the segment row is already 'exited', the exit is stored and durably
// acknowledged (and an identical replay stays a no-op) but the budget
// settlement and result_ready transition are skipped, so the Host can retire
// its spool without ever re-settling the budget, minting or perturbing a
// delivery/acceptance row, becoming collectible, or being accepted or
// delivery-acked. Every value below is an independent contract expectation;
// the ledger is asserted byte-identical across the late event and the two
// refusals.
func TestDeliveryMatrixLateExitedIsHistoryWithoutAcceptanceChange(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t, "collect")
	result := f.commit(t, contract.EventResult, "m-result")
	f.commit(t, contract.EventExited, "m-exit-1")
	if status := f.taskStatus(t, "task"); status != "result_ready" {
		t.Fatalf("first exit settlement status=%s", status)
	}
	ev, revision, slot := eventLedgerRow(t, f.db, result.EventID)
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: ev.EventID, EventRevision: revision, EventHash: ev.PayloadHash, ActionSlot: slot, Decision: "accept", CommandID: "m-accept"}); err != nil || status != contractAccepted {
		t.Fatalf("accept status=%q err=%v", status, err)
	}
	// Independent settlement contract for the first exit: the reported
	// ActiveMS=1000 settles exactly against the hardcoded one-hour default
	// segment grant (an independent contract value, not the store constant),
	// and the task reaches completed via the acceptance above.
	if status, used, granted := ledgerBudgetRow(t, f.db, f.grant.SegmentID); status != "settled" || used != 1000 || granted != 3_600_000 {
		t.Fatalf("first exit budget status=%s used=%d granted=%d", status, used, granted)
	}
	const fingerprintBeforeLate = "task=completed;receipts=0;collections=0;decisions=0;reviews=1;events=m-exit-1=internal;m-result=pending;"
	if got := f.ledgerFingerprint(t); got != fingerprintBeforeLate {
		t.Fatalf("pre-late fingerprint:\nwant %s\ngot  %s", fingerprintBeforeLate, got)
	}

	// Late exited with a fresh event id and a different measurement, so a
	// (wrongly) re-running settlement would be visible in the budget row.
	f.seq++
	lateExit := contract.Event{Version: 1, ProducerID: f.hostID, EventID: "m-exit-2", RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, CommandID: f.grant.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: f.seq, Kind: contract.EventExited, PayloadHash: strings.Repeat("7", 64), ActiveMS: 555}
	ack, err := f.db.CommitHostEvent(ctx, lateExit)
	if err != nil || ack.Status != "durable" {
		t.Fatalf("late exited not durably acked: ack=%+v err=%v", ack, err)
	}
	if replay, err := f.db.CommitHostEvent(ctx, lateExit); err != nil || replay.Status != "durable" {
		t.Fatalf("late exited replay not an idempotent durable ack: ack=%+v err=%v", replay, err)
	}
	// No re-settlement from the fresh measurement, no acceptance/delivery
	// perturbation: only the internal history row is appended.
	if status, used, granted := ledgerBudgetRow(t, f.db, f.grant.SegmentID); status != "settled" || used != 1000 || granted != 3_600_000 {
		t.Fatalf("late exited re-settled budget: status=%s used=%d granted=%d", status, used, granted)
	}
	const fingerprintAfterLate = "task=completed;receipts=0;collections=0;decisions=0;reviews=1;events=m-exit-1=internal;m-exit-2=internal;m-result=pending;"
	if got := f.ledgerFingerprint(t); got != fingerprintAfterLate {
		t.Fatalf("post-late fingerprint:\nwant %s\ngot  %s", fingerprintAfterLate, got)
	}
	// The late exit is internal history: not collectible, never acceptable and
	// never a delivery-ack target.
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Events {
		if e.EventID == lateExit.EventID {
			t.Fatalf("late exited leaked into the collect page: %+v", page)
		}
	}
	lateEvent, lateRevision, lateSlot := eventLedgerRow(t, f.db, "m-exit-2")
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: lateEvent.EventID, EventRevision: lateRevision, EventHash: lateEvent.PayloadHash, ActionSlot: lateSlot, Decision: "accept", CommandID: "accept-late-exit"})
	deliveryErrCode(t, err, contractConflict)
	// Collect mode is acknowledged only through a matching collection receipt;
	// the internal exit has none, so the delivery-ack is refused as a conflict
	// before any decision is applied.
	err = f.db.AckDelivery(ctx, "task", "collection-forged", "", strings.Repeat("b", 64), []AckDecision{{EventID: lateEvent.EventID, EventRevision: lateRevision, EventHash: lateEvent.PayloadHash, ActionSlot: lateSlot, Decision: "handled", CommandID: "ack-late-exit"}})
	deliveryErrCode(t, err, contractConflict)
	// Both refusals leave the ledger exactly in the history-only state.
	if got := f.ledgerFingerprint(t); got != fingerprintAfterLate {
		t.Fatalf("late-exit refusals perturbed the ledger:\nwant %s\ngot  %s", fingerprintAfterLate, got)
	}
}

// twoTaskDelivery drives two independent tasks of one run through the same
// producer, which is what a cross-task acceptance replay needs in order to be
// more than a missing-row refusal.
type twoTaskDelivery struct {
	db     *DB
	hostID string
	seq    int64
	grants map[string]contract.LaunchCommand
}

func newTwoTaskDelivery(t *testing.T, mode string, taskIDs ...string) *twoTaskDelivery {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	specs := make([]TaskSpec, 0, len(taskIDs))
	for _, id := range taskIDs {
		specs = append(specs, TaskSpec{ID: id, RunID: "run", MaxAttempts: 3})
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{DeliveryMode: mode, ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"},
		Tasks: specs,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host-birth", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	f := &twoTaskDelivery{db: db, hostID: hostID, grants: map[string]contract.LaunchCommand{}}
	for range taskIDs {
		grant, err := db.ClaimReady(ctx, hostID, 1, len(taskIDs))
		if err != nil {
			t.Fatalf("claim %d of %d: %v", len(f.grants)+1, len(taskIDs), err)
		}
		if _, dup := f.grants[grant.TaskID]; dup {
			t.Fatalf("duplicate grant for task %s", grant.TaskID)
		}
		f.grants[grant.TaskID] = grant
	}
	if len(f.grants) != len(taskIDs) {
		t.Fatalf("granted %v for %v", f.grants, taskIDs)
	}
	return f
}

func (f *twoTaskDelivery) commit(t *testing.T, taskID, kind, eventID string) contract.Event {
	t.Helper()
	grant := f.grants[taskID]
	f.seq++
	e := contract.Event{Version: 1, ProducerID: f.hostID, EventID: eventID, RunID: "run", TaskID: taskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: f.seq, Kind: kind, PayloadHash: fmt.Sprintf("%064x", f.seq)}
	if kind == contract.EventExited {
		e.ActiveMS = 1000
		zero := 0
		e.ExitCode = &zero
	}
	if _, err := f.db.CommitHostEvent(context.Background(), e); err != nil {
		t.Fatalf("commit %s/%s: %v", taskID, kind, err)
	}
	return e
}

func (f *twoTaskDelivery) status(t *testing.T, taskID string) string {
	t.Helper()
	return reworkTaskStatus(t, f.db, taskID)
}

func (f *twoTaskDelivery) deliveryRows(t *testing.T) string {
	t.Helper()
	var receipts, collections, decisions, reviews int
	for _, table := range []struct {
		name string
		n    *int
	}{{"delivery_receipts", &receipts}, {"collection_receipts", &collections}, {"runtime_decisions", &decisions}, {"review_decisions", &reviews}} {
		if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM ` + table.name).Scan(table.n); err != nil {
			t.Fatal(err)
		}
	}
	var events strings.Builder
	rows, err := f.db.sql.Query(`SELECT event_id,delivery_status FROM runtime_events ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err = rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&events, "%s=%s;", id, status)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("a=%s;b=%s;receipts=%d;collections=%d;decisions=%d;reviews=%d;events=%s", f.status(t, "task-a"), f.status(t, "task-b"), receipts, collections, decisions, reviews, events.String())
}

// Behavior 2b (review remediation): a decided action slot belongs to the task
// and event that minted it. Replaying the exact slot identity of task-a's
// accepted result under task-b's task id must be refused as a conflict and
// must never report a false acceptance, because the idempotent-replay branch
// is keyed by slot and has to prove it is the same decision, not merely the
// same slot.
func TestDeliveryMatrixCrossTaskSlotReplayIsRejected(t *testing.T) {
	ctx := context.Background()
	f := newTwoTaskDelivery(t, "collect", "task-a", "task-b")

	aResult := f.commit(t, "task-a", contract.EventResult, "a-result")
	f.commit(t, "task-a", contract.EventExited, "a-exit")
	if got := f.status(t, "task-a"); got != "result_ready" {
		t.Fatalf("task-a pre-accept status=%s", got)
	}
	aPage, err := f.db.CollectPending(ctx, "task-a", "", 0, false)
	if err != nil || len(aPage.Events) != 1 {
		t.Fatalf("task-a page=%+v err=%v", aPage, err)
	}
	aSlot := aPage.Events[0]
	id, proof, err := f.db.CollectionReceipt(ctx, "task-a", aPage.Events)
	if err != nil || id == "" {
		t.Fatalf("task-a receipt: %q %v", id, err)
	}
	if err = f.db.AckDelivery(ctx, "task-a", id, "", proof, []AckDecision{{EventID: aSlot.EventID, EventRevision: aSlot.EventRevision, EventHash: aSlot.PayloadHash, ActionSlot: aSlot.ActionSlot, Decision: "handled", CommandID: "a-ack"}}); err != nil {
		t.Fatalf("task-a ack: %v", err)
	}
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task-a", WorkRevision: 1, EventID: aResult.EventID, EventRevision: aSlot.EventRevision, EventHash: aSlot.PayloadHash, ActionSlot: aSlot.ActionSlot, Decision: "accept", CommandID: "a-accept"}); err != nil || status != contractAccepted {
		t.Fatalf("task-a accept status=%q err=%v", status, err)
	}

	// task-b is independently live and awaiting its own acceptance, so a
	// refused replay against it is a real refusal, not a missing-row error.
	f.commit(t, "task-b", contract.EventResult, "b-result")
	f.commit(t, "task-b", contract.EventExited, "b-exit")
	if got := f.status(t, "task-b"); got != "result_ready" {
		t.Fatalf("task-b status=%s", got)
	}
	bPage, err := f.db.CollectPending(ctx, "task-b", "", 0, false)
	if err != nil || len(bPage.Events) != 1 {
		t.Fatalf("task-b page=%+v err=%v", bPage, err)
	}
	bSlot := bPage.Events[0]
	before := f.deliveryRows(t)

	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task-b", WorkRevision: 1, EventID: aSlot.EventID, EventRevision: aSlot.EventRevision, EventHash: aSlot.PayloadHash, ActionSlot: aSlot.ActionSlot, Decision: "accept", CommandID: "b-impersonates-a"})
	deliveryErrCode(t, err, contractConflict)
	if got := f.deliveryRows(t); got != before {
		t.Fatalf("cross-task replay perturbed the ledger:\nwant %s\ngot  %s", before, got)
	}
	// Flipping the decision on another task's slot is refused the same way.
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task-b", WorkRevision: 1, EventID: aSlot.EventID, EventRevision: aSlot.EventRevision, EventHash: aSlot.PayloadHash, ActionSlot: aSlot.ActionSlot, Decision: "reject", CommandID: "b-rejects-a-slot"})
	deliveryErrCode(t, err, contractConflict)
	if got := f.deliveryRows(t); got != before {
		t.Fatalf("cross-task reject perturbed the ledger:\nwant %s\ngot  %s", before, got)
	}

	// The same slot on its own task stays an idempotent no-op under a fresh
	// command id, and task-b can still be accepted through its own identity.
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task-a", WorkRevision: 1, EventID: aSlot.EventID, EventRevision: aSlot.EventRevision, EventHash: aSlot.PayloadHash, ActionSlot: aSlot.ActionSlot, Decision: "accept", CommandID: "a-accept-renewed"}); err != nil || status != contractAccepted {
		t.Fatalf("task-a replay after refusal: %q %v", status, err)
	}
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task-b", WorkRevision: 1, EventID: bSlot.EventID, EventRevision: bSlot.EventRevision, EventHash: bSlot.PayloadHash, ActionSlot: bSlot.ActionSlot, Decision: "accept", CommandID: "b-accept"}); err != nil || status != contractAccepted {
		t.Fatalf("task-b own accept: %q %v", status, err)
	}
	// task-b's page was only read, never receipted or acked, so its result row
	// stays pending while its own acceptance still completes the task: the
	// count below is delivery bookkeeping for task-a alone.
	const fingerprintFinal = "a=completed;b=completed;receipts=1;collections=1;decisions=1;reviews=2;events=a-exit=internal;a-result=acked;b-exit=internal;b-result=pending;"
	if got := f.deliveryRows(t); got != fingerprintFinal {
		t.Fatalf("final fingerprint:\nwant %s\ngot  %s", fingerprintFinal, got)
	}
}

// Behavior 2c (review remediation): the collect-mode receipt is proved by its
// digest, not by its id. A well-formed but non-matching collection proof on a
// real receipt id is refused as a conflict, leaves the ledger byte-identical,
// and the digest comparison is case-insensitive on both the receipt and the
// durable delivery-ack layer.
func TestDeliveryMatrixProofDigestMismatchRejected(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t, "collect")
	f.commit(t, contract.EventResult, "d-result")
	f.commit(t, contract.EventExited, "d-exit")
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	e := page.Events[0]
	id, proof, err := f.db.CollectionReceipt(ctx, "task", page.Events)
	if err != nil || id == "" || proof == "" {
		t.Fatalf("receipt: %q %q %v", id, proof, err)
	}
	decisions := []AckDecision{{EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "handled", CommandID: "d-ack"}}
	before := f.ledgerFingerprint(t)

	// Right id, wrong digest (lowercase and uppercase forms of a valid
	// 64-hex string) must both be refused without consuming the receipt.
	err = f.db.AckDelivery(ctx, "task", id, "", strings.Repeat("e", 64), decisions)
	deliveryErrCode(t, err, contractConflict)
	err = f.db.AckDelivery(ctx, "task", id, "", strings.Repeat("E", 64), decisions)
	deliveryErrCode(t, err, contractConflict)
	// A single-character mutation of the true proof is refused too.
	wrong := strings.Repeat("0", 63) + proof[len(proof)-1:]
	err = f.db.AckDelivery(ctx, "task", id, "", wrong, decisions)
	deliveryErrCode(t, err, contractConflict)
	if got := f.ledgerFingerprint(t); got != before {
		t.Fatalf("proof refusals perturbed the ledger:\nwant %s\ngot  %s", before, got)
	}
	if err = f.db.AckDelivery(ctx, "task", id, "", strings.ToUpper(proof), decisions); err != nil {
		t.Fatalf("uppercase proof refused: %v", err)
	}
	// The receipt is now bound; the identical ack under the lowercase form of
	// the same proof is an idempotent replay. (A changed CommandID inside the
	// decision set is a different decisions hash and is refused; that
	// direction is asserted in TestDeliveryMatrixCollectSequencing.)
	if err = f.db.AckDelivery(ctx, "task", id, "", proof, decisions); err != nil {
		t.Fatalf("case-normalized ack replay refused: %v", err)
	}
	const fingerprintAcked = "task=result_ready;receipts=1;collections=1;decisions=1;reviews=0;events=d-exit=internal;d-result=acked;"
	if got := f.ledgerFingerprint(t); got != fingerprintAcked {
		t.Fatalf("post-ack fingerprint:\nwant %s\ngot  %s", fingerprintAcked, got)
	}
}

// Behavior 8 (review remediation): the completion-policy artifact gate is a
// settlement condition, not decoration. With completion_policy=artifact the
// exited event must carry an artifact whose sha256 equals the plan-pinned
// digest before the task can settle as success; a missing or mismatched
// diagnostic artifact may exist as history but never completes the task, and
// only an exact match auto-completes.
func TestDeliveryMatrixArtifactPolicyGate(t *testing.T) {
	ctx := context.Background()
	const expectedArtifact = "5f4dcc3b5aa765d61d8327deb882cf99" + "3d4f2e6a7b8c9d0e1f20314253647586" // 64 hex
	for _, tc := range []struct {
		name       string
		artifact   *contract.ArtifactRef
		wantStatus string
	}{
		{"missing", nil, "result_ready"},
		{"mismatched", &contract.ArtifactRef{ID: "art", Path: "/tmp/diagnostic", Size: 12, SHA256: strings.Repeat("a", 64)}, "result_ready"},
		{"matching", &contract.ArtifactRef{ID: "art", Path: "/tmp/diagnostic", Size: 12, SHA256: expectedArtifact}, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeliveryFixture(t, "collect", func(spec *TaskSpec) {
				spec.CompletionPolicy = "artifact"
				spec.ExpectedArtifactSHA256 = expectedArtifact
			})
			f.commit(t, contract.EventResult, "art-result")
			f.seq++
			exit := contract.Event{Version: 1, ProducerID: f.hostID, EventID: "art-exit", RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.seq, Kind: contract.EventExited, PayloadHash: strings.Repeat("9", 64), ActiveMS: 1000, Artifact: tc.artifact}
			exitZero := 0
			exit.ExitCode = &exitZero
			if _, err := f.db.CommitHostEvent(ctx, exit); err != nil {
				t.Fatalf("commit exited: %v", err)
			}
			if got := f.taskStatus(t, "task"); got != tc.wantStatus {
				t.Fatalf("artifact policy %s: status=%s want=%s", tc.name, got, tc.wantStatus)
			}
			if tc.name == "matching" {
				return
			}
			// The pinned artifact was never satisfied, so the task sits in
			// result_ready awaiting an exit path that already refused to
			// complete it. Acceptance is the owner's business decision on a
			// *delivered* result; it cannot substitute for a completion
			// policy the plan declared and the settlement measured as
			// unsatisfied. Contract: refused as a conflict, task untouched.
			result, revision, slot := eventLedgerRow(t, f.db, "art-result")
			before := f.ledgerFingerprint(t)
			_, acceptErr := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: revision, EventHash: result.PayloadHash, ActionSlot: slot, Decision: "accept", CommandID: "accept-" + tc.name})
			deliveryErrCode(t, acceptErr, contractConflict)
			// A refusal must be a true no-op: not only the task row but the
			// whole delivery/acceptance ledger stays byte-identical.
			if got := f.ledgerFingerprint(t); got != before {
				t.Fatalf("refused accept perturbed the ledger:\nwant %s\ngot  %s", before, got)
			}
			if got := f.taskStatus(t, "task"); got != "result_ready" {
				t.Fatalf("accept completed an unsatisfied artifact policy: %s", got)
			}
			// Rejecting the same delivery stays available to the owner.
			if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: revision, EventHash: result.PayloadHash, ActionSlot: slot, Decision: "reject", CommandID: "reject-" + tc.name}); err != nil || status != "rejected" {
				t.Fatalf("reject on unsatisfied artifact policy: %q %v", status, err)
			}
			if got := f.ledgerFingerprint(t); got == before {
				t.Fatalf("reject wrote no decision row: %s", got)
			}
		})
	}
}

// Behavior 2d (review remediation): the idempotent-replay branch must prove
// the whole stored identity, including the event the slot was minted from.
func TestDeliveryMatrixDecidedSlotRequiresExactEventIdentity(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t, "collect")
	result := f.commit(t, contract.EventResult, "i-result")
	f.commit(t, contract.EventExited, "i-exit")
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	row := page.Events[0]
	if status, err := f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: result.EventID, EventRevision: row.EventRevision, EventHash: row.PayloadHash, ActionSlot: row.ActionSlot, Decision: "accept", CommandID: "i-accept"}); err != nil || status != contractAccepted {
		t.Fatalf("accept: %q %v", status, err)
	}
	before := f.ledgerFingerprint(t)
	// Same task, same slot, same hash and revision, but naming a different
	// event: this is not the decision that was recorded, so it must conflict
	// rather than report an idempotent success.
	_, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: "i-exit", EventRevision: row.EventRevision, EventHash: row.PayloadHash, ActionSlot: row.ActionSlot, Decision: "accept", CommandID: "i-wrong-event"})
	deliveryErrCode(t, err, contractConflict)
	if got := f.ledgerFingerprint(t); got != before {
		t.Fatalf("event-identity mismatch perturbed the ledger:\nwant %s\ngot  %s", before, got)
	}
}
