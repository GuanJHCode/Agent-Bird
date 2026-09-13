// g0-return-lab is a bounded synthetic execution host for macOS experiments.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"
)

type failure string

func (f failure) Error() string { return string(f) }

type options struct {
	sub, dir, nonce                    string
	duration                           time.Duration
	controller, deliveryID, eventsJSON string
	eventID, actionSlot                string
	eventRevision                      int
	revision                           int
	eventHash                          string
	commandID                          string
	decision                           string
	cancel                             bool
}

type job struct {
	Version          int    `json:"version"`
	Status           string `json:"status"`
	Nonce            string `json:"nonce"`
	WorkerPID        int    `json:"worker_pid"`
	CreatedAt        string `json:"created_at"`
	JobFile          string `json:"job_file"`
	ResultFile       string `json:"result_file"`
	ControllerThread string `json:"controller_thread,omitempty"`
	TaskRevision     int    `json:"task_revision,omitempty"`
}

type result struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Nonce   string `json:"nonce"`
	Count   int    `json:"count"`
}

type claim struct {
	Version int    `json:"version"`
	Nonce   string `json:"nonce"`
}

var safeNonce = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		code := "io_error"
		var f failure
		if errors.As(err, &f) {
			code = string(f)
		}
		// Do not print paths, arbitrary file data, environment, or OS error strings.
		output := struct {
			Version int    `json:"version"`
			Status  string `json:"status"`
			Error   string `json:"error"`
		}{1, "error", code}
		if len(os.Args) > 1 && isRetainedCommand(os.Args[1]) {
			writeRetainedError(output)
		} else {
			json.NewEncoder(os.Stderr).Encode(output)
		}
		if code == "timeout" {
			os.Exit(3)
		}
		os.Exit(2)
	}
}

func parse(args []string) (options, error) {
	var o options
	if len(args) == 0 {
		return o, failure("invalid_args")
	}
	o.sub = args[0]
	fs := flag.NewFlagSet(o.sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.dir, "dir", "", "existing absolute private task directory")
	fs.StringVar(&o.nonce, "nonce", "", "task identifier")
	var duration string
	switch o.sub {
	case "start":
		fs.StringVar(&o.controller, "controller-thread", "", "native controller thread")
		fs.StringVar(&duration, "delay", "", "100ms to 60s")
	case "_worker":
		fs.StringVar(&duration, "delay", "", "100ms to 60s")
	case "wait":
		fs.StringVar(&duration, "timeout", "", "100ms to 120s")
	case "ack", "revise":
		fs.StringVar(&o.controller, "controller-thread", "", "native controller thread")
		fs.IntVar(&o.revision, "revision", 0, "expected current revision")
		if o.sub == "ack" {
			fs.StringVar(&o.eventHash, "event-hash", "", "SHA-256 of the bound result envelope")
			fs.StringVar(&o.commandID, "command-id", "", "request identity")
			fs.StringVar(&o.decision, "decision", "", "handled, waiting_user, stale, or rejected")
		} else {
			fs.BoolVar(&o.cancel, "cancel", false, "terminal cancellation of future ACKs")
		}
	case "batch-prepare":
		fs.StringVar(&o.controller, "controller-thread", "", "native controller thread")
		fs.IntVar(&o.revision, "revision", 0, "expected current revision")
		fs.StringVar(&o.deliveryID, "delivery-id", "", "business delivery identity")
		fs.StringVar(&o.eventsJSON, "events-json", "", "bounded event metadata JSON")
	case "batch-ack":
		fs.StringVar(&o.controller, "controller-thread", "", "native controller thread")
		fs.IntVar(&o.revision, "revision", 0, "expected current revision")
		fs.StringVar(&o.eventID, "event-id", "", "business event identity")
		fs.IntVar(&o.eventRevision, "event-revision", 0, "event revision")
		fs.StringVar(&o.eventHash, "event-hash", "", "event payload hash")
		fs.StringVar(&o.actionSlot, "action-slot", "", "business action slot")
		fs.StringVar(&o.commandID, "command-id", "", "request identity")
		fs.StringVar(&o.decision, "decision", "", "handled, waiting_user, stale, or rejected")
	case "batch-status":
	case "batch-uncertain":
		fs.StringVar(&o.controller, "controller-thread", "", "native controller thread")
		fs.IntVar(&o.revision, "revision", 0, "expected current revision")
	case "batch-claim":
		fs.StringVar(&o.controller, "controller-thread", "", "native controller thread")
		fs.IntVar(&o.revision, "revision", 0, "expected current revision")
	case "read", "inspect":
	default:
		return o, failure("invalid_args")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !filepath.IsAbs(o.dir) || !safeNonce.MatchString(o.nonce) {
		return o, failure("invalid_args")
	}
	o.dir = filepath.Clean(o.dir)
	ownerProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "controller-thread" {
			ownerProvided = true
		}
	})
	if (ownerProvided || o.sub == "ack" || o.sub == "revise") && !safeNonce.MatchString(o.controller) {
		return o, failure("invalid_args")
	}
	if (o.sub == "ack" || o.sub == "revise" || o.sub == "batch-prepare" || o.sub == "batch-ack" || o.sub == "batch-uncertain" || o.sub == "batch-claim") && !validRevision(o.revision) {
		return o, failure("invalid_args")
	}
	if o.sub == "ack" && (!safeHash.MatchString(o.eventHash) || !safeNonce.MatchString(o.commandID) || !validDecision(o.decision)) {
		return o, failure("invalid_args")
	}
	if o.sub == "batch-prepare" && (!safeDelivery.MatchString(o.deliveryID) || len(o.eventsJSON) == 0 || len(o.eventsJSON) > 4096) {
		return o, failure("invalid_args")
	}
	if o.sub == "batch-ack" && (!safeDelivery.MatchString(o.eventID) || !validRevision(o.eventRevision) || !safeHash.MatchString(o.eventHash) || !safeDelivery.MatchString(o.actionSlot) || !safeNonce.MatchString(o.commandID) || !validDecision(o.decision)) {
		return o, failure("invalid_args")
	}
	if o.sub == "start" || o.sub == "_worker" || o.sub == "wait" {
		var err error
		o.duration, err = time.ParseDuration(duration)
		max := 60 * time.Second
		if o.sub == "wait" {
			max = 120 * time.Second
		}
		if err != nil || o.duration < 100*time.Millisecond || o.duration > max {
			return o, failure("invalid_args")
		}
	}
	return o, nil
}

func run(args []string) error {
	if runtime.GOOS != "darwin" {
		return failure("unsupported_platform")
	}
	if len(args) > 0 && isRetainedCommand(args[0]) {
		return runRetained(args)
	}
	o, err := parse(args)
	if err != nil {
		return err
	}
	if o.controller != "" && os.Getenv("CODEX_THREAD_ID") != o.controller {
		return failure("thread_mismatch")
	}
	root, err := openPrivateDir(o.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	switch o.sub {
	case "start":
		j, err := start(root, o)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(j)
	case "_worker":
		return work(root, o)
	case "inspect", "ack", "revise":
		value, err := controlCommand(root, o)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(value)
	case "batch-prepare", "batch-ack", "batch-status", "batch-uncertain", "batch-claim":
		value, err := batchCommand(root, o)
		if err != nil {
			return err
		}
		if o.sub == "batch-ack" && os.Getenv("G0_TEST_BLOCK_AFTER_COMMIT") == "1" {
			select {}
		}
		return json.NewEncoder(os.Stdout).Encode(value)
	default:
		deadline := time.Now().Add(o.duration)
		for {
			r, err := readResult(root, o.nonce)
			if err != nil {
				return err
			}
			if r.Status == "completed" || o.sub == "read" {
				return json.NewEncoder(os.Stdout).Encode(r)
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return failure("timeout")
			}
			time.Sleep(min(100*time.Millisecond, remaining))
		}
	}
}

func owned(info os.FileInfo, mode os.FileMode) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode() == mode
}

func openPrivateDir(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil || !owned(info, os.ModeDir|0700) {
		return nil, failure("untrusted_dir")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, failure("untrusted_dir")
	}
	current, err := root.Stat(".")
	if err != nil || !owned(current, os.ModeDir|0700) || !os.SameFile(info, current) {
		root.Close()
		return nil, failure("untrusted_dir")
	}
	return root, nil
}

func strictDecode(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return failure("invalid_artifact")
	}
	if d.Decode(new(any)) != io.EOF {
		return failure("invalid_artifact")
	}
	return nil
}

func readJSON(root *os.Root, name string, value any) error {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return failure("untrusted_file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !owned(info, 0600) || info.Size() <= 0 || info.Size() > 4096 {
		return failure("untrusted_file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return failure("untrusted_file")
	}
	return strictDecode(data, value)
}

func syncDir(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func writeNew(root *os.Root, name string, value any) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(value); err != nil {
		return err
	}
	return f.Sync()
}

func publish(root *os.Root, name string, value any) error {
	// Link publishes a complete file atomically and refuses any existing target.
	// A bounded, owner-only fixed stage from a prior interrupted attempt is
	// recovered before the next publication. Published destinations are never
	// removed by this recovery.
	stage := "." + name + ".tmp"
	if err := clearUnpublishedStage(root, stage); err != nil {
		return err
	}
	if err := writeNew(root, stage, value); err != nil {
		return err
	}
	maybeCrashBatchPublish(name, "before_link")
	defer root.Remove(stage)
	if err := root.Link(stage, name); err != nil {
		return err
	}
	maybeCrashBatchPublish(name, "after_link")
	if err := root.Remove(stage); err != nil {
		return err
	}
	return syncDir(root)
}

func start(root *os.Root, o options) (job, error) {
	var zero job
	if _, err := root.Lstat("claim.json"); err == nil {
		return zero, failure("already_started")
	}
	dir, err := root.Open(".")
	if err != nil {
		return zero, err
	}
	names, err := dir.Readdirnames(1)
	dir.Close()
	if len(names) != 0 || err != io.EOF {
		return zero, failure("dir_not_empty")
	}
	if err := writeNew(root, "claim.json", claim{1, o.nonce}); err != nil {
		return zero, failure("already_started")
	}
	if err := syncDir(root); err != nil {
		return zero, failure("start_failed")
	}
	if o.controller != "" {
		if err := initializeControl(root, o); err != nil {
			return zero, failure("start_failed")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return zero, failure("start_failed")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return zero, failure("start_failed")
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.Command(executable, "_worker", "--dir", o.dir, "--nonce", o.nonce, "--delay", o.duration.String())
	// No shell, no environment rewrite, and no caller stdio or controlling TTY.
	cmd.Dir = o.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{writer}
	if cmd.Start() != nil {
		return zero, failure("start_failed")
	}
	defer cmd.Process.Release()
	writer.Close()
	if reader.SetReadDeadline(time.Now().Add(5*time.Second)) != nil {
		return zero, failure("start_uncertain")
	}
	data, err := io.ReadAll(io.LimitReader(reader, 4097))
	var ready job
	if err != nil || len(data) > 4096 || strictDecode(data, &ready) != nil {
		return zero, failure("start_uncertain")
	}
	onDisk, err := readJob(root, o.nonce)
	if err != nil || onDisk != ready || ready.WorkerPID != cmd.Process.Pid || ready.ControllerThread != o.controller {
		return zero, failure("start_uncertain")
	}
	return ready, nil
}

func readJob(root *os.Root, nonce string) (job, error) {
	var j job
	if err := readJSON(root, "job.json", &j); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return j, failure("job_missing")
		}
		return j, err
	}
	if j.Nonce != nonce {
		return j, failure("nonce_mismatch")
	}
	_, timeErr := time.Parse(time.RFC3339Nano, j.CreatedAt)
	if j.Version != 1 || j.Status != "started" || j.WorkerPID <= 0 || timeErr != nil || j.JobFile != "job.json" || j.ResultFile != "result.json" {
		return j, failure("invalid_artifact")
	}
	if (j.ControllerThread == "" && j.TaskRevision != 0) || (j.ControllerThread != "" && (!safeNonce.MatchString(j.ControllerThread) || j.TaskRevision != 1)) {
		return j, failure("invalid_artifact")
	}
	return j, nil
}

func readResult(root *os.Root, nonce string) (result, error) {
	r := result{1, "pending", nonce, 0}
	if _, err := readJob(root, nonce); err != nil {
		return r, err
	}
	var completed result
	if err := readJSON(root, "result.json", &completed); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return r, err
	}
	if completed.Nonce != nonce {
		return r, failure("nonce_mismatch")
	}
	if completed.Version != 1 || completed.Status != "completed" || completed.Count != 1 {
		return r, failure("invalid_artifact")
	}
	return completed, nil
}

func work(root *os.Root, o options) error {
	ready := os.NewFile(3, "ready")
	if ready == nil {
		return failure("start_failed")
	}
	defer ready.Close()
	var c claim
	if err := readJSON(root, "claim.json", &c); err != nil || c.Version != 1 || c.Nonce != o.nonce {
		return failure("invalid_claim")
	}
	j := job{Version: 1, Status: "started", Nonce: o.nonce, WorkerPID: os.Getpid(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), JobFile: "job.json", ResultFile: "result.json"}
	if _, err := root.Lstat("control.json"); err == nil {
		c, err := readControl(root, o.nonce)
		if err != nil || c.Revision != 1 || c.Cancelled || c.ControllerThread != os.Getenv("CODEX_THREAD_ID") {
			return failure("invalid_artifact")
		}
		j.ControllerThread, j.TaskRevision = c.ControllerThread, c.Revision
	} else if !errors.Is(err, os.ErrNotExist) {
		return failure("invalid_artifact")
	}
	if err := publish(root, "job.json", j); err != nil {
		return failure("start_failed")
	}
	// Losing the starting caller must not cancel an already-owned synthetic task.
	json.NewEncoder(ready).Encode(j)
	ready.Close()
	time.Sleep(o.duration)
	onDisk, err := readJob(root, o.nonce)
	if err != nil || onDisk != j {
		return failure("invalid_artifact")
	}
	return publish(root, "result.json", result{1, "completed", o.nonce, 1})
}
