package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitopsworker"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This fixture uses real Git evidence, the durable Store and the IPC server.
// The registered Host is a harmless child process; no provider is invoked.
type mcpBusinessFixture struct {
	root, state, route, handle string
	f                          *mcpFacade
	db                         *store.DB
	cap                        controlCapability
	ctx                        context.Context
	owner                      store.OwnerGrant
	host                       string
	hello                      contract.HostHello
	grant                      contract.LaunchCommand
}

func mcpTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mcpTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func mcpTestHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func newMCPBusinessFixture(t *testing.T, candidate bool) *mcpBusinessFixture {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "mcp-business-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	state := filepath.Join(root, "state")
	request := filepath.Join(root, "configure.json")
	if err = writeExclusiveJSON(request, mcpConfigureRequest{Version: 1, Root: filepath.Join(root, "config"), StateDir: state, Workspace: root, Providers: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err = configureMCP(request, io.Discard); err != nil {
		t.Fatal(err)
	}
	f, err := newMCPFacade(filepath.Join(root, "config", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Serve(sctx) }()
	t.Cleanup(func() {
		scancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		_ = s.Close()
	})
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateOwnerGrant(ctx, "parent", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	mcpTestGit(t, repo, "init", "-q")
	mcpTestGit(t, repo, "config", "user.name", "Fixture")
	mcpTestGit(t, repo, "config", "user.email", "fixture@example.invalid")
	if err = os.WriteFile(filepath.Join(repo, "calc.py"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mcpTestGit(t, repo, "add", "calc.py")
	mcpTestGit(t, repo, "commit", "-qm", "base")
	base := mcpTestGit(t, repo, "rev-parse", "HEAD")
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/bin/fixture-claude", Version: "2.1.267 (Claude Code)", SHA256: strings.Repeat("a", 64)}}
	payload := mcpTestJSON(t, adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: filepath.Join(root, "work"), Prompt: "original immutable brief", ProviderLock: &lock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"calc.py"}}})
	plan, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: owner.ControllerThread, PlanRevision: 1, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: owner.OriginContextID, HostGeneration: owner.HostGeneration, Executable: "/private/bin/fixture-orchestrator"}, Tasks: []store.TaskSpec{{ID: "edit", RunID: "run", WorkRevision: 1, MaxAttempts: 2, MaxActiveMS: 1000, AdapterPayload: payload, CompletionPolicy: "owner_review"}}})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sleep", "120")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	hostBirth, err := process.Birth(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: owner.HostGeneration, PID: child.Process.Pid, Birth: hostBirth, Executable: "/private/bin/fixture-orchestrator", OwnerRequestChangesV2: true, OwnerReworkRefsV3: true}
	host, err := db.RegisterHost(ctx, hello, s.Epoch())
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := writeBootstrap(state, hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), ProducerID: plan.LaunchID, Hello: hello})
	if err != nil {
		t.Fatal(err)
	}
	control, err := writeControlCapability(state, "run", owner.ControllerThread, plan.ControlToken, bootstrap, plan.LaunchID, plan.LaunchToken)
	if err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(state, "dispatch", "fixture", "handle.json")
	if err = os.MkdirAll(filepath.Dir(handle), 0700); err != nil {
		t.Fatal(err)
	}
	if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "run", StateDir: state, ControlFile: control, TaskIDs: []string{"edit"}, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	route, err := f.mintRoute(handle, owner.ControllerThread)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := loadControlCapability(control)
	if err != nil {
		t.Fatal(err)
	}
	x := &mcpBusinessFixture{root: root, state: state, route: route, handle: handle, f: f, db: db, cap: cap, ctx: ctx, owner: owner, host: host, hello: hello}
	if candidate {
		g, err := db.ClaimReady(ctx, host, s.Epoch(), 2)
		if err != nil {
			t.Fatal(err)
		}
		x.grant = g
		p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
		if err != nil {
			t.Fatal(err)
		}
		journal, err := gitopsworker.OpenJournal(filepath.Join(root, "journal"))
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		gitctx := gitops.WithCandidateJournal(ctx, journal)
		binding := gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
		key := mcpTestHash([]byte(g.RunID + "\x00" + g.TaskID + "\x00" + g.AttemptID + "\x00" + g.SegmentID))
		prepared, err := gitops.PrepareWorkspace(gitctx, gitops.MaterializeRequest{RepoRoot: repo, Worktree: p.Directory, AttemptID: g.AttemptID, BaseOID: base, CandidateOID: base, OrderedInputOIDs: []string{base}, PlanRevision: int64(g.PlanRevision)}, binding, p.CandidateWorkspace.Paths, "refs/orchestrator/g3/candidate-"+key, journal)
		if err != nil {
			t.Fatal(err)
		}
		if err = journal.RecordWorkspacePrepared(gitctx, prepared); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(p.Directory, "calc.py"), []byte("candidate\n"), 0644); err != nil {
			t.Fatal(err)
		}
		receipt, err := gitops.FreezeWorkspace(gitctx, prepared, journal)
		if err != nil {
			t.Fatal(err)
		}
		body, err := gitops.CandidateArtifact("completed", receipt)
		if err != nil {
			t.Fatal(err)
		}
		artifact := filepath.Join(root, "candidate.json")
		if err = os.WriteFile(artifact, body, 0600); err != nil {
			t.Fatal(err)
		}
		e := contract.Event{Version: 1, ProducerID: host, EventID: "candidate", RunID: "run", TaskID: "edit", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: s.Epoch(), CommandID: g.CommandID, Sequence: 1, Kind: contract.EventResult, PayloadHash: mcpTestHash(body), Artifact: &contract.ArtifactRef{ID: "candidate", Path: artifact, Size: int64(len(body)), SHA256: mcpTestHash(body)}}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		e.EventID = "exited"
		e.Sequence = 2
		e.Kind = contract.EventExited
		e.ActiveMS = 100
		zero := 0
		e.ExitCode = &zero
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

func (x *mcpBusinessFixture) review(t *testing.T) string {
	t.Helper()
	value, err := x.f.inspect(x.ctx, mcpTestJSON(t, mcpReadArgs{Handle: x.route}))
	if err != nil {
		t.Fatal(err)
	}
	refs, ok := value["review_tokens"].(map[string]string)
	if !ok || refs["edit"] == "" {
		t.Fatalf("missing verified review token: %+v", value)
	}
	return refs["edit"]
}
func (x *mcpBusinessFixture) status(t *testing.T) string {
	t.Helper()
	v, err := x.db.TaskSnapshot(x.ctx, "edit")
	if err != nil {
		t.Fatal(err)
	}
	return v.Status
}

func TestMCPBusinessACKAndAcceptRemainSeparate(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	review := x.review(t)
	page, err := x.f.collect(x.ctx, mcpTestJSON(t, mcpCollectArgs{Handle: x.route}))
	if err != nil {
		t.Fatal(err)
	}
	var collection struct {
		DeliveryID string           `json:"delivery_id"`
		Proof      string           `json:"collection_proof_sha256"`
		Events     []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(mcpTestJSON(t, page), &collection); err != nil || len(collection.Events) != 1 || len(collection.Proof) != 64 {
		t.Fatalf("collection %+v %v", collection, err)
	}
	e := collection.Events[0]
	ack := mcpAckArgs{Handle: x.route, DeliveryID: collection.DeliveryID, CollectionProofSHA256: collection.Proof, Decisions: []store.AckDecision{{EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "handled", CommandID: "ack"}}}
	for i := 0; i < 2; i++ {
		if _, err = x.f.ack(x.ctx, mcpTestJSON(t, ack)); err != nil {
			t.Fatal(err)
		}
	}
	if got := x.status(t); got != "result_ready" {
		t.Fatalf("ACK accepted candidate: %s", got)
	}
	if got := x.review(t); got != review {
		t.Fatal("ACK changed immutable review binding")
	}
	for i := 0; i < 2; i++ {
		if _, err = x.f.accept(x.ctx, mcpTestJSON(t, mcpAcceptArgs{Handle: x.route, ReviewToken: review, CommandID: "accept"})); err != nil {
			t.Fatal(err)
		}
	}
	if got := x.status(t); got != "completed" {
		t.Fatalf("accept: %s", got)
	}
	var count int
	if err = x.db.SQL().QueryRow(`SELECT COUNT(*) FROM review_decisions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate acceptance count=%d %v", count, err)
	}
}

func TestMCPBusinessCrossEntryRequestChangesReplay(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			x := newMCPBusinessFixture(t, true)
			token := x.review(t)
			brief := ownerChangeBrief{Version: version, TaskID: "edit", WorkRevision: 1, Feedback: "handle negatives", Acceptance: "negative case passes", CommandID: "same-command"}
			var r mcpRoute
			var review mcpReview
			if err := x.f.unseal(x.route, "route", &r); err != nil {
				t.Fatal(err)
			}
			review, err := x.f.resolveReview(token, r)
			if err != nil {
				t.Fatal(err)
			}
			brief.CandidateOID = review.Binding.CandidateOID
			path := filepath.Join(x.root, "old-cli.json")
			if err = writeExclusiveJSON(path, brief); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err = taskRequestChanges(x.ctx, []string{"--handle", x.handle, "--request", path}, &out); err != nil {
				t.Fatalf("old CLI first commit: %s %v", out.String(), err)
			}
			request := mcpChangesArgs{Handle: x.route, ReviewToken: token, CommandID: brief.CommandID, Feedback: brief.Feedback, Acceptance: brief.Acceptance, Version: &version}
			fresh, err := newMCPFacade(x.f.path)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				v, err := fresh.requestChanges(x.ctx, mcpTestJSON(t, request))
				if err != nil {
					t.Fatalf("MCP original receipt replay: %+v %v", v, err)
				}
			}
			request.Feedback = "different body"
			if _, err = fresh.requestChanges(x.ctx, mcpTestJSON(t, request)); err == nil {
				t.Fatal("changed body reused command receipt")
			}
			var revision, count int
			if err = x.db.SQL().QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='edit'`).Scan(&revision); err != nil || revision != 2 {
				t.Fatalf("revision=%d %v", revision, err)
			}
			if err = x.db.SQL().QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("decisions=%d %v", count, err)
			}
		})
	}
}

func TestMCPBusinessMCPFirstLostReplyReplaysThroughWrapper(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			x := newMCPBusinessFixture(t, true)
			token := x.review(t)
			request := mcpChangesArgs{Handle: x.route, ReviewToken: token, CommandID: "mcp-first", Feedback: "handle negatives", Acceptance: "negative case passes", Version: &version}
			_, err := x.f.requestChanges(x.ctx, mcpTestJSON(t, request))
			// The business call committed; its tool transport reply is then lost.
			if err == nil {
				err = codeError("request_changes_uncertain")
			}
			if errorCode(err) != "request_changes_uncertain" {
				t.Fatalf("lost reply not preserved: %v", err)
			}
			var route mcpRoute
			if err = x.f.unseal(x.route, "route", &route); err != nil {
				t.Fatal(err)
			}
			review, err := x.f.resolveReview(token, route)
			if err != nil {
				t.Fatal(err)
			}
			brief := ownerChangeBrief{Version: version, TaskID: "edit", WorkRevision: 1, CandidateOID: review.Binding.CandidateOID, CommandID: request.CommandID, Feedback: request.Feedback, Acceptance: request.Acceptance}
			path := filepath.Join(x.root, "wrapper-replay.json")
			if err = writeExclusiveJSON(path, brief); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				var out bytes.Buffer
				if err = taskRequestChanges(x.ctx, []string{"--handle", x.handle, "--request", path}, &out); err != nil {
					t.Fatalf("wrapper original receipt unavailable: %s %v", out.String(), err)
				}
			}
			if _, err = x.f.requestChanges(x.ctx, mcpTestJSON(t, request)); err != nil {
				t.Fatalf("MCP original receipt unavailable: %v", err)
			}
			brief.Feedback = "changed original command"
			if err = os.WriteFile(path, mcpTestJSON(t, brief), 0600); err != nil {
				t.Fatal(err)
			}
			if err = taskRequestChanges(x.ctx, []string{"--handle", x.handle, "--request", path}, io.Discard); errorCode(err) != "request_changes_command_conflict" {
				t.Fatalf("wrapper changed command: %v", err)
			}
			request.Feedback = brief.Feedback
			if _, err = x.f.requestChanges(x.ctx, mcpTestJSON(t, request)); errorCode(err) != "request_changes_command_conflict" {
				t.Fatalf("MCP changed command: %v", err)
			}
			var revision, count int
			if err = x.db.SQL().QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='edit'`).Scan(&revision); err != nil || revision != 2 {
				t.Fatalf("revision=%d %v", revision, err)
			}
			if err = x.db.SQL().QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("duplicate decisions=%d %v", count, err)
			}
		})
	}
}

func TestMCPBusinessRetiredOwnerCanObserveAndACKButCannotDecide(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	token := x.review(t)
	if _, err := x.db.SQL().Exec(`UPDATE runs SET origin_pid=99999999,origin_birth='retired' WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.f.inspect(x.ctx, mcpTestJSON(t, mcpReadArgs{Handle: x.route})); err != nil {
		t.Fatalf("retired owner inspect: %v", err)
	}
	page, err := x.f.collect(x.ctx, mcpTestJSON(t, mcpCollectArgs{Handle: x.route}))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		DeliveryID string           `json:"delivery_id"`
		Proof      string           `json:"collection_proof_sha256"`
		Events     []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(mcpTestJSON(t, page), &c); err != nil || len(c.Events) != 1 {
		t.Fatalf("collect %+v %v", c, err)
	}
	e := c.Events[0]
	if _, err = x.f.ack(x.ctx, mcpTestJSON(t, mcpAckArgs{Handle: x.route, DeliveryID: c.DeliveryID, CollectionProofSHA256: c.Proof, Decisions: []store.AckDecision{{EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "handled", CommandID: "retired-ack"}}})); err != nil {
		t.Fatalf("retired owner ACK: %v", err)
	}
	if _, err = x.f.accept(x.ctx, mcpTestJSON(t, mcpAcceptArgs{Handle: x.route, ReviewToken: token, CommandID: "retired-accept"})); errorCode(err) != "origin_owner_lost" {
		t.Fatalf("retired acceptance: %v", err)
	}
	if _, err = x.f.requestChanges(x.ctx, mcpTestJSON(t, mcpChangesArgs{Handle: x.route, ReviewToken: token, CommandID: "retired-rework", Feedback: "change", Acceptance: "passes"})); errorCode(err) != "origin_owner_lost" {
		t.Fatalf("retired rework: %v", err)
	}
	if got := x.status(t); got != "result_ready" {
		t.Fatalf("rejected owner changed state: %s", got)
	}
}

func TestMCPBusinessWaitCancellationDoesNotStopOrACK(t *testing.T) {
	x := newMCPBusinessFixture(t, false)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := x.f.server().Connect(x.ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	cs, err := client.Connect(x.ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx, cancel := context.WithTimeout(x.ctx, 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait", Arguments: mcpWaitArgs{Handle: x.route}})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("wait cancellation: %v duration=%s", err, time.Since(start))
	}
	if got := x.status(t); got != "ready" {
		t.Fatalf("cancellation changed execution: %s", got)
	}
	var decisions, attempts int
	if err = x.db.SQL().QueryRow(`SELECT COUNT(*) FROM delivery_receipts`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err = x.db.SQL().QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 || attempts != 0 {
		t.Fatalf("cancelled wait side effects ACK=%d attempts=%d", decisions, attempts)
	}
	result, err := cs.CallTool(x.ctx, &mcp.CallToolParams{Name: "inspect", Arguments: mcpReadArgs{Handle: x.route}})
	if err != nil || result.IsError {
		t.Fatalf("session remains usable after cancel: %+v %v", result, err)
	}
}

func TestMCPBusinessRebindRechecksCurrentPeerWithOriginalTokens(t *testing.T) {
	x := newMCPBusinessFixture(t, true)
	token := x.review(t)
	sibling := exec.Command("/bin/sleep", "120")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sibling.Process.Kill(); _ = sibling.Wait() })
	birth, err := process.Birth(sibling.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	spec := store.RebindOwnerSpec{RunID: "run", OriginContextID: x.owner.OriginContextID, OriginPID: sibling.Process.Pid, OriginBirth: birth, HostGeneration: "new-generation", AttachmentProofSHA256: strings.Repeat("b", 64)}
	if err = x.db.RebindOwner(x.ctx, spec); err != nil {
		t.Fatal(err)
	}
	fresh, err := newMCPFacade(x.f.path)
	if err != nil {
		t.Fatal(err)
	}
	request := mcpTestJSON(t, mcpAcceptArgs{Handle: x.route, ReviewToken: token, CommandID: "rebound-accept"})
	if _, err = fresh.accept(x.ctx, request); errorCode(err) != "owner_peer_mismatch" {
		t.Fatalf("previous process retained mutation authority: %v", err)
	}
	if got := x.status(t); got != "result_ready" {
		t.Fatalf("rejected peer changed task: %s", got)
	}
	spec.OriginPID = x.owner.OriginPID
	spec.OriginBirth = x.owner.OriginBirth
	spec.HostGeneration = "next-generation"
	spec.AttachmentProofSHA256 = strings.Repeat("c", 64)
	if err = x.db.RebindOwner(x.ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.accept(x.ctx, request); err != nil {
		t.Fatalf("logical route did not survive authorized rebind: %v", err)
	}
	if got := x.status(t); got != "completed" {
		t.Fatalf("rebound acceptance status=%s", got)
	}
}

func TestMCPBusinessStdioEOFOnlyCancelsWait(t *testing.T) {
	x := newMCPBusinessFixture(t, false)
	started := make(chan struct{})
	x.f.invoke = func(ctx context.Context, args []string, out io.Writer) error {
		if len(args) > 0 && args[0] == "wait" {
			close(started)
		}
		return taskEntry(ctx, args, out)
	}
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	defer inReader.Close()
	defer outReader.Close()
	defer outWriter.Close()
	done := make(chan error, 1)
	go func() {
		done <- x.f.server().Run(x.ctx, &mcp.IOTransport{Reader: inReader, Writer: mcpWriteCloser{outWriter}, MaxLineLength: mcpFrameMaxBytes})
	}()
	write := func(value any) {
		t.Helper()
		raw := append(mcpTestJSON(t, value), '\n')
		if _, err := inWriter.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}}})
	reader := bufio.NewReader(outReader)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "wait", "arguments": mcpWaitArgs{Handle: x.route}}})
	select {
	case <-started:
	case <-x.ctx.Done():
		t.Fatal("wait handler did not start")
	}
	if err := inWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stdio EOF did not cancel outstanding wait")
	}
	if got := x.status(t); got != "ready" {
		t.Fatalf("EOF changed execution: %s", got)
	}
	var count int
	if err := x.db.SQL().QueryRow(`SELECT COUNT(*) FROM delivery_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("EOF ACK count=%d %v", count, err)
	}
}

func TestMCPBusinessStopReportsObservedConfirmation(t *testing.T) {
	x := newMCPBusinessFixture(t, false)
	value, err := x.f.stop(x.ctx, mcpTestJSON(t, mcpStopArgs{Handle: x.route, WorkRevision: 2}))
	if err == nil || value["requested"] != false || value["confirmed"] != false {
		t.Fatalf("stale stop %+v %v", value, err)
	}
	if _, err = x.db.SQL().Exec(`UPDATE runs SET origin_pid=99999999,origin_birth='retired' WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	value, err = x.f.stop(x.ctx, mcpTestJSON(t, mcpStopArgs{Handle: x.route, WorkRevision: 1}))
	if err != nil && errorCode(err) != "host_unavailable" {
		t.Fatalf("cleanup stop %+v %v", value, err)
	}
	if value["requested"] != true || value["confirmed"] != true {
		t.Fatalf("queued stop was not durably observed: %+v %v", value, err)
	}
	if got := x.status(t); got != "cancelled" {
		t.Fatalf("stop status=%s", got)
	}
}
