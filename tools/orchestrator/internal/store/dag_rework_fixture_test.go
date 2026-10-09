package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

type dagReworkFixture struct {
	db             *DB
	host, dir      string
	controlToken   string
	seq            int64
	spec           ReworkSpec
	grants         map[string]contract.LaunchCommand
	artifacts      map[string]map[string]any
	mutateArtifact func(contract.LaunchCommand, map[string]any)
	acceptResult   func(contract.LaunchCommand) bool
}

func newDAGReworkFixture(t *testing.T, implementer, reviewer adapter.Provider, options ...func([]TaskSpec) []TaskSpec) *dagReworkFixture {
	return newConfiguredDAGReworkFixture(t, implementer, reviewer, nil, options...)
}

func newConfiguredDAGReworkFixture(t *testing.T, implementer, reviewer adapter.Provider, configure func(*dagReworkFixture), options ...func([]TaskSpec) []TaskSpec) *dagReworkFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &dagReworkFixture{db: db, dir: dir, grants: map[string]contract.LaunchCommand{}, artifacts: map[string]map[string]any{}}
	target := adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}
	var tasks []TaskSpec
	for _, id := range []string{"side", "edit", "validate", "audit", "review", "report", "integrate"} {
		task := TaskSpec{ID: id, RunID: "run", MaxAttempts: 3}
		p := adapter.InvocationPayload{Directory: "/private/work/" + id, Prompt: "original scope"}
		role := adapter.Implementer
		permission := adapter.WorkspaceWrite
		provider := implementer
		switch id {
		case "edit", "side":
			p.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1, RepoRoot: "/private/repo", BaseOID: target.BaseOID, Paths: []string{id + ".txt"}}
		case "validate", "audit":
			p.Kind = "candidate"
			task.Dependencies = []string{"edit"}
			p.CandidateAction = &adapter.CandidateAction{Version: 1, DependencyInputVersion: 1, Operation: "validate", SourceTask: "edit", Command: []string{"/usr/bin/true"}, Target: target}
		case "review", "report":
			source := "validate"
			if id == "report" {
				source = "audit"
			}
			task.Dependencies = []string{source, "side"}
			p.CandidateAction = &adapter.CandidateAction{Version: 1, DependencyInputVersion: 1, Operation: "review", SourceTask: source, Target: target}
			role = adapter.Reviewer
			permission = adapter.ReadOnly
			provider = reviewer
		case "integrate":
			p.Kind = "candidate"
			task.Dependencies = []string{"review"}
			p.CandidateAction = &adapter.CandidateAction{Version: 1, DependencyInputVersion: 1, Operation: "integrate", SourceTask: "review", Target: target}
		}
		if p.Kind == "" {
			p.Provider = string(provider)
			p.Profile = &adapter.ExecutionProfile{Version: 1, Role: role, Permission: permission, TimeoutMS: 30000}
			p.ProviderLock = &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: adapter.BinaryPin{Path: "/private/bin/" + string(provider), Version: string(provider) + "-test", SHA256: strings.Repeat("f", 64)}}
		}
		task.AdapterPayload, err = json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for _, option := range options {
		tasks = option(tasks)
	}
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	f.controlToken = plan.ControlToken
	f.host, err = db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(f)
	}
	for steps := 0; steps < len(tasks); steps++ {
		g, err := db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if g.TaskID == "integrate" {
			t.Fatal("integration before accepted review")
		}
		rejected := g.TaskID == "review"
		event := f.finish(t, g, rejected)
		if g.TaskID == "edit" {
			f.spec = ReworkSpec{TaskID: "edit", WorkRevision: 1, Candidate: ReworkEvent{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash}, CandidateOID: strings.Repeat("b", 40), Feedback: "repair branch A", Acceptance: "keep branch B intact", CommandID: "rework-closure"}
		}
		if rejected {
			f.spec.ReviewTaskID = "review"
			f.spec.ReviewWorkRevision = 1
			f.spec.Review = ReworkEvent{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash}
			f.spec.ActionSlot = event.ActionSlot
			return f
		}
	}
	t.Fatal("review did not become schedulable")
	return nil
}

// This is a Store protocol fixture: artifacts stand in for Host-owned Git
// receipts. All events, exits, accepts, captures and budgets use real APIs.
func (f *dagReworkFixture) finish(t *testing.T, g contract.LaunchCommand, reject bool) contract.Event {
	accept := true
	if f.acceptResult != nil {
		accept = f.acceptResult(g)
	}
	return f.publish(t, g, reject, accept)
}

func (f *dagReworkFixture) publish(t *testing.T, g contract.LaunchCommand, reject, accept bool) contract.Event {
	t.Helper()
	ctx := context.Background()
	p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	binding := gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
	var candidate any
	body := map[string]any{"version": 1, "binding": binding}
	if p.CandidateWorkspace != nil {
		oid := strings.Repeat("b", 40)
		if g.TaskID == "side" {
			oid = strings.Repeat("d", 40)
		}
		if g.WorkRevision > 1 {
			oid = strings.Repeat("c", 40)
		}
		if p.CandidateWorkspace.Rework != nil {
			binding.RevisionSHA256 = p.CandidateWorkspace.Rework.Digest()
		}
		candidate = gitops.CandidateReceipt{Binding: &binding, AttemptID: g.AttemptID, RepoRoot: "/private/repo", Worktree: p.Directory, WorktreeIdentity: p.Directory, CommonDir: "/private/repo/.git", BaseOID: p.CandidateWorkspace.BaseOID, CandidateOID: oid, TreeOID: strings.Repeat("e", 40), PrivateRef: "refs/private/" + g.TaskID + "/" + g.AttemptID, PrivateRefExpectedOID: oid, PlanRevision: 7, RootIdentity: gitops.DirectoryIdentity{Device: 17, Inode: 91, Mode: int64(os.ModeDir | 0700), UID: 501}}
	} else {
		source := f.artifacts[p.CandidateAction.SourceTask]
		candidate = source["candidate"]
		body["stage"] = p.CandidateAction.Operation
		body["input_event_id"] = g.Input.Event.EventID
		body["input_sha256"] = g.Input.Event.PayloadHash
		body["owner_decision_id"] = g.Input.DecisionCommandID
		encoded, _ := json.Marshal(candidate)
		var receipt gitops.CandidateReceipt
		if err = json.Unmarshal(encoded, &receipt); err != nil {
			t.Fatal(err)
		}
		target := p.CandidateAction.Target
		body["target"] = gitops.ReviewBinding{TargetWorktreeIdentity: target.Worktree, TargetDirectoryIdentity: gitops.DirectoryIdentity{Device: 17, Inode: 29, Mode: int64(os.ModeDir | 0700), UID: 501}, TargetRef: target.Ref, TargetBaseOID: target.BaseOID, FinalCandidateOID: receipt.CandidateOID, OrderedInputOIDs: receipt.OrderedInputOIDs, PlanRevision: 7}
		if p.CandidateAction.Operation == "validate" {
			body["validation"] = map[string]any{"passed": true}
			body["validation_digest"] = runtimeHash(g.TaskID + "-validation")
		} else if p.CandidateAction.Operation == "review" {
			decision := "approve"
			if reject {
				decision = "reject"
			}
			body["review"] = map[string]string{"decision": decision, "summary": "branch evidence review"}
			body["validation_digest"] = source["validation_digest"]
		}
	}
	body["candidate"] = candidate
	if f.mutateArtifact != nil {
		f.mutateArtifact(g, body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	f.seq++
	kind := contract.EventResult
	if reject {
		kind = contract.EventFailed
	}
	e := contract.Event{Version: 1, ProducerID: f.host, EventID: g.SegmentID + "-result", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: f.seq, Kind: kind, PayloadHash: runtimeHash(string(raw)), Artifact: managedHostArtifactForTest(t, f.db, g, "terminal", 0, raw)}
	if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	f.seq++
	done := e
	done.EventID = g.SegmentID + "-exit"
	done.Sequence = f.seq
	done.Kind = contract.EventExited
	zero := 0
	done.ExitCode = &zero
	done.ActiveMS = 11
	if _, err = f.db.CommitHostEvent(ctx, done); err != nil {
		t.Fatal(err)
	}
	page, err := f.db.CollectPending(ctx, g.TaskID, "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, current := range page.Events {
		if current.EventID == e.EventID {
			e = current
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing new event")
	}
	if !reject && accept {
		if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: g.TaskID, WorkRevision: g.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "accept-" + g.SegmentID}); err != nil {
			t.Fatal(err)
		}
	}
	f.grants[g.TaskID] = g
	f.artifacts[g.TaskID] = body
	return e
}
