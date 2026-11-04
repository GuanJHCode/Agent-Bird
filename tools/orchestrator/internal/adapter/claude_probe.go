package adapter

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Node is an independent executable trust boundary for the legacy JS CLI.
// This is the official v24.13.0 darwin-arm64 archive's bin/node, not a digest
// learned from PATH at launch. Additional runtimes need separate acceptance.
const ClaudeNodeSHA256 = "6f2e835e57feb7a29ffcd53a4d4eb60224ec264a1e002c3ba6d99999ea29807d"

func ClaudeNodePin() (BinaryPin, error) {
	path := os.Getenv("ORCHESTRATOR_CLAUDE_NODE_BINARY")
	if path == "" {
		return BinaryPin{}, errors.New("claude_node_pin_missing")
	}
	pin := BinaryPin{Path: path, Version: "v24.13.0", SHA256: ClaudeNodeSHA256}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || VerifyExecutable(pin, pin.Version) != nil {
		return BinaryPin{}, errors.New("claude_node_runtime_untrusted")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return BinaryPin{}, errors.New("claude_node_runtime_untrusted")
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (identity.Uid != uint32(os.Getuid()) && identity.Uid != 0) {
		return BinaryPin{}, errors.New("claude_node_runtime_untrusted")
	}
	return pin, nil
}

// ProbePinnedOutput keeps non-Claude metadata behavior unchanged. Claude
// discovery must not execute with a caller credential, config or Node loader.
func ProbePinnedOutput(ctx context.Context, provider Provider, pin BinaryPin, args ...string) (string, error) {
	if provider != ProviderClaude {
		return ProbeOutput(ctx, pin.Path, args...)
	}
	window := 2 * time.Second
	if strings.HasSuffix(pin.Path, ".js") {
		// Copying and starting the pinned Node bundle under Seatbelt is slower
		// than native metadata. Keep a separate bounded legacy probe window.
		window = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	if err := VerifyExecutable(pin, pin.Version); err != nil {
		return "", err
	}
	if runtime.GOOS != "darwin" {
		return "", errors.New("claude_probe_sandbox_unsupported")
	}
	home, err := os.MkdirTemp("", "claude-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(home)
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	account, err := user.Current()
	if err != nil {
		return "", errors.New("claude_probe_source_unverified")
	}
	path, argv := pin.Path, append([]string{}, args...)
	rules := []string{"(version 1)", "(allow default)", "(deny network*)", "(deny file-write*)", `(allow file-write* (literal "/dev/null"))`, "(allow file-read* file-write* (subpath " + strconv.Quote(home) + "))"}
	for _, raw := range []string{account.HomeDir, os.Getenv("HOME"), os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("CLAUDE_SECURESTORAGE_CONFIG_DIR")} {
		if raw == "" {
			continue
		}
		root, err := filepath.EvalSymlinks(raw)
		if err != nil || !filepath.IsAbs(raw) || root == "/" {
			return "", errors.New("claude_probe_source_unverified")
		}
		rules = append(rules, "(deny file-read* (subpath "+strconv.Quote(root)+"))")
	}
	rules = append(rules, "(allow file-read* (literal "+strconv.Quote(pin.Path)+"))")
	if strings.HasSuffix(pin.Path, ".js") {
		node, err := ClaudeNodePin()
		if err != nil {
			return "", err
		}
		path, argv = node.Path, append([]string{pin.Path}, args...)
		// A Node executable located under the denied caller home cannot safely
		// initialize there on macOS. Use the same official bytes in private state.
		body, err := os.ReadFile(node.Path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != ClaudeNodeSHA256 {
			return "", errors.New("claude_node_runtime_untrusted")
		}
		path = filepath.Join(home, "node")
		if err := os.WriteFile(path, body, 0700); err != nil {
			return "", err
		}
	}
	for _, service := range []string{"com.apple.SecurityServer", "com.apple.securityd", "com.apple.securityd.system", "com.apple.securityd.systemkeychain"} {
		rules = append(rules, "(deny mach-lookup (global-name "+strconv.Quote(service)+"))")
	}
	argv = append([]string{"-p", strings.Join(rules, "\n"), path}, argv...)
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", argv...)
	cmd.Dir = home
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8", "HOME=" + home, "TMPDIR=" + home, "CLAUDE_CONFIG_DIR=" + home, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + home, "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	output, err := probeCommandOutput(ctx, cmd, args...)
	if err != nil {
		return "", err
	}
	if err := VerifyExecutable(pin, pin.Version); err != nil {
		return "", err
	}
	return output, nil
}
