package codexsession

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPinnedNativeShapeCompositesRejectUnknownFields(t *testing.T) {
	s, err := loadShapes()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(map[string]any)
		ok   bool
	}{
		{"actual_allOf_ref_oneOf_sandbox", func(map[string]any) {}, true},
		{"open_reasoning_string", func(m map[string]any) { m["reasoningEffort"] = "future-effort" }, true},
		{"nullable_reasoning", func(m map[string]any) { m["reasoningEffort"] = nil }, true},
		{"empty_reasoning", func(m map[string]any) { m["reasoningEffort"] = "" }, false},
		{"unknown_top", func(m map[string]any) { m["extra"] = true }, false},
		{"unknown_nested", func(m map[string]any) { m["thread"].(map[string]any)["extra"] = true }, false},
		{"missing_thread_session", func(m map[string]any) { delete(m["thread"].(map[string]any), "sessionId") }, false},
		{"null_required_thread", func(m map[string]any) { m["thread"] = nil }, false},
		{"wrong_timestamp_type", func(m map[string]any) { m["thread"].(map[string]any)["createdAt"] = "1" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": "/private/owned/project", "model": "gpt-5.4", "modelProvider": "openai", "reasoningEffort": "high", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}, "thread": fixtureThread()}
			test.edit(m)
			raw, _ := json.Marshal(m)
			_, err := s.object("response:thread/start", raw)
			if (err == nil) != test.ok {
				t.Fatalf("valid=%v err=%v", test.ok, err)
			}
		})
	}
	item := map[string]any{"id": "final", "type": "agentMessage", "text": "complete", "phase": "final_answer"}
	for _, unknown := range []bool{false, true} {
		if unknown {
			item["permissionGrant"] = true
		}
		raw, _ := json.Marshal(map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": item})
		_, err = s.object("notification:item/completed", raw)
		if (err != nil) != unknown {
			t.Fatalf("item unknown=%v err=%v", unknown, err)
		}
	}
}

func TestPrivateShapeAllOfClosesTheCompleteObject(t *testing.T) {
	// This private adversarial shape protects the same complete-object policy
	// used by pinned allOf references; it is not an exported schema surface.
	raw := []byte(`{"allOf":[{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}]}`)
	schema, _ := strictValue(raw)
	s := &nativeShapes{}
	for _, test := range []struct {
		raw string
		ok  bool
	}{{`{"a":1,"b":"native"}`, true}, {`{"a":1}`, false}, {`{"a":1,"b":"native","extra":true}`, false}} {
		value, _ := strictValue([]byte(test.raw))
		if got := s.match([]any{schema}, value, 0, &shapeBudget{}); got != test.ok {
			t.Fatalf("raw=%s valid=%v", test.raw, got)
		}
	}
	union, _ := strictValue([]byte(`{"oneOf":[{"type":"object","properties":{"a":{"enum":[1]}}},{"type":"object","properties":{"b":{"enum":[2]}}}]}`))
	value, _ := strictValue([]byte(`{"a":1,"b":2}`))
	if s.match([]any{union}, value, 0, &shapeBudget{}) {
		t.Fatal("unselected union fields were allowed")
	}
}

func TestNativeShapeNumbersPreserveFormatPrecision(t *testing.T) {
	for _, test := range []struct {
		format, number string
		ok             bool
	}{
		{"int64", "9007199254740993", true},
		{"int64", "9223372036854775807", true},
		{"int64", "9223372036854775808", false},
		{"int64", "-9223372036854775808", true},
		{"int64", "-9223372036854775809", false},
		{"int32", "2147483647", true}, {"int32", "2147483648", false},
		{"uint", "18446744073709551615", true}, {"uint", "18446744073709551616", false},
		{"uint", "-1", false}, {"uint32", "4294967295", true}, {"uint32", "4294967296", false},
		{"uint16", "65535", true}, {"uint16", "65536", false},
		{"int64", "1.0", false}, {"int64", "1e0", false},
	} {
		t.Run(test.format+"/"+test.number, func(t *testing.T) {
			node := map[string]any{"type": "integer", "format": test.format}
			if got := (&nativeShapes{}).match([]any{node}, json.Number(test.number), 0, &shapeBudget{}); got != test.ok {
				t.Fatalf("valid=%v", got)
			}
		})
	}
}

func TestNativeShapesBoundDepthReferencesAndWork(t *testing.T) {
	if _, err := strictValue([]byte(strings.Repeat("[", maxShapeDepth+1) + "null" + strings.Repeat("]", maxShapeDepth+1))); !errors.Is(err, ErrShapeLimit) {
		t.Fatalf("deep JSON err=%v", err)
	}
	if _, err := strictValue([]byte(`{"threadId":"first","\u0074hreadId":"second"}`)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("duplicate decoded key err=%v", err)
	}
	cycle := map[string]any{"$ref": "#/definitions/self"}
	s := &nativeShapes{definitions: map[string]any{"self": cycle}}
	b := &shapeBudget{}
	if s.match([]any{cycle}, nil, 0, b) || !errors.Is(b.err, ErrShapeLimit) {
		t.Fatalf("reference cycle err=%v", b.err)
	}
	b = &shapeBudget{work: maxShapeWork}
	if s.match([]any{true}, nil, 0, b) || !errors.Is(b.err, ErrShapeLimit) {
		t.Fatalf("exhausted work err=%v", b.err)
	}
	b = &shapeBudget{}
	if s.match([]any{map[string]any{"type": "number", "minimum": json.Number("0")}}, json.Number("1e1000000000"), 0, b) || !errors.Is(b.err, ErrShapeLimit) {
		t.Fatalf("exponent construction was not bounded: %v", b.err)
	}
	if s.match([]any{map[string]any{"unexpectedValidation": true}}, nil, 0, &shapeBudget{}) {
		t.Fatal("unknown validation keyword was ignored")
	}
}
