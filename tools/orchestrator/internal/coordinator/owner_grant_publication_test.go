package coordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Actual owner_bind IPC, with no Provider/Host child. The unidentified segment
// below is a Store gate fixture, not a claim of a live or reaped worker.
func TestOwnerGrantPublicationFailureDoesNotAuthorize(t *testing.T) {
	for _, failure := range []string{"file_publication", "deferred_commit"} {
		t.Run(failure, func(t *testing.T) {
			root, err := os.MkdirTemp("/private/tmp", "bind-pub-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			s, err := NewServer(filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- s.Serve(ctx) }()
			t.Cleanup(func() {
				cancel()
				s.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("Serve did not join")
				}
			})
			birth, err := process.Birth(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			request := OwnerBindRequest{ControllerThread: "publication-master", OriginPID: os.Getpid(), OriginBirth: birth}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			owners := filepath.Join(s.stateDir, "owners")
			if err = os.Mkdir(owners, 0700); err != nil {
				t.Fatal(err)
			}
			external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer external.Close()
			count := func(db *sql.DB, table string) int {
				t.Helper()
				var n int
				if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if failure == "file_publication" {
				if err = os.Chmod(owners, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(owners, 0700) })
			} else {
				for _, stmt := range []string{
					`CREATE TABLE owner_bind_fault_parent(id TEXT PRIMARY KEY)`,
					`CREATE TABLE owner_bind_fault_child(id TEXT REFERENCES owner_bind_fault_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
					`CREATE TRIGGER owner_bind_commit_fault AFTER INSERT ON owner_grants BEGIN INSERT INTO owner_bind_fault_child(id) VALUES(NEW.id); END`,
				} {
					if _, err = s.db.SQL().Exec(stmt); err != nil {
						t.Fatal(err)
					}
				}
			}
			connectionID := func() string {
				t.Helper()
				c, e := s.db.SQL().Conn(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer c.Close()
				var id string
				if e = c.Raw(func(v any) error { id = fmt.Sprintf("%p", v); return nil }); e != nil {
					t.Fatal(e)
				}
				return id
			}
			beforeConnection := connectionID()
			response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindOwnerBind, RequestID: "fault", Payload: raw})
			if err != nil || response.Kind != ipc.KindError {
				t.Fatalf("bind did not reject fault: kind=%s err=%v", response.Kind, err)
			}
			for _, db := range []*sql.DB{s.db.SQL(), external} {
				if n := count(db, "owner_grants"); n != 0 {
					t.Errorf("failed bind persisted %d authorized grants", n)
				}
			}
			entries, err := os.ReadDir(owners)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "file_publication" {
				if len(entries) != 0 {
					t.Error("failed file publication produced capability")
				}
				if err = os.Chmod(owners, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if len(entries) != 1 {
					t.Errorf("failed COMMIT must retain exact published but unauthorized file, got %d", len(entries))
				}
				if connectionID() == beforeConnection {
					t.Error("failed COMMIT reused original physical connection")
				}
				if count(s.db.SQL(), "owner_bind_fault_child") != 0 || count(external, "owner_bind_fault_child") != 0 {
					t.Error("failed COMMIT leaked transaction")
				}
				for _, entry := range entries {
					var grant store.OwnerGrant
					body, e := os.ReadFile(filepath.Join(owners, entry.Name()))
					if e != nil || json.Unmarshal(body, &grant) != nil {
						t.Fatal("invalid retained file")
					}
					if s.db.ValidateOwnerGrant(ctx, grant) == nil {
						t.Error("failed COMMIT file accepted as grant")
					}
				}
				if _, err = s.db.SQL().Exec(`DROP TRIGGER owner_bind_commit_fault`); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "gate-run", ControllerThread: "other", PlanRevision: 1, OriginContextID: "other-origin", OriginPID: 7, OriginBirth: "other-birth"}, Host: store.HostLaunchSpec{OriginContextID: "other-origin", HostGeneration: "other-gen", Executable: "/private/bin/fixture"}, Tasks: []store.TaskSpec{{ID: "gate-task", RunID: "gate-run", MaxAttempts: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			host, err := s.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "other-origin", OriginPID: 7, OriginBirth: "other-birth", HostGeneration: "other-gen", PID: 9, Birth: "fixture-host", Executable: "/private/bin/fixture"}, s.epoch)
			if err != nil {
				t.Fatal(err)
			}
			launch, err := s.db.ClaimReady(ctx, host, s.epoch, 2)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.db.ValidateDispatchPeer(ctx, map[int]string{os.Getpid(): birth}); err != store.CodeError("owner_registration_deferred") {
				t.Errorf("failed bind ancestry obtained dispatch authority: %v", err)
			}
			// This fixture never spawned a worker; release only the synthetic gate row.
			if _, err = s.db.SQL().Exec(`UPDATE segment_runtime SET status='exited' WHERE segment_id=?`, launch.SegmentID); err != nil {
				t.Fatal(err)
			}
			response, err = ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindOwnerBind, RequestID: "retry", Payload: raw})
			if err != nil || response.Kind == ipc.KindError {
				t.Fatalf("same DB retry failed: kind=%s err=%v", response.Kind, err)
			}
			if count(s.db.SQL(), "owner_grants") != 1 || count(external, "owner_grants") != 1 {
				t.Error("retry did not commit exactly one grant")
			}
			var bound struct {
				OwnerCapability string `json:"owner_capability"`
			}
			if json.Unmarshal(response.Payload, &bound) != nil || bound.OwnerCapability == "" {
				t.Fatal("missing successful capability")
			}
			body, err := os.ReadFile(bound.OwnerCapability)
			var grant store.OwnerGrant
			if err != nil || json.Unmarshal(body, &grant) != nil || s.db.ValidateOwnerGrant(ctx, grant) != nil {
				t.Fatal("successful capability invalid")
			}
		})
	}
}

func TestOwnerGrantPublicationCanonicalAncestorAlias(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "bind-alias-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err = os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(filepath.Join(alias, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		cancel()
		s.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not join")
		}
	}()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	response := callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "alias-master", OriginPID: os.Getpid(), OriginBirth: birth})
	var result struct {
		Path string `json:"owner_capability"`
	}
	if json.Unmarshal(response.Payload, &result) != nil {
		t.Fatal("invalid receipt")
	}
	if filepath.Dir(result.Path) != filepath.Join(real, "state", "owners") {
		t.Fatal("capability path not canonical")
	}
	authenticated := context.WithValue(ctx, peerAncestryKey{}, map[int]string{os.Getpid(): birth})
	grant, err := s.localOwner(authenticated, result.Path)
	if err != nil || grant.ControllerThread != "alias-master" {
		t.Fatal("canonical capability not usable", err)
	}
}
