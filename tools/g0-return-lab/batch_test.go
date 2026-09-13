package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func batchCrash(t *testing.T, mode string, args ...string) commandOutput {
	t.Helper()
	cmd := commandAs(testController, args...)
	cmd.Env = append(cmd.Env, "G0_TEST_BATCH_PUBLISH_CRASH="+mode)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return commandOutput{code, out.String(), errOut.String()}
}

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
	assertError(t, invokeAs(t, testController, 2, batchAckArgs(dir, nonce, "event_progress", "conflict", "waiting_user")...), "decision_conflict")
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

func TestBatchCompleteDisablesFurtherSendClaim(t *testing.T) {
	dir, _ := completedOwned(t, "batch_complete")
	invokeAs(t, testController, 0, batchPrepareArgs(dir, "batch_complete", batchEventsJSON(t))...)
	invokeAs(t, testController, 0, batchAckArgs(dir, "batch_complete", "event_progress", "complete_a", "handled")...)
	invokeAs(t, testController, 0, batchAckArgs(dir, "batch_complete", "event_question", "complete_b", "waiting_user")...)
	status := decode(t, invokeAs(t, "synthetic_observer", 0, "batch-status", "--dir", dir, "--nonce", "batch_complete").out)
	if status["business_status"] != "complete" || status["transport_status"] != "ready" || status["send_allowed"] != false {
		t.Fatalf("complete batch remained sendable: %s", mustJSON(status))
	}
	assertError(t, invokeAs(t, testController, 2, "batch-claim", "--dir", dir, "--nonce", "batch_complete", "--controller-thread", testController, "--revision", "1"), "batch_complete")
}

func TestBatchPublishRecoversFixedStagesBeforeAndAfterLink(t *testing.T) {
	for _, mode := range []string{"before_link", "after_link"} {
		t.Run(mode, func(t *testing.T) {
			dir, _ := completedOwned(t, "batch_stage_"+mode)
			invokeAs(t, testController, 0, batchPrepareArgs(dir, "batch_stage_"+mode, batchEventsJSON(t))...)
			args := batchAckArgs(dir, "batch_stage_"+mode, "event_progress", "stage_first_"+mode, "handled")
			crashed := batchCrash(t, mode, args...)
			if crashed.code != 97 || crashed.out != "" {
				t.Fatalf("publish crash was not an actual bounded process failure: %+v", crashed)
			}
			stage := filepath.Join(dir, ".batch-ack-event_progress.json.tmp")
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("crash did not leave fixed stage: %v", err)
			}
			if mode == "before_link" {
				if _, err := os.Lstat(filepath.Join(dir, "batch-ack-event_progress.json")); !os.IsNotExist(err) {
					t.Fatalf("before-link crash unexpectedly published destination: %v", err)
				}
			} else if _, err := os.Stat(filepath.Join(dir, "batch-ack-event_progress.json")); err != nil {
				t.Fatalf("after-link crash lost published destination: %v", err)
			}
			retry := invokeAs(t, testController, 0, batchAckArgs(dir, "batch_stage_"+mode, "event_progress", "stage_retry_"+mode, "handled")...)
			if mode == "before_link" && !strings.Contains(retry.out, `"command_id":"stage_retry_before_link"`) {
				t.Fatalf("before-link retry did not publish: %s", retry.out)
			}
			if mode == "after_link" && !strings.Contains(retry.out, `"command_id":"stage_first_after_link"`) {
				t.Fatalf("after-link retry did not recover committed ACK: %s", retry.out)
			}
			if _, err := os.Lstat(stage); !os.IsNotExist(err) {
				t.Fatalf("fixed stage survived recovery: %v", err)
			}
		})
	}
}

func TestBatchIntentPublishRecoversFixedStagesBeforeAndAfterLink(t *testing.T) {
	for _, mode := range []string{"before_link", "after_link"} {
		t.Run(mode, func(t *testing.T) {
			nonce := "batch_intent_stage_" + mode
			dir, _ := completedOwned(t, nonce)
			args := batchPrepareArgs(dir, nonce, batchEventsJSON(t))
			crashed := batchCrash(t, mode, args...)
			if crashed.code != 97 || crashed.out != "" {
				t.Fatalf("publish crash was not an actual bounded process failure: %+v", crashed)
			}
			stage := filepath.Join(dir, ".batch-intent.json.tmp")
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("crash did not leave intent stage: %v", err)
			}
			if mode == "before_link" {
				invokeAs(t, testController, 0, args...)
			} else {
				assertError(t, invokeAs(t, testController, 2, args...), "batch_intent_exists")
			}
			if _, err := os.Lstat(stage); !os.IsNotExist(err) {
				t.Fatalf("intent stage survived recovery: %v", err)
			}
		})
	}
}

func TestBatchClaimPublishRecoversFixedStagesBeforeAndAfterLink(t *testing.T) {
	for _, mode := range []string{"before_link", "after_link"} {
		t.Run(mode, func(t *testing.T) {
			nonce := "batch_claim_stage_" + mode
			dir, _ := completedOwned(t, nonce)
			invokeAs(t, testController, 0, batchPrepareArgs(dir, nonce, batchEventsJSON(t))...)
			args := []string{"batch-claim", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1"}
			crashed := batchCrash(t, mode, args...)
			if crashed.code != 97 || crashed.out != "" {
				t.Fatalf("publish crash was not an actual bounded process failure: %+v", crashed)
			}
			stage := filepath.Join(dir, ".batch-send-claim.json.tmp")
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("crash did not leave claim stage: %v", err)
			}
			if mode == "before_link" {
				invokeAs(t, testController, 0, args...)
			} else {
				assertError(t, invokeAs(t, testController, 2, args...), "claim_exists")
			}
			if _, err := os.Lstat(stage); !os.IsNotExist(err) {
				t.Fatalf("claim stage survived recovery: %v", err)
			}
		})
	}
}

func TestBatchPublishDoesNotDeleteSymlinkStage(t *testing.T) {
	dir, _ := completedOwned(t, "batch_stage_symlink")
	invokeAs(t, testController, 0, batchPrepareArgs(dir, "batch_stage_symlink", batchEventsJSON(t))...)
	stage := filepath.Join(dir, ".batch-ack-event_progress.json.tmp")
	foreign := filepath.Join(dir, "foreign-stage")
	if err := os.WriteFile(foreign, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, stage); err != nil {
		t.Fatal(err)
	}
	assertError(t, invokeAs(t, testController, 2, batchAckArgs(dir, "batch_stage_symlink", "event_progress", "symlink_stage", "handled")...), "untrusted_file")
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("unsafe stage was removed: %v", err)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign" {
		t.Fatalf("foreign stage target changed: %v %q", err, got)
	}
}

func TestBatchPublishDoesNotDeleteForeignHardlinkStage(t *testing.T) {
	dir, _ := completedOwned(t, "batch_stage_hardlink")
	invokeAs(t, testController, 0, batchPrepareArgs(dir, "batch_stage_hardlink", batchEventsJSON(t))...)
	stage := filepath.Join(dir, ".batch-ack-event_progress.json.tmp")
	foreign := filepath.Join(dir, "foreign-stage-hardlink")
	if err := os.WriteFile(foreign, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(foreign, stage); err != nil {
		t.Fatal(err)
	}
	assertError(t, invokeAs(t, testController, 2, batchAckArgs(dir, "batch_stage_hardlink", "event_progress", "hardlink_stage", "handled")...), "untrusted_file")
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("foreign hardlink stage was removed: %v", err)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign" {
		t.Fatalf("foreign hardlink target changed: %v %q", err, got)
	}
}

func TestBatchUncertainDoesNotBlindResendAndSurvivesRestart(t *testing.T) {
	var dir string
	nonce := "batch_uncertain"
	dir, _ = completedOwned(t, nonce)
	invokeAs(t, testController, 0, batchPrepareArgs(dir, nonce, batchEventsJSON(t))...)
	invokeAs(t, testController, 0, "batch-claim", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1")
	assertError(t, invokeAs(t, testController, 2, "batch-claim", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1"), "claim_exists")
	invokeAs(t, testController, 0, "batch-uncertain", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1")
	// Transport uncertainty blocks a new send claim, but a confirmed business event
	// may still be acknowledged and reconciled.
	invokeAs(t, testController, 0, batchAckArgs(dir, nonce, "event_progress", "ack_after_uncertain", "handled")...)
	assertError(t, invokeAs(t, testController, 2, "batch-claim", "--dir", dir, "--nonce", nonce, "--controller-thread", testController, "--revision", "1"), "transport_uncertain")
	status := decode(t, invokeAs(t, "synthetic_observer", 0, "batch-status", "--dir", dir, "--nonce", nonce).out)
	if status["status"] != "uncertain" || status["pending_count"] != float64(1) || status["ack_count"] != float64(1) {
		t.Fatalf("uncertain intent was lost after restart boundary: %s", mustJSON(status))
	}
}

func TestBatchPrepareUsesCurrentRevisionAfterRevisionChange(t *testing.T) {
	dir, _ := completedOwned(t, "batch_rev2")
	invokeAs(t, testController, 0, reviseArgs(dir, "batch_rev2", "1")...)
	events := `[{"event_id":"event_rev2","event_revision":2,"kind":"result","payload_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","action_slot":"slot_rev2"}]`
	invokeAs(t, testController, 0, "batch-prepare", "--dir", dir, "--nonce", "batch_rev2", "--controller-thread", testController, "--revision", "2", "--delivery-id", "batch_rev2_delivery", "--events-json", events)
	status := decode(t, invokeAs(t, "synthetic_observer", 0, "batch-status", "--dir", dir, "--nonce", "batch_rev2").out)
	if status["revision"] != float64(2) || status["pending_count"] != float64(1) {
		t.Fatalf("revision-two intent was not durable: %s", mustJSON(status))
	}
}

func TestBatchACKRecoversAfterCommittedProcessDiesBeforeResponse(t *testing.T) {
	dir, _ := completedOwned(t, "batch_lost_response")
	invokeAs(t, testController, 0, batchPrepareArgs(dir, "batch_lost_response", batchEventsJSON(t))...)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd := commandAs(testController, batchAckArgs(dir, "batch_lost_response", "event_progress", "lost_first", "handled")...)
	cmd.Stdout = writer
	cmd.Env = append(cmd.Env, "G0_TEST_BLOCK_AFTER_COMMIT=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ackPath := filepath.Join(dir, "batch-ack-event_progress.json")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ackPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("ACK was not committed before response loss")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	retry := invokeAs(t, testController, 0, batchAckArgs(dir, "batch_lost_response", "event_progress", "lost_retry", "handled")...)
	if !strings.Contains(retry.out, `"command_id":"lost_first"`) {
		t.Fatalf("restart reconciliation did not return committed ACK: %s", retry.out)
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

func TestBatchDifferentEventsConcurrentACK(t *testing.T) {
	dir, _ := completedOwned(t, "batch_concurrent")
	invokeAs(t, testController, 0, batchPrepareArgs(dir, "batch_concurrent", batchEventsJSON(t))...)
	outputs := make([]commandOutput, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		outputs[0] = runAs(testController, batchAckArgs(dir, "batch_concurrent", "event_progress", "parallel_a", "handled")...)
	}()
	go func() {
		defer wg.Done()
		outputs[1] = runAs(testController, batchAckArgs(dir, "batch_concurrent", "event_question", "parallel_b", "waiting_user")...)
	}()
	wg.Wait()
	for _, output := range outputs {
		if output.code != 0 {
			t.Fatalf("parallel event ACK failed: %+v", output)
		}
	}
	status := decode(t, invokeAs(t, "synthetic_observer", 0, "batch-status", "--dir", dir, "--nonce", "batch_concurrent").out)
	if status["ack_count"] != float64(2) || status["pending_count"] != float64(0) {
		t.Fatalf("parallel ACKs were not independently durable: %s", mustJSON(status))
	}
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprint(string(data))
}
