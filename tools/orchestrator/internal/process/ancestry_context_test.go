package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func ancestryStartError(errno syscall.Errno) error {
	return &os.PathError{Op: "fork/exec", Path: "/bin/ps", Err: errno}
}

func ancestryIdentity(fields string, parent int) []byte {
	if fields == "lstart=" {
		return []byte("stable birth\n")
	}
	return []byte(fmt.Sprintf("%d %d\n", parent, os.Geteuid()))
}

func TestAncestryContextRetriesOnlyStructuredStartFailures(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EAGAIN, syscall.EINTR} {
		t.Run(errno.Error(), func(t *testing.T) {
			calls := 0
			result, err := ancestryContext(context.Background(), 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
				calls++
				if calls == 1 {
					return nil, fmt.Errorf("wrapped: %w", ancestryStartError(errno))
				}
				return ancestryIdentity(fields, 1), nil
			})
			if err != nil || result[42] != "stable birth" || calls != 4 {
				t.Fatalf("transient start retry: result=%v calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestAncestryContextStopsAfterTwoAttempts(t *testing.T) {
	calls := 0
	result, err := ancestryContext(context.Background(), 42, func(context.Context, string, int) ([]byte, error) {
		calls++
		return nil, ancestryStartError(syscall.EAGAIN)
	})
	if result != nil || !errors.Is(err, syscall.EAGAIN) || calls != 2 {
		t.Fatalf("attempt bound: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextRejectsNonRetryableQueryFailures(t *testing.T) {
	for name, failure := range map[string]error{
		"permission":      ancestryStartError(syscall.EPERM),
		"access":          ancestryStartError(syscall.EACCES),
		"gone":            ancestryStartError(syscall.ESRCH),
		"wrong_operation": &os.PathError{Op: "read", Path: "/bin/ps", Err: syscall.EAGAIN},
		"bare_errno":      syscall.EAGAIN,
		"unknown_exit":    &exec.ExitError{},
		"unknown_text":    errors.New("temporarily unavailable: EAGAIN"),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			result, err := ancestryContext(context.Background(), 42, func(context.Context, string, int) ([]byte, error) {
				calls++
				if calls == 1 {
					return nil, failure
				}
				return []byte("1 0"), nil
			})
			if result != nil || err == nil || calls != 1 {
				t.Fatalf("non-retryable accepted: result=%v calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestAncestryContextSharesNodeBudgetAcrossQueriesAndRetries(t *testing.T) {
	var first context.Context
	calls := 0
	result, err := ancestryContext(context.Background(), 42, func(ctx context.Context, fields string, _ int) ([]byte, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second || ctx.Err() != nil {
			t.Errorf("query lacks original node budget: deadline=%v has=%v err=%v", deadline, ok, ctx.Err())
		}
		if first == nil {
			first = ctx
		} else if ctx != first {
			t.Error("node budget renewed between queries/attempts")
		}
		if calls == 2 {
			return nil, ancestryStartError(syscall.EAGAIN)
		}
		return ancestryIdentity(fields, 1), nil
	})
	if err != nil || result[42] != "stable birth" || calls != 5 {
		t.Fatalf("shared window retry: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextPropagatesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	result, err := ancestryContext(ctx, 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
		calls++
		return ancestryIdentity(fields, 1), nil
	})
	if result != nil || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled ancestry queried/admitted: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextRejectsSuccessfulDataAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	result, err := ancestryContext(ctx, 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
		calls++
		if calls == 3 {
			cancel()
		}
		return ancestryIdentity(fields, 1), nil
	})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-cancel identity admitted: result=%v err=%v", result, err)
	}
}

func TestAncestryContextKeepsCallerDeadlineAndChainCap(t *testing.T) {
	for _, budget := range []time.Duration{100 * time.Millisecond, 2 * time.Minute} {
		t.Run(budget.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			_, err := ancestryContext(ctx, 42, func(node context.Context, fields string, _ int) ([]byte, error) {
				deadline, ok := node.Deadline()
				if !ok || time.Until(deadline) > time.Second || time.Until(deadline) > budget {
					t.Errorf("caller/node deadline lost: %v %v", deadline, ok)
				}
				return ancestryIdentity(fields, 1), nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAncestryContextRejectsBirthChangeAcrossRetry(t *testing.T) {
	calls := 0
	result, err := ancestryContext(context.Background(), 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, ancestryStartError(syscall.EAGAIN)
		}
		if calls >= 3 && fields == "lstart=" {
			return []byte("changed birth\n"), nil
		}
		return ancestryIdentity(fields, 1), nil
	})
	if result != nil || err == nil || err.Error() != "process_identity_changed" || calls != 3 {
		t.Fatalf("retry crossed identity: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextRejectsBirthChangeWithinAttempt(t *testing.T) {
	calls := 0
	result, err := ancestryContext(context.Background(), 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
		calls++
		if calls == 3 {
			return []byte("changed birth\n"), nil
		}
		return ancestryIdentity(fields, 1), nil
	})
	if result != nil || err == nil || err.Error() != "process_identity_changed" || calls != 3 {
		t.Fatalf("snapshot changed identity: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextDiscardsFailedAttemptParent(t *testing.T) {
	calls := 0
	result, err := ancestryContext(context.Background(), 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
		calls++
		if calls == 2 {
			return ancestryIdentity(fields, 99), nil
		}
		if calls == 3 {
			return nil, ancestryStartError(syscall.EAGAIN)
		}
		return ancestryIdentity(fields, 1), nil
	})
	if err != nil || len(result) != 1 || result[42] != "stable birth" || calls != 6 {
		t.Fatalf("failed attempt data reused: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextUIDBoundaryDoesNotRetryOrAdmitNode(t *testing.T) {
	calls := 0
	result, err := ancestryContext(context.Background(), 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
		calls++
		if fields == "ppid=,uid=" {
			return []byte(fmt.Sprintf("1 %d", os.Geteuid()+1)), nil
		}
		return ancestryIdentity(fields, 1), nil
	})
	if err != nil || len(result) != 0 || calls != 2 {
		t.Fatalf("foreign UID retried/admitted: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextRejectsMalformedIdentities(t *testing.T) {
	for _, bad := range []string{"", "0 0", "-1 0", "1 -1", "1 0 extra", "1 invalid", "1\n0", "invalid 0"} {
		t.Run(strconv.Quote(bad), func(t *testing.T) {
			calls := 0
			result, err := ancestryContext(context.Background(), 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
				calls++
				if fields == "ppid=,uid=" {
					return []byte(bad), nil
				}
				return ancestryIdentity(fields, 1), nil
			})
			if result != nil || err == nil || calls != 2 {
				t.Fatalf("malformed identity accepted/retried: result=%v calls=%d err=%v", result, calls, err)
			}
		})
	}
	for _, bad := range []string{"", "birth\nsecond birth"} {
		calls := 0
		result, err := ancestryContext(context.Background(), 42, func(context.Context, string, int) ([]byte, error) { calls++; return []byte(bad), nil })
		if result != nil || err == nil || calls != 1 {
			t.Fatalf("malformed birth accepted/retried: result=%v calls=%d err=%v", result, calls, err)
		}
	}
}

func TestAncestryContextRejectsCyclesAndDepthTruncation(t *testing.T) {
	for _, mode := range []string{"cycle", "too_deep", "invalid_pid"} {
		t.Run(mode, func(t *testing.T) {
			start := 100
			if mode == "invalid_pid" {
				start = 0
			}
			result, err := ancestryContext(context.Background(), start, func(_ context.Context, fields string, pid int) ([]byte, error) {
				parent := pid - 1
				if mode == "cycle" {
					parent = pid
				}
				return ancestryIdentity(fields, parent), nil
			})
			if result != nil || err == nil || !strings.Contains(err.Error(), "process_ancestry_invalid") {
				t.Fatalf("invalid/truncated chain admitted: result=%v err=%v", result, err)
			}
		})
	}
}

func TestAncestryContextCallerDeadlineBoundsBlockingBirth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	result, err := ancestryContext(ctx, 42, func(queryCtx context.Context, _ string, _ int) ([]byte, error) {
		calls++
		<-queryCtx.Done()
		return []byte("stable birth"), nil
	})
	if result != nil || !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("blocking birth ignored caller deadline: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextRetryWaitObservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitAncestryRetry(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry backoff ignored cancellation: %v", err)
	}
	calls := 0
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	result, err := ancestryContext(ctx, 42, func(context.Context, string, int) ([]byte, error) {
		calls++
		cancel()
		return nil, ancestryStartError(syscall.EAGAIN)
	})
	if result != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled transient failure was retried: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextRetryBackoffUsesTenMilliseconds(t *testing.T) {
	calls := 0
	var failedAt time.Time
	result, err := ancestryContext(context.Background(), 42, func(_ context.Context, fields string, _ int) ([]byte, error) {
		calls++
		if calls == 1 {
			failedAt = time.Now()
			return nil, ancestryStartError(syscall.EAGAIN)
		}
		if calls == 2 && time.Since(failedAt) < 10*time.Millisecond {
			t.Error("retry omitted the single ten-millisecond backoff")
		}
		return ancestryIdentity(fields, 1), nil
	})
	if err != nil || result[42] != "stable birth" || calls != 4 {
		t.Fatalf("retry backoff contract: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestAncestryContextAcceptsExactDepthLimitAndRejectsPartialFailure(t *testing.T) {
	result, err := ancestryContext(context.Background(), 65, func(_ context.Context, fields string, pid int) ([]byte, error) {
		return ancestryIdentity(fields, pid-1), nil
	})
	if err != nil || len(result) != 64 {
		t.Fatalf("exact 64-node chain rejected: len=%d err=%v", len(result), err)
	}
	result, err = ancestryContext(context.Background(), 42, func(_ context.Context, fields string, pid int) ([]byte, error) {
		if pid == 41 {
			return nil, ancestryStartError(syscall.EPERM)
		}
		return ancestryIdentity(fields, 41), nil
	})
	if err == nil || result != nil {
		t.Fatalf("failed chain returned authorized prefix: result=%v err=%v", result, err)
	}
}
