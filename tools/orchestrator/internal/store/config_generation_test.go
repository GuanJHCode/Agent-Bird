package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestConfigGenerationReleasableKeepsOriginalRefAndRetryableFailure(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ref := &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("b", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1.0.0"}
	raw, _ := json.Marshal(map[string]any{"kind": "fake", "config_generation": ref, "config_model_source": "not-applicable"})
	_, err = submitExistingConfigHistory(t, db, ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 111, OriginBirth: "birth", DeliveryMode: "collect"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/synthetic/original-executable"}, LaunchID: "launch", LaunchToken: "launch-token", ControlToken: "control-token", Tasks: []TaskSpec{{ID: "completed", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: raw}, {ID: "retryable", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.ExecContext(ctx, `UPDATE tasks SET status=CASE id WHEN 'completed' THEN 'completed' ELSE 'failed' END`); err != nil {
		t.Fatal(err)
	}
	releasable, err := db.ReleasableTasks(ctx)
	if err != nil || len(releasable) != 1 || releasable[0].TaskID != "completed" || releasable[0].RunID != "run" || releasable[0].ConfigGeneration == nil || *releasable[0].ConfigGeneration != *ref {
		t.Fatalf("original proof/ref lost or retryable failure released: %#v %v", releasable, err)
	}
}

func configOriginalRef() *contract.ConfigGenerationRef {
	return &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("b", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1.0.0"}
}

func assertOriginalConfigPayload(t *testing.T, raw []byte, ref *contract.ConfigGenerationRef) {
	t.Helper()
	p, err := adapter.DecodeInvocationPayload(raw)
	if err != nil || p.ConfigGeneration == nil || *p.ConfigGeneration != *ref || p.Profile != nil && (p.Profile.Model != "a" || p.ConfigModelSource != "saved-default") {
		t.Fatalf("durable transition lost original model/ref: %#v %v", p, err)
	}
}

func TestConfigGenerationDurableDecisionsKeepOriginalPayload(t *testing.T) {
	ctx := context.Background()
	ref := configOriginalRef()
	t.Run("question-continuation", func(t *testing.T) {
		f := newTypedContinuationFixtureWithConfig(t, adapter.ProviderAGY, "task", ref)
		f.start(t)
		q := f.question(t, 1)
		model := "b"
		if _, err := f.db.ProviderControl(ctx, "owner", string(adapter.ProviderAGY), "model", &model); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.AnswerQuestion(ctx, AnswerSpec{TaskID: "task", WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: 1, Answer: "17"}); err != nil {
			t.Fatal(err)
		}
		g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		assertOriginalConfigPayload(t, g.AdapterPayload, ref)
		if string(g.AdapterPayload) != string(f.grant.AdapterPayload) || g.Continuation == nil || g.Continuation.AdapterPayloadSHA256 != runtimeHash(string(f.grant.AdapterPayload)) {
			t.Fatal("continuation recreated original payload/proof")
		}
	})
	t.Run("review-retry", func(t *testing.T) {
		f := newReviewRetryFixtureWithConfig(t, adapter.ProviderClaude, contract.EventFailed, ref)
		model := "b"
		if _, err := f.db.ProviderControl(ctx, "owner", string(adapter.ProviderClaude), "model", &model); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
			t.Fatal(err)
		}
		g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		assertOriginalConfigPayload(t, g.AdapterPayload, ref)
		if g.AttemptID == f.grant.AttemptID || g.WorkRevision != f.grant.WorkRevision {
			t.Fatal("retry identity scope changed")
		}
	})
	t.Run("rework", func(t *testing.T) {
		f := newReworkFixtureWithConfig(t, ref)
		model := "b"
		if _, err := f.db.ProviderControl(ctx, "owner", string(adapter.ProviderClaude), "model", &model); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
			t.Fatal(err)
		}
		g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		assertOriginalConfigPayload(t, g.AdapterPayload, ref)
		if g.WorkRevision != 2 || g.AttemptID == f.edit.AttemptID {
			t.Fatal("rework did not use original decision transition")
		}
	})
	// These use the actual Store admission/events/decisions with trusted
	// synthetic Host identities. They do not prove any physical worker action.
}

// These preexisting transition tests seed retained pre-authority configuration
// rows, not new bound admission. Ordinary Submit remains real; only the saved
// legacy task payload is installed before any scheduling/event decision.
func submitExistingConfigHistory(t *testing.T, db *DB, ctx context.Context, p PlanSpec) (SubmitReceipt, error) {
	t.Helper()
	paired := false
	for _, task := range p.Tasks {
		var fields map[string]json.RawMessage
		if json.Unmarshal(task.AdapterPayload, &fields) == nil && fields["config_generation"] != nil {
			paired = true
		}
	}
	if !paired {
		return db.SubmitPlan(ctx, p)
	}
	originals := append([]TaskSpec(nil), p.Tasks...)
	p.Tasks = append([]TaskSpec(nil), p.Tasks...)
	for i := range p.Tasks {
		var fields map[string]json.RawMessage
		if json.Unmarshal(p.Tasks[i].AdapterPayload, &fields) == nil {
			delete(fields, "config_generation")
			delete(fields, "config_model_source")
			p.Tasks[i].AdapterPayload, _ = json.Marshal(fields)
		}
	}
	receipt, e := db.SubmitPlan(ctx, p)
	if e != nil {
		return receipt, e
	}
	for _, task := range originals {
		if len(task.AdapterPayload) == 0 {
			continue
		}
		if _, e = db.sql.ExecContext(ctx, `UPDATE task_runtime SET adapter_payload=? WHERE task_id=?`, string(task.AdapterPayload), task.ID); e != nil {
			return receipt, e
		}
	}
	return receipt, nil
}
