package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/instance"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

type stringsFlag []string

func (s *stringsFlag) String() string     { return fmt.Sprint([]string(*s)) }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

type cliError string

func (e cliError) Error() string { return string(e) }

func main() {
	if err := run(os.Args[1:]); err != nil {
		code := "io_error"
		var ce cliError
		if errors.As(err, &ce) {
			code = string(ce)
		}
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"version": 1, "status": "error", "error": code})
		os.Exit(2)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return cliError("invalid_args")
	}
	switch args[0] {
	case "ensure-running":
		return ensureRunning(args[1:])
	case "submit":
		return submit(args[1:])
	case "status":
		return status(args[1:])
	case "collect":
		return collect(args[1:])
	case "ack":
		return ack(args[1:])
	default:
		return cliError("invalid_args")
	}
}
func stateFlag(fs *flag.FlagSet) (*string, *string) {
	state := fs.String("state-dir", "", "absolute private state directory")
	controller := fs.String("controller-thread", "", "bound controller thread")
	return state, controller
}
func openState(state string) (*instance.Lock, *store.DB, error) {
	if !filepath.IsAbs(state) {
		return nil, nil, cliError("path_not_absolute")
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(state, 0700); err != nil {
		return nil, nil, err
	}
	lock, err := instance.Acquire(state)
	if err != nil {
		return nil, nil, err
	}
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	return lock, db, nil
}
func ensureRunning(args []string) error {
	fs := flag.NewFlagSet("ensure-running", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	state := fs.String("state-dir", "", "absolute private state directory")
	if fs.Parse(args) != nil || *state == "" {
		return cliError("invalid_args")
	}
	lock, db, err := openState(*state)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "status": "ready", "epoch": lock.Epoch()})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	return nil
}
func submit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	state, controller := stateFlag(fs)
	runID := fs.String("run-id", "", "run id")
	taskID := fs.String("task-id", "", "task id")
	origin := fs.String("origin-context", "", "validated source context id")
	birth := fs.String("origin-birth", "", "validated source process birth")
	pid := fs.Int("origin-pid", 0, "validated source process pid")
	max := fs.Int("max-attempts", 3, "attempt limit")
	var deps stringsFlag
	fs.Var(&deps, "dependency", "upstream task id")
	if fs.Parse(args) != nil || *state == "" || *controller == "" || *runID == "" || *taskID == "" || *origin == "" || *birth == "" || *pid <= 0 {
		return cliError("invalid_args")
	}
	lock, db, err := openState(*state)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()
	c, err := coordinator.New(db, filepath.Join(*state, "spool"), 2)
	if err != nil {
		return err
	}
	err = c.Submit(context.Background(), *runID, *controller, *taskID, contract.HostIdentity{OriginContextID: *origin, HostGeneration: "", PID: *pid, Birth: *birth}, deps, *max)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "status": "queued", "run_id": *runID, "task_id": *taskID})
}
func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	state := fs.String("state-dir", "", "absolute private state directory")
	task := fs.String("task-id", "", "task id")
	if fs.Parse(args) != nil || *state == "" || *task == "" {
		return cliError("invalid_args")
	}
	lock, db, err := openState(*state)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()
	a, err := db.LatestAttempt(context.Background(), *task)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(a)
}
func collect(args []string) error {
	fs := flag.NewFlagSet("collect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	state := fs.String("state-dir", "", "absolute private state directory")
	task := fs.String("task-id", "", "task id")
	if fs.Parse(args) != nil || *state == "" || *task == "" {
		return cliError("invalid_args")
	}
	lock, db, err := openState(*state)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()
	c, err := coordinator.New(db, filepath.Join(*state, "spool"), 2)
	if err != nil {
		return err
	}
	events, err := c.Collect(context.Background(), *task)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(events)
}
func ack(args []string) error {
	fs := flag.NewFlagSet("ack", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	state := fs.String("state-dir", "", "absolute private state directory")
	task := fs.String("task-id", "", "task id")
	if fs.Parse(args) != nil || *state == "" || *task == "" {
		return cliError("invalid_args")
	}
	lock, db, err := openState(*state)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()
	c, err := coordinator.New(db, filepath.Join(*state, "spool"), 2)
	if err != nil {
		return err
	}
	if err = c.Ack(context.Background(), *task); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 1, "status": "acknowledged", "task_id": *task})
}
