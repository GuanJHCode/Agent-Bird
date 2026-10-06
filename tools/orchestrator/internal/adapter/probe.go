package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type probeBuffer struct{ bytes.Buffer }

var probeCommand = exec.CommandContext

func ProbeFailureFacts(err error) *contract.FailureFacts {
	var failure *probeFailure
	if !errors.As(err, &failure) {
		return nil
	}
	if failure.facts == nil {
		return ProbeFailureFacts(failure.cause)
	}
	copy := *failure.facts
	return &copy
}
func VersionProbeFailure(err error) error {
	return &probeFailure{code: "binary_version_probe_failed", cause: err}
}

type probeFailure struct {
	code  string
	cause error
	facts *contract.FailureFacts
}

func (failure *probeFailure) Error() string {
	return failure.code
}

func (failure *probeFailure) Unwrap() error {
	return failure.cause
}

func (b *probeBuffer) Write(data []byte) (int, error) {
	if len(data) > 64*1024-b.Len() {
		return 0, errors.New("provider_probe_too_large")
	}
	return b.Buffer.Write(data)
}

func ProbeOutput(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := probeCommand(ctx, path, args...)
	// Discovery must not trigger provider auto-updaters. These switches affect
	// only the probe child; other providers ignore the provider-specific keys.
	cmd.Env = append(os.Environ(), "AGY_CLI_DISABLE_AUTO_UPDATE=true", "GROK_DISABLE_AUTOUPDATER=1")
	var output probeBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	var helpOutput probeBuffer
	isHelp := len(args) == 1 && args[0] == "--help"
	if isHelp {
		// Go flag-based CLIs commonly print successful help to stderr.
		cmd.Stderr = &helpOutput
	}
	if err := cmd.Run(); err != nil {
		var facts *contract.FailureFacts
		var exited *exec.ExitError
		if cmd.Process == nil && cmd.ProcessState == nil && ctx.Err() == nil && !errors.As(err, &exited) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && len(args) == 1 && (args[0] == "--version" || args[0] == "--help") {
			code := ""
			if errors.Is(err, syscall.EAGAIN) {
				code = "probe_start_eagain"
			} else if errors.Is(err, syscall.EINTR) {
				code = "probe_start_eintr"
			}
			if code != "" {
				facts = &contract.FailureFacts{Version: 1, Stage: "prelaunch_probe", Code: code, WorkerState: "not_started", ProbeState: "not_started"}
			}
		}
		return "", &probeFailure{code: "provider_probe_failed", cause: errors.Join(err, ctx.Err()), facts: facts}
	}
	if isHelp {
		if output.Len()+helpOutput.Len() > 64*1024 {
			return "", errors.New("provider_probe_too_large")
		}
		return strings.TrimSpace(output.String() + "\n" + helpOutput.String()), nil
	}
	return strings.TrimSpace(output.String()), nil
}

func Probe(ctx context.Context, provider Provider, path string) (ProviderLock, error) {
	if ProtocolID(provider) == "" || !filepath.IsAbs(path) {
		return ProviderLock{}, errors.New("provider_probe_invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return ProviderLock{}, errors.New("binary_path_changed")
	}
	file, err := os.Open(path)
	if err != nil {
		return ProviderLock{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return ProviderLock{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	// Check mode and digest before executing even the bounded --version probe.
	pin := BinaryPin{Path: path, Version: "probe", SHA256: digest}
	if err = VerifyExecutable(pin, "probe"); err != nil {
		return ProviderLock{}, err
	}
	version, err := ProbeOutput(ctx, path, "--version")
	if err != nil {
		return ProviderLock{}, &probeFailure{code: "binary_version_probe_failed", cause: err}
	}
	if len(version) > 512 || strings.ContainsAny(version, "\r\n\x00") {
		return ProviderLock{}, errors.New("binary_version_probe_failed")
	}
	pin.Version = version
	if err = VerifyExecutable(pin, version); err != nil {
		return ProviderLock{}, err
	}
	return ProviderLock{Version: 1, Provider: provider, Protocol: ProtocolID(provider), Binary: pin}, nil
}
