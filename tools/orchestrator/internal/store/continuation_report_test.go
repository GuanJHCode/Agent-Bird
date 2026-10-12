package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"os"
	"path/filepath"
	"testing"
)

func TestTypedClaudeReportQuestionAcrossTwoAnsweredSegments(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderClaude)
	for cycle := 1; cycle <= 2; cycle++ {
		f.start(t)
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		os.Chmod(dir, 0700)
		g := f.grant
		if _, err = f.db.RegisterReportCapability(context.Background(), contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, TokenHash: runtimeHash("token"), CapabilityDir: dir}); err != nil {
			t.Fatal(err)
		}
		body := []byte(fmt.Sprintf("Question %d", cycle))
		path := filepath.Join(dir, "question.txt")
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		question := fmt.Sprintf("report-question-%d", cycle)
		payload, _ := json.Marshal(map[string]any{"status": "waiting_user", "question_id": question, "question_revision": cycle, "question_kind": "technical", "artifact": contract.ArtifactRef{ID: "technical-question", Path: path, Size: int64(len(body)), SHA256: runtimeHash(string(body))}})
		if _, err = f.db.CommitReportEvent(context.Background(), ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: "token", EventID: fmt.Sprintf("report-%d", cycle), Sequence: 1, Kind: "question", Payload: payload}); err != nil {
			t.Fatalf("new question after prior durable answer rejected at cycle%d: %v", cycle, err)
		}
		stop := f.event(contract.EventStopped)
		stop.StopReason = "owner_stop_requested"
		f.send(t, stop)
		x := f.event(contract.EventExited)
		code := 130
		x.ExitCode = &code
		x.ActiveMS = 200
		f.send(t, x)
		if _, err = f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: g.TaskID, WorkRevision: 1, QuestionID: question, QuestionRevision: cycle, Answer: "Answer"}); err != nil {
			t.Fatal(err)
		}
		f.grant, err = f.db.ClaimReady(context.Background(), f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if f.grant.Continuation == nil || f.grant.Continuation.SourceKind != "report" || f.grant.Continuation.SourceEventID != fmt.Sprintf("report-%d", cycle) || f.grant.AttemptID != g.AttemptID {
			t.Fatalf("report provenance lost: %+v", f.grant)
		}
	}
}
