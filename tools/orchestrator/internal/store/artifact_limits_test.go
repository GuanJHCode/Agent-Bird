package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func TestReportArtifactRejectsOversizedFile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "large.bin")
	data := bytes.Repeat([]byte("x"), 1024*1024+1)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"status": "completed", "artifact": contract.ArtifactRef{ID: "large", Path: path, SHA256: runtimeHash(string(data)), Size: int64(len(data))}})
	if _, err = validateReportPayload("result", raw, root); err == nil || err.Error() != "report_artifact_too_large" {
		t.Fatalf("oversized artifact accepted: %v", err)
	}
}
func TestAcceptedArtifactsConsumeTransactionalQuota(t *testing.T) {
	db, launch := quotaFixture(t)
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "result.bin")
	content := bytes.Repeat([]byte("x"), 1024)
	if err = os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: launch.ReportCapabilityID, ProducerID: launch.HostID, RunID: launch.RunID, TaskID: launch.TaskID, AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: launch.WorkRevision, ExecutionEpoch: launch.ExecutionEpoch, TokenHash: runtimeHash("token"), CapabilityDir: root}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"status": "completed", "artifact": contract.ArtifactRef{ID: "result", Path: path, SHA256: runtimeHash(string(content)), Size: int64(len(content))}})
	spec := ReportEventSpec{CapabilityID: launch.ReportCapabilityID, Token: "token", EventID: "result", Sequence: 1, Kind: "result", Payload: raw}
	db.runControlLimit = 1024
	if _, err = db.CommitReportEvent(ctx, spec); err == nil || err.Error() != "control_spool_full" {
		t.Fatalf("artifact escaped run quota: %v", err)
	}
	db.runControlLimit = 16 * 1024 * 1024
	if _, err = db.CommitReportEvent(ctx, spec); err != nil {
		t.Fatal(err)
	}
	var accounted, body int64
	if err = db.sql.QueryRow(`SELECT accounted_bytes,length(CAST(body_json AS BLOB)) FROM report_events WHERE event_id='result'`).Scan(&accounted, &body); err != nil || accounted != body+1024 {
		t.Fatalf("artifact unaccounted: %d body=%d %v", accounted, body, err)
	}
}
