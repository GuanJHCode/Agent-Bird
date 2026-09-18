package host

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProviderDiagnosticsExactCategories(t *testing.T) {
	body, err := json.Marshal(providerDiagnostics(`{"type":"system|assistant","message":{"content":[{"type":"tool_use","name":"Read|Edit"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "|") || !strings.Contains(string(body), `"events":{"other":1}`) || !strings.Contains(string(body), `"tools":{"other":1}`) {
		t.Fatalf("non-allowlisted category preserved: %s", body)
	}
}

func TestProviderDiagnosticsContainCountsWithoutToolInputs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "producer")
	if err != nil {
		t.Fatal(err)
	}
	script := `printf '%s\n' '{"type":"system","subtype":"init","session_id":"diag"}' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"PRIVATE_SENTINEL"}}]}}' '{"type":"system","subtype":"permission_denied","tool_name":"Task","decision_reason_type":"rule","decision_reason":"PRIVATE_SENTINEL","message":"PRIVATE_SENTINEL"}' '{"type":"result","subtype":"success","session_id":"diag","result":"ok"}'`
	result, err := h.execute(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/bin/sh", Args: []string{"-c", script}}, false, launchMetadata{commandID: "command", workRevision: 1, outputProvider: "claude-code"})
	if err != nil || result.Status != "result_ready" {
		t.Fatalf("fixture: %+v %v", result, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "attempt", "segment", "provider-diagnostics.json"))
	if err != nil {
		t.Fatalf("provider timeout cannot be diagnosed without private transcript: %v", err)
	}
	var diagnostics struct {
		Tools          map[string]int `json:"tools"`
		Events         map[string]int `json:"events"`
		SystemSubtypes map[string]int `json:"system_subtypes"`
		StreamDenials  int            `json:"stream_denials"`
		DeniedTools    map[string]int `json:"stream_denied_tools"`
	}
	if json.Unmarshal(body, &diagnostics) != nil || diagnostics.Tools["Read"] != 1 || diagnostics.Events["result"] != 1 || diagnostics.SystemSubtypes["init"] != 1 || diagnostics.StreamDenials != 1 || diagnostics.DeniedTools["Task"] != 1 || strings.Contains(string(body), "PRIVATE_SENTINEL") {
		t.Fatalf("unsafe or incomplete diagnostics: %s", body)
	}
}

func TestProviderDiagnosticsResultShapeOnly(t *testing.T) {
	for _, tc := range []struct{ text, shape string }{{"", "empty"}, {"PRIVATE_SENTINEL", "text"}, {"```json\n{\"decision\":\"approve\"}\n```", "fenced"}, {`{"decision":"approve","summary":"PRIVATE_SENTINEL"}`, "json_object"}, {`["PRIVATE_SENTINEL"]`, "json_other"}} {
		event, _ := json.Marshal(map[string]any{"type": "result", "result": tc.text, "structured_output": map[string]string{"private": "PRIVATE_SENTINEL"}})
		body, err := json.Marshal(providerDiagnostics(string(event)))
		var got struct {
			Results    map[string]int `json:"result_shapes"`
			Structured int            `json:"structured_results"`
		}
		if err != nil || json.Unmarshal(body, &got) != nil || got.Results[tc.shape] != 1 || got.Structured != 1 || strings.Contains(string(body), "PRIVATE_SENTINEL") {
			t.Fatalf("unsafe or missing shape %s: %s %v", tc.shape, body, err)
		}
	}
}

func TestProviderDiagnosticsToolErrorsAreAllowlisted(t *testing.T) {
	body, _ := json.Marshal(providerDiagnostics(`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"PRIVATE_SENTINEL Operation not permitted"},{"type":"tool_result","is_error":true,"content":"PRIVATE_SENTINEL"}]}}`))
	var got struct {
		Errors map[string]int `json:"tool_errors"`
	}
	if json.Unmarshal(body, &got) != nil || got.Errors["access_denied"] != 1 || got.Errors["other"] != 1 || strings.Contains(string(body), "PRIVATE_SENTINEL") {
		t.Fatalf("unsafe or missing tool errors: %s", body)
	}
}

// Unknown subtype strings, objects and missing values must never become log
// categories; non-system subtype fields must not contaminate these counts.
func TestProviderDiagnosticsSystemSubtypesAreAllowlisted(t *testing.T) {
	events := []map[string]any{}
	for _, subtype := range []any{"init", "hook_started", "hook_progress", "hook_response", "status", "compact_boundary", "task_started", "task_progress", "task_notification", "status", "PRIVATE_SENTINEL /private/credentials", "hook_started|hook_response", " status", "", nil, map[string]any{"PRIVATE_SENTINEL": "secret"}} {
		event := map[string]any{"type": "system", "subtype": subtype, "configuration": map[string]any{"token": "PRIVATE_SENTINEL"}, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "PRIVATE_SENTINEL"}}}}
		if subtype == nil {
			delete(event, "subtype")
		}
		events = append(events, event)
	}
	events = append(events, map[string]any{"type": "assistant", "subtype": "status"}, map[string]any{"type": "PRIVATE_SENTINEL", "subtype": "init"})
	var input strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		input.Write(line)
		input.WriteByte('\n')
	}
	body, err := json.Marshal(providerDiagnostics(input.String()))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Subtypes map[string]int `json:"system_subtypes"`
		Events   map[string]int `json:"events"`
	}
	if err = json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"init": 1, "hook_started": 1, "hook_progress": 1, "hook_response": 1, "status": 2, "compact_boundary": 1, "task_started": 1, "task_progress": 1, "task_notification": 1, "other": 6}
	if !reflect.DeepEqual(got.Subtypes, want) || got.Events["system"] != 16 || got.Events["assistant"] != 1 || got.Events["other"] != 1 {
		t.Fatalf("missing or unsafe subtype counts: %s", body)
	}
	for _, secret := range []string{"PRIVATE_SENTINEL", "/private/credentials", "hook_started|hook_response", "configuration", "token"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("raw field escaped into diagnostics: %s", body)
		}
	}
}

func TestProviderDiagnosticsStreamDenialWithoutResult(t *testing.T) {
	input := `{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"PRIVATE_SENTINEL","agent_id":"PRIVATE_SENTINEL","decision_reason_type":"mode","decision_reason":"PRIVATE_SENTINEL /private/credentials","message":"PRIVATE_SENTINEL denied","tool_input":{"token":"PRIVATE_SENTINEL"},"uuid":"PRIVATE_SENTINEL","session_id":"PRIVATE_SENTINEL"}`
	body, err := json.Marshal(providerDiagnostics(input))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		StreamDenials     int            `json:"stream_denials"`
		PermissionDenials int            `json:"permission_denials"`
		Tools             map[string]int `json:"stream_denied_tools"`
		Reasons           map[string]int `json:"stream_denial_reasons"`
		Subtypes          map[string]int `json:"system_subtypes"`
		Events            map[string]int `json:"events"`
	}
	if err = json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.StreamDenials != 1 || got.PermissionDenials != 0 || got.Tools["Bash"] != 1 || got.Reasons["mode"] != 1 || got.Subtypes["permission_denied"] != 1 || got.Events["system"] != 1 || got.Events["result"] != 0 {
		t.Fatalf("missing stream denial without result: %s", body)
	}
	for _, secret := range []string{"PRIVATE_SENTINEL", "/private/credentials", "tool_input", "decision_reason\"", "message", "uuid", "session_id", "tool_use_id"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("raw denial data escaped: %s", body)
		}
	}
}

func TestProviderDiagnosticsStreamDenialCategoriesAndResultCountsStaySeparate(t *testing.T) {
	var input strings.Builder
	cases := []struct{ tool, reason any }{
		{"Agent", "rule"}, {"Task", "mode"}, {"Bash", "classifier"}, {"Read", "asyncAgent"},
		{"PRIVATE_SENTINEL", "PRIVATE_SENTINEL"}, {map[string]string{"token": "PRIVATE_SENTINEL"}, 7}, {nil, nil},
	}
	for _, tc := range cases {
		e := map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": tc.tool, "decision_reason_type": tc.reason, "message": "PRIVATE_SENTINEL", "decision_reason": map[string]string{"type": "rule", "text": "PRIVATE_SENTINEL"}}
		if tc.tool == nil {
			delete(e, "tool_name")
		}
		if tc.reason == nil {
			delete(e, "decision_reason_type")
		}
		line, _ := json.Marshal(e)
		input.Write(line)
		input.WriteByte('\n')
	}
	input.WriteString(`{"type":"result","permission_denials":[{"tool_name":"PRIVATE_SENTINEL","tool_input":{"token":"PRIVATE_SENTINEL"}},{"tool_name":"Agent"}]}` + "\n")
	input.WriteString(`{"type":"assistant","subtype":"permission_denied","tool_name":"Agent","decision_reason_type":"rule"}` + "\n")
	input.WriteString(`{"type":"system","subtype":"PRIVATE_SENTINEL","tool_name":"Agent","decision_reason_type":"rule"}` + "\n")
	body, err := json.Marshal(providerDiagnostics(input.String()))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		StreamDenials     int            `json:"stream_denials"`
		PermissionDenials int            `json:"permission_denials"`
		Tools             map[string]int `json:"stream_denied_tools"`
		Reasons           map[string]int `json:"stream_denial_reasons"`
		Subtypes          map[string]int `json:"system_subtypes"`
	}
	if err = json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	wantTools := map[string]int{"Agent": 1, "Task": 1, "Bash": 1, "Read": 1, "other": 3}
	wantReasons := map[string]int{"rule": 1, "mode": 1, "classifier": 1, "asyncAgent": 1, "other": 3}
	if got.StreamDenials != 7 || got.PermissionDenials != 2 || !reflect.DeepEqual(got.Tools, wantTools) || !reflect.DeepEqual(got.Reasons, wantReasons) || got.Subtypes["permission_denied"] != 7 || got.Subtypes["other"] != 1 {
		t.Fatalf("unsafe or merged denial counters: %s", body)
	}
	if strings.Contains(string(body), "PRIVATE_SENTINEL") || strings.Contains(string(body), "tool_input") {
		t.Fatalf("raw denial data escaped: %s", body)
	}
}

func TestProviderProtocolDiagnosticsExposeOnlyFixedErrorCategories(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{"event_invalid_json", "event_invalid_json"}, {"provider_session_changed", "provider_session_changed"}, {"event_session_id_invalid", "event_session_id_invalid"}, {"event_invalid_json PRIVATE_SNAPSHOT_SENTINEL", "other"}, {"/private/credentials PRIVATE_SNAPSHOT_SENTINEL", "other"}, {"", "none"}} {
		collector, _ := newProtocolCollector("claude-code")
		if tc.raw != "" {
			collector.err = errors.New(tc.raw)
		}
		report := providerDiagnostics(`{"type":"system","subtype":"init","session_id":"PRIVATE_SNAPSHOT_SENTINEL"}`)
		report.ProtocolSnapshots = &protocolDiagnostics{AfterWait: collector.Snapshot()}
		body, err := json.Marshal(report)
		if err != nil || strings.Contains(string(body), "PRIVATE_SNAPSHOT_SENTINEL") || strings.Contains(string(body), "/private/credentials") {
			t.Fatal("private details escaped fixed diagnostics")
		}
		if report.ProtocolSnapshots.AfterWait.ProtocolError != tc.want {
			t.Fatalf("error category=%q want=%q", report.ProtocolSnapshots.AfterWait.ProtocolError, tc.want)
		}
	}
}
