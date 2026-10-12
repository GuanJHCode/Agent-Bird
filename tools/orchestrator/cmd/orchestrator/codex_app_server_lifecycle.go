package main

import (
	"context"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type appServerStopOps struct {
	wait      func() error
	quiescent func(context.Context, int) (bool, error)
	signal    func(context.Context, int, string, syscall.Signal) error
	confirm   func(context.Context, int) error
}

func (r *appServerRPC) stop() error {
	return r.stopWith(appServerStopOps{
		wait: r.cmd.Wait, quiescent: process.GroupQuiescent,
		signal: signalAppServerGroup, confirm: process.ConfirmGroupExitedContext,
	})
}

func (r *appServerRPC) stopWith(ops appServerStopOps) error {
	r.stopOnce.Do(func() {
		defer close(r.stopped)
		_ = r.stdin.Close()
		_ = r.stdout.Close()
		// Keep the leader waitable (including when it is a zombie) until the
		// last group signal. Reaping it earlier loses the PID/PGID anchor.
		defer func() {
			// Also retain reaping responsibility on an unknown-identity path.
			// In that case Wait may await external termination, but its buffered
			// receipt cannot block and stop still returns a bounded UNKNOWN.
			go func() { r.done <- ops.wait() }()
			select {
			case <-r.done:
			case <-time.After(time.Second):
				r.stopErr = codeError("app_server_process_tree_unknown")
				return
			}
			confirmCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if confirmCtx.Err() != nil {
					r.stopErr = codeError("app_server_process_tree_unknown")
					return
				}
				err := ops.confirm(confirmCtx, r.pid)
				if confirmCtx.Err() != nil {
					r.stopErr = codeError("app_server_process_tree_unknown")
					return
				}
				if err == nil {
					return
				}
				select {
				case <-confirmCtx.Done():
					r.stopErr = codeError("app_server_process_tree_unknown")
					return
				case <-ticker.C:
				}
			}
		}()
		for _, signal := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if signal != 0 {
				if err := ops.signal(ctx, r.pid, r.birth, signal); err != nil {
					cancel()
					r.stopErr = codeError("app_server_process_tree_unknown")
					return
				}
			}
			quiescent, err := waitAppServerGroupQuiescent(ctx, r.pid, ops.quiescent)
			cancel()
			if err != nil {
				r.stopErr = codeError("app_server_process_tree_unknown")
				return
			}
			if quiescent {
				return
			}
		}
		r.stopErr = codeError("app_server_process_tree_unknown")
	})
	return r.stopErr
}

func signalAppServerGroup(ctx context.Context, pid int, birth string, signal syscall.Signal) error {
	return signalAppServerGroupWith(ctx, pid, birth, signal, process.MatchWaitableGroupLeader, syscall.Kill)
}

func signalAppServerGroupWith(ctx context.Context, pid int, birth string, signal syscall.Signal, match func(context.Context, int, string) error, send func(int, syscall.Signal) error) error {
	if ctx.Err() != nil || match(ctx, pid, birth) != nil {
		return codeError("app_server_process_tree_unknown")
	}
	// A successful identity observation does not renew an expired stage.
	if ctx.Err() != nil {
		return codeError("app_server_process_tree_unknown")
	}
	if err := send(-pid, signal); err != nil && err != syscall.ESRCH {
		return codeError("app_server_process_tree_unknown")
	}
	return nil
}

func waitAppServerGroupQuiescent(ctx context.Context, pgid int, query func(context.Context, int) (bool, error)) (bool, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		quiescent, err := query(ctx, pgid)
		if ctx.Err() != nil {
			return false, nil
		}
		if err != nil || quiescent {
			return quiescent, err
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-ticker.C:
		}
	}
}
