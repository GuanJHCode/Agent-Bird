package main

import (
	"context"
	"debug/macho"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var codexThreadPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// currentCodexOwner observes only the calling process ancestry and one non-secret
// thread identifier. The coordinator independently enforces ancestry and worker
// exclusion. The inherited thread ID is a label, not a signed identity assertion.
func currentCodexOwner(ctx context.Context) (coordinator.OwnerBindRequest, error) {
	var req coordinator.OwnerBindRequest
	thread := os.Getenv("CODEX_THREAD_ID")
	if !codexThreadPattern.MatchString(thread) {
		return req, codeError("current_codex_thread_unavailable")
	}
	ancestry, err := process.AncestryContext(ctx, os.Getpid())
	if err != nil {
		return req, codeError("current_codex_origin_unverified")
	}
	var origins []codexOrigin
	for pid, birth := range ancestry {
		if pid == os.Getpid() {
			continue
		}
		path, err := process.ExecutablePath(pid)
		if err != nil {
			return req, codeError("current_codex_origin_unverified")
		}
		if filepath.Base(path) != "codex" {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
			return req, codeError("current_codex_origin_unverified")
		}
		digest, err := process.ExecutableDigest(ctx, path)
		if err != nil {
			return req, codeError("current_codex_origin_unverified")
		}
		executable, err := macho.Open(path)
		if err != nil {
			return req, codeError("current_codex_origin_unverified")
		}
		executable.Close()
		if now, err := process.Birth(pid); err != nil || now != birth {
			return req, codeError("current_codex_origin_unverified")
		}
		if now, err := process.ExecutablePath(pid); err != nil || now != path {
			return req, codeError("current_codex_origin_unverified")
		}
		origins = append(origins, codexOrigin{coordinator.OwnerBindRequest{ControllerThread: thread, OriginPID: pid, OriginBirth: birth}, path, digest})
	}
	return selectCodexOrigin(ctx, origins, process.ObserveArguments)
}

type codexOrigin struct {
	request      coordinator.OwnerBindRequest
	path, digest string
}

// Native Codex App 0.162.1 runs a remote-control server directly beneath its
// launchd-owned pid-update daemon. Accept only that observed pair; arbitrary
// multiple Codex ancestors remain ambiguous. Worker exclusion remains in the
// coordinator and covers the full caller ancestry, including both processes.
func selectCodexOrigin(ctx context.Context, origins []codexOrigin, observe func(context.Context, int) (process.ArgumentSnapshot, error)) (coordinator.OwnerBindRequest, error) {
	var empty coordinator.OwnerBindRequest
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if len(origins) == 0 {
		return empty, codeError("current_codex_origin_unverified")
	}
	if len(origins) == 1 {
		return origins[0].request, nil
	}
	ambiguous := codeError("current_codex_origin_ambiguous")
	if len(origins) != 2 || origins[0].path != origins[1].path || origins[0].digest != origins[1].digest {
		return empty, ambiguous
	}
	snapshots := make([]process.ArgumentSnapshot, 2)
	for i, origin := range origins {
		snapshot, err := observe(ctx, origin.request.OriginPID)
		if err != nil || snapshot.PID != origin.request.OriginPID || snapshot.Birth != origin.request.OriginBirth || snapshot.UID != os.Geteuid() || snapshot.Executable != origin.path {
			return empty, ambiguous
		}
		snapshots[i] = snapshot
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	for i, child := range snapshots {
		daemon := snapshots[1-i]
		path := origins[i].path
		if child.ParentPID == daemon.PID && daemon.ParentPID == 1 &&
			slices.Equal(child.Args, []string{path, "app-server", "--remote-control", "--listen", "unix://", "--analytics-default-enabled", "--managed-daemon"}) &&
			slices.Equal(daemon.Args, []string{path, "app-server", "daemon", "pid-update-loop"}) {
			return origins[i].request, nil
		}
	}
	return empty, ambiguous
}
