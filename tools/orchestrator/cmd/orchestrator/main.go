package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/admincli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/instance"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/nativebridgecli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type codeError string

func (e codeError) Error() string { return string(e) }

type remoteError string

func (e remoteError) Error() string { return string(e) }

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"version": 1, "status": "error", "error": errorCode(err)})
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return codeError("invalid_args")
	}
	switch args[0] {
	case "runtime-status":
		return runtimeStatus(ctx, args[1:], stdout)
	case "routing":
		return routingEntry(ctx, args[1:], stdout)
	case "provider":
		return providerSettingsEntry(ctx, args[1:], stdout)
	case "task":
		return taskEntry(ctx, args[1:], stdout)
	case "preflight":
		return preflight(args[1:], stdout)
	case "marketplace-check":
		return marketplaceCheck(ctx, args[1:], stdout)
	case "plugin-run":
		return pluginRun(ctx, args[1:], stdout)
	case "owner-bind":
		return ownerBind(ctx, args[1:], stdout)
	case "controller":
		return controllerEntry(ctx, args[1:])
	case "provider-probe", "provider-lock":
		return providerControl(ctx, args[0], args[1:], stdout)
	case "serve":
		return serve(ctx, args[1:], stderr)
	case "source-host":
		return sourceHost(ctx, args[1:])
	case "gitops-worker":
		return runGitopsWorker(ctx, args[1:], stdout)
	case "install", "doctor", "uninstall", "pin", "unpin":
		return runAdmin(args[0], args[1:], stdout)
	case "ensure-running":
		state, err := parseStateOnly(args[1:])
		if err != nil {
			return err
		}
		ready, err := ensureServer(ctx, state)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(ready)
	case "submit":
		return submit(ctx, args[1:], stdout)
	case "status":
		return taskControl(ctx, ipc.KindStatus, args[1:], stdout)
	case "summary":
		return taskControl(ctx, ipc.KindSummary, args[1:], stdout)
	case "collect":
		return taskControl(ctx, ipc.KindCollect, args[1:], stdout)
	case "wait-events":
		return waitEvents(ctx, args[1:], stdout)
	case "resume":
		return taskControl(ctx, ipc.KindResume, args[1:], stdout)
	case "retry":
		return retryTask(ctx, args[1:], stdout)
	case "rework":
		return reworkTask(ctx, args[1:], stdout)
	case "accept":
		return reviewResult(ctx, args[1:], stdout)
	case "answer":
		return answerQuestion(ctx, args[1:], stdout)
	case "report":
		return reportEvent(ctx, args[1:], stdout)
	case "native-bridge":
		return runNativeBridge(ctx, args[1:], stdout, stderr)
	case "recover-host":
		return recoverHost(ctx, args[1:], stdout)
	case "rebind-owner":
		return rebindOwner(ctx, args[1:], stdout)
	case "stop":
		return taskControl(ctx, ipc.KindStopTask, args[1:], stdout)
	case "ack":
		return acknowledge(ctx, args[1:], stdout)
	default:
		return codeError("invalid_args")
	}
}

func requestFile(args []string) (string, error) {
	fs := flag.NewFlagSet("request", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("request", "", "owner-only JSON request")
	if fs.Parse(args) != nil || *path == "" || fs.NArg() != 0 {
		return "", codeError("invalid_args")
	}
	return *path, nil
}

func runNativeBridge(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return codeError("invalid_args")
	}
	if args[0] == "rebind" {
		return rebindOwner(ctx, args[1:], stdout)
	}
	path, err := requestFile(args[1:])
	if err != nil {
		return err
	}
	var response json.RawMessage
	switch args[0] {
	case "helper":
		return nativebridgecli.RunHelper(ctx, path, os.Stdin, stdout, stderr)
	case "start":
		response, err = nativebridgecli.StartDriver(ctx, path)
	case "driver":
		return nativebridgecli.RunDriver(ctx, path, stdout, stderr)
	case "decide":
		response, err = nativebridgecli.RecordDriverDecision(path)
	case "status":
		response, err = nativebridgecli.DriverStatus(path)
	default:
		return codeError("invalid_args")
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response, '\n'))
	return err
}

func rebindOwner(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rebind-owner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	path := fs.String("request", "", "verified owner rebind request")
	if fs.Parse(args) != nil || *path == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	var projection map[string]json.RawMessage
	if err := readPrivateJSON(*path, &projection); err != nil {
		return err
	}
	if string(projection["owner_mode"]) == `"local"` {
		return rebindLocalOwner(ctx, *stateArg, *path, stdout)
	}
	verified, err := nativebridgecli.VerifyRebind(*path)
	if err != nil {
		return err
	}
	capability, err := loadControlCapability(verified.ControlFile)
	if err != nil {
		return err
	}
	if capability.ControllerThread != verified.ControllerThread {
		return codeError("owner_mismatch")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	bootstrap, err := loadHostBootstrap(capability)
	if err != nil {
		return err
	}
	if bootstrap.Version != 2 {
		return codeError("legacy_bootstrap_rebind_unsupported")
	}
	request := coordinator.RebindOwnerRequest{RunID: capability.RunID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, OriginContextID: verified.OriginContextID, OriginPID: verified.OriginPID, OriginBirth: verified.OriginBirth, HostGeneration: verified.HostGeneration, AttachmentProofSHA256: verified.AttachmentProofSHA256}
	response, err := call(ctx, state, ipc.KindRebindOwner, request)
	if err != nil {
		return err
	}
	bootstrap.Hello.OriginPID, bootstrap.Hello.OriginBirth = verified.OriginPID, verified.OriginBirth
	bootstrap.Hello.OriginContextID, bootstrap.Hello.HostGeneration = verified.OriginContextID, verified.HostGeneration
	if err = replacePrivateJSON(capability.BootstrapPath, bootstrapMetadata(bootstrap)); err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

func runGitopsWorker(ctx context.Context, args []string, stdout io.Writer) error {
	path, err := requestFile(args)
	if err != nil {
		return err
	}
	return gitopsworker.Run(ctx, path, stdout)
}

func runAdmin(operation string, args []string, stdout io.Writer) error {
	path, err := requestFile(args)
	if err != nil {
		return err
	}
	return admincli.Run(operation, path, stdout)
}

func serve(parent context.Context, args []string, stderr io.Writer) error {
	state, err := parseStateOnly(args)
	if err != nil {
		return err
	}
	server, err := coordinator.NewServer(state)
	if err != nil {
		return err
	}
	defer server.Close()
	if err = writePID(state); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(state, "coordinator.pid"))
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	serveErr := server.Serve(ctx)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	shutdownCancel()
	if serveErr != nil {
		return serveErr
	}
	if shutdownErr != nil && !errors.Is(shutdownErr, context.DeadlineExceeded) {
		_, _ = fmt.Fprintf(stderr, "shutdown: %v\n", shutdownErr)
		return shutdownErr
	}
	return nil
}

func submit(ctx context.Context, args []string, stdout io.Writer) error {
	return submitWithPreparedControl(ctx, args, stdout, nil)
}

// Persist the high-level recovery route before any submission can reach the coordinator.
func submitWithPreparedControl(ctx context.Context, args []string, stdout io.Writer, onPrepared func(string) error) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	requestPath := fs.String("request", "", "owner-only JSON request")
	if fs.Parse(args) != nil || *requestPath == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	var request coordinator.SubmitRequest
	if err = readPrivateJSON(*requestPath, &request); err != nil {
		return err
	}
	if err = preflightTasks(request.Tasks); err != nil {
		return err
	}
	executable, err := resolvedExecutable()
	if err != nil {
		return err
	}
	request.HostExecutable = executable
	if request.HostGeneration == "" || len(request.Tasks) == 0 {
		return codeError("invalid_submit")
	}
	if request.OwnerMode == "local" {
		if err = verifyLocalProjection(request); err != nil {
			return err
		}
	} else if request.OwnerCapability == "" {
		if os.Getenv("ORCHESTRATOR_ENABLE_TEST_FAKE") != "1" {
			return codeError("owner_capability_required")
		}
	} else {
		owner, verifyErr := nativebridgecli.VerifyOwnerCapability(request.OwnerCapability)
		if verifyErr != nil || owner.ControllerThread != request.ControllerThread || owner.OriginContextID != request.OriginContextID || owner.OriginPID != request.OriginPID || owner.OriginBirth != request.OriginBirth || owner.HostGeneration != request.HostGeneration {
			return codeError("owner_capability_mismatch")
		}
	}
	originBirth, err := process.Birth(request.OriginPID)
	if err != nil || originBirth != request.OriginBirth {
		return codeError("origin_identity_mismatch")
	}
	if err = requireTaskCoordinator(ctx, state, request.Tasks); err != nil {
		return err
	}
	launchID, err := randomValue("host-launch")
	if err != nil {
		return err
	}
	launchToken, err := randomValue("host-token")
	if err != nil {
		return err
	}
	controlToken, err := randomValue("control-token")
	if err != nil {
		return err
	}
	bootstrap := hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "host-spool", launchID), ProducerID: launchID, Hello: contract.HostHello{LaunchID: launchID, LaunchToken: launchToken, OriginContextID: request.OriginContextID, OriginPID: request.OriginPID, OriginBirth: request.OriginBirth, HostGeneration: request.HostGeneration, Executable: executable}}
	bootstrapPath, err := writeBootstrap(state, bootstrap)
	if err != nil {
		return err
	}
	controlPath, err := writeControlCapability(state, request.RunID, request.ControllerThread, controlToken, bootstrapPath, launchID, launchToken)
	if err != nil {
		_ = os.Remove(bootstrapPath)
		return err
	}
	pinned := make([]string, 0, len(request.Tasks))
	for _, task := range request.Tasks {
		if _, pinErr := install.PinRunningVersion(executable, task.ID); pinErr != nil {
			if os.Getenv("ORCHESTRATOR_ENABLE_TEST_FAKE") == "1" && pinErr.Error() == "running_package_invalid" {
				continue
			}
			for _, taskID := range pinned { _ = install.UnpinRunningVersion(executable, taskID) }
			_ = os.Remove(controlPath)
			_ = os.Remove(bootstrapPath)
			return pinErr
		}
		pinned = append(pinned, task.ID)
	}
	if onPrepared != nil {
		if err = onPrepared(controlPath); err != nil {
			for _, taskID := range pinned { _ = install.UnpinRunningVersion(executable, taskID) }
			_ = os.Remove(controlPath)
			_ = os.Remove(bootstrapPath)
			return err
		}
	}
	request.LaunchID, request.LaunchToken, request.ControlToken = launchID, launchToken, controlToken
	response, err := call(ctx, state, ipc.KindSubmit, request)
	if err != nil {
		var rejected remoteError
		if errors.As(err, &rejected) {
			for _, taskID := range pinned { _ = install.UnpinRunningVersion(executable, taskID) }
			_ = os.Remove(controlPath)
			_ = os.Remove(bootstrapPath)
			return err
		}
		_ = startSourceHost(executable, state, bootstrap)
		_ = json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "submit_uncertain", "run_id": request.RunID, "control_file": controlPath})
		return codeError("submit_uncertain")
	}
	var receipt coordinator.SubmitResponse
	if err = decode(response.Payload, &receipt); err != nil || receipt.LaunchID != launchID || receipt.LaunchToken != launchToken || receipt.ControlToken != controlToken {
		return codeError("submit_receipt_mismatch")
	}
	if err = startSourceHost(executable, state, bootstrap); err != nil {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "queued_host_unavailable", "run_id": request.RunID, "control_file": controlPath})
		return codeError("source_host_start_failed")
	}
	if err = waitForHost(ctx, state, receipt.LaunchID, receipt.LaunchToken); err != nil {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "queued_host_unavailable", "run_id": request.RunID, "control_file": controlPath})
		return codeError("source_host_start_timeout")
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "queued", "run_id": request.RunID, "control_file": controlPath})
}

type controlCapability struct {
	Version          int    `json:"version"`
	RunID            string `json:"run_id"`
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	BootstrapPath    string `json:"bootstrap_path"`
	HostLaunchID     string `json:"host_launch_id,omitempty"`
	HostLaunchToken  string `json:"host_launch_token,omitempty"`
}

func taskControl(ctx context.Context, kind ipc.Kind, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet(string(kind), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	taskID := fs.String("task-id", "", "task id")
	controlFile := fs.String("control-file", "", "owner-only control capability")
	revision := fs.Int("work-revision", 0, "work revision")
	cursor := fs.String("cursor", "", "opaque delivery cursor")
	includeDiagnostics := fs.Bool("include-diagnostics", false, "include durable progress diagnostics")
	if fs.Parse(args) != nil || *taskID == "" || *controlFile == "" || fs.NArg() != 0 || (kind != ipc.KindCollect && (*cursor != "" || *includeDiagnostics)) {
		return codeError("invalid_args")
	}
	if (kind == ipc.KindResume || kind == ipc.KindStopTask) && *revision < 1 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	capability, err := loadControlCapability(*controlFile)
	if err != nil {
		return err
	}
	if kind == ipc.KindResume {
		if err = requireInterruptionCoordinator(ctx, state); err != nil {
			return err
		}
		response, queryErr := call(ctx, state, ipc.KindSummary, coordinator.TaskControlRequest{TaskID: *taskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken})
		if queryErr != nil {
			return queryErr
		}
		var summary store.RunSummary
		if err = decode(response.Payload, &summary); err != nil {
			return err
		}
		for _, task := range summary.Tasks {
			if task.TaskID == *taskID && task.ResumeBlockedReason != "" {
				return codeError(task.ResumeBlockedReason)
			}
		}
		if err = ensureSourceHost(ctx, state, capability); err != nil {
			return err
		}
	}
	control := coordinator.TaskControlRequest{TaskID: *taskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, WorkRevision: *revision}
	request := any(control)
	if kind == ipc.KindCollect {
		request = coordinator.CollectRequest{TaskControlRequest: control, Cursor: *cursor, IncludeDiagnostics: *includeDiagnostics}
	}
	response, err := call(ctx, state, kind, request)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

func waitEvents(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("wait-events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	taskID := fs.String("task-id", "", "task id")
	controlFile := fs.String("control-file", "", "owner-only control capability")
	cursor := fs.String("cursor", "", "opaque delivery cursor")
	timeoutMS := fs.Int("timeout-ms", 30_000, "bounded wait in milliseconds")
	if fs.Parse(args) != nil || *taskID == "" || *controlFile == "" || *timeoutMS < 1 || *timeoutMS > 30_000 || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	capability, err := loadControlCapability(*controlFile)
	if err != nil {
		return err
	}
	request := coordinator.WaitEventsRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: *taskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken}, Cursor: *cursor, TimeoutMS: *timeoutMS}
	response, err := call(ctx, state, ipc.KindWaitEvents, request)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

type acceptFile struct {
	TaskID        string `json:"task_id"`
	ControlFile   string `json:"control_file"`
	WorkRevision  int    `json:"work_revision"`
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	EventHash     string `json:"event_hash"`
	ActionSlot    string `json:"action_slot"`
	Decision      string `json:"decision"`
	CommandID     string `json:"command_id"`
}

func reviewResult(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("accept", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	requestPath := fs.String("request", "", "owner-only review request")
	var input acceptFile
	fs.StringVar(&input.TaskID, "task-id", "", "task id")
	fs.StringVar(&input.ControlFile, "control-file", "", "owner-only control capability")
	fs.IntVar(&input.WorkRevision, "work-revision", 0, "work revision")
	fs.StringVar(&input.EventID, "event-id", "", "result event id")
	fs.Int64Var(&input.EventRevision, "event-revision", 0, "result event revision")
	fs.StringVar(&input.EventHash, "event-hash", "", "result event hash")
	fs.StringVar(&input.ActionSlot, "action-slot", "", "result action slot")
	fs.StringVar(&input.Decision, "decision", "accept", "accept or reject")
	fs.StringVar(&input.CommandID, "command-id", "", "stable review command id")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	requestSet, businessFlagSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "request":
			requestSet = true
		case "state-dir":
		default:
			businessFlagSet = true
		}
	})
	invalidInput := "invalid_args"
	if requestSet {
		if *requestPath == "" || businessFlagSet {
			return codeError("invalid_args")
		}
		// JSON requests must explicitly choose a decision; the flag default applies
		// only to the legacy flag interface.
		input = acceptFile{}
		if err := readPrivateJSON(*requestPath, &input); err != nil {
			if errorCode(err) == "invalid_json" {
				return codeError("invalid_review")
			}
			return err
		}
		invalidInput = "invalid_review"
	}
	if input.TaskID == "" || input.ControlFile == "" || input.WorkRevision < 1 || input.EventID == "" || input.EventRevision < 1 || input.EventHash == "" || input.ActionSlot == "" || input.CommandID == "" || (input.Decision != "accept" && input.Decision != "reject") {
		return codeError(invalidInput)
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	capability, err := loadControlCapability(input.ControlFile)
	if err != nil {
		return err
	}
	request := coordinator.ReviewRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: input.TaskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, WorkRevision: input.WorkRevision}, EventID: input.EventID, EventRevision: input.EventRevision, EventHash: input.EventHash, ActionSlot: input.ActionSlot, Decision: input.Decision, CommandID: input.CommandID}
	response, err := call(ctx, state, ipc.KindAccept, request)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

func retryTask(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("retry", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	taskID := fs.String("task-id", "", "task id")
	controlFile := fs.String("control-file", "", "owner-only control capability")
	revision := fs.Int("work-revision", 0, "work revision")
	eventID := fs.String("event-id", "", "failed event id")
	eventRevision := fs.Int64("event-revision", 0, "failed event revision")
	eventHash := fs.String("event-hash", "", "failed event payload hash")
	actionSlot := fs.String("action-slot", "", "failed event action slot")
	segmentID := fs.String("segment-id", "", "failed segment id")
	nextAttemptNo := fs.Int("next-attempt", 0, "next attempt number in the frozen budget group")
	commandID := fs.String("command-id", "", "stable retry command id")
	useNext := fs.Bool("use-next-fallback", false, "use the next pre-authorized adapter")
	if fs.Parse(args) != nil || *taskID == "" || *controlFile == "" || *revision < 1 || *eventID == "" || *eventRevision < 1 || *eventHash == "" || *actionSlot == "" || *segmentID == "" || *nextAttemptNo < 2 || *commandID == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	capability, err := loadControlCapability(*controlFile)
	if err != nil {
		return err
	}
	if err = ensureSourceHost(ctx, state, capability); err != nil {
		return err
	}
	request := coordinator.RetryRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: *taskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, WorkRevision: *revision}, EventID: *eventID, EventRevision: *eventRevision, EventHash: *eventHash, ActionSlot: *actionSlot, SegmentID: *segmentID, NextAttemptNo: *nextAttemptNo, UseNextFallback: *useNext, CommandID: *commandID}
	response, err := call(ctx, state, ipc.KindRetry, request)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

type answerFile struct {
	TaskID           string `json:"task_id"`
	ControlFile      string `json:"control_file"`
	WorkRevision     int    `json:"work_revision"`
	QuestionID       string `json:"question_id"`
	QuestionRevision int    `json:"question_revision"`
	Answer           string `json:"answer"`
}

func answerQuestion(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	requestPath := fs.String("request", "", "owner-only answer request")
	if fs.Parse(args) != nil || *requestPath == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	var request answerFile
	if err = readPrivateJSON(*requestPath, &request); err != nil || request.TaskID == "" || request.ControlFile == "" || request.WorkRevision < 1 || request.QuestionID == "" || request.QuestionRevision < 1 || strings.TrimSpace(request.Answer) == "" {
		return codeError("invalid_answer")
	}
	capability, err := loadControlCapability(request.ControlFile)
	if err != nil {
		return err
	}
	if err = ensureSourceHost(ctx, state, capability); err != nil {
		return err
	}
	payload := coordinator.AnswerRequest{TaskID: request.TaskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, WorkRevision: request.WorkRevision, QuestionID: request.QuestionID, QuestionRevision: request.QuestionRevision, Answer: request.Answer}
	response, err := call(ctx, state, ipc.KindAnswer, payload)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

func acknowledge(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	requestPath := fs.String("request", "", "owner-only delivery acknowledgement")
	if fs.Parse(args) != nil || *requestPath == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	var request ackFile
	if err = readPrivateJSON(*requestPath, &request); err != nil || request.Version != 1 || request.TaskID == "" || request.ControlFile == "" || request.DeliveryID == "" || (request.HistoryProofSHA256 == "" && request.CollectionProofSHA256 == "") || len(request.Decisions) == 0 {
		return codeError("invalid_ack")
	}
	capability, err := loadControlCapability(request.ControlFile)
	if err != nil {
		return err
	}
	payload := coordinator.AckRequest{TaskID: request.TaskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, DeliveryID: request.DeliveryID, HistoryProofSHA256: request.HistoryProofSHA256, CollectionProofSHA256: request.CollectionProofSHA256, Decisions: request.Decisions}
	response, err := call(ctx, state, ipc.KindAck, payload)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

type ackFile struct {
	CollectionProofSHA256 string              `json:"collection_proof_sha256,omitempty"`
	Version               int                 `json:"version"`
	TaskID                string              `json:"task_id"`
	ControlFile           string              `json:"control_file"`
	DeliveryID            string              `json:"delivery_id"`
	HistoryProofSHA256    string              `json:"history_proof_sha256"`
	Decisions             []store.AckDecision `json:"decisions"`
}

type reportCapabilityFile struct {
	Version        int    `json:"version"`
	SocketPath     string `json:"socket_path"`
	CapabilityID   string `json:"capability_id"`
	Token          string `json:"token"`
	RunID          string `json:"run_id"`
	TaskID         string `json:"task_id"`
	AttemptID      string `json:"attempt_id"`
	SegmentID      string `json:"segment_id"`
	ProducerID     string `json:"producer_id"`
	WorkRevision   int    `json:"work_revision"`
	ExecutionEpoch uint64 `json:"execution_epoch"`
}

type reportFile struct {
	Version  int             `json:"version"`
	EventID  string          `json:"event_id"`
	Sequence int64           `json:"sequence"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

func reportEvent(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	capabilityPath := fs.String("capability-file", "", "segment report capability")
	requestPath := fs.String("request", "", "structured report request")
	if fs.Parse(args) != nil || *capabilityPath == "" || *requestPath == "" || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	var capability reportCapabilityFile
	if err := readPrivateJSON(*capabilityPath, &capability); err != nil || capability.Version != 1 || capability.SocketPath == "" || capability.CapabilityID == "" || capability.Token == "" {
		return codeError("invalid_report_capability")
	}
	var request reportFile
	if err := readPrivateJSON(*requestPath, &request); err != nil || request.Version != 1 {
		return codeError("invalid_report_event")
	}
	payload, _ := json.Marshal(coordinator.ReportEventRequest{CapabilityID: capability.CapabilityID, Token: capability.Token, EventID: request.EventID, Sequence: request.Sequence, Kind: request.Kind, Payload: request.Payload})
	response, err := ipc.Call(ctx, capability.SocketPath, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReportEvent, RequestID: "report-" + request.EventID, Payload: payload})
	if err != nil {
		return err
	}
	if response.Kind == ipc.KindError {
		var remote coordinator.ErrorResponse
		if decode(response.Payload, &remote) != nil || remote.Error == "" {
			return codeError("remote_error")
		}
		return codeError(remote.Error)
	}
	if response.Kind != ipc.KindResponse {
		return codeError("invalid_response")
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

func call(ctx context.Context, state string, kind ipc.Kind, payload any) (ipc.Envelope, error) {
	if _, err := ensureServer(ctx, state); err != nil {
		return ipc.Envelope{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ipc.Envelope{}, err
	}
	requestID := fmt.Sprintf("control-%d-%d", os.Getpid(), time.Now().UnixNano())
	response, err := ipc.Call(ctx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: requestID, Payload: body})
	if err != nil {
		return ipc.Envelope{}, err
	}
	if response.Kind == ipc.KindError {
		var remote coordinator.ErrorResponse
		if decode(response.Payload, &remote) != nil || remote.Error == "" {
			return ipc.Envelope{}, codeError("remote_error")
		}
		return ipc.Envelope{}, remoteError(remote.Error)
	}
	if response.Kind != ipc.KindResponse {
		return ipc.Envelope{}, codeError("invalid_response")
	}
	return response, nil
}

func ensureServer(ctx context.Context, state string) (map[string]any, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	timeoutError := func() error {
		if parent.Err() != nil {
			return parent.Err()
		}
		return codeError("coordinator_start_timeout")
	}
	if err := instance.PrepareStateDir(state); err != nil {
		return nil, err
	}
	ready := func() (map[string]any, error) {
		requestID := fmt.Sprintf("ready-%d", os.Getpid())
		response, err := ipc.Call(ctx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: requestID, Payload: json.RawMessage(`{}`)})
		if err != nil {
			return nil, err
		}
		var value struct {
			Status       string   `json:"status"`
			Epoch        uint64   `json:"epoch"`
			Capabilities []string `json:"capabilities"`
		}
		if response.Kind != ipc.KindResponse || response.RequestID != requestID || json.Unmarshal(response.Payload, &value) != nil || value.Status != "ready" || value.Epoch == 0 || response.Epoch != value.Epoch {
			return nil, ipc.ErrInvalidMessage
		}
		return map[string]any{"status": value.Status, "epoch": value.Epoch, "capabilities": value.Capabilities}, nil
	}
	if value, err := ready(); err == nil {
		return value, nil
	} else if errors.Is(err, ipc.ErrInvalidMessage) || errors.Is(err, ipc.ErrMessageTooLarge) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, timeoutError()
	}
	executable, err := resolvedExecutable()
	if err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(state, "coordinator.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	command := exec.Command(executable, "serve", "--state-dir", state)
	command.Stdin, command.Stdout, command.Stderr = nil, logFile, logFile
	command.Env = coordinatorEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if value, readyErr := ready(); readyErr == nil {
			return value, nil
		} else if errors.Is(readyErr, ipc.ErrInvalidMessage) || errors.Is(readyErr, ipc.ErrMessageTooLarge) {
			return nil, readyErr
		}
		select {
		case <-ctx.Done():
			return nil, timeoutError()
		case <-ticker.C:
		}
	}
}

func coordinatorEnvironment() []string {
	out := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	if temp := os.TempDir(); temp != "" {
		out = append(out, "TMPDIR="+temp)
	}
	return out
}

type hostBootstrap struct {
	Version    int                `json:"version"`
	SocketPath string             `json:"socket_path"`
	SpoolRoot  string             `json:"spool_root"`
	ProducerID string             `json:"producer_id"`
	Hello      contract.HostHello `json:"hello"`
}

// hostBootstrapMetadataV2 is the token-free disk contract, distinct from the
// in-memory/v1 wire object. Strict decoding rejects injected token fields.
type hostBootstrapMetadataV2 struct {
	Version    int               `json:"version"`
	SocketPath string            `json:"socket_path"`
	SpoolRoot  string            `json:"spool_root"`
	ProducerID string            `json:"producer_id"`
	Hello      hostHelloMetadata `json:"hello"`
}
type hostHelloMetadata struct {
	LaunchID        string `json:"launch_id"`
	OriginContextID string `json:"origin_context_id"`
	OriginPID       int    `json:"origin_pid"`
	OriginBirth     string `json:"origin_birth"`
	HostGeneration  string `json:"host_generation"`
	PID             int    `json:"pid"`
	Birth           string `json:"birth"`
	Executable      string `json:"executable"`
}

func bootstrapMetadata(b hostBootstrap) hostBootstrapMetadataV2 {
	h := b.Hello
	return hostBootstrapMetadataV2{Version: 2, SocketPath: b.SocketPath, SpoolRoot: b.SpoolRoot, ProducerID: b.ProducerID, Hello: hostHelloMetadata{LaunchID: h.LaunchID, OriginContextID: h.OriginContextID, OriginPID: h.OriginPID, OriginBirth: h.OriginBirth, HostGeneration: h.HostGeneration, PID: h.PID, Birth: h.Birth, Executable: h.Executable}}
}
func loadHostBootstrap(c controlCapability) (hostBootstrap, error) {
	var b hostBootstrap
	if c.Version == 1 {
		if c.HostLaunchID != "" || c.HostLaunchToken != "" {
			return b, codeError("invalid_control_capability")
		}
		if err := readPrivateJSON(c.BootstrapPath, &b); err != nil {
			return b, err
		}
		if b.Version != 1 {
			return hostBootstrap{}, codeError("invalid_host_bootstrap")
		}
		return b, nil
	}
	if c.Version != 2 || c.HostLaunchID == "" || c.HostLaunchToken == "" {
		return b, codeError("invalid_control_capability")
	}
	var m hostBootstrapMetadataV2
	if err := readPrivateJSON(c.BootstrapPath, &m); err != nil {
		return b, err
	}
	if m.Version != 2 || m.Hello.LaunchID != c.HostLaunchID || m.ProducerID != c.HostLaunchID {
		return b, codeError("invalid_host_bootstrap")
	}
	h := m.Hello
	return hostBootstrap{Version: 2, SocketPath: m.SocketPath, SpoolRoot: m.SpoolRoot, ProducerID: m.ProducerID, Hello: contract.HostHello{LaunchID: h.LaunchID, LaunchToken: c.HostLaunchToken, OriginContextID: h.OriginContextID, OriginPID: h.OriginPID, OriginBirth: h.OriginBirth, HostGeneration: h.HostGeneration, PID: h.PID, Birth: h.Birth, Executable: h.Executable}}, nil
}

func sourceHost(parent context.Context, args []string) error {
	fs := flag.NewFlagSet("source-host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	bootstrapPath := fs.String("bootstrap", "", "legacy owner-only source bootstrap")
	bootstrapFD := fs.Int("bootstrap-fd", -1, "inherited bootstrap pipe")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	var hasFD, hasPath bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "bootstrap-fd" {
			hasFD = true
		}
		if f.Name == "bootstrap" {
			hasPath = true
		}
	})
	if hasFD == hasPath {
		return codeError("invalid_args")
	}
	var bootstrap hostBootstrap
	var err error
	if hasFD {
		bootstrap, err = readBootstrapFD(*bootstrapFD)
	} else {
		// Read-only compatibility for old private v1 material. Never accept v2
		// metadata as a complete bootstrap or look for a token in that file.
		err = readPrivateJSON(*bootstrapPath, &bootstrap)
		if err == nil && bootstrap.Version != 1 {
			err = codeError("invalid_host_bootstrap")
		}
	}
	if err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	if !validHostBootstrap(state, bootstrap) {
		return codeError("invalid_host_bootstrap")
	}

	birth, err := process.Birth(os.Getpid())
	if err != nil {
		return err
	}
	executable, err := resolvedExecutable()
	if err != nil {
		return err
	}
	bootstrap.Hello.PID, bootstrap.Hello.Birth, bootstrap.Hello.Executable = os.Getpid(), birth, executable
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	err = host.RunSource(ctx, host.SourceConfig{
		SocketPath: bootstrap.SocketPath, SpoolRoot: bootstrap.SpoolRoot,
		ProducerID: bootstrap.ProducerID, Hello: bootstrap.Hello,
		InvocationProvider: func(ctx context.Context, grant contract.LaunchCommand) (contract.InvocationView, error) {
			var discriminator struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(grant.AdapterPayload, &discriminator) == nil && discriminator.Kind == "gitops" {
				return gitopsworker.BuildInvocation(grant.AdapterPayload, executable, bootstrap.SpoolRoot, grant.CommandID)
			}
			return invocationForGrant(ctx, grant)
		},
	})
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type invocationPayload = adapter.InvocationPayload

type localInvocation struct {
	action *adapter.CandidateAction
	args   []string
	dir    string
	stdin  []byte
	env    map[string]string
}

func (i localInvocation) CandidateAction() *adapter.CandidateAction { return i.action }

func (i localInvocation) Args() []string           { return append([]string(nil), i.args...) }
func (i localInvocation) WorkingDirectory() string { return i.dir }
func (i localInvocation) Stdin() []byte            { return append([]byte(nil), i.stdin...) }
func (i localInvocation) Environment() map[string]string {
	out := make(map[string]string, len(i.env))
	for key, value := range i.env {
		out[key] = value
	}
	return out
}

func invocationForGrant(ctx context.Context, grant contract.LaunchCommand) (contract.InvocationView, error) {
	payload, err := adapter.DecodeInvocationPayload(grant.AdapterPayload)
	if err != nil {
		return nil, codeError("invalid_json")
	}
	if action := payload.CandidateAction; action != nil {
		if action.Version != 1 || grant.Input == nil || action.SourceTask != grant.Input.Event.TaskID || payload.CandidateWorkspace != nil || grant.SessionID != "" || payload.SessionID != "" || !filepath.IsAbs(payload.Directory) {
			return nil, codeError("candidate_action_invalid")
		}
		if payload.Kind == "candidate" {
			if payload.Provider != "" || payload.Profile != nil || payload.ProviderLock != nil || payload.BinaryPath != "" || len(payload.Args) != 0 || len(payload.ExtraArgs) != 0 {
				return nil, codeError("candidate_action_invalid")
			}
			if action.Operation != "validate" && action.Operation != "integrate" {
				return nil, codeError("candidate_action_invalid")
			}
			return localInvocation{args: []string{"/usr/bin/true"}, dir: payload.Directory, action: action}, nil
		}
		if action.Operation != "review" || payload.Profile == nil || payload.Profile.Role != adapter.Reviewer || payload.Profile.Permission != adapter.ReadOnly {
			return nil, codeError("candidate_action_invalid")
		}
		brief, err := host.CandidateReviewPrompt(grant)
		if err != nil {
			return nil, err
		}
		payload.Prompt += brief
	}
	if payload.ProviderLock != nil {
		lock := payload.ProviderLock
		if payload.BinaryPath != "" || payload.BinaryVersion != "" || payload.BinarySHA256 != "" {
			return nil, codeError("provider_lock_legacy_conflict")
		}
		payload.BinaryPath, payload.BinaryVersion, payload.BinarySHA256 = lock.Binary.Path, lock.Binary.Version, lock.Binary.SHA256
	}
	if payload.Provider == string(adapter.ProviderCodex) || payload.BinaryVersion == adapter.CodexVersion || strings.EqualFold(payload.BinarySHA256, adapter.CodexSHA256) {
		return nil, codeError("codex_trial_guard_not_ready")
	}
	if grant.Answer != "" {
		if grant.QuestionID == "" || grant.QuestionRevision < 1 || grant.SessionKind == "" || grant.SessionID == "" || payload.Kind == "fake" {
			return nil, codeError("invalid_question_resume")
		}
		payload.Prompt, payload.SessionKind, payload.SessionID = grant.Answer, grant.SessionKind, grant.SessionID
	}
	if payload.Kind == "fake" {
		if os.Getenv("ORCHESTRATOR_ENABLE_TEST_FAKE") != "1" || len(payload.Args) == 0 || payload.Directory == "" {
			return nil, codeError("fake_invocation_disabled")
		}
		return localInvocation{args: payload.Args, dir: payload.Directory, env: map[string]string{}}, nil
	}
	request := adapter.Request{Provider: adapter.Provider(payload.Provider), Binary: adapter.BinaryPin{Path: payload.BinaryPath, Version: payload.BinaryVersion, SHA256: payload.BinarySHA256}, CWD: payload.Directory, Prompt: payload.Prompt, Session: adapter.SessionRef{Kind: adapter.SessionKind(payload.SessionKind), ID: payload.SessionID}, Permission: adapter.Permission{Mode: payload.PermissionMode, Allow: payload.Allow, Deny: payload.Deny}, ExtraArgs: payload.ExtraArgs}
	request.Profile, request.Lock, request.Workspace, request.Action = payload.Profile, payload.ProviderLock, payload.CandidateWorkspace, payload.CandidateAction
	if err := adapter.VerifyExecutable(request.Binary, request.Binary.Version); err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	version, err := adapter.ProbeOutput(probeCtx, request.Binary.Path, "--version")
	if err != nil {
		return nil, codeError("binary_version_probe_failed")
	}
	if err = adapter.VerifyExecutable(request.Binary, strings.TrimSpace(string(version))); err != nil {
		return nil, err
	}
	if request.Profile != nil {
		help, err := adapter.ProbeOutput(probeCtx, request.Binary.Path, "--help")
		if err != nil {
			return nil, err
		}
		if err = adapter.CheckCapabilities(request, help); err != nil {
			return nil, err
		}
	}
	return adapter.BuildInvocation(request)
}

func waitForHost(ctx context.Context, state, launchID, launchToken string) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := call(ctx, state, ipc.KindHostStatus, coordinator.HostStatusRequest{LaunchID: launchID, LaunchToken: launchToken})
		if err == nil {
			var status struct {
				Status string `json:"status"`
			}
			if decode(response.Payload, &status) == nil && status.Status == "ready" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return codeError("source_host_start_timeout")
		case <-ticker.C:
		}
	}
}

const maxBootstrapBytes = 64 * 1024

// readBootstrapFD consumes the inherited pipe before any Host/Worker starts.
// Close-on-exec is set immediately; even error paths close the descriptor.
func readBootstrapFD(fd int) (hostBootstrap, error) {
	var bootstrap hostBootstrap
	if fd < 3 {
		return bootstrap, codeError("invalid_bootstrap_fd")
	}
	// Inherited descriptors start in blocking mode. Set O_NONBLOCK before
	// wrapping so Go registers the pipe with its poller and enforces deadlines.
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return bootstrap, codeError("invalid_bootstrap_fd")
	}
	file := os.NewFile(uintptr(fd), "host-bootstrap-pipe")
	if file == nil {
		return bootstrap, codeError("invalid_bootstrap_fd")
	}
	defer file.Close()
	syscall.CloseOnExec(fd)
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return bootstrap, codeError("invalid_bootstrap_fd")
	}
	if err = file.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return bootstrap, codeError("invalid_bootstrap_fd")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBootstrapBytes+1))
	if err != nil || len(data) > maxBootstrapBytes || decode(data, &bootstrap) != nil || (bootstrap.Version != 1 && bootstrap.Version != 2) {
		return hostBootstrap{}, codeError("invalid_host_bootstrap")
	}
	return bootstrap, nil
}

func validHostBootstrap(state string, b hostBootstrap) bool {
	return (b.Version == 1 || b.Version == 2) && b.SocketPath == filepath.Join(state, "coordinator.sock") && b.ProducerID != "" && b.ProducerID == b.Hello.LaunchID && b.Hello.LaunchToken != ""
}

func startSourceHost(executable, state string, bootstrap hostBootstrap) error {
	if !validHostBootstrap(state, bootstrap) {
		return codeError("invalid_host_bootstrap")
	}
	data, err := json.Marshal(bootstrap)
	if err != nil || len(data) > maxBootstrapBytes {
		return codeError("invalid_host_bootstrap")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return codeError("bootstrap_pipe_failed")
	}
	defer reader.Close()
	defer writer.Close()
	command := exec.Command(executable, "source-host", "--state-dir", state, "--bootstrap-fd", "3")
	command.ExtraFiles = []*os.File{reader}
	command.Stdin = nil
	logFile, err := os.OpenFile(filepath.Join(state, "source-host.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command.Stdout, command.Stderr = logFile, logFile
	command.Env = sourceEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		return err
	}
	defer command.Process.Release()
	_ = reader.Close()
	if err = writer.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return codeError("bootstrap_pipe_failed")
	}
	if _, err = writer.Write(data); err != nil {
		return codeError("bootstrap_pipe_failed")
	}
	return nil
}

func sourceEnvironment() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		key := value
		if index := strings.IndexByte(value, '='); index >= 0 {
			key = value[:index]
		}
		if strings.HasPrefix(key, "ORCHESTRATOR_") && key != "ORCHESTRATOR_ENABLE_TEST_FAKE" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func writeControlCapability(state, runID, controller, token, bootstrapPath, launchID, launchToken string) (string, error) {
	dir := filepath.Join(state, "control")
	if err := privateSubdir(dir); err != nil {
		return "", err
	}
	name, err := randomValue("run")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".json")
	return path, writeExclusiveJSON(path, controlCapability{Version: 2, RunID: runID, ControllerThread: controller, ControlToken: token, BootstrapPath: bootstrapPath, HostLaunchID: launchID, HostLaunchToken: launchToken})
}

func writeBootstrap(state string, bootstrap hostBootstrap) (string, error) {
	dir := filepath.Join(state, "host-bootstrap")
	if err := privateSubdir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, bootstrap.Hello.LaunchID+".json")
	return path, writeExclusiveJSON(path, bootstrapMetadata(bootstrap))
}

func privateSubdir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return codeError("untrusted_private_dir")
	}
	return nil
}

func writeExclusiveJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func replacePrivateJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return codeError("untrusted_private_file")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	suffix, err := randomValue("replace")
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+suffix)
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, path)
	}
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}

func loadControlCapability(path string) (controlCapability, error) {
	var capability controlCapability
	err := readPrivateJSON(path, &capability)
	if err == nil && ((capability.Version != 1 && capability.Version != 2) || capability.ControllerThread == "" || capability.ControlToken == "" || !filepath.IsAbs(capability.BootstrapPath)) {
		err = codeError("invalid_control_capability")
	}
	if err == nil && ((capability.Version == 2 && (capability.HostLaunchID == "" || capability.HostLaunchToken == "")) || (capability.Version == 1 && (capability.HostLaunchID != "" || capability.HostLaunchToken != ""))) {
		err = codeError("invalid_control_capability")
	}
	return capability, err
}

func ensureSourceHost(ctx context.Context, state string, capability controlCapability) error {
	bootstrap, err := loadHostBootstrap(capability)
	if err != nil {
		return err
	}
	response, err := call(ctx, state, ipc.KindHostStatus, coordinator.HostStatusRequest{LaunchID: bootstrap.Hello.LaunchID, LaunchToken: bootstrap.Hello.LaunchToken})
	if err == nil {
		var status coordinator.StatusResponse
		if decode(response.Payload, &status) == nil && status.Status == "ready" {
			return nil
		}
	}
	executable, err := resolvedExecutable()
	if err != nil {
		return err
	}
	if err = startSourceHost(executable, state, bootstrap); err != nil {
		return err
	}
	return waitForHost(ctx, state, bootstrap.Hello.LaunchID, bootstrap.Hello.LaunchToken)
}

func randomValue(prefix string) (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func readPrivateJSON(path string, target any) error {
	if !filepath.IsAbs(path) {
		return codeError("path_not_absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return codeError("untrusted_private_file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return decode(data, target)
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return codeError("invalid_json")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return codeError("invalid_json")
	}
	return nil
}

func parseStateOnly(args []string) (string, error) {
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state-dir", "", "private state directory")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return "", codeError("invalid_args")
	}
	return resolveState(*state)
}

func resolveState(value string) (string, error) {
	if value != "" {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return "", codeError("path_not_absolute")
		}
		return value, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "OpenAI", "Codex Orchestrator"), nil
}

func resolvedExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) {
		return "", codeError("executable_unavailable")
	}
	return path, nil
}

func writePID(state string) error {
	path := filepath.Join(state, "coordinator.pid")
	stage := path + ".tmp"
	_ = os.Remove(stage)
	file, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "%d\n", os.Getpid())
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(stage, path)
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded codeError
	if errors.As(err, &coded) {
		return coded.Error()
	}
	var remote remoteError
	if errors.As(err, &remote) {
		return remote.Error()
	}
	return err.Error()
}
