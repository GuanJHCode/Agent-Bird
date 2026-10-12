package hookcli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func capabilityFixture(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCapabilitiesSourceIsNotRuntimeOrAuthority(t *testing.T) {
	for _, id := range []string{"claude", "codex", "grok", "agy"} {
		c, err := DiscoverCapabilities(context.Background(), id, CapabilityOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if c.Runtime.Status != "NOT_RUN" || c.NativeAcceptance != "WAIT_AUTH" || c.AutomaticStatus == "READY" {
			t.Fatalf("source promoted to runtime: %+v", c)
		}
		if id == "codex" && c.ManagedWorkerStatus != "codex_trial_guard_not_ready" {
			t.Fatalf("Codex worker guard lost: %+v", c)
		}
		if id == "grok" && (c.AutomaticStatus != "UNIMPLEMENTED" || len(c.Commands) < 3) {
			t.Fatalf("explicit collect missing or substitutes automatic: %+v", c)
		}
	}
	if _, err := DiscoverCapabilities(context.Background(), "gemini", CapabilityOptions{}); err == nil {
		t.Fatal("AGY aliased to another provider")
	}
}

func TestCapabilitiesReadsActualRegistration(t *testing.T) {
	providers.Lock()
	prior := providers.byID["claude"]
	delete(providers.byID, "claude")
	providers.Unlock()
	defer func() {
		providers.Lock()
		if prior != nil {
			providers.byID["claude"] = prior
		} else {
			delete(providers.byID, "claude")
		}
		providers.Unlock()
	}()
	c, _ := DiscoverCapabilities(context.Background(), "claude", CapabilityOptions{})
	if c.HookRegistered {
		t.Fatal("unregistered adapter advertised")
	}
	providers.Lock()
	providers.byID["claude"] = func(context.Context, []byte, store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		panic("capability read invoked normalizer")
	}
	providers.Unlock()
	c, _ = DiscoverCapabilities(context.Background(), "claude", CapabilityOptions{})
	if !c.HookRegistered || c.Runtime.Status != "NOT_RUN" {
		t.Fatalf("registration not separated: %+v", c)
	}
}

func TestCapabilitiesVersionOnlyAndResolvedDigest(t *testing.T) {
	p := capabilityFixture(t, "[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 9\nprintf 'codex-cli 0.154.0\\n'\n")
	link := filepath.Join(t.TempDir(), "codex")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	canonical, canonicalErr := filepath.EvalSymlinks(p)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}

	prepareCapabilityFixtureFirstExec(t, p)
	alias := canonical
	if strings.HasPrefix(alias, "/private/tmp/") {
		alias = strings.TrimPrefix(alias, "/private")
	}
	for _, tc := range []struct{ name, path string }{{"symlink", link}, {"canonical", canonical}, {"tmp_alias", alias}} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := DiscoverCapabilities(context.Background(), "codex", CapabilityOptions{Binary: tc.path, ProbeVersion: true})
			if err != nil || c.Runtime.Status != "OBSERVED" || c.Runtime.Version != "0.154.0" || c.Runtime.Executable != canonical || len(c.Runtime.SHA256) != 64 || c.Runtime.VerifiedAt == "" {
				t.Fatalf("observation=%+v err=%v", c.Runtime, err)
			}
		})
	}
}

// Only this resolved-path contract fixture pays a separate first-exec cost.
// Discover retains its original 2s deadline; slow/prohibited probes stay cold.
func prepareCapabilityFixtureFirstExec(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 100 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if err != nil || ctx.Err() != nil || out.String() != "codex-cli 0.154.0\n" || cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("capability_fixture_first_exec_not_ready err=%v ctx=%v output=%q", err, ctx.Err(), out.String())
	}
	t.Logf("CAPABILITY_FIXTURE_PREPARED pid=%d terminal=%v budget=5s product_budget=2s", cmd.Process.Pid, cmd.ProcessState.Exited())
}

func TestCapabilitiesProbeBoundedAndUnsupportedNotLaunched(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	p := capabilityFixture(t, "touch '"+marker+"'\nsleep 8\n")
	for _, id := range []string{"grok", "agy"} {
		c, err := DiscoverCapabilities(context.Background(), id, CapabilityOptions{Binary: p, ProbeVersion: true})
		if err != nil || c.Runtime.Status != "NOT_RUN" || c.Runtime.Reason != "version_probe_read_only_unverified" {
			t.Fatalf("unsafe probe: %+v %v", c, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported CLI launched")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := DiscoverCapabilities(ctx, "codex", CapabilityOptions{Binary: p, ProbeVersion: true})
	if err != nil || c.Runtime.Status != "UNKNOWN" || c.Runtime.Reason != "version_probe_failed" || time.Since(start) > time.Second {
		t.Fatalf("unbounded probe: %+v %v", c, err)
	}
}

func TestCapabilitiesRejectsPublicFactoryTokenAndArbitraryOutput(t *testing.T) {
	token, err := store.NewBridgeClaimToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{token, "codex-cli 0.154.0 account=user@example.test", "private_failure " + token, "codex-cli 0.154.0\n" + token} {
		p := capabilityFixture(t, "printf '%s' '"+text+"'\n")
		c, err := DiscoverCapabilities(context.Background(), "codex", CapabilityOptions{Binary: p, ProbeVersion: true})
		raw, _ := json.Marshal(c)
		if err != nil || c.Runtime.Status != "UNKNOWN" || strings.Contains(string(raw), token) || strings.Contains(string(raw), "user@example.test") || strings.Contains(string(raw), "private_failure") {
			t.Fatalf("public output leak: %s %v", raw, err)
		}
	}
	p := filepath.Join(t.TempDir(), token)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nprintf 'codex-cli 0.154.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c, err := DiscoverCapabilities(context.Background(), "codex", CapabilityOptions{Binary: p, ProbeVersion: true})
	raw, _ := json.Marshal(c)
	if err != nil || c.Runtime.Executable != "" || strings.Contains(string(raw), token) {
		t.Fatalf("public executable leak: %s %v", raw, err)
	}
}

func TestCapabilitiesVersionProbePermissionsAndDefaultNoExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	p := capabilityFixture(t, "touch '"+marker+"'\nprintf 'codex-cli 0.154.0'\n")
	c, err := DiscoverCapabilities(context.Background(), "codex", CapabilityOptions{Binary: p})
	if err != nil || c.Runtime.Status != "NOT_RUN" {
		t.Fatalf("default observation=%+v %v", c, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("default capability read launched executable")
	}
	if err := os.Chmod(p, 0770); err != nil {
		t.Fatal(err)
	}
	c, err = DiscoverCapabilities(context.Background(), "codex", CapabilityOptions{Binary: p, ProbeVersion: true})
	if err != nil || c.Runtime.Reason != "executable_invalid" {
		t.Fatalf("group writable executable trusted: %+v %v", c, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsafe executable launched")
	}
}
