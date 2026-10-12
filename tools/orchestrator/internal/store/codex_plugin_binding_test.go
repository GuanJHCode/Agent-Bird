package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

func TestCodexPluginSessionBindingDoesNotAuthorizeContinuation(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderCodex)
	p, err := adapter.DecodeInvocationPayload(f.payload)
	if err != nil {
		t.Fatal(err)
	}
	p.Profile.CodexHostToolsVersion = 1
	f.payload, _ = json.Marshal(p)
	if _, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id=?`, string(f.payload), f.grant.TaskID); err != nil {
		t.Fatal(err)
	}
	f.start(t) // Native session must receive a durable ACK before turn/start.
	var profile string
	if err = f.db.sql.QueryRow(`SELECT profile_json FROM attempt_provider_bindings WHERE attempt_id=?`, f.grant.AttemptID).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	var frozen adapter.ExecutionProfile
	if json.Unmarshal([]byte(profile), &frozen) != nil || frozen.CodexHostToolsVersion != 1 {
		t.Fatal("plugin binding not frozen", profile)
	}
	if _, err = continuationPayload(f.payload); err != CodeError("profile_resume_not_verified") {
		t.Fatal("plugin continuation admitted", err)
	}
	f.question(t, 1)
	_, err = f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: f.grant.TaskID, WorkRevision: 1, QuestionID: "question-1", QuestionRevision: 1, Answer: "continue"})
	if err == nil {
		t.Fatal("plugin answer queued a continuation")
	}
	var queued int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM segments WHERE attempt_id=?`, f.grant.AttemptID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatal("unexpected continuation command", queued)
	}
}
