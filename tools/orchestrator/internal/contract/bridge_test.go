package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBridgeStrictDecodeRejectsDuplicateKeysAndUnknownFields(t *testing.T) {
	valid := `{"schema_version":1,"event_id":"hook-1","binding_id":"binding-1","owner_epoch":1,"provider_id":"claude","native_session_id":"session-1","native_event_id":null,"kind":"turn_boundary","observed_at":"2026-10-03T00:00:00.123456789Z","metadata":{"native_hook":"UserPromptSubmit"}}`
	if _, err := DecodeBridgeHookEventV1([]byte(valid)); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	bad := map[string]string{
		"missing_nullable_field": strings.Replace(valid, `,"native_event_id":null`, ``, 1),
		"duplicate_top":          strings.Replace(valid, `"event_id":"hook-1"`, `"event_id":"hook-1","event_id":"hook-2"`, 1),
		"duplicate_nested":       strings.Replace(valid, `"native_hook":"UserPromptSubmit"`, `"native_hook":"UserPromptSubmit","native_hook":"SessionStart"`, 1),
		"trailing":               valid + ` {}`,
		"unknown":                strings.Replace(valid, `"kind":"turn_boundary"`, `"prompt":"secret","kind":"turn_boundary"`, 1),
		"metadata_unknown":       strings.Replace(valid, `"native_hook":"UserPromptSubmit"`, `"native_hook":"UserPromptSubmit","tool_input":"secret"`, 1),
		"version":                strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"kind":                   strings.Replace(valid, `"kind":"turn_boundary"`, `"kind":"result"`, 1),
		"timestamp":              strings.Replace(valid, `"observed_at":"2026-10-03T00:00:00.123456789Z"`, `"observed_at":"yesterday"`, 1),
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeBridgeHookEventV1([]byte(raw)); err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
	if len(valid) >= 65536 {
		t.Fatal("fixture too large")
	}
	near := []byte(valid + strings.Repeat(" ", 65536-len(valid)))
	if _, err := DecodeBridgeHookEventV1(near); err != nil {
		t.Fatalf("65536-byte input rejected: %v", err)
	}
	if _, err := DecodeBridgeHookEventV1(append(near, ' ')); err == nil {
		t.Fatal("65537-byte input accepted")
	}
}

func bridgeDataDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join("testdata", "bridge-v1")
	if _, err := os.Stat(filepath.Join(p, "golden", "contracts.json")); err != nil {
		t.Fatalf("fixed contract fixture missing: %v", err)
	}
	return p
}

func TestBridgeContractSchemasAgreeWithGolden(t *testing.T) {
	base := bridgeDataDir(t)
	raw, err := os.ReadFile(filepath.Join(base, "golden/contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	constructors := map[string]func() any{
		"hook_event":        func() any { return new(BridgeHookEventV1) },
		"bind_request":      func() any { return new(BridgeBindRequest) },
		"activate_request":  func() any { return new(BridgeActivateRequest) },
		"ingest_request":    func() any { return new(BridgeIngestRequest) },
		"claim_request":     func() any { return new(BridgeClaimRequest) },
		"emit_request":      func() any { return new(BridgeEmitRequest) },
		"offered_request":   func() any { return new(BridgeOfferedRequest) },
		"receive_request":   func() any { return new(BridgeReceiveRequest) },
		"redeliver_request": func() any { return new(BridgeRedeliverRequest) },
		"status_request":    func() any { return new(BridgeStatusRequest) },
		"unbind_request":    func() any { return new(BridgeUnbindRequest) },
		"binding_view":      func() any { return new(BridgeBindingView) },
		"ingress_ack":       func() any { return new(BridgeIngressAck) },
		"receive_receipt":   func() any { return new(BridgeReceiveReceipt) },
		"attempt_view":      func() any { return new(BridgeAttemptView) },
		"status_view":       func() any { return new(BridgeStatusView) },
		"batch":             func() any { return new(BridgeBatch) },
		"claim_reply":       func() any { return new(BridgeClaimReply) },
	}
	if len(fixtures) != len(constructors) {
		t.Fatalf("fixture count=%d want %d", len(fixtures), len(constructors))
	}
	for name, newValue := range constructors {
		t.Run(name, func(t *testing.T) {
			fixture := fixtures[name]
			if len(fixture) == 0 {
				t.Fatal("missing fixture")
			}
			value := newValue()
			if err := DecodeBridgeStrict(fixture, value); err != nil {
				t.Fatalf("strict decode: %v", err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var fromFixture, fromGo map[string]any
			if err := json.Unmarshal(fixture, &fromFixture); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &fromGo); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fromFixture, fromGo) {
				t.Fatalf("Go fields differ from golden: fixture=%s Go=%s", fixture, encoded)
			}
			schemaRaw, err := os.ReadFile(filepath.Join(base, "schemas", name+".schema.json"))
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties           map[string]json.RawMessage `json:"properties"`
				Required             []string                   `json:"required"`
				AdditionalProperties bool                       `json:"additionalProperties"`
			}
			if err := json.Unmarshal(schemaRaw, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.AdditionalProperties {
				t.Fatal("schema permits unknown top-level properties")
			}
			if len(schema.Properties) != len(fromFixture) {
				t.Fatalf("schema fields=%d golden=%d", len(schema.Properties), len(fromFixture))
			}
			for key := range fromFixture {
				if _, ok := schema.Properties[key]; !ok {
					t.Fatalf("golden field %q not in schema", key)
				}
			}
			for _, key := range schema.Required {
				if _, ok := fromFixture[key]; !ok {
					t.Fatalf("required %q missing from golden", key)
				}
			}
			if !bytes.Contains(schemaRaw, []byte(`"schema_version"`)) && name != "ingress_ack" && name != "receive_receipt" && name != "attempt_view" && name != "binding_view" {
				t.Fatal("version field missing")
			}
			if name == "hook_event" && !bytes.Contains(schemaRaw, []byte(`"x-maxEncodedBytes": 65536`)) {
				t.Fatal("raw byte limit annotation missing")
			}
			if name == "batch" {
				if !bytes.Contains(schemaRaw, []byte(`"x-maxUtf8Bytes": 512`)) || !bytes.Contains(schemaRaw, []byte(`"x-maxEncodedBytes": 8192`)) || !bytes.Contains(schemaRaw, []byte(`"complete provider hookSpecificOutput wrapper`)) {
					t.Fatal("UTF-8 or final-wrapper byte annotation missing")
				}
			}
		})
	}
}

func TestBridgeRPCDecodeRequiredVersionAndSemantics(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(bridgeDataDir(t), "golden/contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	owner := []string{"schema_version", "binding_id", "owner_epoch", "request_id", "owner_mode", "owner_capability_file", "control_file"}
	hook := []string{"schema_version", "binding_id", "owner_epoch", "request_id", "hook_capability"}
	base := []string{"schema_version", "binding_id", "owner_epoch", "request_id"}
	cases := []struct {
		name     string
		required []string
		decode   func([]byte) error
	}{
		{"bind_request", []string{"schema_version", "request_id", "run_id", "provider_id", "native_pid", "native_birth", "native_executable_sha256", "transport_mode", "owner_mode", "owner_capability_file", "control_file", "manifest_sha256"}, func(raw []byte) error { _, e := DecodeBridgeBindRequest(raw); return e }},
		{"activate_request", append(append([]string{}, owner...), "native_session_id", "observation_event_id", "observation_hash"), func(raw []byte) error { _, e := DecodeBridgeActivateRequest(raw); return e }},
		{"ingest_request", append(append([]string{}, hook...), "event"), func(raw []byte) error { _, e := DecodeBridgeIngestRequest(raw); return e }},
		{"claim_request", append(append([]string{}, hook...), "native_hook"), func(raw []byte) error { _, e := DecodeBridgeClaimRequest(raw); return e }},
		{"emit_request", append(append([]string{}, base...), "batch_id", "attempt_id", "claim_token", "render_sha256", "wire_sha256"), func(raw []byte) error { _, e := DecodeBridgeEmitRequest(raw); return e }},
		{"offered_request", append(append([]string{}, base...), "batch_id", "attempt_id", "claim_token", "wire_sha256"), func(raw []byte) error { _, e := DecodeBridgeOfferedRequest(raw); return e }},
		{"receive_request", append(append([]string{}, owner...), "batch_id", "attempt_id", "render_sha256"), func(raw []byte) error { _, e := DecodeBridgeReceiveRequest(raw); return e }},
		{"redeliver_request", append(append([]string{}, owner...), "batch_id", "attempt_id", "reason"), func(raw []byte) error { _, e := DecodeBridgeRedeliverRequest(raw); return e }},
		{"status_request", owner, func(raw []byte) error { _, e := DecodeBridgeStatusRequest(raw); return e }},
		{"unbind_request", owner, func(raw []byte) error { _, e := DecodeBridgeUnbindRequest(raw); return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fixtures[tc.name]
			if err := tc.decode(fixture); err != nil {
				t.Fatalf("golden rejected: %v", err)
			}
			schemaRaw, err := os.ReadFile(filepath.Join(bridgeDataDir(t), "schemas", tc.name+".schema.json"))
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(schemaRaw, &schema); err != nil {
				t.Fatal(err)
			}
			if !sameStringSet(schema.Required, tc.required) {
				t.Fatalf("schema required=%v want=%v", schema.Required, tc.required)
			}
			for _, field := range tc.required {
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(fixture, &obj); err != nil {
					t.Fatal(err)
				}
				delete(obj, field)
				raw, err := json.Marshal(obj)
				if err != nil {
					t.Fatal(err)
				}
				if err := tc.decode(raw); err == nil {
					t.Errorf("missing required %s accepted", field)
				}
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(fixture, &obj); err != nil {
				t.Fatal(err)
			}
			obj["schema_version"] = json.RawMessage(`2`)
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.decode(raw); err == nil {
				t.Error("version 2 accepted")
			}
			obj["schema_version"] = json.RawMessage(`1`)
			obj["forbidden_extra"] = json.RawMessage(`true`)
			raw, err = json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.decode(raw); err == nil {
				t.Error("unknown request field accepted")
			}
		})
	}
	for _, mutation := range []struct{ name, field, value string }{
		{"bind_bad_pid", "native_pid", `0`}, {"bind_bad_mode", "transport_mode", `"attached_push"`},
		{"bind_bad_owner_mode", "owner_mode", `"worker"`}, {"bind_bad_digest", "manifest_sha256", `"bad"`},
	} {
		var obj map[string]json.RawMessage
		_ = json.Unmarshal(fixtures["bind_request"], &obj)
		obj[mutation.field] = json.RawMessage(mutation.value)
		raw, _ := json.Marshal(obj)
		if _, err := DecodeBridgeBindRequest(raw); err == nil {
			t.Errorf("%s accepted", mutation.name)
		}
	}
	var status map[string]json.RawMessage
	_ = json.Unmarshal(fixtures["status_request"], &status)
	delete(status, "cursor")
	firstPage, _ := json.Marshal(status)
	if _, err := DecodeBridgeStatusRequest(firstPage); err != nil {
		t.Fatalf("first status page rejected: %v", err)
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, value := range a {
		seen[value] = true
	}
	for _, value := range b {
		if !seen[value] {
			return false
		}
	}
	return true
}

func TestBridgeNestedIngestEventValidation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(bridgeDataDir(t), "golden/contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ name, field, value string }{
		{"kind", "kind", `"result"`}, {"version", "schema_version", `2`},
		{"binding", "binding_id", `"other"`}, {"epoch", "owner_epoch", `5`},
		{"missing_native_event_id", "native_event_id", ""},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			var req map[string]json.RawMessage
			_ = json.Unmarshal(fixtures["ingest_request"], &req)
			var event map[string]json.RawMessage
			_ = json.Unmarshal(req["event"], &event)
			if mutation.value == "" {
				delete(event, mutation.field)
			} else {
				event[mutation.field] = json.RawMessage(mutation.value)
			}
			req["event"], _ = json.Marshal(event)
			raw, _ := json.Marshal(req)
			if _, err := DecodeBridgeIngestRequest(raw); err == nil {
				t.Fatal("invalid nested event accepted")
			}
		})
	}
}

func TestBridgeOptionalMetadataMatchesSchema(t *testing.T) {
	valid := `{"schema_version":1,"event_id":"hook-1","binding_id":"binding-1","owner_epoch":1,"provider_id":"claude","native_session_id":"session-1","native_event_id":null,"kind":"tool_observed","observed_at":"2026-10-03T00:00:00Z","metadata":{"native_hook":"PostToolUse"}}`
	if _, err := DecodeBridgeHookEventV1([]byte(valid)); err != nil {
		t.Fatalf("optional metadata absent rejected: %v", err)
	}
	for _, field := range []string{"tool_name", "tool_status"} {
		for _, value := range []string{`null`, `""`, `"not-enumerated"`} {
			raw := strings.Replace(valid, `"native_hook":"PostToolUse"`, `"native_hook":"PostToolUse","`+field+`":`+value, 1)
			if _, err := DecodeBridgeHookEventV1([]byte(raw)); err == nil {
				t.Errorf("%s=%s accepted", field, value)
			}
		}
	}
	valid = strings.Replace(valid, `"native_hook":"PostToolUse"`, `"native_hook":"PostToolUse","tool_name":"Read","tool_status":"success"`, 1)
	if _, err := DecodeBridgeHookEventV1([]byte(valid)); err != nil {
		t.Fatalf("enumerated optional metadata rejected: %v", err)
	}
}

func TestBridgeTransportAndProofEnumsFrozen(t *testing.T) {
	base := bridgeDataDir(t)
	for _, name := range []string{"bind_request", "binding_view", "batch", "claim_reply", "status_view"} {
		raw, err := os.ReadFile(filepath.Join(base, "schemas", name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		var walk func(any)
		walk = func(node any) {
			switch value := node.(type) {
			case map[string]any:
				for key, child := range value {
					if key == "transport_mode" || key == "proof_mode" {
						property, ok := child.(map[string]any)
						if !ok {
							t.Fatalf("%s.%s invalid schema", name, key)
						}
						items, ok := property["enum"].([]any)
						if !ok || len(items) != 1 {
							t.Fatalf("%s.%s missing closed enum", name, key)
						}
						want := "hook_pull"
						if key == "proof_mode" {
							want = "collect"
						}
						if items[0] != want {
							t.Fatalf("%s.%s enum=%v want %s", name, key, items, want)
						}
						seen[key]++
					}
					walk(child)
				}
			case []any:
				for _, child := range value {
					walk(child)
				}
			}
		}
		walk(schema)
		if name == "bind_request" || name == "binding_view" || name == "batch" || name == "claim_reply" {
			if seen["transport_mode"] == 0 {
				t.Fatalf("%s has no transport enum", name)
			}
		}
		if name == "batch" || name == "claim_reply" || name == "status_view" {
			if seen["proof_mode"] == 0 {
				t.Fatalf("%s has no proof enum", name)
			}
		}
	}
}

func TestBridgeStrictDecodeRejectsCaseAliasesAtEveryLayer(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(bridgeDataDir(t), "golden", "contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	bind := string(fixtures["bind_request"])
	canonical := `"owner_mode": "local"`
	if !strings.Contains(bind, canonical) {
		t.Fatal("bind fixture changed")
	}
	for name, replacement := range map[string]string{
		"canonical_then_alias": canonical + `, "OWNER_MODE": "native"`,
		"alias_then_canonical": `"OWNER_MODE": "native", ` + canonical,
		"alias_only":           `"OWNER_MODE": "native"`,
		"exact_duplicate":      canonical + `, "owner_mode": "native"`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(strings.Replace(bind, canonical, replacement, 1))
			var target BridgeBindRequest
			if err := DecodeBridgeStrict(raw, &target); err == nil {
				t.Fatal("noncanonical or duplicate key accepted by strict decoder")
			}
			if _, err := DecodeBridgeBindRequest(raw); err == nil {
				t.Fatal("noncanonical or duplicate key accepted by typed RPC decoder")
			}
		})
	}
	for _, tc := range []struct {
		name, fixture, canonical, alias string
		target                          any
	}{
		{"nested_event_collision", "ingest_request", `"kind": "turn_boundary"`, `"KIND": "session_started"`, new(BridgeIngestRequest)},
		{"nested_event_alias_only", "ingest_request", `"kind": "turn_boundary"`, `"KIND": "turn_boundary"`, new(BridgeIngestRequest)},
		{"metadata_collision", "ingest_request", `"native_hook": "UserPromptSubmit"`, `"NATIVE_HOOK": "SessionStart"`, new(BridgeIngestRequest)},
		{"metadata_reverse", "ingest_request", `"native_hook": "UserPromptSubmit"`, `"NATIVE_HOOK": "SessionStart"`, new(BridgeIngestRequest)},
		{"metadata_alias_only", "ingest_request", `"native_hook": "UserPromptSubmit"`, `"NATIVE_HOOK": "UserPromptSubmit"`, new(BridgeIngestRequest)},
		{"array_event_collision", "batch", `"event_id": "event-1"`, `"EVENT_ID": "event-2"`, new(BridgeBatch)},
		{"array_event_reverse", "batch", `"event_id": "event-1"`, `"EVENT_ID": "event-2"`, new(BridgeBatch)},
		{"array_event_alias_only", "batch", `"event_id": "event-1"`, `"EVENT_ID": "event-1"`, new(BridgeBatch)},
		{"array_receipt_collision", "batch", `"proof_mode": "collect"`, `"PROOF_MODE": "native"`, new(BridgeBatch)},
		{"array_receipt_alias_only", "batch", `"proof_mode": "collect"`, `"PROOF_MODE": "collect"`, new(BridgeBatch)},
		{"nested_artifact_alias", "batch", `"id": "artifact-1"`, `"ID": "artifact-2"`, new(BridgeBatch)},
		{"status_counts_alias", "status_view", `"pending": 0`, `"PENDING": 1`, new(BridgeStatusView)},
		{"status_batch_alias", "status_view", `"batch_id": "batch-1"`, `"BATCH_ID": "batch-2"`, new(BridgeStatusView)},
		{"claim_reply_nested_alias", "claim_reply", `"batch_id": "batch-1"`, `"BATCH_ID": "batch-2"`, new(BridgeClaimReply)},
		{"attempt_view_alias", "attempt_view", `"state": "UNCERTAIN"`, `"STATE": "OFFERED"`, new(BridgeAttemptView)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := string(fixtures[tc.fixture])
			if !strings.Contains(fixture, tc.canonical) {
				t.Fatal("fixture field missing")
			}
			var replacement string
			if strings.HasSuffix(tc.name, "alias_only") {
				replacement = tc.alias
			} else if strings.HasSuffix(tc.name, "reverse") {
				replacement = tc.alias + "," + tc.canonical
			} else {
				replacement = tc.canonical + "," + tc.alias
			}
			raw := []byte(strings.Replace(fixture, tc.canonical, replacement, 1))
			if err := DecodeBridgeStrict(raw, tc.target); err == nil {
				t.Fatal("nested case alias accepted")
			}
		})
	}
	unknownKey := "SECRET_TOKEN_SENSITIVE_KEY"
	unknownValue := "canary-secret-value"
	raw := []byte(strings.Replace(bind, canonical, canonical+`, "`+unknownKey+`": "`+unknownValue+`"`, 1))
	var target BridgeBindRequest
	err = DecodeBridgeStrict(raw, &target)
	if err == nil || strings.Contains(err.Error(), unknownKey) || strings.Contains(err.Error(), unknownValue) {
		t.Fatalf("unknown-field diagnostic leaked key/value: %v", err)
	}
	// Key spelling is exact; ordinary string values retain their original case.
	if _, err := DecodeBridgeBindRequest(fixtures["bind_request"]); err != nil {
		t.Fatalf("canonical key rejected: %v", err)
	}
	batch := strings.Replace(string(fixtures["batch"]), `"summary": "你好 \"review\"\n"`, `"summary": "OWNER_MODE native_hook"`, 1)
	if batch == string(fixtures["batch"]) {
		t.Fatal("summary fixture changed")
	}
	var visible BridgeBatch
	if err := DecodeBridgeStrict([]byte(batch), &visible); err != nil || visible.Events[0].Summary != "OWNER_MODE native_hook" {
		t.Fatalf("case-sensitive string data changed: value=%q err=%v", visible.Events[0].Summary, err)
	}
}
