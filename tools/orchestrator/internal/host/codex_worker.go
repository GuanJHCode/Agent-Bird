package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/execbridge"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// prepareCodexWorker keeps model IO in the read-only parent and all tool IO in
// a separate, network-denied executor. Neither process applies nested sandboxing.
func prepareCodexWorker(ctx context.Context, original process.Command, profile *adapter.ExecutionProfile, scratch string, review bool, state *codexRuntimeState) (cmd process.Command, failure error) {
	prepared, err := prepareCodexRuntime(ctx, original, profile, scratch, review, state)
	if err != nil {
		return original, err
	}
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, state.close())
		}
	}()
	home := state.home
	if _, err := os.Lstat(filepath.Join(home.source, "environments.toml")); !os.IsNotExist(err) {
		return original, errors.New("codex_source_environments_unverified")
	}
	helper, err := os.Executable()
	if err != nil {
		return original, err
	}
	helper, err = filepath.EvalSymlinks(helper)
	if err != nil {
		return original, err
	}
	// The executor is not a model process and receives no authentication aliases
	// or inherited secrets in its environment. Source tool env policy is carried
	// by the pinned native RPC contract and remains subject to the OS sandbox.
	executor := process.Command{Path: original.Path, PinnedPath: original.PinnedPath, PinnedSHA256: original.PinnedSHA256, Args: []string{"exec-server", "--listen", "stdio"}, Dir: original.Dir, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home.path, "CODEX_HOME=" + home.path, "TMPDIR=" + home.runtime}}
	executor, err = codexSandboxCommand(ctx, executor, profile, scratch, home)
	if err != nil {
		return original, err
	}
	executor.Args[1] += "\n(deny network*)\n(deny file-read* (literal " + strconv.Quote(filepath.Join(home.source, "auth.json")) + "))\n(deny file-read* (literal " + strconv.Quote(filepath.Join(home.path, "auth.json")) + "))"
	broker, err := execbridge.Start(ctx, executor, helper, filepath.Join(scratch, "executor-process.json"))
	if err != nil {
		return original, err
	}
	state.bridge = broker
	birth, err := process.KernelStartID(os.Getpid())
	if err != nil {
		return original, err
	}
	environment := "default = \"bird\"\ninclude_local = false\n[[environments]]\nid = \"bird\"\nprogram = " + strconv.Quote(helper) + "\nargs = [\"codex-exec-bridge\", " + strconv.Quote(broker.Endpoint()) + ", " + strconv.Quote(strconv.Itoa(os.Getpid())) + ", " + strconv.Quote(birth) + "]\ncwd = " + strconv.Quote(original.Dir) + "\n"
	envPath := filepath.Join(home.path, "environments.toml")
	if err := writeCodexPrivate(envPath, []byte(environment)); err != nil {
		return original, err
	}
	if err := home.recordSnapshot(envPath, []byte(environment)); err != nil {
		return original, err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return original, err
	}
	state.input = read
	finals := []string{}
	for i, arg := range prepared.Args {
		if arg == "--output-last-message" && i+1 < len(prepared.Args) {
			finals = append(finals, prepared.Args[i+1])
		}
	}
	var schema any
	if review {
		if err := json.Unmarshal([]byte(adapter.CandidateReviewSchema()), &schema); err != nil {
			return original, err
		}
	}
	state.turn = codexrpc.NewTurn(codexrpc.TurnConfig{
		Home: home.path, Directory: original.Dir, Prompt: string(original.Stdin), Model: string(profile.Model), Reasoning: string(profile.Reasoning), Schema: schema,
		Approval: state.config["approval_policy"], Reviewer: state.config["approvals_reviewer"], EnableTools: broker.EnableTools,
		VerifyConfig: func(r map[string]any) error {
			if err := verifyCodexConfigLayers(r, home.runtime); err != nil {
				return err
			}
			cfg, _ := r["config"].(map[string]any)
			for _, key := range []string{"approval_policy", "approvals_reviewer", "sandbox_mode", "model_provider"} {
				if !reflect.DeepEqual(cfg[key], state.config[key]) {
					return errors.New("codex_execution_policy_changed")
				}
			}
			return home.verifyInputs()
		},
		SaveFinal: func(text string) error {
			for _, path := range finals {
				if filepath.Dir(path) != scratch {
					return errors.New("codex_output_path_untrusted")
				}
				fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
				if err != nil {
					return err
				}
				f := os.NewFile(uintptr(fd), path)
				info, err := f.Stat()
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					f.Close()
					return errors.New("codex_output_file_untrusted")
				}
				if err = f.Truncate(0); err == nil {
					_, err = io.WriteString(f, text)
				}
				if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
					return err
				}
			}
			return nil
		},
	}, write)
	cmd = original
	cmd.Args, err = codexWorkerParentArgs(home, state.config)
	if err != nil {
		return original, err
	}
	cmd.Env = prepared.Env
	cmd.Stdin = nil
	cmd.StdinFile = read
	cmd.OutputFilter = state.turn.Output
	cmd.Started = state.bind
	parentProfile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: profile.TimeoutMS}
	cmd, err = codexSandboxCommand(ctx, cmd, parentProfile, scratch, home)
	if err != nil {
		return original, err
	}
	cmd.Args[1] += "\n(deny process-exec*)\n(allow process-exec* (literal " + strconv.Quote(original.Path) + ") (literal " + strconv.Quote(helper) + "))"
	// Make loss of remote execution fail closed even for local filesystem tools.
	if !strings.HasPrefix(original.Dir, scratch+string(os.PathSeparator)) {
		cmd.Args[1] += "\n(deny file-write* (subpath " + strconv.Quote(original.Dir) + "))"
	}
	return cmd, nil
}

func codexWorkerParentArgs(home *codexHome, config map[string]any) ([]string, error) {
	restrictions, err := codexPolicyRestrictions(config)
	if err != nil {
		return nil, err
	}
	args := append(codexRuntimeFlags(home), restrictions...)
	return append(args, "app-server", "--listen", "stdio://"), nil
}
