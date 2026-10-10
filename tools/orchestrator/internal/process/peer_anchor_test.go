package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func stablePeerObserver(records map[int]peerRecord, reads map[int]int) func(context.Context, int) (peerRecord, error) {
	return func(_ context.Context, pid int) (peerRecord, error) {
		reads[pid]++
		record, ok := records[pid]
		if !ok {
			return peerRecord{}, ErrProcessTreeUnknown
		}
		return record, nil
	}
}

func TestVerifyLiveDescendantAcceptsStableChain(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}}
	h.peerOwner = h
	reads := map[int]int{}
	err := h.verifyLiveDescendantWith(context.Background(), 11, stablePeerObserver(map[int]peerRecord{
		10: {pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1},
		11: {pid: 11, birth: "boot:101", euid: 501, pgid: 10, ppid: 10},
	}, reads))
	if err != nil || reads[10] != 2 || reads[11] != 2 {
		t.Fatalf("stable chain err=%v reads=%v", err, reads)
	}
}

func TestVerifyLiveDescendantRejectsExactLeaderBirthDrift(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}}
	h.peerOwner = h
	reads, drifted := map[int]int{}, false
	err := h.verifyLiveDescendantWith(context.Background(), 11, func(_ context.Context, pid int) (peerRecord, error) {
		reads[pid]++
		if pid == 10 && reads[pid] == 2 {
			drifted = true
			return peerRecord{pid: 10, birth: "boot:999", euid: 501, pgid: 10, ppid: 1}, nil
		}
		return map[int]peerRecord{10: {pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, 11: {pid: 11, birth: "boot:101", euid: 501, pgid: 10, ppid: 10}}[pid], nil
	})
	if err == nil || !drifted || reads[10] != 2 || reads[11] != 1 {
		t.Fatalf("exact leader birth drift err=%v drifted=%t reads=%v", err, drifted, reads)
	}
}

func TestVerifyLiveDescendantRejectsIntermediateReparent(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}}
	h.peerOwner = h
	reads, reparented := map[int]int{}, false
	err := h.verifyLiveDescendantWith(context.Background(), 12, func(_ context.Context, pid int) (peerRecord, error) {
		reads[pid]++
		if pid == 11 && reads[pid] == 2 {
			reparented = true
			return peerRecord{pid: 11, birth: "boot:101", euid: 501, pgid: 10, ppid: 1}, nil
		}
		return map[int]peerRecord{10: {pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, 11: {pid: 11, birth: "boot:101", euid: 501, pgid: 10, ppid: 10}, 12: {pid: 12, birth: "boot:102", euid: 501, pgid: 10, ppid: 11}}[pid], nil
	})
	if err == nil || !reparented || reads[10] != 2 || reads[11] != 2 || reads[12] != 1 {
		t.Fatalf("middle-chain reparent err=%v reparented=%t reads=%v", err, reparented, reads)
	}
}

func TestStopRevokesPeerAnchorBeforeStopFailure(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}, pgid: 10, identity: Identity{PID: 10, Birth: "public", BirthKnown: true}}
	h.peerOwner = h
	err := h.stopWith(context.Background(), stopOperations{
		birth:         func(int) (string, error) { return "public", nil },
		signal:        func(int, syscall.Signal) error { return errors.New("signal failure") },
		groupAlive:    func(int) (bool, error) { return true, nil },
		waitGroupGone: func(context.Context, int) bool { return false },
	})
	if err == nil {
		t.Fatal("fixture did not reach Stop failure")
	}
	if err := h.verifyLiveDescendantWith(context.Background(), 10, func(context.Context, int) (peerRecord, error) {
		return peerRecord{pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, nil
	}); err == nil {
		t.Fatal("Stop failure did not permanently revoke peer authority")
	}
}

func TestStartAnchorCaptureFailureRetainsExistingStartBehavior(t *testing.T) {
	spec := duplexCommand(t, "quick")
	h, _, err := startCommandUsingWithPeerAnchor(context.Background(), spec, false, processBirth, func(cmd *exec.Cmd) error { return cmd.Start() }, func(context.Context, int) (peerAnchor, error) {
		return peerAnchor{}, errors.New("anchor unavailable")
	})
	if err != nil || h == nil {
		t.Fatalf("anchor failure changed Start result: handle=%t err=%v", h != nil, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Wait(ctx); err != nil || h.ConfirmTreeExited() != nil {
		t.Fatalf("original Wait/tree changed: %v", err)
	}
	if err := h.VerifyLiveDescendant(context.Background(), 1); err == nil {
		t.Fatal("missing anchor granted authority")
	}
}

func TestVerifyLiveDescendantNativeLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("kernel peer authority is unsupported")
	}
	release, pidPath := filepath.Join(t.TempDir(), "release"), filepath.Join(t.TempDir(), "child-pid")
	spec := duplexCommand(t, "peer-anchor-leader")
	spec.Env = append(spec.Env, "C06_DUPLEX_RELEASE="+release, "C06_DUPLEX_CHILD_PID="+pidPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, err := Start(ctx, spec)
	if err != nil || h == nil {
		t.Fatalf("Start original leader: handle=%t err=%v", h != nil, err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release"), 0600)
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := h.Stop(cleanup); err != nil {
			t.Errorf("cleanup Stop: %v", err)
		}
		if err := h.Wait(cleanup); err != nil {
			t.Errorf("cleanup Wait: %v", err)
		}
		if err := h.ConfirmTreeExited(); err != nil {
			t.Errorf("cleanup tree: %v", err)
		}
	})
	var peer int
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if raw, readErr := os.ReadFile(pidPath); readErr == nil {
			peer, _ = strconv.Atoi(string(raw))
			break
		}
	}
	if peer <= 0 {
		t.Fatal("owned peer PID was not published")
	}
	if err := h.VerifyLiveDescendant(ctx, peer); err != nil {
		t.Fatalf("live owned descendant rejected: %v", err)
	}
	bound, err := h.BindLiveDescendant(ctx, peer)
	if err != nil || bound == nil {
		t.Fatalf("bind live owned descendant: binding=%t err=%v", bound != nil, err)
	}
	if err := bound.Verify(ctx); err != nil {
		t.Fatalf("bound live owned descendant rejected: %v", err)
	}
	if err := h.VerifyLiveDescendant(ctx, os.Getpid()); err == nil {
		t.Fatal("foreign process accepted")
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Wait(ctx); err != nil || h.ConfirmTreeExited() != nil {
		t.Fatalf("original Wait/tree after release: %v", err)
	}
	if err := h.VerifyLiveDescendant(context.Background(), peer); err == nil {
		t.Fatal("terminal Handle retained peer authority")
	}
}

func TestLiveDescendantRejectsPIDReuseWithNewBirth(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}}
	h.peerOwner = h
	bound, err := h.bindLiveDescendantWith(context.Background(), 11, func(_ context.Context, pid int) (peerRecord, error) {
		if pid == 10 {
			return peerRecord{pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, nil
		}
		return peerRecord{pid: 11, birth: "boot:101", euid: 501, pgid: 10, ppid: 10}, nil
	})
	if err != nil || bound == nil {
		t.Fatalf("bind stable peer: binding=%t err=%v", bound != nil, err)
	}
	if err := bound.verifyWith(context.Background(), func(_ context.Context, pid int) (peerRecord, error) {
		if pid == 10 {
			return peerRecord{pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, nil
		}
		return peerRecord{pid: 11, birth: "boot:999", euid: 501, pgid: 10, ppid: 10}, nil
	}); err == nil {
		t.Fatal("reused PID with new birth retained bound authority")
	}
}

func TestVerifyLiveDescendantRejectsHandleCopy(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}}
	h.peerOwner = h
	copy := *h // test fixture is unlocked; copied values must not retain authority.
	calls := 0
	err := copy.verifyLiveDescendantWith(context.Background(), 10, func(context.Context, int) (peerRecord, error) {
		calls++
		return peerRecord{pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, nil
	})
	if err == nil || calls != 0 {
		t.Fatalf("copied Handle err=%v observation_calls=%d", err, calls)
	}
}
