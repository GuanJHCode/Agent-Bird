package coordinator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestHostRegistrationSerializesCompetingLivePeers(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "host-registration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	pid, birth, exe := testHostIdentity(t)
	r, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: exe}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	oldPID, oldBirth := testRetiredOwner(t)
	hello := contract.HostHello{LaunchID: r.LaunchID, LaunchToken: r.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "gen", PID: oldPID, Birth: oldBirth, Executable: exe}
	if _, err := s.db.RegisterHost(ctx, hello, s.epoch); err != nil {
		t.Fatal(err)
	}
	var peers []contract.HostHello
	for i := 0; i < 2; i++ {
		p := exec.Command("/bin/sleep", "30")
		if err := p.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Process.Kill(); _ = p.Wait() })
		h := hello
		h.PID = p.Process.Pid
		h.Birth, err = process.Birth(h.PID)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, h)
	}
	gate := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, h := range peers {
		wg.Add(1)
		go func() { defer wg.Done(); <-gate; _, err := s.registerHost(ctx, h); results <- err }()
	}
	close(gate)
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if err == store.ErrHostRejected {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
}

func TestHostIdentityExitProofRejectsUnknownAndDistinguishesPIDReuse(t *testing.T) {
	pid, birth, _ := testHostIdentity(t)
	for _, tc := range []struct {
		pid    int
		birth  string
		exited bool
		bad    bool
	}{{pid, birth, false, false}, {pid, "different-birth", true, false}, {0, "", false, true}} {
		got, err := hostIdentityExited(tc.pid, tc.birth)
		if (err != nil) != tc.bad || got != tc.exited {
			t.Fatalf("exited=%v err=%v", got, err)
		}
	}
	pid, birth = testRetiredOwner(t)
	if exited, err := hostIdentityExited(pid, birth); err != nil || !exited {
		t.Fatalf("retired identity: %v %v", exited, err)
	}
}
