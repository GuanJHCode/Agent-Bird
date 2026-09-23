package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/execbridge"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// The test binary supplies the same child entry as the packaged executable.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "codex-native-crash-fixture" {
		os.Exit(nativeCrashFixture(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "codex-exec-bridge" {
		if len(os.Args) != 5 {
			os.Exit(2)
		}
		pid, err := strconv.Atoi(os.Args[3])
		if err != nil {
			os.Exit(2)
		}
		if err := execbridge.Forward(context.Background(), os.Args[2], pid, os.Args[4], os.Stdin, os.Stdout); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNativeCodexRemoteThreadWithoutModel(t *testing.T) {
	runNativeCodexRemoteThreadWithoutModel(t, false)
}

func TestNativeCodexRemoteSkillRootsWithoutModel(t *testing.T) {
	runNativeCodexRemoteThreadWithoutModel(t, true)
}

func runNativeCodexRemoteThreadWithoutModel(t *testing.T, projectSkills bool) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_REMOTE") != "1" {
		t.Skip("opt-in native remote thread, no model")
	}
	binary := os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY")
	if binary == "" {
		t.Fatal("fixed binary required")
	}
	root, err := os.MkdirTemp("/private/tmp", "bird-codex-remote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native evidence: %s", root)
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if projectSkills {
		skill := filepath.Join(work, ".agents", "skills", "bird-fixture")
		if err := os.MkdirAll(filepath.Join(skill, "agents"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: bird-fixture\ndescription: Native metadata-only fixture.\n---\nPreserve task isolation.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "agents", "openai.yaml"), []byte("interface:\n  display_name: Bird Fixture\n  short_description: Verify native skill discovery.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	state := &codexRuntimeState{}
	defer state.close()
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 40000}
	cmd, err := prepareCodexWorker(ctx, process.Command{Path: binary, PinnedPath: binary, PinnedSHA256: adapter.CodexSHA256, Dir: work, Args: []string{"exec", "--json", "--ephemeral", "--sandbox", "read-only", "-"}, Stdin: []byte("must not start a model")}, profile, filepath.Join(root, "scratch"), false, state)
	if err != nil {
		t.Fatal(err)
	}
	_ = state.turn.Close()
	_ = state.input.Close()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	state.input = read
	cmd.StdinFile = read
	defer write.Close()
	var input io.WriteCloser = write
	var skillProbe *nativeSkillListProbe
	if projectSkills {
		// Keep stdin open after the stop-before-model sentinel until the
		// metadata-only skills/list response arrives. Never send turn/start.
		input = nativeDeferredClose{Writer: write}
		skillProbe = &nativeSkillListProbe{input: write, directory: work}
	}
	reached := false
	stopBeforeModel := errors.New("metadata_only_stop_before_model")
	state.turn = codexrpc.NewTurn(codexrpc.TurnConfig{Home: state.home.path, Directory: work, Approval: state.config["approval_policy"], Reviewer: state.config["approvals_reviewer"], DynamicTools: codexReadTools(ctx, state.reader), VerifyConfig: func(r map[string]any) error {
		if err := verifyCodexConfigLayers(r, state.home.runtime); err != nil {
			return err
		}
		cfg, _ := r["config"].(map[string]any)
		return verifyCodexReadEditConfig(cfg)
	}, EnableTools: func() error {
		if err := state.bridge.EnableTools(); err != nil {
			return err
		}
		reached = true
		if projectSkills {
			if err := json.NewEncoder(write).Encode(map[string]any{"id": 91, "method": "skills/list", "params": map[string]any{"cwds": []string{work}, "forceReload": false}}); err != nil {
				return err
			}
		}
		return stopBeforeModel
	}}, input)
	cmd.OutputFilter = state.turn.Output
	if skillProbe != nil {
		cmd.OutputFilter = func(output io.Writer) io.Writer {
			return io.MultiWriter(skillProbe, state.turn.Output(output))
		}
	}
	cmd.Stdout = io.Discard
	child, err := process.Start(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(ctx); err != nil {
		child.Stop(context.Background())
		t.Fatal(err)
	}
	if err := child.ConfirmTreeExited(); err != nil {
		t.Fatal(err)
	}
	t.Logf("turn=%v bridge=%v state=%v", state.turn.Err(), state.bridge.Err(), state.bridge.Diagnostics())
	if err := state.bridge.Finish(); err != nil {
		t.Fatal(err)
	}
	if !reached || !errors.Is(state.turn.Err(), stopBeforeModel) || state.bridge.Err() != nil {
		t.Fatalf("remote startup failed: turn=%v bridge=%v output=%s", state.turn.Err(), state.bridge.Err(), safeCodexMetadataFailure(child.Output()))
	}
	if skillProbe != nil {
		if !skillProbe.found {
			t.Fatal("tool-phase skills/list did not return the enabled fixture and its interface")
		}
		t.Log("tool-phase skills/list returned the enabled fixture and interface; no model requested")
	}
	if err := state.home.verifyInputs(); err != nil {
		t.Fatal(err)
	}
	if err := state.skills.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	t.Log("remote environment connected; stopped before turn/start; both process groups exited")
}

type nativeDeferredClose struct{ io.Writer }

func (nativeDeferredClose) Close() error { return nil }

type nativeSkillListProbe struct {
	input     io.Closer
	directory string
	buffer    []byte
	found     bool
}

func (p *nativeSkillListProbe) Write(raw []byte) (int, error) {
	p.buffer = append(p.buffer, raw...)
	if len(p.buffer) > 2*1024*1024 {
		_ = p.input.Close()
		return 0, errors.New("native_skill_probe_frame_limit")
	}
	for {
		i := bytes.IndexByte(p.buffer, '\n')
		if i < 0 {
			return len(raw), nil
		}
		var frame map[string]any
		err := json.Unmarshal(p.buffer[:i], &frame)
		p.buffer = p.buffer[i+1:]
		if err != nil || frame["id"] != float64(91) {
			continue
		}
		_ = p.input.Close()
		result, _ := frame["result"].(map[string]any)
		data, _ := result["data"].([]any)
		if frame["error"] != nil || len(data) != 1 {
			continue
		}
		entry, _ := data[0].(map[string]any)
		errors, ok := entry["errors"].([]any)
		if entry["cwd"] != p.directory || !ok || len(errors) != 0 {
			continue
		}
		skills, _ := entry["skills"].([]any)
		for _, rawSkill := range skills {
			skill, _ := rawSkill.(map[string]any)
			ui, _ := skill["interface"].(map[string]any)
			if skill["name"] == "bird-fixture" && skill["enabled"] == true && skill["path"] == filepath.Join(p.directory, ".agents", "skills", "bird-fixture", "SKILL.md") && ui["displayName"] == "Bird Fixture" {
				p.found = true
			}
		}
	}
}

// Only fixed error categories are retained; config/account contents are not logs.
func safeCodexMetadataFailure(raw string) string {
	return "native_output_bytes=" + strconv.Itoa(len(raw))
}
