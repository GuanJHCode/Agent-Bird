package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const managedOwnerRequestEnv = "AGENT_BIRD_OWNER_REQUEST"

// managedOwnerRequest describes one controller-launched CLI process scope. It
// deliberately contains no provider-native conversation or chat identifier.
type managedOwnerRequest struct {
	Version          int    `json:"version"`
	Provider         string `json:"provider"`
	Scope            string `json:"scope"`
	ControllerThread string `json:"controller_thread"`
	LauncherPID      int    `json:"launcher_pid"`
	LauncherBirth    string `json:"launcher_birth"`
	LauncherPath     string `json:"launcher_path"`
	LauncherDigest   string `json:"launcher_digest"`
}

// currentOwner resolves a native Codex session when present, or the explicitly
// managed process scope created by controller start. A supplied managed context
// must validate; it never falls through to an unrelated Codex environment.
func currentOwner(ctx context.Context) (coordinator.OwnerBindRequest, error) {
	if os.Getenv(managedOwnerRequestEnv) != "" {
		return currentLocalOwner(ctx)
	}
	return currentCodexOwner(ctx)
}

func currentLocalOwner(ctx context.Context) (coordinator.OwnerBindRequest, error) {
	var req coordinator.OwnerBindRequest
	path := os.Getenv(managedOwnerRequestEnv)
	var managed managedOwnerRequest
	if err := readPrivateJSON(path, &managed); err != nil {
		return req, codeError("current_owner_unavailable")
	}
	if managed.Version != 1 || managed.Scope != "managed_process" || !strings.HasPrefix(managed.ControllerThread, "managed-"+managed.Provider+"-") || managed.LauncherPID <= 0 || managed.LauncherBirth == "" || !filepath.IsAbs(managed.LauncherPath) || len(managed.LauncherDigest) != 64 {
		return req, codeError("current_owner_unavailable")
	}
	if _, _, err := taskProvider(managed.Provider); err != nil {
		return req, codeError("current_owner_unavailable")
	}
	ancestry, err := process.Ancestry(os.Getpid())
	if err != nil || managed.LauncherPID == os.Getpid() || ancestry[managed.LauncherPID] != managed.LauncherBirth {
		return req, codeError("current_owner_unavailable")
	}
	pathNow, err := process.ExecutablePath(managed.LauncherPID)
	if err != nil || pathNow != managed.LauncherPath {
		return req, codeError("current_owner_unavailable")
	}
	self, err := resolvedExecutable()
	if err != nil || self != managed.LauncherPath {
		return req, codeError("current_owner_unavailable")
	}
	birth, err := process.Birth(managed.LauncherPID)
	if err != nil || birth != managed.LauncherBirth {
		return req, codeError("current_owner_unavailable")
	}
	digest, err := process.ExecutableDigest(ctx, managed.LauncherPath)
	if err != nil || !strings.EqualFold(digest, managed.LauncherDigest) {
		return req, codeError("current_owner_unavailable")
	}
	return coordinator.OwnerBindRequest{ControllerThread: managed.ControllerThread, OriginPID: managed.LauncherPID, OriginBirth: managed.LauncherBirth}, nil
}
