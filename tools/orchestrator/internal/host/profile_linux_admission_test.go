//go:build linux

package host

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// These locks only select the existing history-preparation branches. The
// fixture never executes or qualifies a Provider, sandbox, or another OS actor.
func TestLinuxProfileAdmissionRejectsBeforeHistory(t *testing.T) {
	for _, provider := range []string{"claude-code", "antigravity-cli"} {
		t.Run(provider, func(t *testing.T) {
			h, cmd, profile, grant := linuxProfileAdmissionFixture(t, provider)
			original := cmd
			original.Args = append([]string(nil), cmd.Args...)
			original.Env = append([]string(nil), cmd.Env...)
			before := linuxProfileAdmissionSnapshot(t, cmd.Dir)
			scratch := filepath.Join(h.spoolRoot, "scratch")

			got, err := h.sandboxProfileCommand(context.Background(), cmd, profile, scratch, grant)
			if err == nil || err.Error() != "profile_sandbox_unsupported" {
				t.Errorf("Linux profile must be refused by platform admission: %v", err)
			}
			if !reflect.DeepEqual(got, original) {
				t.Errorf("refused command was rewritten: got %+v; want %+v", got, original)
			}
			if !reflect.DeepEqual(cmd, original) {
				t.Error("refused call mutated caller command slices")
			}
			after := linuxProfileAdmissionSnapshot(t, cmd.Dir)
			for path, old := range before {
				current, exists := after[path]
				if !exists {
					t.Errorf("refused call removed fixture entry %q", path)
					continue
				}
				if !os.SameFile(old.info, current.info) || old.info.Mode() != current.info.Mode() ||
					!old.info.ModTime().Equal(current.info.ModTime()) || old.body != current.body || old.link != current.link {
					t.Errorf("refused call changed fixture entry %q", path)
				}
			}
			for path := range after {
				if _, exists := before[path]; !exists {
					t.Errorf("refused call created fixture entry %q", path)
				}
			}
		})
	}
}

func linuxProfileAdmissionFixture(t *testing.T, provider string) (*Host, process.Command, *adapter.ExecutionProfile, contract.LaunchCommand) {
	t.Helper()
	if provider == "antigravity-cli" {
		h, cmd, profile, grant, _ := agyHistoryFixture(t)
		return h, cmd, profile, grant
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte("private-config-fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.BindContinuationOwner("owner"); err != nil {
		t.Fatal(err)
	}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
	pin := adapter.BinaryPin{Path: "/unexecuted/claude-fixture", Version: "2.1.285 (Claude Code)"}
	payload := adapter.InvocationPayload{Kind: "provider", Provider: provider, Directory: root, Profile: profile,
		ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: pin}}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "first", WorkRevision: 1, PlanRevision: 1, AdapterPayload: body}
	cmd := process.Command{Path: pin.Path, Dir: root, Args: []string{"--output-format", "stream-json"},
		Env: []string{"CLAUDE_CONFIG_DIR=" + config, "CLAUDE_CODE_SKIP_PROMPT_HISTORY=0", "AUTH_SOURCE_SENTINEL=unchanged"}}
	return h, cmd, profile, grant
}

type linuxProfileAdmissionEntry struct {
	info os.FileInfo
	body string
	link string
}

func linuxProfileAdmissionSnapshot(t *testing.T, root string) map[string]linuxProfileAdmissionEntry {
	t.Helper()
	entries := make(map[string]linuxProfileAdmissionEntry)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := linuxProfileAdmissionEntry{info: info}
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.body = string(body)
		} else if info.Mode()&os.ModeSymlink != 0 {
			entry.link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		entries[relative] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
