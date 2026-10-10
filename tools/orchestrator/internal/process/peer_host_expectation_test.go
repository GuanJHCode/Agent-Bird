package process

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestVerifyHostPeerExpectationStableAndDriftedRecords(t *testing.T) {
	uid := os.Geteuid()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	expected := HostPeerExpectation{Domain: hostPeerExpectationDomain, PID: 44, RawBirth: "raw-birth", EUID: uid}
	base := peerRecord{pid: 44, ppid: 10, birth: "raw-birth", euid: uid, pgid: 10}
	for _, kind := range []string{"stable", "identity", "uid", "parent", "pgid"} {
		t.Run(kind, func(t *testing.T) {
			calls, fired := 0, false
			err := verifyHostPeerExpectationWith(ctx, 44, expected, func(context.Context, int) (peerRecord, error) {
				calls++
				if calls == 2 && kind != "stable" {
					fired = true
					r := base
					switch kind {
					case "identity":
						r.birth = "changed"
					case "uid":
						r.euid++
					case "parent":
						r.ppid++
					case "pgid":
						r.pgid++
					}
					return r, nil
				}
				return base, nil
			})
			if kind == "stable" {
				if err != nil || calls != 2 {
					t.Fatalf("stable err=%v calls=%d", err, calls)
				}
			} else if err == nil || !fired || calls != 2 {
				t.Fatalf("%s err=%v fired=%t calls=%d", kind, err, fired, calls)
			}
		})
	}
}

func TestVerifyHostPeerExpectationRejectsInputBeforeObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	valid := HostPeerExpectation{Domain: hostPeerExpectationDomain, PID: 44, RawBirth: "raw", EUID: os.Geteuid()}
	bad := []HostPeerExpectation{{}, {Domain: "wrong", PID: 44, RawBirth: "raw", EUID: os.Geteuid()}, {Domain: hostPeerExpectationDomain, PID: 44, RawBirth: "bad\n", EUID: os.Geteuid()}, {Domain: hostPeerExpectationDomain, PID: 44, RawBirth: string(make([]byte, 129)), EUID: os.Geteuid()}, {Domain: hostPeerExpectationDomain, PID: 44, RawBirth: "raw", EUID: os.Geteuid() + 1}}
	for _, expected := range bad {
		calls := 0
		if err := verifyHostPeerExpectationWith(ctx, 44, expected, func(context.Context, int) (peerRecord, error) { calls++; return peerRecord{}, nil }); err == nil || calls != 0 {
			t.Fatalf("invalid expectation err=%v calls=%d", err, calls)
		}
	}
	for _, pid := range []int{0, -1, 1 << 31} {
		calls := 0
		if err := verifyHostPeerExpectationWith(ctx, pid, valid, func(context.Context, int) (peerRecord, error) { calls++; return peerRecord{}, nil }); err == nil || calls != 0 {
			t.Fatalf("invalid peer pid=%d err=%v calls=%d", pid, err, calls)
		}
	}
	calls := 0
	if err := verifyHostPeerExpectationWith(ctx, 45, valid, func(context.Context, int) (peerRecord, error) { calls++; return peerRecord{}, nil }); err == nil || calls != 0 {
		t.Fatalf("wrong kernel pid err=%v calls=%d", err, calls)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := verifyHostPeerExpectationWith(ctx, 44, valid, func(context.Context, int) (peerRecord, error) {
		t.Fatal("cancelled ctx observed kernel")
		return peerRecord{}, nil
	}); err == nil {
		t.Fatal("cancelled context accepted")
	}
}

func TestCaptureHostPeerExpectationSeamLifecycle(t *testing.T) {
	uid, pid := os.Geteuid(), os.Getpid()
	for _, kind := range []string{"nil", "unbounded", "cancelled", "cancelled_during_observe", "positive"} {
		t.Run(kind, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			switch kind {
			case "nil":
				ctx = nil
			case "unbounded":
				ctx = context.Background()
			default:
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if kind == "cancelled" {
					cancel()
				}
			}
			calls, fired := 0, false
			expectation, err := captureHostPeerExpectationWith(ctx, func(context.Context, int) (peerRecord, error) {
				calls++
				if kind == "cancelled_during_observe" {
					cancel()
					fired = true
				}
				return peerRecord{pid: pid, ppid: 1, birth: "darwin:1:2", euid: uid, pgid: 1}, nil
			})
			if kind == "positive" {
				if err != nil || calls != 1 || expectation.PID != pid || expectation.EUID != uid || expectation.RawBirth != "darwin:1:2" {
					t.Fatalf("capture=%+v err=%v calls=%d", expectation, err, calls)
				}
			} else if err == nil || (kind != "cancelled_during_observe" && calls != 0) || (kind == "cancelled_during_observe" && (!fired || calls != 1)) {
				t.Fatalf("%s err=%v calls=%d fired=%t", kind, err, calls, fired)
			}
		})
	}
}

func TestVerifyHostPeerExpectationRejectsCancellationDuringSecondRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	uid := os.Geteuid()
	expected := HostPeerExpectation{Domain: hostPeerExpectationDomain, PID: 44, RawBirth: "raw", EUID: uid}
	calls, fired := 0, false
	err := verifyHostPeerExpectationWith(ctx, 44, expected, func(context.Context, int) (peerRecord, error) {
		calls++
		if calls == 2 {
			cancel()
			fired = true
		}
		return peerRecord{pid: 44, ppid: 1, birth: "raw", euid: uid, pgid: 44}, nil
	})
	if err == nil || !fired || calls != 2 {
		t.Fatalf("err=%v fired=%t calls=%d", err, fired, calls)
	}
}

func TestHostPeerExpectationNativeSelf(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("unsupported platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	expected, err := CaptureHostPeerExpectation(ctx)
	if err != nil || expected.Domain != hostPeerExpectationDomain || expected.PID != os.Getpid() || expected.RawBirth == "" || expected.EUID != os.Geteuid() {
		t.Fatalf("capture=%+v err=%v", expected, err)
	}
	if err := VerifyHostPeerExpectation(ctx, os.Getpid(), expected); err != nil {
		t.Fatalf("self verify: %v", err)
	}
	expected.PID++
	if err := VerifyHostPeerExpectation(ctx, os.Getpid(), expected); err == nil {
		t.Fatal("wrong expectation accepted")
	}
}

func TestCaptureHostPeerExpectationRejectsUnboundedContext(t *testing.T) {
	if _, err := CaptureHostPeerExpectation(context.Background()); err == nil {
		t.Fatal("unbounded capture context accepted")
	}
}
