// Package adapter contains the pinned, protocol-only surface for the
// external coding CLIs. It deliberately does not authenticate, select an
// account, or persist provider output.
package adapter

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Provider string

const (
	ProviderClaude Provider = "claude-code"
	ProviderAGY    Provider = "antigravity-cli"
	ProviderGrok   Provider = "grok-build"
	ProviderCodex  Provider = "codex-cli"

	CodexVersion = "codex-cli 0.154.0"
	CodexSHA256  = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
)

type SessionKind string

const (
	SessionID         SessionKind = "session-id"
	ConversationID    SessionKind = "conversation-id"
	SessionMostRecent SessionKind = "most-recent"
)

type BinaryPin struct {
	Path    string
	Version string
	SHA256  string
}

type SessionRef struct {
	Kind SessionKind
	ID   string
}

type Permission struct {
	Mode  string
	Allow []string
	Deny  []string
}

type Request struct {
	Provider   Provider
	Binary     BinaryPin
	CWD        string
	Prompt     string
	Session    SessionRef
	Permission Permission
	ExtraArgs  []string
}

type Invocation struct {
	provider Provider
	pin      BinaryPin
	argv     []string
	cwd      string
	input    []byte
	env      map[string]string
}

func (i Invocation) ProviderName() Provider   { return i.provider }
func (i Invocation) OutputProvider() string   { return string(i.provider) }
func (i Invocation) Pin() BinaryPin           { return i.pin }
func (i Invocation) Args() []string           { return append([]string(nil), i.argv...) }
func (i Invocation) WorkingDirectory() string { return i.cwd }
func (i Invocation) Stdin() []byte            { return append([]byte(nil), i.input...) }
func (i Invocation) Environment() map[string]string {
	copy := map[string]string{}
	for key, value := range i.env {
		copy[key] = value
	}
	return copy
}

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
	Kind      EventKind
	SessionID string
	Status    string
	Text      string
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

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidatePin(pin BinaryPin, actualVersion, actualSHA256 string) error {
	if pin.Path == "" || !filepath.IsAbs(pin.Path) || filepath.Clean(pin.Path) != pin.Path {
		return errors.New("binary_path_invalid")
	}
	if pin.Version == "" || actualVersion == "" || pin.Version != actualVersion {
		return errors.New("binary_version_mismatch")
	}
	if !hex64.MatchString(strings.ToLower(pin.SHA256)) || !hex64.MatchString(strings.ToLower(actualSHA256)) || strings.ToLower(pin.SHA256) != strings.ToLower(actualSHA256) {
		return errors.New("binary_sha256_mismatch")
	}
	return nil
}

// VerifyExecutable re-reads the current pinned executable. The caller supplies
// the output of an already bounded --version invocation; this function never
// reads provider configuration or authentication state.
func VerifyExecutable(pin BinaryPin, actualVersion string) error {
	if err := ValidatePin(pin, actualVersion, pin.SHA256); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(pin.Path)
	if err != nil || resolved != pin.Path {
		return errors.New("binary_path_changed")
	}
	info, err := os.Stat(pin.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Mode()&0o022 != 0 {
		return errors.New("binary_file_unsafe")
	}
	file, err := os.Open(pin.Path)
	if err != nil {
		return errors.New("binary_read_failed")
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return errors.New("binary_read_failed")
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != strings.ToLower(pin.SHA256) {
		return errors.New("binary_sha256_mismatch")
	}
	return nil
}

func BuildInvocation(req Request) (Invocation, error) {
	if req.Provider != ProviderClaude && req.Provider != ProviderAGY && req.Provider != ProviderGrok && req.Provider != ProviderCodex {
		return Invocation{}, errors.New("provider_unknown")
	}
	if req.Binary.Path == "" || !filepath.IsAbs(req.Binary.Path) || filepath.Clean(req.Binary.Path) != req.Binary.Path {
		return Invocation{}, errors.New("binary_path_invalid")
	}
	if req.Binary.Version == "" || !hex64.MatchString(strings.ToLower(req.Binary.SHA256)) {
		return Invocation{}, errors.New("binary_pin_invalid")
	}
	if req.Provider == ProviderCodex && (req.Binary.Version != CodexVersion || strings.ToLower(req.Binary.SHA256) != CodexSHA256) {
		return Invocation{}, errors.New("codex_binary_pin_mismatch")
	}
	if req.CWD == "" || !filepath.IsAbs(req.CWD) || filepath.Clean(req.CWD) != req.CWD {
		return Invocation{}, errors.New("cwd_invalid")
	}
	if req.Provider == ProviderCodex && (req.Prompt == "" || strings.ContainsRune(req.Prompt, '\x00')) {
		return Invocation{}, errors.New("codex_prompt_invalid")
	}
	if err := validatePermission(req.Provider, req.Permission); err != nil {
		return Invocation{}, err
	}
	if err := validateExtraArgs(req.ExtraArgs); err != nil {
		return Invocation{}, err
	}
	if req.Session.Kind == SessionMostRecent {
		return Invocation{}, errors.New("most_recent_session_not_routable")
	}
	if req.Session.ID != "" && req.Session.Kind == "" {
		return Invocation{}, errors.New("session_reference_kind_missing")
	}
	if req.Session.Kind != "" && (req.Session.Kind != SessionID && req.Session.Kind != ConversationID || !safeID(req.Session.ID)) {
		return Invocation{}, errors.New("session_reference_invalid")
	}
	if req.Session.ID != "" {
		if req.Provider == ProviderAGY && req.Session.Kind != ConversationID {
			return Invocation{}, errors.New("agy_requires_conversation_id")
		}
		if req.Provider != ProviderAGY && req.Session.Kind != SessionID {
			return Invocation{}, errors.New("provider_requires_session_id")
		}
	}

	args := []string{req.Binary.Path}
	input := []byte(nil)
	switch req.Provider {
	case ProviderClaude:
		args = append(args, "-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--include-partial-messages")
		if req.Session.ID != "" {
			args = append(args, "--resume", req.Session.ID)
		}
		input = userEvent(req.Prompt)
	case ProviderAGY:
		args = append(args, "--output-format", "stream-json", "--input-format", "stream-json")
		if req.Session.ID != "" {
			args = append(args, "--conversation", req.Session.ID)
		}
		input = agyUserEvent(req.Prompt)
	case ProviderGrok:
		args = append(args, "-p", req.Prompt, "--output-format", "streaming-json", "--no-auto-update")
		if req.Session.ID != "" {
			args = append(args, "--resume", req.Session.ID)
		}
	case ProviderCodex:
		args = append(args,
			"exec", "--json", "--color", "never",
			"--model", "gpt-5.6-luna",
			"--sandbox", "read-only",
			"-c", `model_reasoning_effort="medium"`,
			"-c", `approval_policy="never"`,
		)
		if req.Session.ID != "" {
			args = append(args, "resume", req.Session.ID)
		}
		args = append(args, "-")
		input = []byte(req.Prompt)
	}
	appendPermissionArgs(&args, req.Provider, req.Permission)
	env := map[string]string{}
	if req.Provider == ProviderAGY {
		// Prevent a self-update from changing the pinned executable mid-run.
		env["AGY_CLI_DISABLE_AUTO_UPDATE"] = "true"
	}
	return Invocation{provider: req.Provider, pin: req.Binary, argv: args, cwd: req.CWD, input: input, env: env}, nil
}

func validatePermission(provider Provider, p Permission) error {
	if p.Mode == "bypassPermissions" || p.Mode == "dangerously-skip-permissions" || p.Mode == "always-approve" || p.Mode == "yolo" {
		return errors.New("permission_expansion_forbidden")
	}
	if len(p.Allow) > 0 {
		return errors.New("permission_allow_expansion_forbidden")
	}
	if provider == ProviderCodex {
		if p.Mode != "plan" || len(p.Deny) > 0 {
			return errors.New("codex_readonly_plan_required")
		}
		return nil
	}
	allowed := map[Provider]map[string]bool{
		ProviderClaude: {"": true, "default": true, "plan": true, "dontAsk": true},
		ProviderAGY:    {"": true, "default": true, "plan": true},
		ProviderGrok:   {"": true, "default": true, "plan": true, "dontAsk": true},
	}
	if provider == ProviderAGY && len(p.Deny) > 0 {
		return errors.New("agy_permission_rules_unsupported")
	}
	if !allowed[provider][p.Mode] {
		return errors.New("permission_mode_unsupported")
	}
	for _, item := range append(append([]string{}, p.Allow...), p.Deny...) {
		if item == "" || strings.ContainsAny(item, "\r\n\x00") {
			return errors.New("permission_rule_invalid")
		}
	}
	return nil
}

func appendPermissionArgs(args *[]string, provider Provider, p Permission) {
	if provider == ProviderCodex {
		return
	}
	if p.Mode != "" && p.Mode != "default" {
		flag := "--permission-mode"
		if provider == ProviderAGY {
			flag = "--mode"
		}
		*args = append(*args, flag, p.Mode)
	}
	if len(p.Deny) > 0 {
		flag := "--disallowed-tools"
		if provider == ProviderGrok {
			flag = "--deny"
		}
		*args = append(*args, flag, strings.Join(p.Deny, ","))
	}
}

func validateExtraArgs(args []string) error {
	if len(args) > 0 {
		return errors.New("extra_args_forbidden")
	}
	return nil
}

func safeID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e || strings.ContainsRune("/\\", r) {
			return false
		}
	}
	return true
}

func userEvent(prompt string) []byte {
	return jsonLine(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": prompt}}}})
}

func agyUserEvent(prompt string) []byte {
	return jsonLine(map[string]any{"event": "user", "message": map[string]any{"content": prompt}})
}

func jsonLine(value any) []byte {
	data, _ := json.Marshal(value)
	return append(data, '\n')
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
		return Event{Kind: EventResult, SessionID: session, Status: subtype, Text: text}, nil
	case "assistant", "user":
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
		text, err := stringNestedField(value, "text_delta")
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
		text, err := stringNestedField(value, "response")
		kind := EventResult
		if status == "WAITING" {
			kind = EventQuestion
		}
		return Event{Kind: kind, SessionID: session, Status: status, Text: text}, err
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
