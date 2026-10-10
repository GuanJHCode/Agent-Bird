package process

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func testPeerAnchor() peerAnchor {
	return peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}
}

func TestVerifyLiveDescendantRejectsExactLeaderBirthDrift(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}}
	reads := 0
	err := h.verifyLiveDescendantWith(context.Background(), 11, func(context.Context, int) (peerRecord, error) {
		reads++
		switch reads {
		case 1, 3:
			return peerRecord{pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, nil
		case 2, 4:
			return peerRecord{pid: 11, birth: "boot:101", euid: 501, pgid: 10, ppid: 10}, nil
		default:
			return peerRecord{pid: 10, birth: "boot:999", euid: 501, pgid: 10, ppid: 1}, nil
		}
	})
	if err == nil {
		t.Fatal("exact leader birth drift passed")
	}
}

func TestVerifyLiveDescendantRejectsIntermediateReparent(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}}
	reads := 0
	err := h.verifyLiveDescendantWith(context.Background(), 12, func(context.Context, int) (peerRecord, error) {
		reads++
		switch reads {
		case 1, 4:
			return peerRecord{pid: 10, birth: "boot:100", euid: 501, pgid: 10, ppid: 1}, nil
		case 2:
			return peerRecord{pid: 12, birth: "boot:102", euid: 501, pgid: 10, ppid: 11}, nil
		case 3, 5:
			return peerRecord{pid: 11, birth: "boot:101", euid: 501, pgid: 10, ppid: 10}, nil
		default:
			return peerRecord{pid: 12, birth: "boot:102", euid: 501, pgid: 10, ppid: 1}, nil
		}
	})
	if err == nil {
		t.Fatal("middle-chain reparent passed")
	}
}

func TestStopRevokesPeerAnchorBeforeStopFailure(t *testing.T) {
	h := &Handle{peerAnchor: &peerAnchor{pid: 10, birth: "boot:100", euid: 501, pgid: 10}, pgid: 10, identity: Identity{PID: 10, Birth: "public", BirthKnown: true}}
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.Wait(ctx); err != nil || h.ConfirmTreeExited() != nil {
		t.Fatalf("original Wait/tree changed: %v", err)
	}
	if err := h.VerifyLiveDescendant(context.Background(), 1); err == nil {
		t.Fatal("missing anchor granted authority")
	}
}
