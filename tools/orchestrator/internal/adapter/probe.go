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
	"time"
)

type probeBuffer struct{ bytes.Buffer }

func (b *probeBuffer) Write(data []byte) (int, error) {
	if len(data) > 64*1024-b.Len() {
		return 0, errors.New("provider_probe_too_large")
	}
	return b.Buffer.Write(data)
}

func ProbeOutput(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
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
		return "", errors.New("provider_probe_failed")
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
	if err != nil || len(version) > 512 || strings.ContainsAny(version, "\r\n\x00") {
		return ProviderLock{}, errors.New("binary_version_probe_failed")
	}
	pin.Version = version
	if err = VerifyExecutable(pin, version); err != nil {
		return ProviderLock{}, err
	}
	return ProviderLock{Version: 1, Provider: provider, Protocol: ProtocolID(provider), Binary: pin}, nil
}
