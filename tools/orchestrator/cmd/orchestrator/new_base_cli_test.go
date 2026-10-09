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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Real public CLI + Coordinator/Store + source Host + Git. The two providers
// consume their actual managed protocol prompt; no model/account is contacted.
func TestCLINewBaseOrderedGeneration(t *testing.T) {
	root, e := os.MkdirTemp("/private/tmp", "new-base-cli-")
	if e != nil {
		t.Fatal(e)
	}
	clean := false
	t.Cleanup(func() {
		if clean && !t.Failed() {
			os.RemoveAll(root)
		} else {
			t.Logf("retained new base runtime: %s", root)
		}
	})
	timeout := 90 * time.Second
	if os.Getenv("NEW_BASE_NATIVE_RACE") == "1" {
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	bin, state, repo := filepath.Join(root, "orchestrator"), filepath.Join(root, "state"), filepath.Join(root, "repo")
	buildArgs := []string{"build", "-o", bin, "."}
	if os.Getenv("NEW_BASE_NATIVE_RACE") == "1" {
		buildArgs = []string{"build", "-race", "-o", bin, "."}
	}
	t.Logf("native orchestrator build args: %v", buildArgs)
	if out, e := exec.Command("go", buildArgs...).CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, out)
	}
	for _, p := range []string{state, repo} {
		if e = os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, key := range []string{"CANDIDATE_FIXTURE_MODE", "GROK_REVIEW_FIXTURE", "GROK_INDEXED_FIXTURE", "C04_FIXTURE_PROVIDER"} {
		t.Setenv(key, "")
	}
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, e := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.name", "New Base Fixture")
	git(repo, "config", "user.email", "new-base@example.invalid")
	if e = os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	if e = os.WriteFile(filepath.Join(repo, "README"), []byte("user unfinished work\n"), 0600); e != nil {
		t.Fatal(e)
	}
	before := git(repo, "status", "--porcelain=v1")
	dirty, _ := os.ReadFile(filepath.Join(repo, "README"))
	target := filepath.Join(root, "target")
	git(repo, "worktree", "add", "-b", "delivery", target, base)
	fixture, e := os.ReadFile(os.Args[0])
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(fixture)
	lock := func(name string, provider adapter.Provider) *adapter.ProviderLock {
		t.Helper()
		p := filepath.Join(root, name)
		if e = os.WriteFile(p, fixture, 0700); e != nil {
			t.Fatal(e)
		}
		return &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: adapter.BinaryPin{Path: p, Version: "fixture-v1", SHA256: hex.EncodeToString(sum[:])}}
	}
	claude := lock("compose-cross-claude-fixture", adapter.ProviderClaude)
	agy := lock("compose-cross-agy-fixture", adapter.ProviderAGY)
	reviewLock := lock("compose-cross-review-claude-fixture", adapter.ProviderClaude)
	stop, _ := startIndexedCLICoordinator(t, ctx, root, bin, state, 1)
	defer stop()
	requestPath := filepath.Join(root, "request.json")
	write := func(v any) {
		t.Helper()
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(requestPath, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	owner := func() coordinator.SubmitRequest {
		t.Helper()
		birth, e := process.Birth(os.Getpid())
		if e != nil {
			t.Fatal(e)
		}
		write(map[string]any{"controller_thread": "generation-owner", "origin_pid": os.Getpid(), "origin_birth": birth})
		var req coordinator.SubmitRequest
		if e = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", requestPath), &req); e != nil {
			t.Fatal(e)
		}
		return req
	}
	req := owner()
	req.RunID = "generation-one"
	req.PlanRevision = 7
	req.DeliveryMode = "collect"
	for _, id := range []string{"A", "B", "compose", "validate", "review", "integrate"} {
		p := adapter.InvocationPayload{Directory: filepath.Join(root, "worker-"+id), Prompt: "controlled implementation"}
		task := coordinator.TaskRequest{ID: id, MaxAttempts: 2, MaxActiveMS: 30000, CompletionPolicy: "owner_review", BudgetGroupID: "g1-" + id}
		switch id {
		case "A", "B":
			p.Provider = string(adapter.ProviderClaude)
			p.ProviderLock = claude
			if id == "B" {
				p.Provider = string(adapter.ProviderAGY)
				p.ProviderLock = agy
			}
			p.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 30000}
			p.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1, AutoDirectory: true, RepoRoot: repo, BaseOID: base, Paths: []string{strings.ToLower(id) + ".txt"}}
		case "compose":
			task.Dependencies = []string{"A", "B"}
			p.Kind = "candidate"
			p.CandidateAction = &adapter.CandidateAction{Version: 2, DependencyInputVersion: 2, Operation: "compose", OrderedSourceTasks: []string{"B", "A"}}
		default:
			p.Kind = "candidate"
			p.CandidateAction = &adapter.CandidateAction{Version: 1, AutoDirectory: true, Operation: id, Target: adapter.CandidateTarget{Worktree: target, Ref: "refs/heads/delivery", BaseOID: base}}
			switch id {
			case "validate":
				task.Dependencies = []string{"compose"}
				p.CandidateAction.SourceTask = "compose"
				p.CandidateAction.Command = []string{"/usr/bin/grep", "-q", "compose-b", "b.txt"}
			case "review":
				task.Dependencies = []string{"validate"}
				p.CandidateAction.SourceTask = "validate"
				p.Kind = ""
				p.Provider = string(adapter.ProviderClaude)
				p.ProviderLock = reviewLock
				p.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 30000}
			case "integrate":
				task.Dependencies = []string{"review"}
				p.CandidateAction.SourceTask = "review"
				p.Directory = target
			}
		}
		task.AdapterPayload, _ = json.Marshal(p)
		req.Tasks = append(req.Tasks, task)
	}
	submit := func(source string) string {
		t.Helper()
		write(req)
		args := []string{"submit", "--state-dir", state, "--request", requestPath}
		if source != "" {
			args = append(args, "--source-control-file", source)
		}
		var out struct {
			Status      string `json:"status"`
			ControlFile string `json:"control_file"`
		}
		raw := runBinary(t, ctx, bin, args...)
		if e = json.Unmarshal(raw, &out); e != nil || out.Status != "queued" {
			t.Fatalf("submit=%s %v", raw, e)
		}
		return out.ControlFile
	}
	sourceControl := submit("")
	type generation struct {
		events     map[string]contract.Event
		deliveries map[string]host.CandidateDelivery
	}
	complete := func(control string, n int) generation {
		t.Helper()
		g := generation{events: map[string]contract.Event{}, deliveries: map[string]host.CandidateDelivery{}}
		for _, task := range req.Tasks {
			for {
				status := taskStatus(t, ctx, bin, state, task.ID, control)
				if status.Status == "result_ready" {
					break
				}
				if status.Status == "failed" || status.Status == "unknown" || status.Status == "blocked_dependency" || ctx.Err() != nil {
					raw, _ := os.ReadFile(filepath.Join(state, "source-host.log"))
					t.Fatalf("generation%d %s status=%+v log=%s", n, task.ID, status, raw)
				}
				time.Sleep(25 * time.Millisecond)
			}
			var page struct {
				Events []contract.Event `json:"events"`
			}
			raw := runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", task.ID, "--control-file", control)
			if e = json.Unmarshal(raw, &page); e != nil || len(page.Events) != 1 || page.Events[0].Artifact == nil {
				t.Fatalf("collect %s %s %v", task.ID, raw, e)
			}
			event := page.Events[0]
			g.events[task.ID] = event
			raw, e = os.ReadFile(event.Artifact.Path)
			if e != nil {
				t.Fatal(e)
			}
			var d host.CandidateDelivery
			if e = json.Unmarshal(raw, &d); e != nil {
				t.Fatal(e)
			}
			g.deliveries[task.ID] = d
			acceptIndexedCLIEvent(t, ctx, bin, state, control, event)
		}
		return g
	}
	first := complete(sourceControl, 1)
	integrated := first.deliveries["integrate"].Integration
	if integrated == nil || integrated.State != "integrated" || git(target, "rev-parse", "HEAD") != integrated.TargetHEAD {
		t.Fatal("first integration missing")
	}
	db, e := store.Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	snapshot := newBaseCLISourceSnapshot(t, db, "generation-one")
	oldTasks := req.Tasks
	req = owner()
	req.RunID = "generation-two"
	req.PlanRevision = 7
	req.DeliveryMode = "collect"
	spec := &store.NewBaseReworkSpec{Version: 1, SourceIntegrationTaskID: "integrate", SourceEvent: store.ReworkEvent{EventID: first.events["integrate"].EventID, EventRevision: first.events["integrate"].EventRevision, EventHash: first.events["integrate"].PayloadHash}, TaskMap: map[string]string{}, Feedback: "NEW_BASE_FEEDBACK_MARKER: extend both contributions", Acceptance: "NEW_BASE_ACCEPTANCE_MARKER: keep old work and add generation two", CommandID: "new-base-generation-two"}
	for _, task := range oldTasks {
		spec.TaskMap[task.ID] = "next-" + task.ID
	}
	for _, task := range oldTasks {
		p, _ := adapter.DecodeInvocationPayload(task.AdapterPayload)
		task.ID = spec.TaskMap[task.ID]
		task.BudgetGroupID = "g2-" + task.ID
		task.Dependencies = append([]string(nil), task.Dependencies...)
		for i, id := range task.Dependencies {
			task.Dependencies[i] = spec.TaskMap[id]
		}
		if p.CandidateWorkspace != nil {
			p.CandidateWorkspace.BaseOID = integrated.TargetHEAD
		}
		if p.CandidateAction != nil {
			a := p.CandidateAction
			if a.SourceTask != "" {
				a.SourceTask = spec.TaskMap[a.SourceTask]
			}
			for i, id := range a.OrderedSourceTasks {
				a.OrderedSourceTasks[i] = spec.TaskMap[id]
			}
			if a.Operation != "compose" {
				a.Target.BaseOID = integrated.TargetHEAD
			}
		}
		task.AdapterPayload, _ = json.Marshal(p)
		req.Tasks = append(req.Tasks, task)
	}
	req.NewBaseRework = spec
	nextControl := submit(sourceControl)
	if nextControl == sourceControl {
		t.Fatal("control identity reused")
	}
	if replay := submit(sourceControl); replay != nextControl {
		t.Fatal("public exact retry changed control")
	}
	second := complete(nextControl, 2)
	if newBaseCLISourceSnapshot(t, db, "generation-one") != snapshot {
		t.Fatal("source durable history changed")
	}
	composition := second.deliveries["next-compose"]
	if composition.Composition == nil || composition.Composition.Binding.NewBaseSHA256 == "" || composition.Candidate.BaseOID != integrated.TargetHEAD || composition.Composition.Sources[0].TaskID != "next-B" || composition.Composition.Sources[1].TaskID != "next-A" {
		t.Fatalf("second ordered composition missing: %+v", composition)
	}
	for _, id := range []string{"A", "B", "compose", "validate", "review", "integrate"} {
		old, new := first.events[id], second.events["next-"+id]
		if old.EventID == new.EventID || old.AttemptID == new.AttemptID || old.SegmentID == new.SegmentID {
			t.Fatalf("reused %s event identity", id)
		}
		d := second.deliveries["next-"+id]
		if id == "A" || id == "B" {
			if d.Candidate.BaseOID != integrated.TargetHEAD || d.Candidate.Binding.NewBaseSHA256 == "" {
				t.Fatal("producer lineage unbound")
			}
		} else if !reflect.DeepEqual(d.Composition, composition.Composition) {
			t.Fatal("downstream lost new lineage")
		}
	}
	final := second.deliveries["next-integrate"].Integration
	if final == nil || final.State != "integrated" || final.TargetHEAD == integrated.TargetHEAD || git(target, "rev-parse", "HEAD") != final.TargetHEAD {
		t.Fatalf("second integration: %+v", final)
	}
	if git(target, "show", "HEAD:a.txt") != "compose-a-generation-two" || git(target, "show", "HEAD:b.txt") != "compose-b-generation-two" {
		t.Fatal("new generation instructions not applied")
	}
	// Derive a third generation from second-generation producer receipts. This
	// exercises both the retained lineage digest and same-transaction hold release.
	secondSnapshot := newBaseCLISourceSnapshot(t, db, "generation-two")
	secondTasks := req.Tasks
	req = owner()
	req.RunID = "generation-three"
	req.PlanRevision = 7
	req.DeliveryMode = "collect"
	thirdSpec := &store.NewBaseReworkSpec{Version: 1, SourceIntegrationTaskID: "next-integrate", SourceEvent: store.ReworkEvent{EventID: second.events["next-integrate"].EventID, EventRevision: second.events["next-integrate"].EventRevision, EventHash: second.events["next-integrate"].PayloadHash}, TaskMap: map[string]string{}, Feedback: "NEW_BASE_FEEDBACK_MARKER NEW_BASE_THIRD_MARKER: extend third generation", Acceptance: "NEW_BASE_ACCEPTANCE_MARKER: preserve all prior work", CommandID: "new-base-generation-three"}
	for _, task := range secondTasks {
		thirdSpec.TaskMap[task.ID] = "third-" + task.ID
	}
	for _, task := range secondTasks {
		var raw string
		if e = db.SQL().QueryRow(`SELECT adapter_payload FROM task_runtime WHERE task_id=?`, task.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		p, _ := adapter.DecodeInvocationPayload([]byte(raw))
		task.ID = thirdSpec.TaskMap[task.ID]
		task.BudgetGroupID = "g3-" + task.ID
		task.Dependencies = append([]string(nil), task.Dependencies...)
		for i, id := range task.Dependencies {
			task.Dependencies[i] = thirdSpec.TaskMap[id]
		}
		if p.CandidateWorkspace != nil {
			p.CandidateWorkspace.BaseOID = final.TargetHEAD
			p.CandidateWorkspace.NewBaseRework = nil
		}
		if a := p.CandidateAction; a != nil {
			if a.SourceTask != "" {
				a.SourceTask = thirdSpec.TaskMap[a.SourceTask]
			}
			for i, id := range a.OrderedSourceTasks {
				a.OrderedSourceTasks[i] = thirdSpec.TaskMap[id]
			}
			if a.Operation != "compose" {
				a.Target.BaseOID = final.TargetHEAD
			}
		}
		task.AdapterPayload, _ = json.Marshal(p)
		req.Tasks = append(req.Tasks, task)
	}
	req.NewBaseRework = thirdSpec
	thirdControl := submit(nextControl)
	if thirdControl == nextControl || thirdControl == sourceControl {
		t.Fatal("third control reused")
	}
	third := complete(thirdControl, 3)
	thirdComposition := third.deliveries["third-next-compose"]
	thirdFinal := third.deliveries["third-next-integrate"].Integration
	if thirdFinal == nil || thirdFinal.State != "integrated" || thirdFinal.TargetHEAD == final.TargetHEAD || thirdComposition.Candidate.BaseOID != final.TargetHEAD || thirdComposition.Candidate.Binding.NewBaseSHA256 == composition.Candidate.Binding.NewBaseSHA256 {
		t.Fatalf("third lineage lost: %+v", thirdFinal)
	}
	if newBaseCLISourceSnapshot(t, db, "generation-one") != snapshot || newBaseCLISourceSnapshot(t, db, "generation-two") != secondSnapshot {
		t.Fatal("third generation changed old history")
	}
	if git(target, "show", "HEAD:a.txt") != "compose-a-generation-three" || git(target, "show", "HEAD:b.txt") != "compose-b-generation-three" {
		t.Fatal("third instructions not applied")
	}
	t.Logf("third generation integrated=%s; prior producer/compose lineage remains authenticated, fresh budget/task/event identities", thirdFinal.TargetHEAD)
	currentDirty, _ := os.ReadFile(filepath.Join(repo, "README"))
	if git(repo, "rev-parse", "HEAD") != base || git(repo, "status", "--porcelain=v1") != before || string(currentDirty) != string(dirty) {
		t.Fatal("user dirty checkout changed")
	}
	// Drift after admission is refused at the producer Host/common-lock boundary.
	producerEvent := second.events["next-A"]
	var payloadRaw string
	if e = db.SQL().QueryRow(`SELECT adapter_payload FROM task_runtime WHERE task_id='next-A'`).Scan(&payloadRaw); e != nil {
		t.Fatal(e)
	}
	p, _ := adapter.DecodeInvocationPayload([]byte(payloadRaw))
	g := contract.LaunchCommand{RunID: "generation-two", TaskID: "next-A", AttemptID: "drift-attempt", SegmentID: "drift-segment", CommandID: "drift-command", ReservationID: "drift-reservation", WorkRevision: 1, PlanRevision: 7, GrantedActiveMS: 30000, AdapterPayload: json.RawMessage(payloadRaw)}
	g = candidateHostGrant(t, g, "drift-host")
	g.LaunchIntentHash, _ = contract.CandidateIntentHash(g)
	inv, e := invocationForGrant(ctx, g, filepath.Join(root, "drift", "host-spool"))
	if e != nil {
		t.Fatal(e)
	}
	h, e := host.NewIPC(filepath.Join(root, "drift", "host-spool", g.HostID), g.HostID)
	if e != nil {
		t.Fatal(e)
	}
	r, e := h.ExecuteLaunch(ctx, g, inv)
	if e == nil || r.Status != "failed" {
		t.Fatalf("HEAD drift accepted: %+v %v", r, e)
	}
	if p.CandidateWorkspace.NewBaseRework == nil || producerEvent.RunID != "generation-two" {
		t.Fatal("source generation context missing")
	}
	t.Logf("new generations: source=%s successor=%s ordered=[B,A] then [next-B,next-A], distinct six fresh event/attempt/segment identities; dirty checkout preserved; drift refused; original coordinator Wait retained", integrated.TargetHEAD, final.TargetHEAD)
	stop()
	clean = true
}

func newBaseCLISourceSnapshot(t *testing.T, db *store.DB, run string) string {
	t.Helper()
	var data []byte
	queries := []string{`SELECT * FROM runs WHERE id=?`, `SELECT * FROM tasks WHERE run_id=?`, `SELECT tr.* FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE t.run_id=?`, `SELECT a.* FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE t.run_id=?`, `SELECT s.* FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=?`, `SELECT b.* FROM budget_runtime b JOIN segments s ON s.id=b.segment_id JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=?`, `SELECT e.* FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id=?`, `SELECT d.* FROM review_decisions d JOIN tasks t ON t.id=d.task_id WHERE t.run_id=?`}
	for _, q := range queries {
		rows, e := db.SQL().Query(q, run)
		if e != nil {
			t.Fatal(e)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			v := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range v {
				dest[i] = &v[i]
			}
			if e = rows.Scan(dest...); e != nil {
				t.Fatal(e)
			}
			b, _ := json.Marshal(v)
			data = append(data, b...)
		}
		rows.Close()
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}
