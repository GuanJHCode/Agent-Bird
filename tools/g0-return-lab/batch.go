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

func batchIntentName() string            { return "batch-intent.json" }
func batchAckName(eventID string) string { return "batch-ack-" + eventID + ".json" }

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
	if intent.Version != 1 || !safeDelivery.MatchString(intent.DeliveryID) || intent.Nonce != c.Nonce || intent.ControllerThread != c.ControllerThread || intent.ControllerEpoch != 1 || !validRevision(intent.Revision) || intent.Revision != 1 || intent.State == "" || intent.PayloadHash != batchHash(intent) || len(intent.Events) == 0 || len(intent.Events) > 8 {
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
	stage := ".batch-intent.tmp"
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
			acks := []batchAck{}
			pending := 0
			for _, event := range intent.Events {
				ack, readErr := readBatchAck(root, intent, event)
				if readErr == nil {
					acks = append(acks, ack)
				} else if errors.Is(readErr, os.ErrNotExist) {
					pending++
				} else {
					return readErr
				}
			}
			state := intent.State
			if state == "transport_uncertain" {
				state = "uncertain"
			}
			if state == "prepared" && pending == 0 {
				state = "complete"
			} else if state == "prepared" && len(acks) > 0 {
				state = "partial"
			}
			output = map[string]any{"version": 1, "status": state, "delivery_id": intent.DeliveryID, "revision": intent.Revision, "event_count": len(intent.Events), "ack_count": len(acks), "pending_count": pending, "acks": acks}
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
				return failure("intent_uncertain")
			}
			output = intent
			return nil
		}
		intent, err := readBatchIntent(root, c)
		if err != nil {
			return err
		}
		if intent.State == "transport_uncertain" {
			return failure("transport_uncertain")
		}
		if intent.Revision != c.Revision {
			return failure("stale_revision")
		}
		if o.sub == "batch-uncertain" {
			intent.State = "transport_uncertain"
			if err := rewriteBatchIntent(root, intent); err != nil {
				return failure("intent_uncertain")
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
			output = existing
			return nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		ack := batchAck{1, "acknowledged", intent.DeliveryID, intent.Nonce, intent.ControllerThread, intent.ControllerEpoch, intent.Revision, event.EventID, event.EventRevision, event.PayloadHash, event.ActionSlot, o.commandID, o.decision, 1, effectCount(o.decision)}
		if err := publish(root, batchAckName(event.EventID), ack); err != nil {
			return failure("ack_uncertain")
		}
		output = ack
		return nil
	})
	return output, err
}
