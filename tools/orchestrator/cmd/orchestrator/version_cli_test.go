package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func TestProbeRejectsUnstableAnnotationsEndToEnd(t *testing.T) {
	notes := []string{
		"2.1.316 (nightly.20261102)",
		"2.1.316 (alpha.1)",
		"2.1.316 (dev build abc)",
		"2.1.316 (custom channel)",
	}
	for _, version := range notes {
		t.Run(version, func(t *testing.T) {
			root := privateTaskTemp(t)
			binary := filepath.Join(root, "claude")
			script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '" + version + "';;\n--help) echo '--output-format --input-format --permission-mode --disallowed-tools';;\n*) exit 99;;\nesac\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			warmProvider(t, binary)
			var out bytes.Buffer
			if err := taskProbe(context.Background(), []string{"--provider", "claude", "--binary", binary}, &out); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Admitted       bool                `json:"admitted"`
				Supported      bool                `json:"profile_supported"`
				Reason         string              `json:"reason"`
				Provider       string              `json:"provider"`
				Normalized     string              `json:"normalized"`
				Baseline       string              `json:"baseline"`
				Ceiling        string              `json:"ceiling"`
				PolicyAdmitted bool                `json:"-"`
				VersionPolicy  adapter.VersionView `json:"version_policy"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.VersionPolicy.Admitted || got.Supported || got.Normalized != "2.1.316" || got.Baseline == "" || got.Ceiling == "" || got.Provider != string(adapter.ProviderClaude) {
				t.Fatalf("probe treated unstable note as stable: %s", out.Bytes())
			}
			if version == "2.1.316 (custom channel)" {
				if got.Reason != "cli_version_unrecognized_note" {
					t.Fatalf("reason=%s", got.Reason)
				}
			} else if got.Reason != "cli_version_prerelease" {
				t.Fatalf("reason=%s", got.Reason)
			}
			reqPath := filepath.Join(root, "request.json")
			body, _ := json.Marshal(providerRequest{Provider: adapter.ProviderClaude, BinaryPath: binary, LockFile: filepath.Join(root, "lock.json"), ConfirmSHA256: strings.Repeat("a", 64)})
			if err := os.WriteFile(reqPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			err := providerControl(context.Background(), "provider-lock", []string{"--request", reqPath}, &bytes.Buffer{})
			response := errorResponse(err)
			if response["reason"] != got.Reason || response["normalized"] != "2.1.316" || response["baseline"] == "" || response["ceiling"] == "" || fmt.Sprint(response["provider"]) != string(adapter.ProviderClaude) {
				t.Fatalf("lock error lost version window: %#v", response)
			}
		})
	}
}

func TestKnownStableNotesStayAdmitted(t *testing.T) {
	root := privateTaskTemp(t)
	binary := filepath.Join(root, "claude")
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.316 (Claude Code)';;\n--help) echo '--output-format --input-format --permission-mode --disallowed-tools';;\n*) exit 99;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	warmProvider(t, binary)
	var out bytes.Buffer
	if err := taskProbe(context.Background(), []string{"--provider", "claude", "--binary", binary}, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Supported bool                `json:"profile_supported"`
		Policy    adapter.VersionView `json:"version_policy"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Policy.Admitted || !got.Supported || got.Policy.Normalized != "2.1.316" {
		t.Fatalf("known Claude note rejected: %s", out.Bytes())
	}
}

func TestLaunchVersionRejectionKeepsWindow(t *testing.T) {
	root := t.TempDir()
	canary := filepath.Join(root, "must-not-run")
	payload, _ := json.Marshal(map[string]any{
		"provider": string(adapter.ProviderClaude), "binary_path": canary,
		"binary_version": "2.1.316 (nightly.20261102)", "binary_sha256": strings.Repeat("a", 64),
		"directory": root, "prompt": "inspect", "permission_mode": "plan",
	})
	_, err := invocationForGrant(context.Background(), contract.LaunchCommand{AdapterPayload: payload})
	var rejected *adapter.VersionError
	if !errors.As(err, &rejected) || rejected.Normalized != "2.1.316" || rejected.Baseline == "" || rejected.Ceiling == "" || rejected.Code != "cli_version_prerelease" {
		t.Fatalf("launch err=%v", err)
	}
	encoded, _ := json.Marshal(errorResponse(err))
	for _, field := range []string{`"provider":"claude-code"`, `"normalized":"2.1.316"`, `"reason":"cli_version_prerelease"`, `"baseline":`, `"ceiling":`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("stderr missing %s: %s", field, encoded)
		}
	}
	if _, statErr := os.Stat(canary); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("rejected version launched")
	}
}

func TestCodexProbeNamesTrialGaps(t *testing.T) {
	root := privateTaskTemp(t)
	binary := filepath.Join(root, "codex")
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo 'codex-cli 0.166.0';;\nexec) [ \"$2\" = --help ] || exit 98; echo '--json --color --model --config --ephemeral --skip-git-repo-check --strict-config --ignore-rules';;\n*) exit 99;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	warmProvider(t, binary)
	var out bytes.Buffer
	if err := taskProbe(context.Background(), []string{"--provider", "codex", "--binary", binary}, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Supported bool                `json:"profile_supported"`
		Reason    string              `json:"reason"`
		Missing   []string            `json:"missing"`
		Policy    adapter.VersionView `json:"version_policy"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Policy.Admitted || got.Supported || got.Reason != "codex_trial_guard_not_ready" || len(got.Missing) == 0 {
		t.Fatalf("codex probe: %s", out.Bytes())
	}
}

func warmProvider(t *testing.T, binary string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, binary, "--version").Run(); err != nil {
		t.Fatalf("fixture warmup: %v", err)
	}
}
