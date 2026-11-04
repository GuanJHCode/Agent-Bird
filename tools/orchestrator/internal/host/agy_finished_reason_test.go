package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

// The synthetic CLI exits zero and emits progress plus a terminal envelope.
// Only a complete result may become a result/question or carry a final artifact.
func TestAGYFinishedReasonCannotPublishPartialResult(t *testing.T) {
	tests := []struct {
		name, status, reason, wantStatus, wantEvent, wantText string
	}{
		{"normal-success", "SUCCESS", "", "result_ready", contract.EventResult, "FINAL"},
		{"normal-waiting", "WAITING", "", "waiting_user", contract.EventQuestion, "QUESTION"},
	}
	for _, status := range []string{"SUCCESS", "WAITING"} {
		for _, reason := range []string{"print_timeout", "interrupted", "error"} {
			tests = append(tests, struct {
				name, status, reason, wantStatus, wantEvent, wantText string
			}{status + "-" + reason, status, reason, "failed", contract.EventFailed, ""})
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			h, err := NewIPC(filepath.Join(root, "spool"), "agy-finished-producer")
			if err != nil {
				t.Fatal(err)
			}
			text := tc.wantText
			if tc.reason != "" {
				text = "PRIVATE_PARTIAL"
			}
			terminal := map[string]any{"conversation_id": "agy-finished", "status": tc.status, "response": text}
			if tc.reason != "" {
				terminal["finished_reason"] = tc.reason
			}
			body, err := json.Marshal(map[string]any{"event": "result", "result": terminal})
			if err != nil {
				t.Fatal(err)
			}
			stream := "{\"event\":\"init\",\"conversation_id\":\"agy-finished\"}\n" +
				"{\"event\":\"step_update\",\"step_update\":{\"conversation_id\":\"agy-finished\",\"text_delta\":\"PRIVATE_PARTIAL\"}}\n" + string(body) + "\n"
			inv := providerInvocation{launchInvocation: launchInvocation{
				args: []string{"/bin/sh", "-c", `printf '%s' "$1"`, "agy-fixture", stream}, dir: root,
			}, provider: "antigravity-cli"}
			grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 3000}
			result, err := h.ExecuteLaunch(context.Background(), grant, inv)
			if err != nil || result.ExitCode != 0 || result.Status != tc.wantStatus {
				t.Fatalf("terminal outcome=%+v err=%v", result, err)
			}
			if tc.reason != "" && result.ArtifactPath != "" {
				t.Fatal("partial result became a final artifact")
			}
			sink := &durableSink{}
			if err := h.PublishAll(context.Background(), sink, 1); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range sink.events {
				if tc.reason != "" && (event.Kind == contract.EventResult || event.Kind == contract.EventQuestion) {
					t.Fatal("incomplete turn became an acceptable result or question")
				}
				if event.Kind != tc.wantEvent {
					continue
				}
				found = true
				if tc.reason != "" {
					if event.Artifact != nil {
						t.Fatal("incomplete turn published a partial artifact")
					}
				} else {
					if event.Artifact == nil {
						t.Fatal("normal turn lost its final artifact")
					}
					content, err := os.ReadFile(event.Artifact.Path)
					if err != nil || string(content) != tc.wantText {
						t.Fatalf("normal artifact=%q err=%v", content, err)
					}
				}
			}
			if !found {
				t.Fatalf("expected durable terminal %s missing", tc.wantEvent)
			}
		})
	}
}
