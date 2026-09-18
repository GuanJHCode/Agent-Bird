package adapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type EventKind string

const (
	EventInit       EventKind = "init"
	EventProgress   EventKind = "progress"
	EventMessage    EventKind = "message"
	EventQuestion   EventKind = "question"
	EventResult     EventKind = "result"
	EventDiagnostic EventKind = "diagnostic"
	EventUnknown    EventKind = "unknown"
)

// Event is intentionally a projection. Raw provider JSON is never retained.
type Event struct {
	Kind           EventKind
	SessionID      string
	Status         string
	Text           string
	StructuredText string
}

type StreamParser struct {
	provider Provider
	lineMax  int
	totalMax int
	total    int
}

func NewStreamParser(provider Provider, lineMax, totalMax int) *StreamParser {
	return &StreamParser{provider: provider, lineMax: lineMax, totalMax: totalMax}
}

func (p *StreamParser) Parse(line string) (Event, error) {
	if p == nil || p.lineMax <= 0 || p.totalMax <= 0 {
		return Event{}, errors.New("stream_limits_invalid")
	}
	if len(line) > p.lineMax || p.total > p.totalMax-len(line) {
		return Event{}, errors.New("stream_output_limit")
	}
	event, err := ParseEvent(p.provider, line, p.lineMax)
	if err != nil {
		return Event{}, err
	}
	p.total += len(line)
	return event, nil
}

func (p *StreamParser) Consume(reader io.Reader, callback func(Event) error) error {
	if reader == nil || callback == nil {
		return errors.New("stream_consumer_invalid")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), p.lineMax)
	for scanner.Scan() {
		event, err := p.Parse(scanner.Text())
		if err != nil {
			return err
		}
		if err := callback(event); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("stream_read_failed")
	}
	return nil
}

func ParseEvent(provider Provider, line string, maxBytes int) (Event, error) {
	if maxBytes <= 0 || len(line) > maxBytes {
		return Event{}, errors.New("event_too_large")
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader([]byte(line)))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return Event{}, errors.New("event_invalid_json")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Event{}, errors.New("event_multiple_json")
	}
	switch provider {
	case ProviderClaude:
		return parseClaude(object)
	case ProviderAGY:
		return parseAGY(object)
	case ProviderGrok:
		return parseGrok(object)
	case ProviderCodex:
		return parseCodex(object)
	default:
		return Event{}, errors.New("provider_unknown")
	}
}

func parseCodex(object map[string]json.RawMessage) (Event, error) {
	typeName, err := stringField(object, "type")
	if err != nil {
		return Event{}, err
	}
	switch typeName {
	case "thread.started":
		session, err := stringField(object, "thread_id")
		if err != nil || !safeID(session) {
			if err != nil {
				return Event{}, err
			}
			return Event{}, errors.New("event_thread_id_invalid")
		}
		return Event{Kind: EventInit, SessionID: session}, nil
	case "item.completed":
		var item map[string]json.RawMessage
		if err := json.Unmarshal(object["item"], &item); err != nil || item == nil {
			return Event{}, errors.New("event_item_invalid")
		}
		if _, err := stringField(item, "id"); err != nil {
			return Event{}, err
		}
		itemType, err := stringField(item, "type")
		if err != nil {
			return Event{}, err
		}
		if itemType != "agent_message" {
			return Event{Kind: EventDiagnostic}, nil
		}
		text, err := stringField(item, "text")
		if err != nil {
			return Event{}, err
		}
		return Event{Kind: EventMessage, Text: text}, nil
	case "turn.completed":
		if err := validateCodexUsage(object["usage"]); err != nil {
			return Event{}, err
		}
		return Event{Kind: EventResult, Status: "completed"}, nil
	case "turn.failed":
		var failure map[string]json.RawMessage
		if err := json.Unmarshal(object["error"], &failure); err != nil || failure == nil {
			return Event{}, errors.New("event_error_invalid")
		}
		message, err := stringField(failure, "message")
		if err != nil {
			return Event{}, err
		}
		return Event{Kind: EventResult, Status: "failed", Text: message}, nil
	case "error":
		message, err := stringField(object, "message")
		if err != nil {
			return Event{}, err
		}
		return Event{Kind: EventDiagnostic, Status: "error", Text: message}, nil
	default:
		return Event{Kind: EventDiagnostic}, nil
	}
}

func validateCodexUsage(raw json.RawMessage) error {
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(raw, &usage); err != nil || usage == nil {
		return errors.New("event_usage_invalid")
	}
	for _, name := range []string{"input_tokens", "cached_input_tokens", "cache_write_input_tokens", "output_tokens", "reasoning_output_tokens"} {
		value, ok := usage[name]
		var count *int64
		if !ok || json.Unmarshal(value, &count) != nil || count == nil || *count < 0 {
			return errors.New("event_usage_invalid")
		}
	}
	return nil
}

func parseClaude(object map[string]json.RawMessage) (Event, error) {
	typeName, err := stringField(object, "type")
	if err != nil {
		return Event{}, err
	}
	session, err := stringFieldOptional(object, "session_id")
	if err != nil {
		return Event{}, err
	}
	switch typeName {
	case "system":
		subtype, err := stringFieldOptional(object, "subtype")
		if err != nil {
			return Event{}, err
		}
		if subtype == "init" {
			return Event{Kind: EventInit, SessionID: session}, nil
		}
		return Event{Kind: EventDiagnostic, SessionID: session}, nil
	case "result":
		subtype, err := stringFieldOptional(object, "subtype")
		if err != nil {
			return Event{}, err
		}
		if !validClaudeStatus(subtype) {
			return Event{}, errors.New("event_status_invalid")
		}
		text, err := stringFieldOptional(object, "result")
		if err != nil {
			return Event{}, err
		}
		return Event{Kind: EventResult, SessionID: session, Status: subtype, Text: text, StructuredText: string(object["structured_output"])}, nil
	case "assistant":
		// Only completed public text blocks are progress. Never project thinking,
		// tool arguments/results, user messages, or raw stream deltas.
		var message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		var text string
		if json.Unmarshal(object["message"], &message) == nil {
			for _, block := range message.Content {
				if block.Type == "text" {
					text += block.Text
				}
			}
		}
		return Event{Kind: EventMessage, SessionID: session, Text: text}, nil
	case "user":
		return Event{Kind: EventMessage, SessionID: session}, nil
	default:
		return Event{Kind: EventUnknown, SessionID: session}, nil
	}
}

func parseAGY(object map[string]json.RawMessage) (Event, error) {
	eventName, err := stringField(object, "event")
	if err != nil {
		return Event{}, err
	}
	switch eventName {
	case "init":
		session, err := stringNestedField(object, "conversation_id")
		return Event{Kind: EventInit, SessionID: session}, err
	case "step_update":
		var value map[string]json.RawMessage
		if err := json.Unmarshal(object["step_update"], &value); err != nil {
			return Event{}, errors.New("event_step_invalid")
		}
		session, err := stringNestedField(value, "conversation_id")
		if err != nil {
			return Event{}, err
		}
		// User-input, tool and checkpoint steps need not contain response text.
		text, err := stringFieldOptional(value, "text_delta")
		return Event{Kind: EventProgress, SessionID: session, Text: text}, err
	case "result":
		var value map[string]json.RawMessage
		if err := json.Unmarshal(object["result"], &value); err != nil {
			return Event{}, errors.New("event_result_invalid")
		}
		session, err := stringNestedField(value, "conversation_id")
		if err != nil {
			return Event{}, err
		}
		status, err := stringNestedField(value, "status")
		if err != nil {
			return Event{}, err
		}
		if !validAGYStatus(status) {
			return Event{}, errors.New("event_status_invalid")
		}
		text, err := stringFieldOptional(value, "response")
		if status == "SUCCESS" || status == "WAITING" {
			text, err = stringNestedField(value, "response")
		}
		structured, structuredErr := objectFieldOptional(value, "structured_output")
		if structuredErr != nil {
			return Event{}, structuredErr
		}
		kind := EventResult
		if status == "WAITING" {
			kind = EventQuestion
		}
		// AGY's response is aggregate commentary. Only its final object envelope
		// is authoritative for a native schema-constrained candidate review.
		return Event{Kind: kind, SessionID: session, Status: status, Text: text, StructuredText: structured}, err
	default:
		return Event{Kind: EventUnknown}, nil
	}
}

func parseGrok(object map[string]json.RawMessage) (Event, error) {
	typeName, err := stringField(object, "type")
	if err != nil {
		return Event{}, err
	}
	session, err := stringFieldEitherOptional(object, "sessionId", "session_id")
	if err != nil {
		return Event{}, err
	}
	switch typeName {
	case "text":
		text, err := stringFieldOptional(object, "data")
		return Event{Kind: EventMessage, SessionID: session, Text: text}, err
	case "end":
		status, err := stringFieldOptional(object, "stopReason")
		return Event{Kind: EventResult, SessionID: session, Status: status}, err
	case "error":
		return Event{Kind: EventResult, SessionID: session, Status: "error"}, nil
	case "result":
		status, err := stringFieldOptional(object, "status")
		if err != nil {
			return Event{}, err
		}
		text, err := stringFieldOptional(object, "text")
		if err != nil {
			return Event{}, err
		}
		if text == "" {
			text, err = stringFieldOptional(object, "result")
		}
		return Event{Kind: EventResult, SessionID: session, Status: status, Text: text}, err
	default:
		return Event{Kind: EventUnknown}, nil
	}
}

func stringField(object map[string]json.RawMessage, name string) (string, error) {
	value, ok := object[name]
	if !ok {
		return "", fmt.Errorf("event_%s_missing", name)
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil || result == "" {
		return "", fmt.Errorf("event_%s_invalid", name)
	}
	return result, nil
}

func stringFieldOptional(object map[string]json.RawMessage, name string) (string, error) {
	value, ok := object[name]
	if !ok {
		return "", nil
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", fmt.Errorf("event_%s_invalid", name)
	}
	return result, nil
}

func validClaudeStatus(status string) bool {
	return status == "success" || strings.HasPrefix(status, "error") || strings.Contains(status, "cancel") || status == "interrupted"
}

func validAGYStatus(status string) bool {
	switch status {
	case "SUCCESS", "ERROR", "CANCELED", "INTERRUPTED", "INVALID", "WAITING", "RUNNING":
		return true
	default:
		return false
	}
}

func stringNestedField(object map[string]json.RawMessage, name string) (string, error) {
	return stringField(object, name)
}

func stringFieldEitherOptional(object map[string]json.RawMessage, first, second string) (string, error) {
	if _, ok := object[first]; ok {
		return stringFieldOptional(object, first)
	}
	if _, ok := object[second]; ok {
		return stringFieldOptional(object, second)
	}
	return "", nil
}

func objectFieldOptional(object map[string]json.RawMessage, name string) (string, error) {
	value, ok := object[name]
	if !ok {
		return "", nil
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(value, &nested); err != nil || nested == nil {
		return "", fmt.Errorf("event_%s_invalid", name)
	}
	return string(value), nil
}
