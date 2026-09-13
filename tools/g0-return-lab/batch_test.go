package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func batchEventsJSON(t *testing.T) string {
	t.Helper()
	value := []batchEvent{
		{EventID: "event_progress", EventRevision: 1, Kind: "progress", PayloadHash: strings.Repeat("a", 64), ActionSlot: "slot_progress"},
		{EventID: "event_question", EventRevision: 1, Kind: "question", PayloadHash: strings.Repeat("b", 64), ActionSlot: "slot_question"},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func batchPrepareArgs(dir string, nonce string, events string) []string {
	return []string{"batch-prepare", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1", "--delivery-id", nonce + "_delivery", "--events-json", events}
}

func batchAckArgs(dir, nonce, eventID, commandID, decision string) []string {
	hash := strings.Repeat("a", 64)
	slot := "slot_progress"
	if eventID == "event_question" {
		hash = strings.Repeat("b", 64)
		slot = "slot_question"
	}
	return []string{"batch-ack", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1", "--event-id", eventID, "--event-revision", "1", "--event-hash", hash, "--action-slot", slot, "--command-id", commandID, "--decision", decision}
}

func TestBatchIntentPersistsPartialAndDuplicateSlotACK(t *testing.T) {
	var dir string
	nonce := "batch_partial"
	dir, _ = completedOwned(t, nonce)
	invokeAs(t, testController, 0, batchPrepareArgs(dir, nonce, batchEventsJSON(t))...)
	first := invokeAs(t, testController, 0, batchAckArgs(dir, nonce, "event_progress", "command_a", "handled")...)
	duplicate := invokeAs(t, testController, 0, batchAckArgs(dir, nonce, "event_progress", "command_b", "handled")...)
	if first.out != duplicate.out {
		t.Fatalf("same action slot created a second effect: %s / %s", first.out, duplicate.out)
	}
	status := decode(t, invokeAs(t, "synthetic_observer", 0, "batch-status", "--dir", dir, "--nonce", nonce).out)
	if status["status"] != "partial" || status["pending_count"] != float64(1) || status["ack_count"] != float64(1) {
		t.Fatalf("unexpected partial status: %s", mustJSON(status))
	}
	invokeAs(t, testController, 0, batchAckArgs(dir, nonce, "event_question", "command_q", "waiting_user")...)
	status = decode(t, invokeAs(t, "synthetic_observer", 0, "batch-status", "--dir", dir, "--nonce", nonce).out)
	if status["status"] != "complete" || status["pending_count"] != float64(0) {
		t.Fatalf("unexpected complete status: %s", mustJSON(status))
	}
	if _, err := os.Stat(filepath.Join(dir, "batch-ack-event_progress.json")); err != nil {
		t.Fatal(err)
	}
}

func TestBatchUncertainDoesNotBlindResendAndSurvivesRestart(t *testing.T) {
	var dir string
	nonce := "batch_uncertain"
	dir, _ = completedOwned(t, nonce)
	invokeAs(t, testController, 0, batchPrepareArgs(dir, nonce, batchEventsJSON(t))...)
	invokeAs(t, testController, 0, "batch-uncertain", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1")
	assertError(t, invokeAs(t, testController, 2, batchAckArgs(dir, nonce, "event_progress", "retry_after_uncertain", "handled")...), "transport_uncertain")
	status := decode(t, invokeAs(t, "synthetic_observer", 0, "batch-status", "--dir", dir, "--nonce", nonce).out)
	if status["status"] != "uncertain" || status["pending_count"] != float64(2) {
		t.Fatalf("uncertain intent was lost after restart boundary: %s", mustJSON(status))
	}
}

func TestBatchRevisionAndCancellationRejectOldEffects(t *testing.T) {
	var dir string
	nonce := "batch_revision"
	dir, _ = completedOwned(t, nonce)
	invokeAs(t, testController, 0, batchPrepareArgs(dir, nonce, batchEventsJSON(t))...)
	invokeAs(t, testController, 0, append(reviseArgs(dir, nonce, "1"), "--cancel")...)
	assertError(t, invokeAs(t, testController, 2, batchAckArgs(dir, nonce, "event_progress", "cancelled_old", "handled")...), "cancelled")
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprint(string(data))
}
