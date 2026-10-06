package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type providerRequest struct {
	Provider       adapter.Provider `json:"provider"`
	BinaryPath     string           `json:"binary_path"`
	LockFile       string           `json:"lock_file,omitempty"`
	ConfirmSHA256  string           `json:"confirm_sha256,omitempty"`
	PreviousSHA256 string           `json:"previous_sha256,omitempty"`
}

func providerControl(ctx context.Context, operation string, args []string, out io.Writer) error {
	path, err := requestFile(args)
	if err != nil {
		return err
	}
	var req providerRequest
	if err = readPrivateJSON(path, &req); err != nil {
		return err
	}
	lock, err := adapter.Probe(ctx, req.Provider, req.BinaryPath)
	if err != nil {
		return err
	}
	reason := ""
	if req.Provider == adapter.ProviderCodex {
		reason = "codex_trial_guard_not_ready"
	} else if req.Provider != adapter.ProviderClaude && req.Provider != adapter.ProviderAGY && req.Provider != adapter.ProviderGrok {
		reason = "execution_profile_unsupported"
	}
	help, err := adapter.ProbeOutput(ctx, req.BinaryPath, "--help")
	if err != nil {
		return err
	}
	if reason == "" {
		check := adapter.Request{Provider: req.Provider, Binary: lock.Binary, Lock: &lock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}}
		// Capability discovery is not task write authorization. Actual requests
		// must explicitly opt in and Host derives their fresh session directory.
		check.Profile.GrokSessionWrite = req.Provider == adapter.ProviderGrok
		if err = adapter.CheckCapabilities(check, help); err != nil {
			reason = err.Error()
		}
	}
	if operation == "provider-lock" {
		if req.LockFile == "" || req.ConfirmSHA256 != lock.Binary.SHA256 {
			return codeError("provider_lock_confirmation_required")
		}
		var old adapter.ProviderLock
		if _, err := os.Lstat(req.LockFile); err == nil {
			if err = readPrivateJSON(req.LockFile, &old); err != nil {
				return err
			}
			if req.PreviousSHA256 == "" || old.Binary.SHA256 != req.PreviousSHA256 {
				return codeError("provider_lock_changed")
			}
			if err = replacePrivateJSON(req.LockFile, lock); err != nil {
				return err
			}
		} else if os.IsNotExist(err) {
			if req.PreviousSHA256 != "" {
				return codeError("provider_lock_changed")
			}
			if err = writeExclusiveJSON(req.LockFile, lock); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return json.NewEncoder(out).Encode(providerProjection(lock, reason, operation, help))
}

func providerProjection(lock adapter.ProviderLock, reason, operation, help string) map[string]any {
	return map[string]any{"version": 1, "lock": lock, "profile_supported": reason == "", "profile_resume_supported": false, "profile_resume_scope": "legacy_or_caller_supplied_session", "profile_resume_reason": "profile_resume_not_verified", "continuation_capabilities": continuationCapabilities(lock, reason, help), "reason": reason, "requires_confirmation": operation == "provider-probe", "requires_session_write_authorization": lock.Provider == adapter.ProviderGrok}
}

type continuationCapability struct {
	Role                   adapter.Role `json:"role"`
	Path                   string       `json:"path"`
	ProtocolSupported      bool         `json:"protocol_supported"`
	BlockedReason          string       `json:"blocked_reason,omitempty"`
	RequiresTrustedBinding bool         `json:"requires_trusted_binding"`
	NativeAcceptance       string       `json:"native_acceptance"`
}

// Discovery describes protocol support, not authority to resume any session.
// The Store/Host still require the exact private binding, native history,
// exited source tree, original workspace and cumulative remaining budget.
func continuationCapabilities(lock adapter.ProviderLock, profileReason, help string) []continuationCapability {
	items := make([]continuationCapability, 0, 4)
	for _, role := range []adapter.Role{adapter.Reviewer, adapter.Implementer} {
		for _, path := range []string{contract.ContinuationQuestion, contract.ContinuationInterrupted} {
			item := continuationCapability{Role: role, Path: path, RequiresTrustedBinding: true, NativeAcceptance: "pending"}
			item.BlockedReason = profileReason
			if lock.Provider != adapter.ProviderAGY && lock.Provider != adapter.ProviderClaude {
				item.BlockedReason = "profile_resume_not_verified"
			} else if item.BlockedReason == "" && lock.Provider == adapter.ProviderClaude && !adapter.ClaudeHistorySupported(lock.Binary) {
				item.BlockedReason = "claude_history_version_unverified"
			}
			if item.BlockedReason == "" {
				permission := adapter.ReadOnly
				kind := adapter.ConversationID
				if lock.Provider == adapter.ProviderClaude {
					kind = adapter.SessionID
				}
				if role == adapter.Implementer {
					permission = adapter.WorkspaceWrite
				}
				// Only a shape for checking exact help flags; never a launch grant.
				ref := &contract.ContinuationRef{Version: 1, Mode: path, SourceKind: "host", SourceSegmentID: "probe", SourceEventID: "probe", SourceEventRevision: 1, SourceEventSHA256: strings.Repeat("0", 64), SessionEventID: "probe", AdapterPayloadSHA256: strings.Repeat("0", 64), OwnerContextID: "probe"}
				req := adapter.Request{Provider: lock.Provider, Binary: lock.Binary, Lock: &lock, Profile: &adapter.ExecutionProfile{Version: 1, Role: role, Permission: permission, TimeoutMS: 1000, CLIModelDefault: true}, Session: adapter.SessionRef{Kind: kind, ID: "probe"}, Continuation: ref}
				if role == adapter.Implementer {
					ref.WorkspaceSourceSegmentID = "probe"
					req.Workspace = &adapter.CandidateWorkspace{Version: 1}
				}
				if err := adapter.CheckCapabilities(req, help); err != nil {
					item.BlockedReason = err.Error()
				}
			}
			item.ProtocolSupported = item.BlockedReason == ""
			items = append(items, item)
		}
	}
	return items
}
