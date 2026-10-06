package hookcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Capabilities are observations, never a binding, send permit, or Ready signal.
// HookRegistered reads exactly the same registration consumed by Run.
type CapabilityReport struct {
	SchemaVersion       int                `json:"schema_version"`
	Provider            string             `json:"provider"`
	AdapterFamily       string             `json:"adapter_family"`
	ManagedWorkerStatus string             `json:"managed_worker_status"`
	HookRegistered      bool               `json:"hook_registered"`
	Mode                string             `json:"mode"`
	AutomaticStatus     string             `json:"automatic_status"`
	NativeAcceptance    string             `json:"native_acceptance"`
	SourceEvidence      []string           `json:"source_evidence"`
	OfficialEvidence    []string           `json:"official_evidence"`
	Limitations         []string           `json:"limitations"`
	Commands            []string           `json:"commands"`
	Runtime             RuntimeObservation `json:"runtime"`
}

type RuntimeObservation struct {
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Executable string `json:"executable,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Version    string `json:"version,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
}

type CapabilityOptions struct {
	Binary       string
	ProbeVersion bool
}

// No caller-controlled diagnostic or arbitrary provider stdout enters this DTO.
var capabilitySecret = regexp.MustCompile(`(?i)(owner-token-|control-token-|report-token-|hook-|bridge-claim-)[0-9a-f]{24,64}`)
var codexVersion = regexp.MustCompile(`^(?:codex-cli )?([0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.]+)?)$`)
var claudeVersion = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.]+)?)(?: \(Claude Code\))?$`)

func DiscoverCapabilities(ctx context.Context, id string, options CapabilityOptions) (CapabilityReport, error) {
	c := CapabilityReport{SchemaVersion: 1, Provider: id, NativeAcceptance: "WAIT_AUTH", Runtime: RuntimeObservation{Status: "NOT_RUN", Reason: "version_probe_not_requested"}}
	collect := []string{"codex-orchestrator task submit --request <private-collect-plan> --handle <new-private-handle> --state-dir <private-state>", "codex-orchestrator task collect --handle <private-handle> --task-id <task-id>", "codex-orchestrator task ack --handle <private-handle> --request <private-ack-decision>"}
	c.Limitations = []string{"capability_read_does_not_authorize_or_activate", "version_does_not_verify_native_consumption", "ack_is_distinct_from_business_acceptance"}
	switch id {
	case "claude":
		c.AdapterFamily = "claude-code"
		c.ManagedWorkerStatus = "source_supported"
		c.Mode = "hook_pull"
		c.AutomaticStatus = "ADAPTER_NOT_REGISTERED"
		c.SourceEvidence = []string{"internal/hookcli:RegisterProvider/Run", "internal/store:BindBridge collect-only/ClaimBridge"}
		c.OfficialEvidence = []string{"https://code.claude.com/docs/en/hooks", "https://code.claude.com/docs/en/cli-reference"}
		c.Commands = collect
	case "codex":
		c.AdapterFamily = "codex-cli"
		c.ManagedWorkerStatus = "codex_trial_guard_not_ready"
		c.Mode = "attached_push"
		c.AutomaticStatus = "SOURCE_ONLY"
		c.SourceEvidence = []string{"plugins/codex-orchestrator/lib/native_bridge/bridge.py:DeliveryLedger", "runtime/native/owner_helper.py:fixed turn/start/toolOutput and thread/read", "internal/adapter/profile.go:codex_trial_guard_not_ready"}
		c.OfficialEvidence = []string{"https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md"}
		c.Commands = []string{"codex-orchestrator native-bridge start --request <private-native-driver-request>", "codex-orchestrator native-bridge status --request <private-driver-status-request>", "codex-orchestrator native-bridge decide --request <private-history-decision-request>"}
		c.Limitations = append(c.Limitations, "sole_original_ledger_sender_no_bridge_begin_emit", "sending_or_uncertain_audit_only_no_automatic_resend", "source_pin_is_not_installed_version")
	case "grok":
		c.AdapterFamily = "grok-build"
		c.ManagedWorkerStatus = "source_supported"
		c.Mode = "explicit_collect"
		c.AutomaticStatus = "UNIMPLEMENTED"
		c.SourceEvidence = []string{"internal/adapter:grok", "cmd/orchestrator/task_entry.go:task collect"}
		c.OfficialEvidence = []string{"https://docs.x.ai/build/features/hooks", "https://docs.x.ai/build/cli/reference"}
		c.Commands = collect
		c.Limitations = append(c.Limitations, "official_passive_hook_stdout_ignored", "automatic_reinjection_unimplemented", "explicit_collect_does_not_complete_automatic_requirement")
	case "agy":
		c.AdapterFamily = "antigravity-cli"
		c.ManagedWorkerStatus = "source_supported"
		c.Mode = "hook_pull"
		c.AutomaticStatus = "ADAPTER_NOT_REGISTERED"
		c.SourceEvidence = []string{"internal/hookcli:RegisterProvider/NormalizeAGY/Run", "internal/store:BindBridge collect-only/ClaimBridge"}
		c.OfficialEvidence = []string{"https://antigravity.google/docs/hooks/", "https://antigravity.google/docs/cli/reference/"}
		c.Commands = collect
		c.Limitations = append(c.Limitations, "preinvocation_only", "native_failure_semantics_unverified")
	default:
		return CapabilityReport{}, errors.New("bridge_provider_unknown")
	}
	providers.RLock()
	c.HookRegistered = providers.byID[id] != nil
	providers.RUnlock()
	if (id == "claude" || id == "agy") && c.HookRegistered {
		c.AutomaticStatus = "REGISTERED_UNVERIFIED"
	}
	c.Runtime = observeVersion(ctx, id, options)
	return c, nil
}

func observeVersion(ctx context.Context, id string, options CapabilityOptions) RuntimeObservation {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result := RuntimeObservation{Status: "NOT_RUN", Reason: "version_probe_not_requested"}
	// Grok documents `version`, not a verified --version option. AGY has no
	// verified read-only --version contract here. Never substitute a session command.
	if options.ProbeVersion && id != "claude" && id != "codex" {
		result.Reason = "version_probe_read_only_unverified"
		return result
	}
	path := options.Binary
	if path == "" {
		var err error
		path, err = exec.LookPath(id)
		if err != nil {
			if options.ProbeVersion {
				result.Status = "UNKNOWN"
				result.Reason = "executable_unavailable"
			}
			return result
		}
	}
	if !filepath.IsAbs(path) {
		if options.ProbeVersion {
			result.Status = "UNKNOWN"
		}
		result.Reason = "executable_invalid"
		return result
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || len(resolved) > 4096 || strings.ContainsAny(resolved, "\r\n\x00") || capabilitySecret.MatchString(resolved) {
		if options.ProbeVersion {
			result.Status = "UNKNOWN"
		}
		result.Reason = "executable_invalid"
		return result
	}
	digest, err := capabilityDigest(ctx, resolved)
	if err != nil {
		if options.ProbeVersion {
			result.Status = "UNKNOWN"
		}
		result.Reason = "executable_invalid"
		return result
	}
	result.Executable = resolved
	result.SHA256 = digest
	if !options.ProbeVersion {
		return result
	}
	command := exec.CommandContext(ctx, resolved, "--version")
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C", "DISABLE_AUTOUPDATER=1"}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 100 * time.Millisecond
	var out capabilityBuffer
	command.Stdout = &out
	command.Stderr = io.Discard
	runErr := command.Run()
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if runErr != nil || ctx.Err() != nil {
		result.Status = "UNKNOWN"
		result.Reason = "version_probe_failed"
		return result
	}
	// Digest again: do not pair a version with a changed executable observation.
	after, err := capabilityDigest(ctx, resolved)
	if err != nil || after != digest {
		result.Status = "UNKNOWN"
		result.Reason = "executable_changed"
		return result
	}
	text := strings.TrimSuffix(out.String(), "\n")
	text = strings.TrimSuffix(text, "\r")
	pattern := codexVersion
	if id == "claude" {
		pattern = claudeVersion
	}
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 || capabilitySecret.MatchString(text) {
		result.Status = "UNKNOWN"
		result.Reason = "version_output_invalid"
		return result
	}
	result.Status = "OBSERVED"
	result.Reason = "read_only_version_only"
	result.Version = match[1]
	result.VerifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.Evidence = "resolved_executable_sha256_and_version_argv_only"
	return result
}

type capabilityBuffer struct{ value []byte }

func (b *capabilityBuffer) Write(p []byte) (int, error) {
	if len(p) > 512-len(b.value) {
		return 0, errors.New("version_output_invalid")
	}
	b.value = append(b.value, p...)
	return len(p), nil
}
func (b *capabilityBuffer) String() string { return string(b.value) }

func capabilityDigest(ctx context.Context, path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512<<20 || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 || !ok || s.Nlink != 1 || (s.Uid != 0 && s.Uid != uint32(os.Geteuid())) {
		return "", errors.New("executable_invalid")
	}
	hash := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			_, _ = hash.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return "", errors.New("executable_changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
