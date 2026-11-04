package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
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
	versionPolicy := adapter.DescribeVersion(req.Provider, lock.Binary.Version)
	reason := versionPolicy.Reason
	var admissionErr error
	if versionPolicy.Admitted {
		admissionErr = nativeExecutionAdmission(req.Provider, lock.Binary)
		if admissionErr != nil {
			reason = admissionErr.Error()
		}
	}
	if versionPolicy.Admitted {
		help, helpErr := providerHelp(ctx, req.Provider, lock.Binary)
		if helpErr != nil {
			return helpErr
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
	}
	if operation == "provider-lock" {
		if !versionPolicy.Admitted {
			return adapter.AdmitVersion(req.Provider, lock.Binary.Version)
		}
		if err = storeProviderLock(req, lock); err != nil {
			return err
		}
	}
	body := map[string]any{"version": 1, "lock": lock, "provider": req.Provider, "normalized": versionPolicy.Normalized, "baseline": versionPolicy.Baseline, "ceiling": versionPolicy.Ceiling, "profile_supported": reason == "", "profile_resume_supported": false, "profile_resume_reason": "profile_resume_not_verified", "reason": reason, "version_policy": versionPolicy, "requires_confirmation": operation == "provider-probe", "requires_session_write_authorization": req.Provider == adapter.ProviderGrok}
	var trial *adapter.TrialBlockedError
	if errors.As(admissionErr, &trial) {
		body["missing"] = trial.Missing
	}
	return json.NewEncoder(out).Encode(body)
}

func storeProviderLock(req providerRequest, lock adapter.ProviderLock) error {
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
		return replacePrivateJSON(req.LockFile, lock)
	} else if os.IsNotExist(err) {
		if req.PreviousSHA256 != "" {
			return codeError("provider_lock_changed")
		}
		return writeExclusiveJSON(req.LockFile, lock)
	} else {
		return err
	}
}
