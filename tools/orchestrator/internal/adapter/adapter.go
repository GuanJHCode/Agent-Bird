// Package adapter contains the pinned, protocol-only surface for the three
// external coding CLIs. It deliberately does not authenticate, select an
// account, or persist provider output.
package adapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Provider string

const (
	ProviderClaude Provider = "claude-code"
	ProviderAGY    Provider = "antigravity-cli"
	ProviderGrok   Provider = "grok-build"
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
	Provider Provider
	Pin      BinaryPin
	Argv     []string
	CWD      string
	Input    []byte
	Env      map[string]string
}

type EventKind string

const (
	EventInit       EventKind = "init"
	EventProgress   EventKind = "progress"
	EventMessage    EventKind = "message"
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

type RunStatus string

const (
	RunCompleted  RunStatus = "completed"
	RunCancelled  RunStatus = "cancelled"
	RunFailed     RunStatus = "failed"
	RunParseError RunStatus = "parse_error"
)

type RunResult struct {
	Status      RunStatus
	Events      []Event
	ExitCode    int
	StderrBytes int64
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

// Run owns exactly one child process. Context cancellation terminates that
// child; stderr is counted and discarded, and stdout is retained only as the
// bounded Event projection.
func Run(ctx context.Context, invocation Invocation, lineMax, totalMax int) (RunResult, error) {
	if ctx == nil || len(invocation.Argv) == 0 || invocation.Argv[0] == "" {
		return RunResult{Status: RunFailed, ExitCode: -1}, errors.New("invocation_invalid")
	}
	parser := NewStreamParser(invocation.Provider, lineMax, totalMax)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	command := exec.CommandContext(runCtx, invocation.Argv[0], invocation.Argv[1:]...)
	command.Dir = invocation.CWD
	var envErr error
	command.Env, envErr = mergedEnvironment(invocation.Env)
	if envErr != nil {
		return RunResult{Status: RunFailed, ExitCode: -1}, envErr
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return RunResult{Status: RunFailed, ExitCode: -1}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return RunResult{Status: RunFailed, ExitCode: -1}, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return RunResult{Status: RunFailed, ExitCode: -1}, err
	}
	if err := command.Start(); err != nil {
		if ctx.Err() != nil {
			return RunResult{Status: RunCancelled, ExitCode: -1}, nil
		}
		return RunResult{Status: RunFailed, ExitCode: -1}, err
	}
	stderrCount := make(chan int64, 1)
	go func() {
		count, _ := io.Copy(&countWriter{}, stderr)
		stderrCount <- count
	}()
	if len(invocation.Input) > 0 {
		if _, err := stdin.Write(invocation.Input); err != nil && ctx.Err() == nil {
			stop()
		}
	}
	_ = stdin.Close()
	result := RunResult{Status: RunCompleted, ExitCode: -1}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), lineMax)
	for scanner.Scan() {
		event, parseErr := parser.Parse(scanner.Text())
		if parseErr != nil {
			result.Status = RunParseError
			stop()
			break
		}
		result.Events = append(result.Events, event)
	}
	if scanner.Err() != nil && result.Status == RunCompleted {
		result.Status = RunParseError
		stop()
	}
	waitErr := command.Wait()
	result.StderrBytes = <-stderrCount
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		result.Status = RunCancelled
		return result, nil
	}
	if result.Status == RunParseError {
		return result, errors.New("event_stream_invalid")
	}
	if waitErr != nil {
		result.Status = RunFailed
	}
	return result, nil
}

type countWriter struct{ count int64 }

func (w *countWriter) Write(data []byte) (int, error) {
	w.count += int64(len(data))
	return len(data), nil
}

func mergedEnvironment(overrides map[string]string) ([]string, error) {
	env := os.Environ()
	for key, value := range overrides {
		if key != "AGY_CLI_DISABLE_AUTO_UPDATE" && key != "GROK_DISABLE_AUTOUPDATER" {
			return nil, errors.New("environment_override_forbidden")
		}
		for i, item := range env {
			if strings.HasPrefix(item, key+"=") {
				env[i] = key + "=" + value
				goto next
			}
		}
		env = append(env, key+"="+value)
	next:
	}
	return env, nil
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
	if req.Provider != ProviderClaude && req.Provider != ProviderAGY && req.Provider != ProviderGrok {
		return Invocation{}, errors.New("provider_unknown")
	}
	if req.Binary.Path == "" || !filepath.IsAbs(req.Binary.Path) || filepath.Clean(req.Binary.Path) != req.Binary.Path {
		return Invocation{}, errors.New("binary_path_invalid")
	}
	if req.Binary.Version == "" || !hex64.MatchString(strings.ToLower(req.Binary.SHA256)) {
		return Invocation{}, errors.New("binary_pin_invalid")
	}
	if req.CWD == "" || !filepath.IsAbs(req.CWD) || filepath.Clean(req.CWD) != req.CWD {
		return Invocation{}, errors.New("cwd_invalid")
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
		args = append(args, "-p", req.Prompt, "--output-format", "streaming-json")
		if req.Session.ID != "" {
			args = append(args, "--resume", req.Session.ID)
		}
	}
	appendPermissionArgs(&args, req.Provider, req.Permission)
	args = append(args, req.ExtraArgs...)
	env := map[string]string{}
	if req.Provider == ProviderAGY {
		// Prevent a self-update from changing the pinned executable mid-run.
		env["AGY_CLI_DISABLE_AUTO_UPDATE"] = "true"
	}
	if req.Provider == ProviderGrok {
		env["GROK_DISABLE_AUTOUPDATER"] = "1"
	}
	return Invocation{Provider: req.Provider, Pin: req.Binary, Argv: args, CWD: req.CWD, Input: input, Env: env}, nil
}

func validatePermission(provider Provider, p Permission) error {
	if p.Mode == "bypassPermissions" || p.Mode == "dangerously-skip-permissions" || p.Mode == "always-approve" || p.Mode == "yolo" {
		return errors.New("permission_expansion_forbidden")
	}
	allowed := map[Provider]map[string]bool{
		ProviderClaude: {"": true, "default": true, "plan": true, "dontAsk": true},
		ProviderAGY:    {"": true, "default": true, "plan": true},
		ProviderGrok:   {"": true, "default": true, "plan": true, "dontAsk": true},
	}
	if provider == ProviderAGY && (len(p.Allow) > 0 || len(p.Deny) > 0) {
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
	if p.Mode != "" {
		flag := "--permission-mode"
		if provider == ProviderAGY {
			flag = "--mode"
		}
		*args = append(*args, flag, p.Mode)
	}
	if len(p.Allow) > 0 {
		flag := "--allowed-tools"
		if provider == ProviderAGY {
			flag = "--allow"
		}
		*args = append(*args, flag, strings.Join(p.Allow, ","))
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
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if strings.ContainsAny(arg, "\r\n\x00") || lower == "login" || lower == "logout" || lower == "auth" || lower == "authenticate" || strings.HasPrefix(lower, "--oauth") || strings.HasPrefix(lower, "--api-key") || strings.HasPrefix(lower, "--account") || strings.HasPrefix(lower, "--provider") || strings.HasPrefix(lower, "--project") || strings.Contains(lower, "dangerously-skip-permissions") || lower == "--always-approve" || lower == "--yolo" {
			return errors.New("auth_or_permission_argument_forbidden")
		}
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
	default:
		return Event{}, errors.New("provider_unknown")
	}
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
		text, err := stringNestedField(value, "response")
		return Event{Kind: EventResult, SessionID: session, Status: status, Text: text}, err
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
