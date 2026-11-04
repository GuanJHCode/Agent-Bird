package main

import (
	"context"
	"debug/macho"
	"os"
	"path/filepath"
	"regexp"

	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
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
	ancestry, err := process.Ancestry(os.Getpid())
	if err != nil {
		return req, codeError("current_codex_origin_unverified")
	}
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
		if _, err = process.ExecutableDigest(ctx, path); err != nil {
			return req, codeError("current_codex_origin_unverified")
		}
		executable, err := macho.Open(path)
		if err != nil {
			return req, codeError("current_codex_origin_unverified")
		}
		executable.Close()
		daemon, err := process.IsCodexAppServerDaemon(pid, path)
		if err != nil {
			return req, codeError("current_codex_origin_unverified")
		}
		if now, err := process.Birth(pid); err != nil || now != birth {
			return req, codeError("current_codex_origin_unverified")
		}
		if now, err := process.ExecutablePath(pid); err != nil || now != path {
			return req, codeError("current_codex_origin_unverified")
		}
		req, err = observeCodexOwner(req, thread, pid, birth, daemon)
		if err != nil {
			return req, err
		}
	}
	if req.OriginPID == 0 {
		return req, codeError("current_codex_origin_unverified")
	}
	return req, nil
}

// Only a verified exact daemon role is excluded. No ordering/nearest-ancestor
// heuristic can turn two ordinary Codex processes into one owner.
func observeCodexOwner(current coordinator.OwnerBindRequest, thread string, pid int, birth string, daemon bool) (coordinator.OwnerBindRequest, error) {
	if daemon {
		return current, nil
	}
	if current.OriginPID != 0 {
		return current, codeError("current_codex_origin_ambiguous")
	}
	return coordinator.OwnerBindRequest{ControllerThread: thread, OriginPID: pid, OriginBirth: birth}, nil
}
