//go:build linux

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLinuxExecutableRejectsKernelTargetInodeMismatch(t *testing.T) {
	dir := t.TempDir()
	kernel := filepath.Join(dir, "kernel")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(kernel, []byte("a"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("b"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := linuxExecutablePathWith(context.Background(), 42, func(context.Context, int) (string, error) { return "linux:b:1", nil }, func(string) (string, error) { return target, nil }, func(string) (string, error) { return target, nil }, func(name string) (os.FileInfo, error) {
		if name == "/proc/42/exe" {
			return os.Stat(kernel)
		}
		return os.Stat(name)
	})
	if err == nil {
		t.Fatal("kernel executable inode mismatch admitted")
	}
}

func TestLinuxAncestryUsesObservedStatForBirth(t *testing.T) {
	calls := 0
	got, err := linuxAncestryContextWith(context.Background(), 42, func(context.Context, int) (linuxStat, int, error) {
		calls++
		return linuxStat{ppid: 1, pgid: 7, start: "old"}, os.Geteuid(), nil
	}, func(context.Context) (string, error) { return "boot", nil })
	if err != nil || calls != 1 || got[42] != "linux:boot:old" {
		t.Fatalf("got=%v calls=%d err=%v", got, calls, err)
	}
}
func TestLinuxAncestryCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := linuxAncestryContextWith(ctx, 42, func(context.Context, int) (linuxStat, int, error) {
		t.Fatal("observe called")
		return linuxStat{}, 0, nil
	}, func(context.Context) (string, error) { return "", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestLinuxAncestryObserveCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := linuxAncestryContextWith(ctx, 42, func(context.Context, int) (linuxStat, int, error) {
		cancel()
		return linuxStat{}, 0, context.Canceled
	}, func(context.Context) (string, error) { return "boot", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestLinuxAncestryUIDBoundaryCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := linuxAncestryContextWith(ctx, 42, func(context.Context, int) (linuxStat, int, error) {
		cancel()
		return linuxStat{ppid: 1, start: "1"}, os.Geteuid() + 1, nil
	}, func(context.Context) (string, error) { return "boot", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestLinuxParseStatAcceptsSpacesAndRightParensInComm(t *testing.T) {
	fields := []string{"S", "7", "8"}
	fields = append(fields, make([]string, 16)...)
	for i := 3; i < len(fields); i++ {
		fields[i] = "0"
	}
	fields = append(fields, "123")
	got, err := linuxParseStat([]byte("42 (a bad) comm) " + strings.Join(fields, " ") + "\n"))
	if err != nil || got.ppid != 7 || got.pgid != 8 || got.state != 'S' || got.start != "123" {
		t.Fatalf("stat=%+v err=%v", got, err)
	}
}

func TestLinuxParseStatRejectsMalformedOrUntrustedValues(t *testing.T) {
	for _, value := range []string{
		"42 no-parens S 7 8 9", "42 (x) S -1 8 9 0 0 0 0 0 0 0 0 0 0 0 0 0 0 123",
		"42 (x) S 7 8 9 0 0 0 0 0 0 0 0 0 0 0 0 0 0 nope",
	} {
		if _, err := linuxParseStat([]byte(value)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestLinuxGroupAliveUsesKernelGroupProof(t *testing.T) {
	for _, scenario := range []string{"alive", "gone", "permission_unknown", "io_unknown", "cancel_before", "cancel_at_probe", "invalid_group"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pgid := 424242
			if scenario == "invalid_group" {
				pgid = 0
			}
			if scenario == "cancel_before" {
				cancel()
			}
			calls := 0
			found, err := linuxGroupAliveWith(ctx, pgid, func(pid int, signal syscall.Signal) error {
				calls++
				if pid != -424242 || signal != 0 {
					t.Fatal("not an observational group probe", pid, signal)
				}
				switch scenario {
				case "alive":
					return nil
				case "gone":
					return syscall.ESRCH
				case "permission_unknown":
					return syscall.EPERM
				case "io_unknown":
					return syscall.EIO
				case "cancel_at_probe":
					cancel()
					return syscall.ESRCH
				default:
					t.Fatal("invalid/cancelled probe executed")
					return nil
				}
			})
			switch scenario {
			case "alive":
				if !found || err != nil || calls != 1 {
					t.Fatal("kernel live group lost", found, err, calls)
				}
			case "gone":
				if found || err != nil || calls != 1 {
					t.Fatal("kernel ESRCH not accepted", found, err, calls)
				}
			case "cancel_before", "cancel_at_probe":
				wantCalls := 0
				if scenario == "cancel_at_probe" {
					wantCalls = 1
				}
				if err == nil || found || calls != wantCalls {
					t.Fatal("cancellation invented group absence", found, err, calls)
				}
			default:
				if err == nil || found {
					t.Fatal("unknown group observation invented absence", found, err)
				}
				if scenario == "invalid_group" && calls != 0 {
					t.Fatal("invalid group was probed")
				}
			}
		})
	}
}
