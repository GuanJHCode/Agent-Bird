package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The real admitted worker first emits a native failure, then attempts the
// actual legacy report CLI if Source has granted that authority. The test
// waits for the original physical exit before asserting exclusivity.
func TestPublicManagedNativeCompletionRejectsLegacyReportAuthority(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			runC04PublicContinuationFixture(t, provider, adapter.Reviewer, "report-bypass")
		})
	}
}

func c04LegacyReportBypassFixture(provider, session string, fail func(error)) {
	if provider == "claude-code" {
		fmt.Printf("{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"session_id\":%q,\"is_error\":true,\"result\":\"owned native failure, no structured completion\"}\n", session)
	} else {
		fmt.Printf("{\"event\":\"result\",\"result\":{\"conversation_id\":%q,\"status\":\"ERROR\",\"response\":\"owned native failure, no structured completion\"}}\n", session)
	}
	executable, capability := os.Getenv("ORCHESTRATOR_REPORT_EXECUTABLE"), os.Getenv("ORCHESTRATOR_REPORT_CAPABILITY")
	if executable == "" && capability == "" {
		return
	}
	if executable == "" || capability == "" {
		fail(fmt.Errorf("partial legacy report authority"))
	}
	body, err := os.ReadFile(capability)
	if err != nil {
		fail(err)
	}
	var binding reportCapabilityFile
	if json.Unmarshal(body, &binding) != nil || binding.Token == "" {
		fail(fmt.Errorf("unusable legacy report binding"))
	}
	artifact := filepath.Join(filepath.Dir(capability), "artifacts", "bypass-question.txt")
	text := []byte("Legacy helper business question without native success")
	if err = os.WriteFile(artifact, text, 0600); err != nil {
		fail(err)
	}
	sum := sha256.Sum256(text)
	payload, _ := json.Marshal(map[string]any{
		"status": "waiting_user", "question_id": "bypass-question", "question_revision": 1, "question_kind": "technical",
		"artifact": contract.ArtifactRef{Path: artifact, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(text))},
	})
	request := filepath.Join(filepath.Dir(artifact), "bypass-request.json")
	encoded, _ := json.Marshal(reportFile{Version: 1, EventID: "bypass-question-event", Sequence: 1, Kind: "question", Payload: payload})
	if err = os.WriteFile(request, encoded, 0600); err != nil {
		fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Output may contain ACK hashes. Keep it private and report only success;
	// the parent verifies actual Store rows, rather than trusting this message.
	if _, err = exec.CommandContext(ctx, executable, "report", "--capability-file", capability, "--request", request).Output(); err != nil {
		fail(fmt.Errorf("legacy report helper refused: %v", err))
	}
}

func c04AssertNoLegacyReportAuthority(t *testing.T, ctx context.Context, state string, provider adapter.Provider) {
	t.Helper()
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for {
		var ended int
		if err = db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE status IN ('exited','not_started','unknown')`).Scan(&ended); err != nil {
			t.Fatal(err)
		}
		if ended == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("original Source worker did not reach its bounded physical exit")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var capRows, registered, usable, reports, questions, spawned, sessions, failed, results int
	queries := []struct {
		sql string
		dst *int
	}{
		{`SELECT COUNT(*) FROM report_capabilities`, &capRows},
		{`SELECT COUNT(*) FROM report_capabilities WHERE status='registered'`, &registered},
		{`SELECT COUNT(*) FROM report_capabilities WHERE token_hash!='' OR capability_dir!=''`, &usable},
		{`SELECT COUNT(*) FROM report_events`, &reports},
		{`SELECT COUNT(*) FROM runtime_questions`, &questions},
		{`SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='spawned'`, &spawned},
		{`SELECT COUNT(*) FROM segment_sessions`, &sessions},
		{`SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='failed'`, &failed},
		{`SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='result'`, &results},
	}
	for _, q := range queries {
		if err = db.SQL().QueryRowContext(ctx, q.sql).Scan(q.dst); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err = db.SQL().QueryRowContext(ctx, `SELECT body_json FROM runtime_events WHERE json_extract(body_json,'$.kind')='exited'`).Scan(&raw); err != nil {
		t.Fatal("fixture did not preserve original Exited proof:", err)
	}
	var exited contract.Event
	if json.Unmarshal([]byte(raw), &exited) != nil || exited.Process == nil || exited.ActiveMS <= 0 || spawned != 1 || sessions != 1 {
		t.Fatal("fixture never reached original managed native Start/session/Wait")
	}
	if err = process.ConfirmGroupExited(exited.Process.PGID); err != nil {
		t.Fatal(err)
	}
	t.Logf("managed_report_channel_receipt provider=%s cap_rows=%d registered=%d usable_binding=%d report_events=%d questions=%d failed=%d results=%d pgid=%d group_absent=true active_ms=%d", provider, capRows, registered, usable, reports, questions, failed, results, exited.Process.PGID, exited.ActiveMS)
	if capRows != 1 || registered != 0 || usable != 0 || reports != 0 || questions != 0 || failed != 1 || results != 0 || exited.ExitCode == nil || *exited.ExitCode != 0 {
		t.Fatal("managed native failure acquired legacy report authority or business question")
	}
}

func c04AssertCandidateReportCapabilitiesRemain(t *testing.T, ctx context.Context, state string) {
	t.Helper()
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, task := range []string{"validate", "review", "integrate"} {
		var count int
		if err = db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM report_capabilities WHERE task_id=? AND status IN ('registered','revoked') AND token_hash!='' AND capability_dir!=''`, task).Scan(&count); err != nil || count < 1 {
			t.Fatalf("candidate action lost its original report registration: task=%s count=%d err=%v", task, count, err)
		}
	}
}
