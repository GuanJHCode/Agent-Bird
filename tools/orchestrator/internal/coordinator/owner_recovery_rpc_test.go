package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The historical owner is trusted Store fixture data; the successor is a real
// signing child. Its handoff traverses the actual private socket, DB and files.
type recoveryRPCFixture struct {
	hello  contract.HostHello
	server *Server
	step   func(ownerChildStep) ownerChildReply
	finish func()
	bound  contract.OwnerBindReply
	path   string
	prior  []byte
}

func newRecoveryRPCFixture(t *testing.T) recoveryRPCFixture {
	return newRecoveryRPCFixtureMode(t, false)
}
func newRecoveryRPCFixtureMode(t *testing.T, initial bool) recoveryRPCFixture {
	t.Helper()
	var s *Server
	var h contract.HostHello
	if initial {
		s, h = ownerRuntimeFixture(t)
	} else {
		s, h = successorTransportFixture(t)
	}
	c, frame := successorTransportConnect(t, s, h)
	if frame.Kind != ipc.KindOwnerHostPending {
		t.Fatal(frame.Kind)
	}
	token := strings.Repeat("e", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	body, _ := json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: "recovery-bootstrap", BootstrapTokenSHA256: hash, StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	if err := ipc.Write(c, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "recovery-prepare", Epoch: s.epoch, Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.Read(c)
	if err != nil || frame.Kind != ipc.KindResponse {
		t.Fatal(frame.Kind, err, string(frame.Payload))
	}
	var prepared contract.OwnerPrepared
	if err = contract.DecodeOwnerRuntimeStrict(frame.Payload, &prepared); err != nil {
		t.Fatal(err)
	}
	step, finish := startOwnerRPCChild(t, ownerChildConfig{Socket: s.OwnerSocketPath(), Prepared: prepared, BootstrapToken: token})
	reply := step(ownerChildStep{Kind: "bind", ID: prepared.Intent.RequestID})
	if reply.Error != "" {
		t.Fatal(reply.Error)
	}
	var bound contract.OwnerBindReply
	if err = contract.DecodeOwnerRuntimeStrict(reply.Body, &bound); err != nil {
		t.Fatal(err)
	}
	if initial {
		return recoveryRPCFixture{hello: h, server: s, step: step, finish: finish, bound: bound, path: filepath.Join(s.stateDir, "host-spool", h.LaunchID, ".coordinator-recovery.json")}
	}
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	var priorID string
	if err = external.QueryRow(`SELECT predecessor_owner_id FROM owner_succession_authorizations WHERE command_id=?`, h.ResumeCommandID).Scan(&priorID); err != nil {
		t.Fatal(err)
	}
	prior, err := s.db.InspectOriginalOwner(context.Background(), priorID)
	if err != nil {
		t.Fatal(err)
	}
	path, raw := recoveryRPCPriorLedgerFixture(t, s.stateDir, *prior.Binding)
	return recoveryRPCFixture{hello: h, server: s, step: step, finish: finish, bound: bound, path: path, prior: raw}
}

// This writes trusted historical fixture state, not a real prior owner runtime.
func recoveryRPCPriorLedgerFixture(t *testing.T, stateDir string, prior contract.OriginalOwnerBinding) (string, []byte) {
	t.Helper()
	dir := filepath.Join(stateDir, "host-spool", prior.Scope.LaunchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(stateDir, "host-spool"), 0700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, ".coordinator-recovery.lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var d, l syscall.Stat_t
	if err := syscall.Stat(dir, &d); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(lock, &l); err != nil {
		t.Fatal(err)
	}
	a := sha256.Sum256([]byte(fmt.Sprintf("original-owner-recovery-authority-v2\x00%d:%d:%d:%d", d.Dev, d.Ino, l.Dev, l.Ino)))
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(prior.Scope, prior.Owner)
	// Field order is deliberately the frozen on-disk v2 canonical layout.
	record := struct {
		Version     int    `json:"version"`
		Authority   string `json:"authority_sha256"`
		Scope       string `json:"scope_sha256"`
		Attempts    int    `json:"attempts"`
		Next        int64  `json:"next_attempt_unix_ms"`
		Unavailable bool   `json:"unavailable"`
	}{2, hex.EncodeToString(a[:]), scope, 2, 123, false}
	raw, _ := json.Marshal(record)
	raw = append(raw, '\n')
	path := filepath.Join(dir, ".coordinator-recovery.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := host.OpenExistingOwnerRecoveryLease(stateDir, prior.Scope, prior.Owner)
	if err != nil {
		t.Fatal("fixture is not a valid existing recovery ledger", err)
	}
	lease.Close()
	return path, raw
}
func (f recoveryRPCFixture) handoff(t *testing.T) ownerChildReply {
	t.Helper()
	id, err := contract.OwnerRecoveryScopeRequestID(f.bound.Binding.Scope, f.bound.Binding.Owner)
	if err != nil {
		t.Fatal(err)
	}
	return f.step(ownerChildStep{Kind: "recovery_scope", ID: id, Body: json.RawMessage(`{"version":1}`)})
}
func (f recoveryRPCFixture) rawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.server.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func recoveryRPCRead(t *testing.T, path string) ([]byte, os.FileInfo) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw, info
}

func TestOwnerRecoveryRPCNativeCommitsScopeAndReplaysLostACK(t *testing.T) {
	f := newRecoveryRPCFixture(t)
	db := f.rawDB(t)
	if got := f.step(ownerChildStep{Kind: "applied", ID: "before-handoff"}); got.Error == "" {
		t.Fatal("owner applied before FS handoff")
	}
	var dropped atomic.Bool
	f.server.ownerRPCReplyHook = func(r contract.OwnerRPCRequest) bool {
		return r.Kind == "recovery_scope" && dropped.CompareAndSwap(false, true)
	}
	if got := f.handoff(t); got.Error == "" || !dropped.Load() {
		t.Fatal("actual applied reply was not discarded", got.Error)
	}
	after, info := recoveryRPCRead(t, f.path)
	var prior, next map[string]any
	if json.Unmarshal(f.prior, &prior) != nil || json.Unmarshal(after, &next) != nil {
		t.Fatal("bad ledger")
	}
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(f.bound.Binding.Scope, f.bound.Binding.Owner)
	if next["scope_sha256"] != scope || next["attempts"] != float64(2) {
		t.Fatal("wrong current scope/budget", next)
	}
	prior["scope_sha256"] = next["scope_sha256"]
	a, _ := json.Marshal(prior)
	b, _ := json.Marshal(next)
	if !bytes.Equal(a, b) {
		t.Fatal("handoff changed non-scope budget")
	}
	var applied, count int
	if err := db.QueryRow(`SELECT COUNT(*),MAX(applied) FROM owner_recovery_scope_handoffs`).Scan(&count, &applied); err != nil || count != 1 || applied != 1 {
		t.Fatal("reply loss preceded durable apply", count, applied, err)
	}
	got := f.handoff(t)
	var reply contract.OwnerRecoveryScopeReply
	if got.Error != "" || contract.DecodeOwnerRuntimeStrict(got.Body, &reply) != nil || reply.Version != 1 || reply.Status != "recovery_scope_applied" {
		t.Fatal("same owner could not replay", got.Error, string(got.Body))
	}
	again, againInfo := recoveryRPCRead(t, f.path)
	if !bytes.Equal(after, again) || !os.SameFile(info, againInfo) || !info.ModTime().Equal(againInfo.ModTime()) {
		t.Fatal("ACK replay rewrote ledger")
	}
	if got = f.step(ownerChildStep{Kind: "applied", ID: "after-handoff"}); got.Error != "" {
		t.Fatal("proved successor could not apply", got.Error)
	}
	f.finish()
}

func TestOwnerRecoveryRPCNativeDatabaseFailurePreservesPriorOrPreparedNext(t *testing.T) {
	for _, stage := range []string{"prepare", "apply"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryRPCFixture(t)
			db := f.rawDB(t)
			op := "BEFORE INSERT"
			if stage == "apply" {
				op = "BEFORE UPDATE OF applied"
			}
			if _, err := db.Exec(`CREATE TRIGGER handoff_rpc_failure ` + op + ` ON owner_recovery_scope_handoffs BEGIN SELECT RAISE(ABORT,'handoff_fixture_failure'); END`); err != nil {
				t.Fatal(err)
			}
			before, info := recoveryRPCRead(t, f.path)
			if got := f.handoff(t); got.Error == "" {
				t.Fatal("database fault ignored")
			}
			current, currentInfo := recoveryRPCRead(t, f.path)
			var count, applied int
			if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(applied),0) FROM owner_recovery_scope_handoffs`).Scan(&count, &applied); err != nil {
				t.Fatal(err)
			}
			if stage == "prepare" {
				if count != 0 || !bytes.Equal(before, current) || !os.SameFile(info, currentInfo) || !info.ModTime().Equal(currentInfo.ModTime()) {
					t.Fatal("failed intent changed file")
				}
			} else {
				if count != 1 || applied != 0 || bytes.Equal(before, current) {
					t.Fatal("apply fault did not preserve prepared next")
				}
			}
			if _, err := db.Exec(`DROP TRIGGER handoff_rpc_failure`); err != nil {
				t.Fatal(err)
			}
			if got := f.handoff(t); got.Error != "" {
				t.Fatal("exact retry failed", got.Error)
			}
			if stage == "apply" {
				after, afterInfo := recoveryRPCRead(t, f.path)
				if !bytes.Equal(current, after) || !os.SameFile(currentInfo, afterInfo) || !currentInfo.ModTime().Equal(afterInfo.ModTime()) {
					t.Fatal("prepared next was rewritten")
				}
			}
			f.finish()
		})
	}
}

func TestOwnerRecoveryRPCNativeRejectsCallerAuthorityWithoutWrites(t *testing.T) {
	f := newRecoveryRPCFixture(t)
	db := f.rawDB(t)
	id, _ := contract.OwnerRecoveryScopeRequestID(f.bound.Binding.Scope, f.bound.Binding.Owner)
	before, info := recoveryRPCRead(t, f.path)
	for _, body := range []string{`null`, `{}`, `{"version":0}`, `{"version":null}`, `{"version":1,"version":1}`, `{"version":1,"path":"/tmp/foreign"}`, `{"version":1,"authorization_id":"foreign"}`, `{"version":1,"prior_scope_sha256":"foreign"}`, `{"version":1,"owner_id":"foreign"}`} {
		t.Run(body, func(t *testing.T) {
			if got := f.step(ownerChildStep{Kind: "recovery_scope", ID: id, Body: json.RawMessage(body)}); got.Error == "" {
				t.Fatal("caller authority/body accepted")
			}
		})
	}
	for _, change := range []string{"request_id", "zero_revision", "later_revision"} {
		t.Run(change, func(t *testing.T) {
			request := ownerChildStep{Kind: "recovery_scope", ID: id, Body: json.RawMessage(`{"version":1}`)}
			if change == "request_id" {
				request.ID += "-foreign"
			} else {
				revision := uint64(0)
				if change == "later_revision" {
					revision = 2
				}
				request.Revision = &revision
			}
			if got := f.step(request); got.Error == "" {
				t.Fatal("unbound ID/revision accepted")
			}
		})
	}
	after, afterInfo := recoveryRPCRead(t, f.path)
	if !bytes.Equal(before, after) || !os.SameFile(info, afterInfo) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("rejected request changed file")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected request wrote intent", count, err)
	}
	if got := f.handoff(t); got.Error != "" {
		t.Fatal("rejections poisoned valid same owner", got.Error)
	}
	f.finish()
}

func TestOwnerRecoveryRPCNativeRejectsUnsafeOrRolledBackLedger(t *testing.T) {
	for _, scenario := range []string{"missing", "busy", "applied_rollback"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRecoveryRPCFixture(t)
			db := f.rawDB(t)
			var release func()
			switch scenario {
			case "missing":
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			case "busy":
				file, err := os.OpenFile(filepath.Join(filepath.Dir(f.path), ".coordinator-recovery.lock"), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				release = func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }
				defer release()
			case "applied_rollback":
				if got := f.handoff(t); got.Error != "" {
					t.Fatal(got.Error)
				}
				if err := os.WriteFile(f.path, f.prior, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(f.path)
			if got := f.handoff(t); got.Error == "" {
				t.Fatal("unsafe ledger accepted", scenario)
			}
			after, err := os.ReadFile(f.path)
			if scenario == "missing" {
				if !os.IsNotExist(err) {
					t.Fatal("missing ledger repaired", err)
				}
			} else if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejection repaired/changed ledger", err)
			}
			var count int
			if err = db.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "applied_rollback" {
				want = 1
			}
			if count != want {
				t.Fatal("unexpected intent mutation", count)
			}
			f.finish()
		})
	}
}

func TestOwnerRecoveryRPCNativeRejectsInitialOrAbsentPhysicalSession(t *testing.T) {
	for _, scenario := range []string{"initial", "closed_session", "physical_drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRecoveryRPCFixtureMode(t, scenario == "initial")
			db := f.rawDB(t)
			before, _ := os.ReadFile(f.path)
			if scenario == "closed_session" {
				f.server.mu.Lock()
				session := f.server.ownerTransports[f.hello.LaunchID]
				f.server.mu.Unlock()
				if session == nil {
					t.Fatal("missing fixture session")
				}
				session.conn.Close()
				waitSuccessorTransportState(t, f.server, f.hello.LaunchID, false, false)
			}
			if scenario == "physical_drift" {
				if _, err := db.Exec(`UPDATE runtime_hosts SET pid=pid+1 WHERE id=?`, f.hello.LaunchID); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.handoff(t); got.Error == "" {
				t.Fatal("unbound physical session admitted", scenario)
			}
			after, err := os.ReadFile(f.path)
			if scenario == "initial" {
				if !os.IsNotExist(err) {
					t.Fatal("initial handoff created ledger", err)
				}
			} else if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected physical session changed ledger", err)
			}
			var count int
			if err = db.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs`).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejection wrote intent", count, err)
			}
			f.finish()
		})
	}
}

func TestOwnerRecoveryRPCNativeOldBindPermitSurvivesCoordinatorEpoch(t *testing.T) {
	for _, stage := range []string{"before_prepare", "prepared_next"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryRPCFixture(t)
			db := f.rawDB(t)
			if stage == "prepared_next" {
				if _, err := db.Exec(`CREATE TRIGGER handoff_epoch_apply_failure BEFORE UPDATE OF applied ON owner_recovery_scope_handoffs BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
				if got := f.handoff(t); got.Error == "" {
					t.Fatal("prepared next fixture did not interrupt apply")
				}
				var count, applied int
				if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(applied),0) FROM owner_recovery_scope_handoffs`).Scan(&count, &applied); err != nil || count != 1 || applied != 0 {
					t.Fatal("fixture did not reach prepared handoff", count, applied, err)
				}
				raw, _ := recoveryRPCRead(t, f.path)
				var prior, current map[string]any
				if json.Unmarshal(f.prior, &prior) != nil || json.Unmarshal(raw, &current) != nil {
					t.Fatal("invalid prepared ledger")
				}
				scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(f.bound.Binding.Scope, f.bound.Binding.Owner)
				if current["scope_sha256"] != scope {
					t.Fatal("prepared ledger did not reach successor scope")
				}
				prior["scope_sha256"] = scope
				want, _ := json.Marshal(prior)
				got, _ := json.Marshal(current)
				if !bytes.Equal(want, got) {
					t.Fatal("prepared handoff changed non-scope ledger fields")
				}
				if _, err := db.Exec(`DROP TRIGGER handoff_epoch_apply_failure`); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "before_prepare" {
				var count int
				raw, _ := recoveryRPCRead(t, f.path)
				if err := db.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs`).Scan(&count); err != nil || count != 0 || !bytes.Equal(raw, f.prior) {
					t.Fatal("fixture no longer has untouched prior ledger", count, err)
				}
			}
			oldEpoch := f.server.epoch
			if err := f.server.Close(); err != nil {
				t.Fatal(err)
			}
			next, err := NewServer(f.server.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer next.Close()
			go func() { _ = next.Serve(ctx) }()
			f.server = next
			_, frame := successorTransportConnect(t, next, f.hello)
			if frame.Kind != ipc.KindOwnerHostPending || next.epoch <= oldEpoch {
				t.Fatal("same physical host did not reconnect in new epoch", frame.Kind, next.epoch)
			}
			state, err := next.db.InspectOriginalOwner(ctx, f.bound.Binding.Scope.OwnerID)
			if err != nil || state.ControlRevision != 1 || state.CoordinatorEpoch != oldEpoch {
				t.Fatal("fixture rewrote old permit", state, err)
			}
			if got := f.handoff(t); got.Error != "" {
				t.Fatal("old Bind/current RPC epoch handoff failed", got.Error)
			}
			attach := contract.OwnerAttachRequest{ExpectedControlRevision: 1, OldHost: f.bound.Binding.FirstHost, NewHost: f.bound.Binding.FirstHost}
			raw, _ := json.Marshal(attach)
			if got := f.step(ownerChildStep{Kind: "attach", ID: "startup-epoch-renew", Body: raw}); got.Error != "" {
				t.Fatal("same tuple renewal after handoff failed", got.Error)
			}
			if got := f.step(ownerChildStep{Kind: "applied", ID: "startup-epoch-applied"}); got.Error != "" {
				t.Fatal(got.Error)
			}
			state, err = next.db.InspectOriginalOwner(ctx, f.bound.Binding.Scope.OwnerID)
			if err != nil || state.ControlRevision != 2 || state.AppliedControlRevision != 2 || state.CoordinatorEpoch != next.epoch || *state.CurrentHost != f.bound.Binding.FirstHost {
				t.Fatal("renewed owner/host mismatch", state, err)
			}
			f.finish()
		})
	}
}

// The transport-ticket test deliberately fixtures FirstBind in Store. Complete
// that same fixture's new filesystem/Store handoff precondition explicitly;
// this helper is not evidence of signed recovery RPC or original-owner startup.
func completeRecoveryHandoffTransportFixture(t *testing.T, s *Server, spec store.OwnerRecoveryHandoffSpec) {
	t.Helper()
	ctx := context.Background()
	resolved, err := s.db.ResolveOriginalOwnerRecoveryHandoff(ctx, spec, s.ownerPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	recoveryRPCPriorLedgerFixture(t, s.stateDir, resolved.PriorBinding)
	lease, err := host.OpenExistingOwnerRecoveryHandoff(s.stateDir, resolved.PriorBinding.Scope, resolved.PriorBinding.Owner, resolved.Binding.Scope, resolved.Binding.Owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	facts, err := lease.Facts()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.db.PrepareOriginalOwnerRecoveryHandoff(ctx, spec, ownerRecoveryStoreFacts(facts), s.ownerPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.CommitPrepared(ownerRecoveryFileProof(proof)); err != nil {
		t.Fatal(err)
	}
	proof, err = s.db.ApplyOriginalOwnerRecoveryHandoff(ctx, spec, ownerRecoveryStoreFacts(facts), s.ownerPublicKey())
	if err != nil || !proof.Applied {
		t.Fatal("fixture handoff not applied", err)
	}
	if err = lease.CommitPrepared(ownerRecoveryFileProof(proof)); err != nil {
		t.Fatal(err)
	}
}
