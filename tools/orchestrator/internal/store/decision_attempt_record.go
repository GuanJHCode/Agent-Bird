package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type attemptDecisionRecord struct {
	slot, task, event, hash, segment, payload string
	revision, promise                         int
	eventRevision                             int64
	fallback                                  bool
	origin, policySHA                         string
}

func strictDecisionJSON(raw string, target any) error {
	if len(raw) == 0 || len(raw) > 256*1024 || !utf8.ValidString(raw) {
		return ErrConflict
	}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return ErrConflict
		}
		t, err := dec.Token()
		if err != nil || t == nil {
			return ErrConflict
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		end := json.Delim(']')
		if delim == '{' {
			end = '}'
			seen := map[string]bool{}
			for dec.More() {
				t, err := dec.Token()
				key, ok := t.(string)
				if err != nil || !ok || seen[key] {
					return ErrConflict
				}
				seen[key] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		} else if delim == '[' {
			for dec.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		} else {
			return ErrConflict
		}
		if t, err := dec.Token(); err != nil || t != end {
			return ErrConflict
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrConflict
	}
	if exactDecisionFields([]byte(raw), reflect.TypeOf(target)) != nil {
		return ErrConflict
	}
	dec = json.NewDecoder(bytes.NewBufferString(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return ErrConflict
	}
	return nil
}

func exactDecisionFields(body []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var values map[string]json.RawMessage
		if json.Unmarshal(body, &values) != nil || values == nil {
			return ErrConflict
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		for key, value := range values {
			f, ok := fields[key]
			if !ok || exactDecisionFields(value, f) != nil {
				return ErrConflict
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(body, &values) != nil || values == nil {
			return ErrConflict
		}
		for _, value := range values {
			if err := exactDecisionFields(value, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if json.Unmarshal(body, &values) != nil {
			return ErrConflict
		}
		for _, value := range values {
			if err := exactDecisionFields(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func readAttemptDecision(ctx context.Context, tx *sql.Tx, slot string) (attemptDecisionRecord, error) {
	var r attemptDecisionRecord
	err := tx.QueryRowContext(ctx, `SELECT action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,revision_payload,origin,policy_sha256 FROM retry_decisions WHERE action_slot=?`, slot).Scan(&r.slot, &r.task, &r.revision, &r.event, &r.eventRevision, &r.hash, &r.segment, &r.promise, &r.fallback, &r.payload, &r.origin, &r.policySHA)
	return r, err
}

func decisionRecordKind(r attemptDecisionRecord) (string, error) {
	if r.payload == "" {
		return "ordinary", nil
	}
	var fields map[string]json.RawMessage
	if len(r.payload) > 256*1024 || json.Unmarshal([]byte(r.payload), &fields) != nil {
		return "", ErrConflict
	}
	if _, ok := fields["kind"]; ok {
		_, err := parseReviewRetry(r.payload)
		return reviewRetryKind, err
	}
	var discriminator struct {
		Version int `json:"version"`
	}
	if raw, ok := fields["request"]; !ok || json.Unmarshal(raw, &discriminator) != nil {
		return "", ErrConflict
	}
	if discriminator.Version == 1 {
		_, err := parseDAGRework(r.payload)
		return dagReworkKind, err
	}
	if discriminator.Version != 0 {
		return "", ErrConflict
	}
	var saved reworkDecision
	if _, ok := fields["request"]; !ok || strictDecisionJSON(r.payload, &saved) != nil {
		return "", ErrConflict
	}
	return "rework", nil
}

// The tuple and original lifecycle are checked independently of current task
// revision. The caller supplies the exact source task for rework's reviewer.
func decisionSource(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, task string, revision int, reports bool) (contract.Event, error) {
	var body, hash, slot, segment, status, outcome, settled string
	var rev int64
	err := tx.QueryRowContext(ctx, `SELECT e.body_json,e.payload_hash,e.event_revision,e.action_slot,e.segment_id,s.status,s.outcome,b.status FROM runtime_events e JOIN segment_runtime s ON s.segment_id=e.segment_id AND s.attempt_id=e.attempt_id AND s.host_id=e.producer_id JOIN budget_runtime b ON b.id=s.reservation_id AND b.segment_id=s.segment_id WHERE e.event_id=? AND e.task_id=?`, r.event, task).Scan(&body, &hash, &rev, &slot, &segment, &status, &outcome, &settled)
	reportSource := errors.Is(err, sql.ErrNoRows) && reports
	var capability, capProducer, capRun, capTask, capAttempt, capStatus string
	var capRevision int
	var capEpoch uint64
	if reportSource {
		err = tx.QueryRowContext(ctx, `SELECT e.body_json,e.payload_hash,e.event_revision,e.action_slot,c.segment_id,s.status,s.outcome,b.status,c.capability_id,c.producer_id,c.run_id,c.task_id,c.attempt_id,c.work_revision,c.execution_epoch,c.status FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id JOIN segment_runtime s ON s.segment_id=c.segment_id AND s.attempt_id=c.attempt_id AND s.host_id=c.producer_id JOIN budget_runtime b ON b.id=s.reservation_id AND b.segment_id=s.segment_id WHERE e.event_id=? AND c.task_id=? AND e.kind='failure'`, r.event, task).Scan(&body, &hash, &rev, &slot, &segment, &status, &outcome, &settled, &capability, &capProducer, &capRun, &capTask, &capAttempt, &capRevision, &capEpoch, &capStatus)
	}
	var event contract.Event
	if err != nil || hash != r.hash || rev != r.eventRevision || slot != r.slot || segment != r.segment || (status != "exited" && !(r.origin == "policy" && status == "not_started")) || settled != "settled" || json.Unmarshal([]byte(body), &event) != nil || event.EventID != r.event || event.TaskID != task || event.SegmentID != segment || event.WorkRevision != revision || event.PayloadHash != hash || outcome != event.Kind {
		return event, ErrConflict
	}
	var run, attempt, command, producer string
	var segmentRevision int
	var hostEpoch uint64
	if tx.QueryRowContext(ctx, `SELECT t.run_id,a.id,s.command_id,s.host_id,c.work_revision,h.coordinator_epoch FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN runtime_hosts h ON h.id=s.host_id JOIN report_capabilities c ON c.segment_id=s.segment_id AND c.attempt_id=a.id AND c.task_id=t.id AND c.run_id=t.run_id AND c.producer_id=s.host_id WHERE s.segment_id=? AND t.id=?`, segment, task).Scan(&run, &attempt, &command, &producer, &segmentRevision, &hostEpoch) != nil || event.RunID != run || event.AttemptID != attempt || event.WorkRevision != segmentRevision || event.ExecutionEpoch == 0 || event.ExecutionEpoch > hostEpoch {
		return event, ErrConflict
	}
	if reportSource {
		// Committed reports retain capability provenance, including the empty
		// CommandID. Re-registration can advance cap/Host epochs; the durable
		// event row remains the historical wire-epoch authority. No lower bound
		// comes from mutable segment_runtime.execution_epoch, and no token is
		// reactivated by reading an already committed revoked capability.
		if event.ProducerID != capability || event.CommandID != "" || capProducer != producer || capRun != run || capTask != task || capAttempt != attempt || capRevision != revision || event.ExecutionEpoch > capEpoch || (capStatus != "registered" && capStatus != "revoked") {
			return event, ErrConflict
		}
	} else if event.CommandID != command || event.ProducerID != producer {
		return event, ErrConflict
	}
	if event.Kind != contract.EventFailed && event.Kind != contract.EventStopped && event.Kind != contract.EventResult && !(r.origin == "policy" && event.Kind == contract.EventLaunchRejected) {
		return event, ErrConflict
	}
	if event.Kind == contract.EventResult {
		var decision string
		if err = tx.QueryRowContext(ctx, `SELECT decision FROM review_decisions WHERE action_slot=? AND event_id=? AND event_revision=? AND event_hash=?`, slot, event.EventID, rev, hash).Scan(&decision); err != nil || decision != "reject" {
			return event, ErrConflict
		}
	}
	event.EventRevision, event.ActionSlot = rev, slot
	return event, nil
}
