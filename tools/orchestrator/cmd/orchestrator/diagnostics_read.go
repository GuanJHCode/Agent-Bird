package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type diagnosticsCLIInput struct {
	read    store.DiagnosticsReadRequest
	delete  store.DiagnosticsDeleteRequest
	receipt store.DiagnosticsReceipt
	task    string
	kind    ipc.Kind
}

func diagnosticsCLIError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return codeError("diagnostics_cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return codeError("diagnostics_timeout")
	}
	// Keep stable public codes, never private path or database error detail.
	s := err.Error()
	valid := len(s) > 0 && len(s) <= 64
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			valid = false
			break
		}
	}
	if valid {
		return codeError(s)
	}
	return codeError("diagnostics_command_failed")
}

func readDiagnosticsRequestFile(path string) ([]byte, error) {
	invalid := codeError("diagnostics_request_file_invalid")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, invalid
	}
	if canonical, err := filepath.EvalSymlinks(path); err != nil || canonical != path {
		return nil, invalid
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, invalid
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 1 || before.Size() > 8192 {
		return nil, invalid
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, invalid
	}
	raw := make([]byte, before.Size())
	if _, err = io.ReadFull(io.NewSectionReader(f, 0, before.Size()), raw); err != nil {
		return nil, invalid
	}
	after, err := f.Stat()
	if err != nil || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return nil, invalid
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(after, current) || current.Mode() != before.Mode() {
		return nil, invalid
	}
	return raw, nil
}

func loadDiagnosticsCLIInput(action, path string) (in diagnosticsCLIInput, err error) {
	raw, err := readDiagnosticsRequestFile(path)
	if err != nil {
		return in, err
	}
	if action == "diagnostics-read" {
		in.kind = ipc.KindDiagnosticsRead
		if err = json.Unmarshal(raw, &in.read); err != nil {
			return in, codeError("diagnostics_request_invalid")
		}
		in.task = in.read.TaskID
	} else if action == "diagnostics-delete" {
		in.kind = ipc.KindDiagnosticsDelete
		if err = json.Unmarshal(raw, &in.delete); err != nil {
			return in, codeError("diagnostics_request_invalid")
		}
		in.task = in.delete.TaskID
	} else if action == "diagnostics-ack" {
		in.kind = ipc.KindDiagnosticsAck
		if err = json.Unmarshal(raw, &in.receipt); err != nil {
			return in, codeError("diagnostics_receipt_invalid")
		}
		in.task = in.receipt.Identity.TaskID
	} else {
		return in, codeError("invalid_args")
	}
	return in, nil
}

func performDiagnosticsCLI(ctx context.Context, state string, capability controlCapability, in diagnosticsCLIInput, out io.Writer) error {
	var payload any
	if in.kind == ipc.KindDiagnosticsRead {
		payload = coordinator.DiagnosticsReadRequest{ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, Request: in.read}
	} else if in.kind == ipc.KindDiagnosticsDelete {
		payload = coordinator.DiagnosticsDeleteRequest{ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, Request: in.delete}
	} else {
		payload = coordinator.DiagnosticsAckRequest{ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, Receipt: in.receipt}
	}
	response, err := call(ctx, state, in.kind, payload)
	if err != nil {
		return err
	}
	_, err = out.Write(append(response.Payload, '\n'))
	return err
}

func diagnosticsCLI(ctx context.Context, action string, args []string, out io.Writer) (err error) {
	defer func() { err = diagnosticsCLIError(err) }()
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	controlFile := fs.String("control-file", "", "original run control capability")
	requestFile := fs.String("request", "", "owner-only diagnostics request or exact receipt")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *controlFile == "" || *requestFile == "" {
		return codeError("invalid_args")
	}
	in, err := loadDiagnosticsCLIInput(action, *requestFile)
	if err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	capability, err := loadControlCapability(*controlFile)
	if err != nil {
		return err
	}
	return performDiagnosticsCLI(ctx, state, capability, in, out)
}

func taskDiagnosticsCLI(ctx context.Context, action string, args []string, out io.Writer) (err error) {
	defer func() { err = diagnosticsCLIError(err) }()
	fs := flag.NewFlagSet("task "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	handle := fs.String("handle", "", "original task handle")
	request := fs.String("request", "", "owner-only diagnostics request or exact receipt")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *handle == "" || *request == "" {
		return codeError("invalid_args")
	}
	in, err := loadDiagnosticsCLIInput(action, *request)
	if err != nil {
		return err
	}
	if err = privateHandleParent(*handle); err != nil {
		return err
	}
	var h taskHandle
	if err = readPrivateJSON(*handle, &h); err != nil {
		return err
	}
	if h.Version != 1 || h.RunID == "" || !filepath.IsAbs(h.StateDir) || h.ControlFile == "" || !slices.Contains(h.TaskIDs, in.task) {
		return codeError("task_handle_scope_mismatch")
	}
	capability, err := loadControlCapability(h.ControlFile)
	if err != nil {
		return err
	}
	if capability.RunID != h.RunID {
		return codeError("task_handle_scope_mismatch")
	}
	bootstrap, err := loadHostBootstrap(capability)
	if err != nil {
		return err
	}
	if bootstrap.SocketPath != filepath.Join(h.StateDir, "coordinator.sock") {
		return codeError("task_handle_scope_mismatch")
	}
	return performDiagnosticsCLI(ctx, h.StateDir, capability, in, out)
}
