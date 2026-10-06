package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TaskCompletionMode is selected once by the admitted adapter invocation.
// Payloads and provider text cannot request or replace this mode/schema.
type TaskCompletionMode string

const (
	TaskCompletionNone TaskCompletionMode = ""
	TaskCompletionV1   TaskCompletionMode = "native-task-v1"
)

const managedTaskSchema = `{"type":"object","properties":{"version":{"type":"integer","enum":[1]},"kind":{"type":"string","enum":["technical_question","result"]},"text":{"type":"string","minLength":1,"maxLength":65536}},"required":["version","kind","text"],"additionalProperties":false}`
const managedTaskInstruction = `Agent Bird managed completion v1. Complete this segment using the fixed native structured output. When missing business input blocks authorized work, finish with {"version":1,"kind":"technical_question","text":"your exact business question"}; after the owner's answer, continue the exact original native session. When work is complete, finish with {"version":1,"kind":"result","text":"your exact completed result"}. Do not use native ask_question/AskUserQuestion or execute a shell/report helper to transport this output. Native permission requests are separate and must never be represented as a business technical_question. Keep the granted task scope, profile and permissions unchanged.`

// ManagedTaskSchema and ManagedTaskInstruction share the fixed business output
// contract with the managed Codex session. Neither accepts caller overrides.
func ManagedTaskSchema() string      { return managedTaskSchema }
func ManagedTaskInstruction() string { return managedTaskInstruction }

func usesManagedTaskSchema(req Request) bool {
	return req.Profile != nil && req.Action == nil && (req.Provider == ProviderAGY || req.Provider == ProviderClaude)
}

func (i Invocation) TaskCompletionMode() TaskCompletionMode { return i.completionMode }

type TaskCompletion struct {
	Version int
	Kind    string
	Text    string
}

// DecodeTaskCompletion accepts only the native StructuredText channel. The
// caller supplies its existing provider frame bound; no independent quota,
// truncation or prose fallback is introduced here.
func DecodeTaskCompletion(raw string, maxBytes int) (TaskCompletion, error) {
	invalid := errors.New("managed_completion_invalid")
	if maxBytes <= 0 || len(raw) > maxBytes {
		return TaskCompletion{}, errors.New("managed_completion_too_large")
	}
	if !utf8.ValidString(raw) {
		return TaskCompletion{}, invalid
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return TaskCompletion{}, invalid
	}
	fields := make(map[string]json.RawMessage, 3)
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok || (name != "version" && name != "kind" && name != "text") {
			return TaskCompletion{}, invalid
		}
		if _, duplicate := fields[name]; duplicate {
			return TaskCompletion{}, invalid
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return TaskCompletion{}, invalid
		}
		fields[name] = value
	}
	last, err := dec.Token()
	if err != nil || last != json.Delim('}') || len(fields) != 3 {
		return TaskCompletion{}, invalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return TaskCompletion{}, invalid
	}
	var result TaskCompletion
	if json.Unmarshal(fields["version"], &result.Version) != nil || result.Version != 1 || json.Unmarshal(fields["kind"], &result.Kind) != nil || (result.Kind != "technical_question" && result.Kind != "result") {
		return TaskCompletion{}, invalid
	}
	if !validCompletionString(fields["text"]) || json.Unmarshal(fields["text"], &result.Text) != nil || strings.TrimSpace(result.Text) == "" {
		return TaskCompletion{}, invalid
	}
	if len(result.Text) > maxBytes {
		return TaskCompletion{}, errors.New("managed_completion_too_large")
	}
	return result, nil
}

// encoding/json replaces unpaired escaped UTF-16 surrogates with U+FFFD.
// Reject those malformed strings without rejecting an actual valid U+FFFD.
func validCompletionString(raw []byte) bool {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	for index := 1; index < len(raw)-1; index++ {
		if raw[index] != '\\' {
			continue
		}
		index++
		if index >= len(raw)-1 {
			return false
		}
		if raw[index] != 'u' {
			continue
		}
		if index+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if value >= 0xDC00 && value <= 0xDFFF {
			return false
		}
		if value < 0xD800 || value > 0xDBFF {
			continue
		}
		if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[index+3:index+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		index += 6
	}
	return true
}
