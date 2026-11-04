package host

import (
	"bufio"
	"encoding/json"
	"strings"
)

type providerDiagnosticReport struct {
	CodexAuthGuardCodes map[string]int       `json:"codex_auth_guard_codes,omitempty"`
	ToolErrors          map[string]int       `json:"tool_errors"`
	ResultShapes        map[string]int       `json:"result_shapes"`
	StructuredResults   int                  `json:"structured_results"`
	Version             int                  `json:"version"`
	Bytes               int                  `json:"captured_bytes"`
	Truncated           bool                 `json:"truncated"`
	Events              map[string]int       `json:"events"`
	SystemSubtypes      map[string]int       `json:"system_subtypes"`
	Tools               map[string]int       `json:"tools"`
	PermissionDenials   int                  `json:"permission_denials"`
	StreamDenials       int                  `json:"stream_denials"`
	StreamDeniedTools   map[string]int       `json:"stream_denied_tools"`
	StreamDenialReasons map[string]int       `json:"stream_denial_reasons"`
	ProtocolSnapshots   *protocolDiagnostics `json:"protocol_snapshots,omitempty"`
}

// Capture only allowlisted event/tool categories from the already bounded
// process buffer. Prompts, tool arguments, messages and environment are omitted.
func providerDiagnostics(output string) providerDiagnosticReport {
	result := providerDiagnosticReport{CodexAuthGuardCodes: map[string]int{}, ToolErrors: map[string]int{}, ResultShapes: map[string]int{}, Version: 1, Bytes: len(output), Truncated: len(output) >= 1024*1024, Events: map[string]int{}, SystemSubtypes: map[string]int{}, Tools: map[string]int{}, StreamDeniedTools: map[string]int{}, StreamDenialReasons: map[string]int{}}
	category := func(value string, allowed string) string {
		for _, entry := range strings.Split(allowed, "|") {
			if value == entry {
				return entry
			}
		}
		return "other"
	}
	type block struct {
		Type    string          `json:"type"`
		Name    string          `json:"name"`
		IsError bool            `json:"is_error"`
		Content json.RawMessage `json:"content"`
	}
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 64*1024)
	records := 0
	for scanner.Scan() {
		records++
		if records > 4096 {
			result.Truncated = true
			break
		}
		var event struct {
			Code               string          `json:"code"`
			Result             string          `json:"result"`
			StructuredOutput   json.RawMessage `json:"structured_output"`
			Type               string          `json:"type"`
			Subtype            json.RawMessage `json:"subtype"`
			Message            json.RawMessage `json:"message"`
			GrokToolName       json.RawMessage `json:"toolName"`
			ToolName           json.RawMessage `json:"tool_name"`
			DecisionReasonType json.RawMessage `json:"decision_reason_type"`
			Event              json.RawMessage `json:"event"`
			PermissionDenials  []struct{}      `json:"permission_denials"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			result.Events["unparsed"]++
			continue
		}
		// These are untrusted bounded diagnostics, never admission evidence.
		if event.Type == "codex_auth_guard" {
			result.CodexAuthGuardCodes[category(event.Code, "ready|codex_policy_untrusted|auth_expiry_unverified|auth_kind_unsupported|native_bundle_untrusted|refresh_override_already_present|auth_access_token_expired|auth_metadata_expired|auth_source_changed|auth_projection_changed|auth_source_unavailable|auth_source_invalid|auth_source_untrusted|auth_config_source_unsupported|auth_config_source_unverified|auth_projection_path_untrusted|auth_environment_unsupported|native_login_status_failed|native_login_status_unverified|native_version_mismatch|native_sha256_mismatch|native_path_untrusted|native_binary_unavailable|codex_exec_args_invalid|launch_failed")]++
			continue
		}
		// Providers disagree about the shape of "event": Anthropic-shaped output
		// carries an object, AGY names each lifecycle step with a string. Only an
		// object can hold a content_block, so a string is read as "nothing to
		// count" rather than as unparseable output.
		var streamEvent struct {
			ContentBlock *block `json:"content_block"`
		}
		if rawEvent := strings.TrimSpace(string(event.Event)); rawEvent != "" {
			if rawEvent[0] != '"' && (rawEvent[0] != '{' || json.Unmarshal(event.Event, &streamEvent) != nil) {
				result.Events["unparsed"]++
				continue
			}
		}
		var message struct {
			Content []block `json:"content"`
		}
		if len(event.Message) != 0 && json.Unmarshal(event.Message, &message) != nil && event.Type != "system" {
			result.Events["unparsed"]++
			continue
		}
		if event.Type == "system" {
			var subtype string
			// Missing/non-string values are only "other". The allowlist is a
			// diagnostic vocabulary, not a claim about every Provider version.
			_ = json.Unmarshal(event.Subtype, &subtype)
			result.SystemSubtypes[category(subtype, "init|hook_started|hook_progress|hook_response|status|compact_boundary|task_started|task_progress|task_notification|permission_denied")]++
			if subtype == "permission_denied" {
				var tool, reasonType string
				_ = json.Unmarshal(event.ToolName, &tool)
				_ = json.Unmarshal(event.DecisionReasonType, &reasonType)
				result.StreamDenials++
				result.StreamDeniedTools[category(tool, "Read|Edit|Write|Bash|Glob|Grep|Agent|Task|Skill|ExitPlanMode|EnterPlanMode|AskUserQuestion|TodoWrite")]++
				// SDK reason types are open strings. This is our fixed vocabulary,
				// not an exhaustive Provider enum. Never inspect decision_reason.
				result.StreamDenialReasons[category(reasonType, "rule|mode|classifier|asyncAgent")]++
			}
		}
		if event.Type == "result" {
			shape := "text"
			value := strings.TrimSpace(event.Result)
			switch {
			case value == "":
				shape = "empty"
			case strings.HasPrefix(value, "```"):
				shape = "fenced"
			case json.Valid([]byte(value)):
				shape = "json_other"
				if strings.HasPrefix(value, "{") {
					shape = "json_object"
				}
			}
			result.ResultShapes[shape]++
			if len(event.StructuredOutput) != 0 && string(event.StructuredOutput) != "null" {
				result.StructuredResults++
			}
		}
		if event.Type == "tool_call" {
			var name string
			_ = json.Unmarshal(event.GrokToolName, &name)
			result.Tools[category(name, "read_file|list_directory|search|grep|glob|Read|Glob|Grep")]++
		}
		result.Events[category(event.Type, "system|assistant|user|result|stream_event|error|rate_limit_event|text|tool_started|tool_completed|tool_call|tool_call_update|usage|end")]++
		count := func(b block) {
			if b.Type == "tool_result" && b.IsError {
				code := "other"
				if strings.Contains(string(b.Content), "Operation not permitted") || strings.Contains(string(b.Content), "Permission denied") {
					code = "access_denied"
				}
				result.ToolErrors[code]++
			}
			if b.Type == "tool_use" {
				result.Tools[category(b.Name, "Read|Edit|Write|Bash|Glob|Grep|Agent|Task|Skill|ExitPlanMode|EnterPlanMode|AskUserQuestion|TodoWrite")]++
			}
		}
		for _, b := range message.Content {
			count(b)
		}
		if streamEvent.ContentBlock != nil {
			count(*streamEvent.ContentBlock)
		}
		result.PermissionDenials += len(event.PermissionDenials)
	}
	if scanner.Err() != nil {
		result.Truncated = true
	}
	return result
}
