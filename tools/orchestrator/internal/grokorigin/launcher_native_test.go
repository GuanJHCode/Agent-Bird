//go:build darwin

package grokorigin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const originActorVersion = "owned-origin 1.0"

// Only these exact provider argv shapes activate the owned fixture. No actor
// marker is injected into the production ExactEnv allowlist.
func init() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		os.Exit(originLaunchActor(true))
	}
	if len(os.Args) == 4 && strings.Join(os.Args[1:], " ") == "agent --no-leader stdio" {
		os.Exit(originLaunchActor(false))
	}
}

func originLaunchActor(version bool) int {
	modeRaw, _ := os.ReadFile("origin-actor-mode")
	mode := string(modeRaw)
	if os.Getenv("AGENT_BIRD_ORIGIN_AMBIENT_SENTINEL") != "" || os.Getenv("GROK_AUTH_TOKEN") != "" || os.Getenv("XAI_API_KEY") != "" || len(os.Environ()) != 8 {
		return 31
	}
	if version {
		if os.WriteFile(filepath.Join(os.Getenv("HOME"), "version-probed"), []byte("one"), 0600) != nil {
			return 32
		}
		switch mode {
		case "version_hang":
			time.Sleep(time.Minute)
			return 33
		case "version_stderr":
			fmt.Fprintln(os.Stderr, "synthetic diagnostic")
		case "version_long":
			fmt.Print(strings.Repeat("x", 1000))
			return 0
		case "version_multiline":
			fmt.Println(originActorVersion)
			fmt.Println("extra")
			return 0
		case "version_bad":
			fmt.Println("other version")
			return 0
		}
		fmt.Println(originActorVersion)
		return 0
	}
	if mode == "agent_exit" {
		return 34
	}
	if os.WriteFile(filepath.Join(os.Getenv("HOME"), "agent-started"), []byte("one"), 0600) != nil {
		return 35
	}
	r := bufio.NewScanner(os.Stdin)
	for r.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(r.Bytes(), &req) != nil {
			return 36
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case "session/new":
			result = map[string]any{"sessionId": "owned-origin-session"}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
		default:
			return 37
		}
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
		}{"2.0", req.ID, result}); err != nil {
			return 38
		}
	}
	if r.Err() != nil {
		return 39
	}
	if mode == "agent_ignore_eof" {
		time.Sleep(time.Minute)
	}
	return 0
}

func originNativeRequest(t *testing.T, mode string) (string, Request) {
	t.Helper()
	if os.Getenv("ORIGIN_NATIVE") != "1" {
		t.Skip("owned native origin launcher requires process token")
	}
	_, workspace := originWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspace, "origin-actor-mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	sha, err := process.ExecutableDigest(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{SchemaVersion: 1, Binary: binary, BinarySHA256: sha, BinaryVersion: originActorVersion, Workspace: workspace, InitialPrompt: "initial", OperationTimeoutMS: 3000, TotalTimeoutMS: 15000, OriginProfile: "readonly", AuthMode: "none"}
	return originState(t), r
}

func originOwnPrepared(t *testing.T, p *Prepared) {
	t.Helper()
	if p != nil {
		t.Cleanup(func() {
			if err := p.Close(context.Background()); err != nil {
				t.Errorf("original launcher cleanup: %v", err)
			}
		})
	}
}

func TestOriginLaunchRetainsOriginalProcessAcrossTurns(t *testing.T) {
	state, req := originNativeRequest(t, "")
	t.Setenv("AGENT_BIRD_ORIGIN_AMBIENT_SENTINEL", "owned-sentinel")
	t.Setenv("GROK_AUTH_TOKEN", "owned-sentinel")
	p, err := Prepare(context.Background(), state, req)
	originOwnPrepared(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if p.probe == nil || p.probe.ExitCode() != 0 || p.probe.ConfirmTreeExited() != nil {
		t.Fatal("version probe original exit proof missing")
	}
	if _, err = os.Stat(filepath.Join(p.layout.home, "version-probed")); err != nil {
		t.Fatal("version probe bypassed private HOME")
	}
	l, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id := l.Identity()
	if id.PID != p.launch.handle.PID() || id.Executable != req.Binary || id.ExecutableSHA256 != req.BinarySHA256 || !id.BirthKnown {
		t.Fatalf("pin identity not original: %+v", id)
	}
	client := l.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, req.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{req.InitialPrompt, "delivery one", "delivery two"} {
		turn, err := client.Prompt(ctx, session, text)
		if err != nil || turn.RPCRequestID != uint64(i+3) || turn.SessionID != session {
			t.Fatalf("turn=%+v err=%v", turn, err)
		}
		if l.Identity() != id {
			t.Fatal("original identity drifted")
		}
	}
	if _, err = p.Start(context.Background()); err == nil {
		t.Fatal("second process launch accepted")
	}
	private := p.layout.root
	if err = p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.launch.handle.ExitCode() != 0 || p.launch.handle.ConfirmTreeExited() != nil {
		t.Fatal("original natural Wait/tree missing")
	}
	if _, err = os.Stat(private); !os.IsNotExist(err) {
		t.Fatal("proven-exited private directory retained")
	}
	if _, err = p.Start(context.Background()); err == nil {
		t.Fatal("closed prepared launch accepted")
	}
	t.Logf("version PID=%d Wait=0; agent PID=%d PGID=%d birth=%s Wait=0 tree_exited=true RPC=3,4,5", p.probe.PID(), id.PID, id.PGID, id.Birth)
}

func TestOriginLaunchRejectsVersionFailures(t *testing.T) {
	for _, mode := range []string{"version_bad", "version_stderr", "version_long", "version_multiline", "version_hang"} {
		t.Run(mode, func(t *testing.T) {
			state, req := originNativeRequest(t, mode)
			if mode == "version_hang" {
				req.OperationTimeoutMS = 100
			}
			p, err := Prepare(context.Background(), state, req)
			originOwnPrepared(t, p)
			if err == nil || p == nil || p.probe == nil {
				t.Fatalf("invalid probe accepted or original ownership lost: %v", err)
			}
			if _, err = p.Start(context.Background()); err == nil {
				t.Fatal("failed prepare launched agent")
			}
			if err = p.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if p.probe.ConfirmTreeExited() != nil {
				t.Fatal("failed version probe remains unproven")
			}
		})
	}
}

func TestOriginLaunchRechecksPrivateLayoutBeforeSpawn(t *testing.T) {
	state, req := originNativeRequest(t, "")
	p, err := Prepare(context.Background(), state, req)
	originOwnPrepared(t, p)
	if err != nil {
		t.Fatal(err)
	}
	path := p.layout.home
	if err = os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Start(context.Background()); err == nil || p.launch != nil {
		t.Fatal("private layout drift launched process")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+"-original", path); err != nil {
		t.Fatal(err)
	}
}

func TestOriginLaunchRechecksPinAndCapabilitiesBeforeSpawn(t *testing.T) {
	for _, kind := range []string{"pin", "source"} {
		t.Run(kind, func(t *testing.T) {
			state, req := originNativeRequest(t, "")
			if kind == "pin" {
				raw, err := os.ReadFile(req.Binary)
				if err != nil {
					t.Fatal(err)
				}
				req.Binary = filepath.Join(originState(t), "owned-executable")
				if err = os.WriteFile(req.Binary, raw, 0700); err != nil {
					t.Fatal(err)
				}
			}
			p, err := Prepare(context.Background(), state, req)
			originOwnPrepared(t, p)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "pin" {
				if err = os.WriteFile(req.Binary, []byte("changed executable"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.WriteFile(filepath.Join(req.Workspace, ".mcp.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = p.Start(context.Background()); err == nil || p.launch != nil {
				t.Fatal("drifted launch started an original process")
			}
			if _, err = p.Start(context.Background()); err == nil {
				t.Fatal("failed one-shot start was retried")
			}
		})
	}
}

func TestOriginLaunchFailedVersionCleanupCancellationRetainsOwnership(t *testing.T) {
	state, req := originNativeRequest(t, "version_hang")
	req.OperationTimeoutMS = 100
	p, err := Prepare(context.Background(), state, req)
	originOwnPrepared(t, p)
	if err == nil || p == nil || p.probe == nil {
		t.Fatalf("probe ownership missing: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = p.Close(canceled); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("canceled cleanup=%v", err)
	}
	if p.closed || p.probe == nil || p.layout.leaf == nil {
		t.Fatal("UNKNOWN discarded original ownership")
	}
	if _, err = os.Stat(p.layout.root); err != nil {
		t.Fatal("UNKNOWN removed private layout")
	}
	if _, err = p.Start(context.Background()); err == nil {
		t.Fatal("UNKNOWN allowed another origin")
	}
	if err = p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.probe.ConfirmTreeExited() != nil {
		t.Fatal("original probe not proven exited")
	}
}

func TestOriginLaunchEarlyExitKeepsOriginalCleanupOwner(t *testing.T) {
	state, req := originNativeRequest(t, "agent_exit")
	p, err := Prepare(context.Background(), state, req)
	originOwnPrepared(t, p)
	if err != nil {
		t.Fatal(err)
	}
	l, err := p.Start(context.Background())
	if p.launch == nil || p.launch.handle == nil {
		t.Fatal("actual started process lost its original owner")
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = l.Client().Initialize(ctx)
		cancel()
		if err == nil {
			t.Fatal("exited origin initialized")
		}
	}
	if _, err = p.Start(context.Background()); err == nil {
		t.Fatal("early-exit origin silently respawned")
	}
	if err = p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.launch.handle.ExitCode() != 34 || p.launch.handle.ConfirmTreeExited() != nil {
		t.Fatal("early-exit original Wait/tree not retained")
	}
}

func TestOriginLaunchCancellationDuringGraceRetainsReaderAndOwnership(t *testing.T) {
	state, req := originNativeRequest(t, "agent_ignore_eof")
	p, err := Prepare(context.Background(), state, req)
	originOwnPrepared(t, p)
	if err != nil {
		t.Fatal(err)
	}
	l, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, done := context.WithTimeout(context.Background(), time.Second)
	if err = l.Client().Initialize(ctx); err != nil {
		done()
		t.Fatal(err)
	}
	done()
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = p.Close(short); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("grace cancellation=%v", err)
	}
	if l.Client().Err() != nil {
		t.Fatalf("reader closed after caller deadline: %v", l.Client().Err())
	}
	if p.closed || p.launch.handle.ConfirmTreeExited() == nil {
		t.Fatal("grace cancellation lost live original ownership")
	}
	if _, err = os.Stat(p.layout.root); err != nil {
		t.Fatal("grace cancellation removed private layout")
	}
	if err = p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.launch.handle.ConfirmTreeExited() != nil {
		t.Fatal("original process not recovered by later cleanup")
	}
}
