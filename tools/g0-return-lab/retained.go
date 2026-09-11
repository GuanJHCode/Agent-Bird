package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type retainedOptions struct {
	sub, dir, nonce, controller string
	steps, revision, segment    int
	interval                    time.Duration
}

type retainedTask struct {
	Version          int    `json:"version"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	Revision         int    `json:"revision"`
	TotalSteps       int    `json:"total_steps"`
	IntervalMS       int64  `json:"interval_ms"`
}

type retainedState struct {
	Version          int    `json:"version"`
	Status           string `json:"status"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	Revision         int    `json:"revision"`
	TaskHash         string `json:"task_hash"`
	Segment          int    `json:"segment"`
	CompletedSteps   int    `json:"completed_steps"`
	WorkerPID        int    `json:"worker_pid"`
	SegmentStartedAt string `json:"segment_started_at"`
	UpdatedAt        string `json:"updated_at"`
}

type retainedStep struct {
	Version          int    `json:"version"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	Revision         int    `json:"revision"`
	TaskHash         string `json:"task_hash"`
	Segment          int    `json:"segment"`
	Step             int    `json:"step"`
	Payload          string `json:"payload"`
	Count            int    `json:"count"`
	CompletedAt      string `json:"completed_at"`
}

type retainedSnapshot struct {
	Version          int    `json:"version"`
	Status           string `json:"status"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	Revision         int    `json:"revision"`
	Segment          int    `json:"segment"`
	CompletedSteps   int    `json:"completed_steps"`
	TotalSteps       int    `json:"total_steps"`
	IntervalMS       int64  `json:"interval_ms"`
	WorkerPID        int    `json:"worker_pid"`
	SegmentStartedAt string `json:"segment_started_at"`
	UpdatedAt        string `json:"updated_at"`
	EffectCount      int    `json:"effect_count"`
	LivenessChecked  bool   `json:"liveness_checked"`
}

func isRetainedCommand(sub string) bool {
	return sub == "retained-start" || sub == "retained-inspect" || sub == "retained-resume"
}

// A stopped retained process must not hang on its final diagnostic when a native
// exec/PTY merges stderr into the same full stream as stdout. This affects only
// retained commands; the caller still exits with its original fixed error code.
func writeRetainedError(value any) {
	done := make(chan struct{}, 1)
	go func() {
		json.NewEncoder(os.Stderr).Encode(value)
		done <- struct{}{}
	}()
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func parseRetained(args []string) (retainedOptions, error) {
	o := retainedOptions{sub: args[0]}
	fs := flag.NewFlagSet(o.sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.dir, "dir", "", "existing absolute private task directory")
	fs.StringVar(&o.nonce, "nonce", "", "retained task identifier")
	var interval string
	if o.sub != "retained-inspect" {
		fs.StringVar(&o.controller, "controller-thread", "", "native controller thread")
	}
	if o.sub == "retained-start" {
		fs.IntVar(&o.steps, "steps", 0, "1 to 32 fixed synthetic steps")
		fs.StringVar(&interval, "interval", "", "integer milliseconds, at least 100ms")
	}
	if o.sub == "retained-resume" {
		fs.IntVar(&o.revision, "revision", 0, "expected task revision")
		fs.IntVar(&o.segment, "segment", 0, "expected last execution segment")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !filepath.IsAbs(o.dir) || !safeNonce.MatchString(o.nonce) {
		return o, failure("invalid_args")
	}
	o.dir = filepath.Clean(o.dir)
	if o.sub != "retained-inspect" && !safeNonce.MatchString(o.controller) {
		return o, failure("invalid_args")
	}
	if o.sub == "retained-resume" && (!validRevision(o.revision) || !validRevision(o.segment)) {
		return o, failure("invalid_args")
	}
	if o.sub == "retained-start" {
		var err error
		o.interval, err = time.ParseDuration(interval)
		if err != nil || o.interval%time.Millisecond != 0 || !validRetainedBounds(o.steps, o.interval.Milliseconds()) {
			return o, failure("invalid_args")
		}
	}
	return o, nil
}

func validRetainedBounds(steps int, interval int64) bool {
	return steps >= 1 && steps <= 32 && interval >= 100 && interval <= 120000/int64(steps)
}

func retainedTaskHash(task retainedTask) string {
	data, _ := json.Marshal(task)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func retainedTimestamp(value string) bool {
	t, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && t.UTC().Format(time.RFC3339Nano) == value
}

func retainedLater(a, b string) string {
	x, _ := time.Parse(time.RFC3339Nano, a)
	y, _ := time.Parse(time.RFC3339Nano, b)
	if y.After(x) {
		return b
	}
	return a
}

// The only allowed two-link state is our own interrupted Link publication,
// whose fixed staging path is the same inode. Foreign hard links are rejected.
func retainedTrustedFile(root *os.Root, name string, info os.FileInfo, stage bool) bool {
	if !owned(info, 0600) || info.Size() > 4096 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if stat.Nlink == 1 {
		return true
	}
	if stat.Nlink != 2 || name == "retained.lock" {
		return false
	}
	other := "." + name + ".tmp"
	if stage {
		if len(name) < 6 {
			return false
		}
		other = name[1 : len(name)-4]
	}
	paired, err := root.Lstat(other)
	if err == nil && owned(paired, 0600) && os.SameFile(info, paired) {
		return true
	}
	// The publisher may have removed its staging link after our initial Stat.
	// Recheck the same inode at the original path before calling it foreign.
	current, err := root.Lstat(name)
	if err != nil || !owned(current, 0600) || current.Size() > 4096 || !os.SameFile(info, current) {
		return false
	}
	currentStat, ok := current.Sys().(*syscall.Stat_t)
	return ok && currentStat.Nlink == 1
}

func retainedReadJSON(root *os.Root, name string, value any) error {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return failure("untrusted_file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= 0 || !retainedTrustedFile(root, name, info, false) {
		return failure("untrusted_file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return failure("untrusted_file")
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return failure("snapshot_changed")
	}
	return strictDecode(data, value)
}

func openRetainedLock(root *os.Root, acquire bool) (*os.File, error) {
	f, err := root.OpenFile("retained.lock", os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, failure("untrusted_file")
	}
	fail := func(err error) (*os.File, error) { f.Close(); return nil, err }
	info, err := f.Stat()
	if err != nil || !retainedTrustedFile(root, "retained.lock", info, false) {
		return fail(failure("untrusted_file"))
	}
	var lock struct {
		Version int `json:"version"`
	}
	if err := retainedReadJSON(root, "retained.lock", &lock); err != nil {
		return fail(err)
	}
	if lock.Version != 1 {
		return fail(failure("invalid_artifact"))
	}
	if acquire {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return fail(failure("busy"))
		}
		if err != nil {
			return fail(failure("lock_failed"))
		}
	}
	current, err := root.Lstat("retained.lock")
	if err != nil || !os.SameFile(info, current) || !retainedTrustedFile(root, "retained.lock", current, false) {
		return fail(failure("untrusted_file"))
	}
	return f, nil
}

func retainedStepName(step int) string { return fmt.Sprintf("step-%03d.json", step) }

func retainedNames(task retainedTask) map[string]bool {
	names := map[string]bool{"retained.lock": true, "retained-task.json": true, "retained-state.json": true, ".retained-task.json.tmp": true, ".retained-state.json.tmp": true}
	for n := 1; n <= task.TotalSteps; n++ {
		name := retainedStepName(n)
		names[name], names["."+name+".tmp"] = true, true
	}
	return names
}

func validateRetainedNames(root *os.Root, task retainedTask) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(70)
	if err != nil && err != io.EOF {
		return err
	}
	allowed := retainedNames(task)
	if len(names) > len(allowed) {
		return failure("unexpected_artifact")
	}
	for _, name := range names {
		if !allowed[name] {
			return failure("unexpected_artifact")
		}
	}
	return nil
}

func retainedReadTask(root *os.Root, nonce string) (retainedTask, error) {
	var task retainedTask
	if err := retainedReadJSON(root, "retained-task.json", &task); err != nil {
		return task, err
	}
	if task.Nonce != nonce {
		return task, failure("nonce_mismatch")
	}
	if task.Version != 1 || !safeNonce.MatchString(task.ControllerThread) || task.Revision != 1 || !validRetainedBounds(task.TotalSteps, task.IntervalMS) {
		return task, failure("invalid_artifact")
	}
	return task, nil
}

func validateRetainedState(task retainedTask, state retainedState) error {
	if state.Version != 1 || state.Nonce != task.Nonce || state.ControllerThread != task.ControllerThread || state.Revision != task.Revision || state.TaskHash != retainedTaskHash(task) || !validRevision(state.Segment) || state.WorkerPID <= 0 || state.CompletedSteps < 0 || state.CompletedSteps > task.TotalSteps || !retainedTimestamp(state.SegmentStartedAt) || !retainedTimestamp(state.UpdatedAt) || retainedLater(state.SegmentStartedAt, state.UpdatedAt) != state.UpdatedAt {
		return failure("invalid_artifact")
	}
	if state.Status != "running" && state.Status != "interrupted" && state.Status != "completed" {
		return failure("invalid_artifact")
	}
	if state.Status == "completed" && state.CompletedSteps != task.TotalSteps {
		return failure("invalid_artifact")
	}
	return nil
}

// Read-only inspection never acquires/replaces the execution lock, cleans a
// stage, or changes running to interrupted. Progress is a validated prefix of
// immutable effects, which may be ahead of the cached mutable state after death.
func retainedReadState(root *os.Root, task retainedTask) (retainedState, error) {
	var zero retainedState
	for attempt := 0; attempt < 3; attempt++ {
		var state retainedState
		if err := retainedReadJSON(root, "retained-state.json", &state); err != nil {
			if err == failure("snapshot_changed") {
				continue
			}
			return zero, err
		}
		if err := validateRetainedState(task, state); err != nil {
			return zero, err
		}
		if err := validateRetainedNames(root, task); err != nil {
			return zero, err
		}
		original := state
		count, previousSegment, gap := 0, 0, false
		var scanErr error
		for n := 1; n <= task.TotalSteps; n++ {
			var step retainedStep
			err := retainedReadJSON(root, retainedStepName(n), &step)
			if errors.Is(err, os.ErrNotExist) {
				gap = true
				continue
			}
			if err != nil {
				scanErr = err
				break
			}
			if gap || step.Version != 1 || step.Nonce != task.Nonce || step.ControllerThread != task.ControllerThread || step.Revision != task.Revision || step.TaskHash != state.TaskHash || step.Segment < 1 || step.Segment > state.Segment || step.Segment < previousSegment || step.Step != n || step.Payload != "synthetic_step_complete" || step.Count != 1 || !retainedTimestamp(step.CompletedAt) {
				scanErr = failure("invalid_artifact")
				break
			}
			count, previousSegment = n, step.Segment
			state.UpdatedAt = retainedLater(state.UpdatedAt, step.CompletedAt)
		}
		var after retainedState
		if err := retainedReadJSON(root, "retained-state.json", &after); err != nil {
			if err == failure("snapshot_changed") {
				continue
			}
			return zero, err
		}
		if original != after {
			continue
		}
		if scanErr != nil {
			return zero, scanErr
		}
		if state.CompletedSteps > count {
			return zero, failure("invalid_artifact")
		}
		state.CompletedSteps = count
		if count == task.TotalSteps {
			state.Status = "completed"
		}
		return state, nil
	}
	return zero, failure("snapshot_changed")
}

func retainedSnapshotOf(task retainedTask, state retainedState) retainedSnapshot {
	return retainedSnapshot{1, state.Status, task.Nonce, task.ControllerThread, task.Revision, state.Segment, state.CompletedSteps, task.TotalSteps, task.IntervalMS, state.WorkerPID, state.SegmentStartedAt, state.UpdatedAt, state.CompletedSteps, false}
}

func clearRetainedStage(root *os.Root, name string) error {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return failure("untrusted_file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !retainedTrustedFile(root, name, info, true) {
		return failure("untrusted_file")
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return failure("untrusted_file")
	}
	return root.Remove(name)
}

func cleanRetainedStages(root *os.Root, task retainedTask) error {
	for name := range retainedNames(task) {
		if name[0] == '.' {
			if err := clearRetainedStage(root, name); err != nil {
				return err
			}
		}
	}
	// Also makes any visible effect publication durable before resuming, even if
	// the old process died between Link and its directory sync.
	return syncDir(root)
}

func writeRetainedState(root *os.Root, state retainedState) error {
	const stage = ".retained-state.json.tmp"
	if err := clearRetainedStage(root, stage); err != nil {
		return err
	}
	if err := writeNew(root, stage, state); err != nil {
		return err
	}
	defer root.Remove(stage)
	if err := root.Rename(stage, "retained-state.json"); err != nil {
		return err
	}
	return syncDir(root)
}

// Only this goroutine writes task state/effects. The cancellable output goroutine
// writes stdout bytes only, so signal/deadline handling cannot release the lease
// while another business writer remains active.
func runRetainedSegment(ctx context.Context, root *os.Root, task retainedTask, state retainedState, parent int) error {
	emit := func() error {
		value := retainedSnapshotOf(task, state)
		done := make(chan error, 1)
		go func() { done <- json.NewEncoder(os.Stdout).Encode(value) }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	stop := func(cause error) error {
		if state.CompletedSteps == task.TotalSteps {
			state.Status = "completed"
		} else {
			state.Status = "interrupted"
			state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if err := writeRetainedState(root, state); err != nil {
			return failure("checkpoint_uncertain")
		}
		if errors.Is(cause, context.Canceled) && context.Cause(ctx) == failure("parent_exited") {
			return failure("parent_exited")
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return failure("timeout")
		}
		if errors.Is(cause, context.Canceled) {
			return failure("interrupted")
		}
		return cause
	}
	if os.Getppid() != parent {
		return stop(failure("parent_exited"))
	}
	if err := emit(); err != nil {
		return stop(err)
	}
	for state.CompletedSteps < task.TotalSteps {
		if os.Getppid() != parent {
			return stop(failure("parent_exited"))
		}
		timer := time.NewTimer(time.Duration(task.IntervalMS) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return stop(ctx.Err())
		case <-timer.C:
		}
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		if os.Getppid() != parent {
			return stop(failure("parent_exited"))
		}
		next := state.CompletedSteps + 1
		now := time.Now().UTC().Format(time.RFC3339Nano)
		step := retainedStep{1, task.Nonce, task.ControllerThread, task.Revision, state.TaskHash, state.Segment, next, "synthetic_step_complete", 1, now}
		if err := publish(root, retainedStepName(next), step); err != nil {
			return failure("checkpoint_uncertain")
		}
		state.CompletedSteps, state.UpdatedAt = next, now
		if next == task.TotalSteps {
			state.Status = "completed"
		}
		// Publication precedes output. A killed or blocked response can leave the
		// cache behind, but the already durable synthetic effect will never repeat.
		if err := emit(); err != nil {
			return stop(err)
		}
		if err := writeRetainedState(root, state); err != nil {
			return failure("checkpoint_uncertain")
		}
	}
	return nil
}

func runRetained(args []string) error {
	o, err := parseRetained(args)
	if err != nil {
		return err
	}
	if o.sub != "retained-inspect" && os.Getenv("CODEX_THREAD_ID") != o.controller {
		return failure("thread_mismatch")
	}
	parent := os.Getppid()
	if o.sub != "retained-inspect" && parent <= 1 {
		return failure("orphaned_parent")
	}
	root, err := openPrivateDir(o.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if o.sub == "retained-start" {
		dir, err := root.Open(".")
		if err != nil {
			return err
		}
		names, readErr := dir.Readdirnames(1)
		dir.Close()
		if len(names) != 0 || readErr != io.EOF {
			return failure("dir_not_empty")
		}
		if err := writeNew(root, "retained.lock", struct {
			Version int `json:"version"`
		}{1}); err != nil {
			return failure("already_started")
		}
		if err := syncDir(root); err != nil {
			return err
		}
	}
	lock, err := openRetainedLock(root, o.sub != "retained-inspect")
	if err != nil {
		return err
	}
	defer lock.Close()
	if o.sub == "retained-inspect" {
		task, err := retainedReadTask(root, o.nonce)
		if err != nil {
			return err
		}
		state, err := retainedReadState(root, task)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(retainedSnapshotOf(task, state))
	}
	interrupted, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	parentContext, parentExited := context.WithCancelCause(interrupted)
	defer parentExited(nil)
	ctx, cancel := context.WithTimeout(parentContext, 120*time.Second)
	defer cancel()
	// This observes only our direct launch parent, not an inferred TUI identity.
	// It owns no task writes. Parent loss also interrupts blocked stdout output.
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			if os.Getppid() != parent {
				parentExited(failure("parent_exited"))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	var task retainedTask
	var state retainedState
	if o.sub == "retained-start" {
		task = retainedTask{1, o.nonce, o.controller, 1, o.steps, o.interval.Milliseconds()}
		if err := publish(root, "retained-task.json", task); err != nil {
			return failure("checkpoint_uncertain")
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		state = retainedState{1, "running", task.Nonce, task.ControllerThread, 1, retainedTaskHash(task), 1, 0, os.Getpid(), now, now}
	} else {
		task, err = retainedReadTask(root, o.nonce)
		if err != nil {
			return err
		}
		state, err = retainedReadState(root, task)
		if err != nil {
			return err
		}
		if task.ControllerThread != o.controller {
			return failure("owner_mismatch")
		}
		if task.Revision != o.revision {
			return failure("stale_revision")
		}
		if state.Segment != o.segment {
			return failure("stale_segment")
		}
		if err := cleanRetainedStages(root, task); err != nil {
			return err
		}
		if state.Status == "completed" {
			var cached retainedState
			if err := retainedReadJSON(root, "retained-state.json", &cached); err != nil {
				return err
			}
			if cached != state {
				if err := writeRetainedState(root, state); err != nil {
					return failure("checkpoint_uncertain")
				}
			}
			return runRetainedSegment(ctx, root, task, state, parent)
		}
		if !validRevision(state.Segment + 1) {
			return failure("segment_limit")
		}
		state.Segment++
		state.Status, state.WorkerPID = "running", os.Getpid()
		state.SegmentStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
		state.UpdatedAt = state.SegmentStartedAt
	}
	if err := writeRetainedState(root, state); err != nil {
		return failure("checkpoint_uncertain")
	}
	return runRetainedSegment(ctx, root, task, state, parent)
}
