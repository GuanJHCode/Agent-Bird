package coordinator

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The owner is an actual signed-RPC child, observed through the real kernel.
// Interrupted task/control capability are synthetic Store setup; the trusted
// peer context below does not itself establish public CLI peer acceptance.
func originalOwnerResumeFixture(t *testing.T) (*Server, ResumeRequest, context.Context, func()) {
	t.Helper()
	s, h := ownerRuntimeFixture(t)
	c, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	body, _ := json.Marshal(h)
	if err = ipc.Write(c, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "resume-host", Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.Read(c)
	if err != nil || frame.Kind != ipc.KindOwnerHostPending {
		t.Fatal("pending", frame.Kind, err)
	}
	token := strings.Repeat("a", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	body, _ = json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: "resume-bootstrap", BootstrapTokenSHA256: hash, StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	if err = ipc.Write(c, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "resume-prepare", Epoch: s.epoch, Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err = ipc.Read(c)
	if err != nil || frame.Kind != ipc.KindResponse {
		t.Fatal("prepare", frame.Kind, err)
	}
	var prepared contract.OwnerPrepared
	if err = contract.DecodeOwnerRuntimeStrict(frame.Payload, &prepared); err != nil {
		t.Fatal(err)
	}
	step, finish := startOwnerRPCChild(t, ownerChildConfig{Socket: prepared.RPCSocketPath, Prepared: prepared, BootstrapToken: token})
	for _, kind := range []string{"bind", "applied"} {
		if reply := step(ownerChildStep{Kind: kind, ID: "resume-" + kind}); reply.Error != "" {
			t.Fatal(kind, reply.Error)
		}
	}
	r := ResumeRequest{TaskControlRequest: TaskControlRequest{TaskID: "owner-runtime-task", ControllerThread: "controller", ControlToken: "resume-control-secret", WorkRevision: 1}, CommandID: "public-resume-command"}
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	digest := sha256.Sum256([]byte(r.ControlToken))
	if _, err = external.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='owner-runtime-run'; UPDATE tasks SET status='interrupted' WHERE id='owner-runtime-task'`, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), peerAncestryKey{}, map[int]string{h.OriginPID: h.OriginBirth})
	return s, r, ctx, finish
}

func TestOriginalOwnerResumeSocketReplayPrecedesUnrelatedMutableDispatchGate(t *testing.T) {
	// Synthetic trusted Store binding/observations isolate RPC admission order.
	// The socket peer is real and must still pass kernel role authentication.
	s, err := NewServer(ownerKeyTestState(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx)
	plan := store.PlanSpec{Run: store.RunSpec{ID: "replay-run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "synthetic-origin", OriginPID: 7, OriginBirth: "synthetic-origin-birth"}, Host: store.HostLaunchSpec{ExecutionOwnerMode: "supervised-v1", OriginContextID: "synthetic-origin", HostGeneration: "generation", Executable: "/private/fake-orchestrator"}, Tasks: []store.TaskSpec{{ID: "replay-task", RunID: "replay-run", MaxAttempts: 1}}}
	receipt, err := s.db.SubmitPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	h := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: plan.Run.OriginContextID, OriginPID: plan.Run.OriginPID, OriginBirth: plan.Run.OriginBirth, HostGeneration: plan.Host.HostGeneration, Executable: plan.Host.Executable, PID: 9, Birth: "synthetic-host"}
	if _, err = s.db.RegisterHost(ctx, h, s.epoch); err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.Repeat("a", 64)
	bootstrapHash, _ := contract.OriginalOwnerBootstrapSHA256(bootstrap)
	pin := strings.Repeat("e", 64)
	i, err := s.db.PrepareOriginalOwner(ctx, store.PrepareOriginalOwnerSpec{Host: h, Epoch: s.epoch, ExecutableSHA256: pin, BootstrapID: "socket-replay-bootstrap", BootstrapTokenSHA256: bootstrapHash, CoordinatorKeyID: s.ownerKeys.id, RequestID: "socket-replay-prepare", RequestSHA256: strings.Repeat("f", 64), StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("o", 32)))
	owner := contract.OriginalOwnerIdentity{PID: 11, Birth: "synthetic-owner", PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey))}
	auth := store.OriginalOwnerAuthentication{Owner: owner, OriginPID: plan.Run.OriginPID, OriginBirth: plan.Run.OriginBirth, ExecutableSHA256: pin}
	b, err := s.db.BindOriginalOwner(ctx, store.BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: i.Scope, BootstrapID: i.BootstrapID, BootstrapToken: bootstrap, Owner: owner, FirstHost: i.FirstHost, CoordinatorKeyID: i.CoordinatorKeyID}, Host: h, Epoch: s.epoch, Authenticated: auth}, s.ownerKeys.sign)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: b.Permit, Reconciled: contract.HostReconciled{ProducerID: i.Scope.ProducerID}}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.CommitOriginalOwnerApplied(ctx, store.OriginalOwnerAppliedSpec{Applied: applied, Host: h, Epoch: s.epoch, Authentication: auth}, s.ownerPublicKey()); err != nil {
		t.Fatal(err)
	}
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err = external.Exec(`UPDATE tasks SET status='interrupted' WHERE id='replay-task'`); err != nil {
		t.Fatal(err)
	}
	r := ResumeRequest{TaskControlRequest: TaskControlRequest{TaskID: "replay-task", ControllerThread: "controller", ControlToken: receipt.ControlToken, WorkRevision: 1}, CommandID: "immutable-socket-replay"}
	sr := store.OriginalOwnerResumeRequest{TaskID: r.TaskID, ControllerThread: r.ControllerThread, ControlToken: r.ControlToken, WorkRevision: r.WorkRevision, CommandID: r.CommandID}
	prior, err := s.db.ResumeExistingOriginalOwner(ctx, sr, store.OriginalOwnerLiveObservation{OwnerID: i.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: owner.PID, Birth: owner.Birth}, Origin: store.RunOwnerBinding{OriginContextID: h.OriginContextID, OriginPID: h.OriginPID, OriginBirth: h.OriginBirth}, Epoch: s.epoch, ExecutableSHA256: pin, OwnerAliveAt: time.Now().UTC().Format(time.RFC3339Nano)}, s.ownerPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(ResumeResponse{Version: 1, Status: "resume_queued", OriginalOwnerResumeReceipt: prior})
	call := func(r ResumeRequest) ipc.Envelope {
		t.Helper()
		body, _ := json.Marshal(r)
		response, e := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResume, RequestID: "socket-resume", Payload: body})
		if e != nil {
			t.Fatal(e)
		}
		return response
	}
	first := call(r)
	if first.Kind != ipc.KindResponse || string(first.Payload) != string(want) {
		t.Fatal("initial real socket replay failed", first.Kind, string(first.Payload))
	}
	other := plan
	other.Run.ID = "other-run"
	other.Run.OriginPID = 13
	other.Run.OriginBirth = "other-origin"
	other.Run.OriginContextID = "other-context"
	other.Host.ExecutionOwnerMode = "legacy"
	other.Host.OriginContextID = "other-context"
	other.Tasks = []store.TaskSpec{{ID: "other-task", RunID: "other-run", MaxAttempts: 1}}
	o, err := s.db.SubmitPlan(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	oh := h
	oh.LaunchID, oh.LaunchToken = o.LaunchID, o.LaunchToken
	oh.OriginContextID, oh.OriginPID, oh.OriginBirth = "other-context", 13, "other-origin"
	oh.PID, oh.Birth = 15, "other-host"
	if _, err = s.db.RegisterHost(ctx, oh, s.epoch); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ClaimReady(ctx, o.LaunchID, s.epoch, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.db.ValidateDispatchPeer(ctx, nil); err != store.CodeError("owner_registration_deferred") {
		t.Fatal("missing global unidentified fixture", err)
	}
	response := call(r)
	if response.Kind != ipc.KindResponse || string(response.Payload) != string(first.Payload) {
		t.Fatal("unrelated mutable gate blocked immutable socket replay", response.Kind, string(response.Payload))
	}
	var segments, queued int
	if err = external.QueryRow(`SELECT (SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM tasks WHERE id='replay-task' AND status='resume_queued')`).Scan(&segments, &queued); err != nil || segments != 1 || queued != 1 {
		t.Fatal("replay dispatched new execution", segments, queued, err)
	}
	wrong := r
	wrong.ControlToken = "wrong"
	response = call(wrong)
	if response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "owner_mismatch") {
		t.Fatal("wrong-token replay reached mutable gate before auth", string(response.Payload))
	}
	fresh := r
	fresh.CommandID = "new-command"
	response = call(fresh)
	if response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "owner_registration_deferred") {
		t.Fatal("fresh command bypassed existing dispatch gate", string(response.Payload))
	}
}

func TestOriginalOwnerResumeKeepsLiveOwnerAndReplaysAfterExit(t *testing.T) {
	s, request, ctx, finish := originalOwnerResumeFixture(t)
	state, err := s.db.InspectOriginalOwnerResumeTarget(ctx, request.TaskID, request.ControllerThread, request.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(request)
	response, err := s.control(ctx, ipc.KindResume, body)
	if err != nil {
		t.Fatal("live original owner could not resume", err)
	}
	var got ResumeResponse
	if err = json.Unmarshal(response, &got); err != nil || got.Mode != "existing_owner" || got.CommandID != request.CommandID || got.OwnerID != state.Owner.Intent.Scope.OwnerID {
		t.Fatal("missing immutable live response", got, err)
	}
	after, err := s.db.InspectOriginalOwnerResumeTarget(ctx, request.TaskID, request.ControllerThread, request.ControlToken)
	if err != nil || !reflect.DeepEqual(after.Owner, state.Owner) {
		t.Fatal("live resume replaced or retired owner", err)
	}
	finish() // Real original child Wait precedes the exact replay.
	again, err := s.control(context.Background(), ipc.KindResume, body)
	if err != nil || string(again) != string(response) {
		t.Fatal("exact replay re-observed dead owner or required fresh origin", err)
	}
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	newToken := "rotated-resume-control-secret"
	digest := sha256.Sum256([]byte(newToken))
	if _, err = external.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='owner-runtime-run'`, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if _, err = s.control(context.Background(), ipc.KindResume, body); err != store.CodeError("owner_mismatch") {
		t.Fatal("revoked capability replay disclosed old receipt", err)
	}
	request.ControlToken = newToken
	body, _ = json.Marshal(request)
	if _, err = s.control(context.Background(), ipc.KindResume, body); err != store.ErrConflict {
		t.Fatal("rotated body silently reused old command", err)
	}
}

func TestOriginalOwnerResumeRejectsMissingCommandAndFreshForeignPeer(t *testing.T) {
	s, request, ctx, finish := originalOwnerResumeFixture(t)
	defer finish()
	r := request
	r.CommandID = ""
	body, _ := json.Marshal(r)
	if _, err := s.control(ctx, ipc.KindResume, body); err != store.CodeError("owner_resume_command_required") {
		t.Fatal("supervised resume lacks stable command", err)
	}
	body, _ = json.Marshal(request)
	if _, err := s.control(context.Background(), ipc.KindResume, body); err != store.CodeError("owner_peer_mismatch") {
		t.Fatal("fresh foreign peer resumed original owner", err)
	}
	if _, found, err := s.db.LookupOriginalOwnerResume(ctx, store.OriginalOwnerResumeRequest{TaskID: request.TaskID, ControllerThread: request.ControllerThread, ControlToken: request.ControlToken, WorkRevision: request.WorkRevision, CommandID: request.CommandID}); err != nil || found {
		t.Fatal("rejected peer left a command receipt", err)
	}
}

func TestOriginalOwnerResumeDeadOwnerCannotRetireLivingHost(t *testing.T) {
	s, request, ctx, finish := originalOwnerResumeFixture(t)
	finish()
	body, _ := json.Marshal(request)
	if _, err := s.control(ctx, ipc.KindResume, body); err != store.CodeError("owner_resume_host_alive") {
		t.Fatal("living original Host was retired", err)
	}
	target, err := s.db.InspectOriginalOwnerResumeTarget(ctx, request.TaskID, request.ControllerThread, request.ControlToken)
	if err != nil || target.Owner.Phase != "bound" || !target.Owner.IsCurrent {
		t.Fatal("refused death decision changed current owner", err)
	}
}

func TestOriginalOwnerResumePreservesLegacyEmptyCommandBehavior(t *testing.T) {
	s, err := NewServer(ownerKeyTestState(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "legacy-resume", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "origin"}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/fake-orchestrator"}, Tasks: []store.TaskSpec{{ID: "legacy-task", RunID: "legacy-resume", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err = external.Exec(`UPDATE tasks SET status='interrupted' WHERE id='legacy-task'`); err != nil {
		t.Fatal(err)
	}
	r := ResumeRequest{TaskControlRequest: TaskControlRequest{TaskID: "legacy-task", ControllerThread: "controller", ControlToken: receipt.ControlToken, WorkRevision: 1}, CommandID: "unsupported-legacy-id"}
	body, _ := json.Marshal(r)
	if _, err = s.control(ctx, ipc.KindResume, body); err != store.CodeError("resume_command_not_supported") {
		t.Fatal("legacy pretended to promise stable idempotency", err)
	}
	r.CommandID = ""
	body, _ = json.Marshal(r)
	got, err := s.control(ctx, ipc.KindResume, body)
	if err != nil || string(got) != `{"status":"resume_queued"}` {
		t.Fatal("legacy empty-command resume changed", string(got), err)
	}
}
