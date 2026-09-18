package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

var safeDelivery = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type batchEvent struct {
	EventID       string `json:"event_id"`
	EventRevision int    `json:"event_revision"`
	Kind          string `json:"kind"`
	PayloadHash   string `json:"payload_hash"`
	ActionSlot    string `json:"action_slot"`
}

type batchIntent struct {
	Version          int          `json:"version"`
	DeliveryID       string       `json:"delivery_id"`
	Nonce            string       `json:"nonce"`
	ControllerThread string       `json:"controller_thread"`
	ControllerEpoch  int          `json:"controller_epoch"`
	Revision         int          `json:"revision"`
	State            string       `json:"state"`
	Events           []batchEvent `json:"events"`
	PayloadHash      string       `json:"payload_hash"`
}

type batchAck struct {
	Version          int    `json:"version"`
	Status           string `json:"status"`
	DeliveryID       string `json:"delivery_id"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	ControllerEpoch  int    `json:"controller_epoch"`
	Revision         int    `json:"revision"`
	EventID          string `json:"event_id"`
	EventRevision    int    `json:"event_revision"`
	EventHash        string `json:"event_hash"`
	ActionSlot       string `json:"action_slot"`
	CommandID        string `json:"command_id"`
	Decision         string `json:"decision"`
	DecisionCount    int    `json:"decision_count"`
	EffectCount      int    `json:"effect_count"`
}

type batchClaim struct {
	Version          int    `json:"version"`
	Status           string `json:"status"`
	DeliveryID       string `json:"delivery_id"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	Revision         int    `json:"revision"`
}

func batchIntentName() string            { return "batch-intent.json" }
func batchAckName(eventID string) string { return "batch-ack-" + eventID + ".json" }
func batchClaimName() string             { return "batch-send-claim.json" }

func uncertainOr(err error, fallback string) error {
	var known failure
	if errors.As(err, &known) {
		return err
	}
	return failure(fallback)
}

func maybeCrashBatchPublish(name, phase string) {
	if strings.HasPrefix(name, "batch-") && os.Getenv("G0_TEST_BATCH_PUBLISH_CRASH") == phase {
		os.Exit(97)
	}
}

func batchHash(intent batchIntent) string {
	intent.PayloadHash = ""
	intent.State = ""
	data, _ := json.Marshal(intent)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func readBatchIntent(root *os.Root, c control) (batchIntent, error) {
	var intent batchIntent
	if err := readJSON(root, batchIntentName(), &intent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return intent, failure("batch_intent_missing")
		}
		return intent, err
	}
	if intent.Version != 1 || !safeDelivery.MatchString(intent.DeliveryID) || intent.Nonce != c.Nonce || intent.ControllerThread != c.ControllerThread || intent.ControllerEpoch != 1 || !validRevision(intent.Revision) || (intent.State != "prepared" && intent.State != "transport_uncertain") || intent.PayloadHash != batchHash(intent) || len(intent.Events) == 0 || len(intent.Events) > 8 {
		return intent, failure("invalid_artifact")
	}
	ids, slots := map[string]bool{}, map[string]bool{}
	for _, event := range intent.Events {
		if !safeDelivery.MatchString(event.EventID) || !validRevision(event.EventRevision) || event.Kind != "progress" && event.Kind != "question" && event.Kind != "result" || !safeHash.MatchString(event.PayloadHash) || !safeDelivery.MatchString(event.ActionSlot) || ids[event.EventID] || slots[event.ActionSlot] {
			return intent, failure("invalid_artifact")
		}
		ids[event.EventID], slots[event.ActionSlot] = true, true
	}
	return intent, nil
}

func readBatchAck(root *os.Root, intent batchIntent, event batchEvent) (batchAck, error) {
	var ack batchAck
	if err := readJSON(root, batchAckName(event.EventID), &ack); err != nil {
		return ack, err
	}
	if ack.Version != 1 || ack.Status != "acknowledged" || ack.DeliveryID != intent.DeliveryID || ack.Nonce != intent.Nonce || ack.ControllerThread != intent.ControllerThread || ack.ControllerEpoch != intent.ControllerEpoch || ack.Revision != intent.Revision || ack.EventID != event.EventID || ack.EventRevision != event.EventRevision || ack.EventHash != event.PayloadHash || ack.ActionSlot != event.ActionSlot || !safeNonce.MatchString(ack.CommandID) || !validDecision(ack.Decision) || ack.DecisionCount != 1 || ack.EffectCount != effectCount(ack.Decision) {
		return ack, failure("invalid_artifact")
	}
	return ack, nil
}

func readBatchClaim(root *os.Root, intent batchIntent) (batchClaim, error) {
	var claim batchClaim
	if err := readJSON(root, batchClaimName(), &claim); err != nil {
		return claim, err
	}
	if claim.Version != 1 || claim.Status != "claimed" || claim.DeliveryID != intent.DeliveryID || claim.Nonce != intent.Nonce || claim.ControllerThread != intent.ControllerThread || claim.Revision != intent.Revision {
		return claim, failure("invalid_artifact")
	}
	return claim, nil
}

func batchAcks(root *os.Root, intent batchIntent) ([]batchAck, int, error) {
	acks := make([]batchAck, 0, len(intent.Events))
	pending := 0
	for _, event := range intent.Events {
		ack, err := readBatchAck(root, intent, event)
		if err == nil {
			acks = append(acks, ack)
		} else if errors.Is(err, os.ErrNotExist) {
			pending++
		} else {
			return nil, 0, err
		}
	}
	return acks, pending, nil
}

func decodeBatchEvents(raw string) ([]batchEvent, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, failure("invalid_args")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var events []batchEvent
	if decoder.Decode(&events) != nil || decoder.Decode(new(any)) != io.EOF || len(events) == 0 || len(events) > 8 {
		return nil, failure("invalid_args")
	}
	return events, nil
}

func validateBatchEvents(events []batchEvent) error {
	ids, slots := map[string]bool{}, map[string]bool{}
	for _, event := range events {
		if !safeDelivery.MatchString(event.EventID) || !validRevision(event.EventRevision) || event.Kind != "progress" && event.Kind != "question" && event.Kind != "result" || !safeHash.MatchString(event.PayloadHash) || !safeDelivery.MatchString(event.ActionSlot) || ids[event.EventID] || slots[event.ActionSlot] {
			return failure("invalid_args")
		}
		ids[event.EventID], slots[event.ActionSlot] = true, true
	}
	return nil
}

func rewriteBatchIntent(root *os.Root, intent batchIntent) error {
	stage := "." + batchIntentName() + ".tmp"
	if err := clearUnpublishedStage(root, stage); err != nil {
		return err
	}
	if err := writeNew(root, stage, intent); err != nil {
		return err
	}
	defer root.Remove(stage)
	if err := root.Rename(stage, batchIntentName()); err != nil {
		return err
	}
	return syncDir(root)
}

func batchCommand(root *os.Root, o options) (any, error) {
	var output any
	err := withControlLock(root, func() error {
		c, err := readControl(root, o.nonce)
		if err != nil {
			return err
		}
		if o.sub == "batch-status" {
			intent, err := readBatchIntent(root, c)
			if err != nil {
				return err
			}
			acks, pending, err := batchAcks(root, intent)
			if err != nil {
				return err
			}
			state := intent.State
			transportStatus := "ready"
			if state == "transport_uncertain" {
				transportStatus = "uncertain"
				state = "uncertain"
			}
			businessStatus := "partial"
			if state == "prepared" && pending == 0 {
				businessStatus = "complete"
			} else if state == "prepared" && len(acks) > 0 {
				businessStatus = "partial"
			} else if state == "prepared" {
				businessStatus = "prepared"
			}
			overallStatus := businessStatus
			if transportStatus == "uncertain" {
				overallStatus = "uncertain"
			}
			if c.Cancelled {
				overallStatus = "cancelled"
			} else if c.Revision != intent.Revision {
				overallStatus = "stale"
			}
			claim, claimErr := readBatchClaim(root, intent)
			claimState := "unclaimed"
			if claimErr == nil {
				claimState = claim.Status
			} else if !errors.Is(claimErr, os.ErrNotExist) {
				return claimErr
			}
			sendAllowed := transportStatus == "ready" && claimState == "unclaimed" && pending > 0 && !c.Cancelled && c.Revision == intent.Revision
			output = map[string]any{"version": 1, "status": overallStatus, "transport_status": transportStatus, "business_status": businessStatus, "send_allowed": sendAllowed, "delivery_id": intent.DeliveryID, "revision": intent.Revision, "event_count": len(intent.Events), "ack_count": len(acks), "pending_count": pending, "claim": claimState, "acks": acks}
			return nil
		}
		if c.ControllerThread != o.controller {
			return failure("owner_mismatch")
		}
		if c.Cancelled {
			return failure("cancelled")
		}
		if c.Revision != o.revision {
			return failure("stale_revision")
		}
		if o.sub == "batch-prepare" {
			if _, statErr := root.Lstat(batchIntentName()); statErr == nil {
				if err := clearUnpublishedStage(root, "."+batchIntentName()+".tmp"); err != nil {
					return err
				}
				return failure("batch_intent_exists")
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return failure("untrusted_file")
			}
			events, err := decodeBatchEvents(o.eventsJSON)
			if err != nil || validateBatchEvents(events) != nil || !safeDelivery.MatchString(o.deliveryID) {
				return failure("invalid_args")
			}
			intent := batchIntent{1, o.deliveryID, c.Nonce, c.ControllerThread, 1, c.Revision, "prepared", events, ""}
			intent.PayloadHash = batchHash(intent)
			if err := publish(root, batchIntentName(), intent); err != nil {
				return uncertainOr(err, "intent_uncertain")
			}
			output = intent
			return nil
		}
		intent, err := readBatchIntent(root, c)
		if err != nil {
			return err
		}
		if intent.Revision != c.Revision {
			return failure("stale_revision")
		}
		if o.sub == "batch-claim" {
			if intent.State == "transport_uncertain" {
				return failure("transport_uncertain")
			}
			if _, claimErr := readBatchClaim(root, intent); claimErr == nil {
				if err := clearUnpublishedStage(root, "."+batchClaimName()+".tmp"); err != nil {
					return err
				}
				return failure("claim_exists")
			} else if !errors.Is(claimErr, os.ErrNotExist) {
				return claimErr
			}
			if _, pending, pendingErr := batchAcks(root, intent); pendingErr != nil {
				return pendingErr
			} else if pending == 0 {
				return failure("batch_complete")
			}
			claim := batchClaim{1, "claimed", intent.DeliveryID, intent.Nonce, intent.ControllerThread, intent.Revision}
			if err := publish(root, batchClaimName(), claim); err != nil {
				return uncertainOr(err, "claim_uncertain")
			}
			output = claim
			return nil
		}
		if o.sub == "batch-uncertain" {
			intent.State = "transport_uncertain"
			if err := rewriteBatchIntent(root, intent); err != nil {
				return uncertainOr(err, "intent_uncertain")
			}
			output = intent
			return nil
		}
		var event batchEvent
		for _, candidate := range intent.Events {
			if candidate.EventID == o.eventID {
				event = candidate
				break
			}
		}
		if event.EventID == "" || event.EventRevision != o.eventRevision || event.PayloadHash != o.eventHash || event.ActionSlot != o.actionSlot {
			return failure("event_mismatch")
		}
		existing, readErr := readBatchAck(root, intent, event)
		if readErr == nil {
			if existing.Decision != o.decision {
				return failure("decision_conflict")
			}
			if err := clearUnpublishedStage(root, "."+batchAckName(event.EventID)+".tmp"); err != nil {
				return err
			}
			output = existing
			return nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		ack := batchAck{1, "acknowledged", intent.DeliveryID, intent.Nonce, intent.ControllerThread, intent.ControllerEpoch, intent.Revision, event.EventID, event.EventRevision, event.PayloadHash, event.ActionSlot, o.commandID, o.decision, 1, effectCount(o.decision)}
		if err := publish(root, batchAckName(event.EventID), ack); err != nil {
			return uncertainOr(err, "ack_uncertain")
		}
		output = ack
		return nil
	})
	return output, err
}
