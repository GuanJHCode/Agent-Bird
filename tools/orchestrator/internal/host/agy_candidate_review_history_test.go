package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

// Exercise the exact post-materialization review routing helper. Only real
// owned files/metadata/command construction; no Git, provider or sandbox actor.
func TestAGYCandidateReviewPreparesOwnedHistory(t *testing.T) {
	t.Run("agy_owned_history", func(t *testing.T) {
		h, cmd, profile, grant, original := agyHistoryFixture(t)
		var payload adapter.InvocationPayload
		if err := json.Unmarshal(grant.AdapterPayload, &payload); err != nil {
			t.Fatal(err)
		}
		payload.Kind = ""
		payload.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validate"}
		payload.Directory = filepath.Join(cmd.Dir, "materialized-review")
		if err := os.Mkdir(payload.Directory, 0700); err != nil {
			t.Fatal(err)
		}
		grant.TaskID, grant.AttemptID, grant.SegmentID = "review", "review-attempt", "review-segment"
		grant.AdapterPayload, _ = json.Marshal(payload)
		cmd.Dir = payload.Directory
		beforeArgs, beforeEnv := append([]string(nil), cmd.Args...), append([]string(nil), cmd.Env...)
		before := map[string]agyConfigReference{}
		for _, name := range []string{"settings.json", "keybindings.json", "cache/default_project_id.txt"} {
			ref, err := agyFileSnapshot(filepath.Join(original, name), name, agyConfigLimit)
			if err != nil {
				t.Fatal(err)
			}
			before[name] = ref
		}
		scratch := filepath.Join(cmd.Dir, "scratch")
		wrapped, err := h.prepareCandidateReviewSandbox(context.Background(), cmd, profile, scratch, grant, payload.ProviderLock)
		if err != nil {
			t.Fatal(err)
		}
		if wrapped.Path != "/usr/bin/sandbox-exec" || wrapped.Dir != cmd.Dir || len(wrapped.Args) < 4 || wrapped.Args[2] != cmd.Path {
			t.Fatal("review command/workspace changed")
		}
		var history agyHistoryBinding
		if err := readContinuationMetadata(filepath.Join(h.spoolRoot, grant.AttemptID, grant.SegmentID, "provider-history.json"), &history); err != nil {
			t.Fatalf("actual review branch bypassed owned history: %v", err)
		}
		if history.Origin != h.originForGrant(grant, "antigravity-cli") || history.Pin != payload.ProviderLock.Binary || history.Workspace != cmd.Dir || history.Path == "" {
			t.Fatal("review history grant/pin/workspace binding changed")
		}
		if !reflect.DeepEqual(cmd.Args, beforeArgs) || !reflect.DeepEqual(cmd.Env, beforeEnv) {
			t.Fatal("parent command mutated")
		}
		wantArgs := append(append([]string(nil), beforeArgs...), "--app_data_dir", history.Relative)
		if !reflect.DeepEqual(wrapped.Args[3:], wantArgs) {
			t.Fatal("native args changed beyond owned app-data argument")
		}
		policy := wrapped.Args[1]
		if !strings.Contains(policy, "(allow file-write* (subpath "+strconv.Quote(history.Path)+"))") || strings.Contains(policy, "(allow file-write* (subpath "+strconv.Quote(cmd.Dir)+"))") {
			t.Fatal("owned history or readonly candidate policy incorrect")
		}
		for name, ref := range before {
			after, err := agyFileSnapshot(filepath.Join(original, name), name, agyConfigLimit)
			if err != nil || after != ref {
				t.Fatal("source config reference changed")
			}
			for _, path := range []string{filepath.Join(original, name), filepath.Join(history.Path, name)} {
				if !strings.Contains(policy, "(deny file-write* (literal "+strconv.Quote(path)+"))") {
					t.Fatal("source/link config write protection missing")
				}
			}
		}
		// The existing implementer preparation owns a distinct grant/history.
		implementGrant := grant
		implementGrant.TaskID = "implement"
		implementGrant.AttemptID = "implement-attempt"
		implementGrant.SegmentID = "implement-segment"
		implementPayload := payload
		implementPayload.CandidateAction = nil
		implementPayload.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}
		implementGrant.AdapterPayload, _ = json.Marshal(implementPayload)
		_, implementScope, err := h.prepareAGYHistory(cmd, implementGrant, payload.ProviderLock.Binary)
		if err != nil || implementScope.Path == history.Path {
			t.Fatalf("model stages reused owned history: %v", err)
		}
	})
	t.Run("non_agy_original_routing", func(t *testing.T) {
		h, cmd, profile, grant, _ := agyHistoryFixture(t)
		lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/pinned/claude", Version: "synthetic", SHA256: strings.Repeat("a", 64)}}
		cmd.Path = lock.Binary.Path
		scratch := filepath.Join(cmd.Dir, "scratch")
		want, err := sandboxCommand(context.Background(), cmd, profile, scratch)
		if err != nil {
			t.Fatal(err)
		}
		got, err := h.prepareCandidateReviewSandbox(context.Background(), cmd, profile, scratch, grant, lock)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("non-AGY review changed: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(h.spoolRoot, grant.AttemptID, grant.SegmentID, "provider-history.json")); !os.IsNotExist(err) {
			t.Fatal("non-AGY acquired AGY history")
		}
	})
}
