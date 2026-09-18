// Package adapter contains the pinned, protocol-only surface for the
// external coding CLIs. It deliberately does not authenticate, select an
// account, or persist provider output.
package adapter

import (
	"encoding/json"
	"errors"
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
	Path    string `json:"path"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
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
	Action     *CandidateAction
	Provider   Provider
	Binary     BinaryPin
	CWD        string
	Prompt     string
	Session    SessionRef
	Permission Permission
	ExtraArgs  []string
	Profile    *ExecutionProfile
	Lock       *ProviderLock
	Workspace  *CandidateWorkspace
}

type Invocation struct {
	action    *CandidateAction
	provider  Provider
	pin       BinaryPin
	argv      []string
	cwd       string
	input     []byte
	env       map[string]string
	profile   *ExecutionProfile
	workspace *CandidateWorkspace
}

func (i Invocation) ExecutablePin() (string, string) { return i.pin.Path, i.pin.SHA256 }

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

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func BuildInvocation(req Request) (Invocation, error) {
	if err := validateWorkspace(req); err != nil {
		return Invocation{}, err
	}
	if err := validateProfile(req); err != nil {
		return Invocation{}, err
	}
	if req.Profile != nil {
		copy := *req.Profile
		req.Profile = &copy
		if req.Profile.Role == Reviewer {
			req.Permission = Permission{Mode: "plan"}
		} else {
			req.Permission = Permission{Mode: "default"}
		}
		if req.Provider == ProviderClaude {
			req.Permission.Deny = []string{"Agent", "Task"}
		}
	}
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
		args = append(args, "-p", req.Prompt, "--output-format", "streaming-json")
		if req.Profile != nil {
			args = append(args, "--no-subagents", "--disable-web-search", "--tools", "read_file,list_dir,grep", "--deny", "MCPTool(*)")
		}
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
	if usesCandidateReviewSchema(req) {
		args = append(args, "--json-schema", candidateReviewSchema)
	}
	if req.Profile != nil && (req.Provider == ProviderClaude || req.Provider == ProviderAGY || req.Provider == ProviderGrok) {
		if req.Profile.Model != "" {
			args = append(args, "--model", string(req.Profile.Model))
		}
		if req.Profile.Reasoning != "" {
			args = append(args, "--effort", string(req.Profile.Reasoning))
		}
	}
	env := map[string]string{}
	if req.Provider == ProviderGrok {
		env["GROK_DISABLE_AUTOUPDATER"] = "1"
	}
	if req.Provider == ProviderAGY {
		// Prevent a self-update from changing the pinned executable mid-run.
		env["AGY_CLI_DISABLE_AUTO_UPDATE"] = "true"
	}
	return Invocation{provider: req.Provider, pin: req.Binary, argv: args, cwd: req.CWD, input: input, env: env, profile: req.Profile, workspace: cloneWorkspace(req.Workspace), action: req.Action}, nil
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
