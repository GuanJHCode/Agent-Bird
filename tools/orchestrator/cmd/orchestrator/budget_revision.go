package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func reviseBudget(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("budget-revise", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "private state directory")
	taskID := fs.String("task-id", "", "anchor task in the budget group")
	controlFile := fs.String("control-file", "", "owner-only control capability")
	requestFile := fs.String("request", "", "owner-only absolute budget revision JSON file")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *taskID == "" || *controlFile == "" || *requestFile == "" {
		return codeError("invalid_args")
	}
	raw, err := readBudgetRevisionRequestFile(*requestFile)
	if err != nil {
		return err
	}
	budget, err := store.DecodeBudgetRevisionRequest(raw)
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
	raw, err = json.Marshal(budget)
	if err != nil {
		return err
	}
	response, err := call(ctx, state, ipc.KindBudgetRevise, coordinator.BudgetRevisionRequest{TaskID: *taskID, ControllerThread: capability.ControllerThread, ControlToken: capability.ControlToken, Request: raw})
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(response.Payload, '\n'))
	return err
}

func readBudgetRevisionRequestFile(path string) ([]byte, error) {
	invalid := codeError("budget_revision_request_file_invalid")
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
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() <= 0 || before.Size() > store.MaxBudgetRevisionRequestBytes {
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
