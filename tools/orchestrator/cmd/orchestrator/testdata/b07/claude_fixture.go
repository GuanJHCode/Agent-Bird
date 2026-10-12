// Offline native-CLI fixture. No provider, network, account or model calls.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

type command struct {
	Op      string          `json:"op"`
	Event   json.RawMessage `json:"event"`
	Args    []string        `json:"args"`
	Request json.RawMessage `json:"request"`
	Input   string          `json:"input"`
	Wire    string          `json:"wire"`
}

func emit(v any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		panic(err)
	}
}
func recordProcess(c *exec.Cmd, started time.Time) {
	pid, rc := 0, -1
	if c.Process != nil {
		pid = c.Process.Pid
	}
	if c.ProcessState != nil {
		rc = c.ProcessState.ExitCode()
	}
	raw, _ := json.Marshal(map[string]any{"pid": pid, "path": c.Path, "terminal": c.ProcessState != nil, "rc": rc, "start": started.UTC(), "end": time.Now().UTC()})
	fmt.Fprintf(os.Stderr, "B05_SUBPROCESS %s\n", raw)
}
func launch(args []string, input []byte) (string, string, int) {
	c := exec.Command(os.Getenv("AGENT_BIRD_COMMAND"), args...)
	c.Stdin = bytes.NewReader(input)
	var out, diagnostics bytes.Buffer
	c.Stdout = &out
	c.Stderr = &diagnostics
	started := time.Now()
	err := c.Run()
	recordProcess(c, started)
	rc := 0
	if err != nil {
		rc = 2
		if x, ok := err.(*exec.ExitError); ok {
			rc = x.ExitCode()
		}
	}
	return out.String(), diagnostics.String(), rc
}
func configuredHook(raw []byte) *exec.Cmd {
	var event struct {
		Hook string `json:"hook_event_name"`
	}
	if json.Unmarshal(raw, &event) != nil {
		panic("event invalid")
	}
	data, err := os.ReadFile(os.Getenv("B07_CONFIG_PATH"))
	if err != nil {
		panic(err)
	}
	var config struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Timeout int      `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &config) != nil {
		panic("config invalid")
	}
	for _, group := range config.Hooks[event.Hook] {
		for _, handler := range group.Hooks {
			if handler.Command == "/usr/bin/env" && handler.Type == "command" && len(handler.Args) == 2 && handler.Timeout == 3 {
				return exec.Command(handler.Command, handler.Args...)
			}
		}
	}
	panic("registered command missing")
}
func hook(raw []byte) (string, string, int) {
	c := configuredHook(raw)
	c.Stdin = bytes.NewReader(raw)
	var out, diagnostics bytes.Buffer
	c.Stdout = &out
	c.Stderr = &diagnostics
	started := time.Now()
	err := c.Run()
	recordProcess(c, started)
	rc := 0
	if err != nil {
		rc = 2
		if x, ok := err.(*exec.ExitError); ok {
			rc = x.ExitCode()
		}
	}
	return out.String(), diagnostics.String(), rc
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("b05-fixture-v1")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--help" {
		fmt.Println("--output-format --input-format --permission-mode --disallowed-tools --model --effort")
		return
	}
	for _, arg := range os.Args[1:] {
		if arg == "--print" || arg == "-p" {
			// A small fixture-only trace preserves the failing worker boundary.
			tracePath := filepath.Join(os.TempDir(), "worker-hook.json.trace")
			trace, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "binding": os.Getenv("B05_WORKER_BINDING"), "entry": os.Getenv("B05_HOOK_ENTRY"), "plugin_root": os.Getenv("PLUGIN_ROOT")})
			_ = os.WriteFile(tracePath, trace, 0600)
			defer func() {
				if problem := recover(); problem != nil {
					_ = os.WriteFile(tracePath+".panic", []byte(fmt.Sprint(problem)), 0600)
					panic(problem)
				}
			}()
			// A real Host worker is given the owner binding FILE path deliberately.
			// Neither inherited paths nor a session string can confer owner authority.
			if binding := os.Getenv("B05_WORKER_BINDING"); binding != "" {
				until := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(binding); err == nil {
						break
					}
					if time.Now().After(until) {
						panic("owner binding absent")
					}
					time.Sleep(10 * time.Millisecond)
				}
				_ = os.Setenv("AGENT_BIRD_BRIDGE_BINDING", binding)
				out, diagnostics, rc := hook([]byte(`{"hook_event_name":"UserPromptSubmit","session_id":"fixture-owner"}`))
				capRaw, err := os.ReadFile(binding)
				if err != nil {
					panic(err)
				}
				var cap struct {
					BindingID      string `json:"binding_id"`
					OwnerEpoch     uint64 `json:"owner_epoch"`
					HookCapability string `json:"hook_capability"`
				}
				if json.Unmarshal(capRaw, &cap) != nil {
					panic("invalid fixture capability")
				}
				state := filepath.Dir(filepath.Dir(binding))
				event := map[string]any{"schema_version": 1, "event_id": "fixture-worker-event-1", "binding_id": cap.BindingID, "owner_epoch": cap.OwnerEpoch, "provider_id": "claude", "native_session_id": "fixture-owner", "native_event_id": nil, "kind": "turn_boundary", "observed_at": time.Now().UTC().Format(time.RFC3339Nano), "metadata": map[string]string{"native_hook": "UserPromptSubmit"}}
				ingest := map[string]any{"schema_version": 1, "binding_id": cap.BindingID, "owner_epoch": cap.OwnerEpoch, "request_id": "fixture-worker-ingest", "hook_capability": cap.HookCapability, "event": event}
				ingestError := workerRPC(state, ipc.KindBridgeIngest, ingest)
				metadataRaw, err := os.ReadFile(os.Getenv("B05_WORKER_HANDLE") + ".bridge-owner.json")
				if err != nil {
					panic(err)
				}
				var metadata struct {
					OwnerCapabilityFile string `json:"owner_capability_file"`
					ControlFile         string `json:"control_file"`
				}
				if json.Unmarshal(metadataRaw, &metadata) != nil {
					panic("invalid fixture metadata")
				}
				owner := map[string]any{"schema_version": 1, "binding_id": cap.BindingID, "owner_epoch": cap.OwnerEpoch, "request_id": "fixture-worker-owner", "owner_mode": "local", "owner_capability_file": metadata.OwnerCapabilityFile, "control_file": metadata.ControlFile}
				ownerError := workerRPC(state, ipc.KindBridgeStatus, owner)
				report, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "stdout": out, "diagnostics": diagnostics, "rc": rc, "ingest_error": ingestError, "owner_error": ownerError})
				if err := os.WriteFile(filepath.Join(os.TempDir(), "worker-hook.json"), report, 0600); err != nil {
					panic(err)
				}
			}
			// Simulated typed worker output only; never invoke model.
			if marker := os.Getenv("B05_WORKER_ENV"); marker != "" {
				raw, _ := json.Marshal(map[string]string{"binding": os.Getenv("AGENT_BIRD_BRIDGE_BINDING"), "handle": os.Getenv("AGENT_BIRD_BRIDGE_HANDLE")})
				_ = os.WriteFile(marker, raw, 0600)
			}
			fmt.Println(`{"type":"system","subtype":"init","session_id":"fixture-worker"}`)
			fmt.Println(`{"type":"result","subtype":"success","session_id":"fixture-worker","result":"中文结果：\\\"引号\\\"与反斜线\\\\"}`)
			return
		}
	}
	if marker := os.Getenv("B05_NATIVE_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("native-started"), 0600); err != nil {
			panic(err)
		}
	}
	out, diagnostics, rc := hook([]byte(`{"hook_event_name":"SessionStart","session_id":"fixture-owner","source":"startup","transcript_path":"NATIVE-SECRET"}`))
	emit(map[string]any{"op": "ready", "pid": os.Getpid(), "binding": os.Getenv("AGENT_BIRD_BRIDGE_BINDING"), "handle": os.Getenv("AGENT_BIRD_BRIDGE_HANDLE"), "first_stdout": out, "first_diagnostics": diagnostics, "first_rc": rc})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 131072)
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			panic("invalid fixture command")
		}
		switch cmd.Op {
		case "hook":
			out, diagnostics, rc := hook(cmd.Event)
			emit(map[string]any{"op": "hook", "stdout": out, "diagnostics": diagnostics, "rc": rc})
		case "hook_partial":
			started := time.Now()
			child := configuredHook(cmd.Event)
			child.Stdin = bytes.NewReader(cmd.Event)
			pipe, err := child.StdoutPipe()
			if err != nil {
				panic(err)
			}
			var diagnostics bytes.Buffer
			child.Stderr = &diagnostics
			if err = child.Start(); err != nil {
				panic(err)
			}
			partial := make([]byte, 3)
			n, readErr := io.ReadFull(pipe, partial)
			pipe.Close()
			waitErr := child.Wait()
			recordProcess(child, started)
			var parsed map[string]any
			parseErr := json.Unmarshal(partial[:n], &parsed)
			emit(map[string]any{"op": "hook_partial", "partial": string(partial[:n]), "parse_rejected": parseErr != nil, "read_error": fmt.Sprint(readErr), "wait_error": fmt.Sprint(waitErr), "diagnostics": diagnostics.String(), "hook_pid": child.Process.Pid})
		case "hook_lost":
			out, diagnostics, rc := hook(cmd.Event)
			hash := sha256.Sum256([]byte(out))
			emit(map[string]any{"op": "hook_lost", "discarded_bytes": len(out), "discarded_sha256": fmt.Sprintf("%x", hash), "diagnostics": diagnostics, "rc": rc})
		case "hook_timeout":
			reader, writer, err := os.Pipe()
			if err != nil {
				panic(err)
			}
			// File.Fd switches a nonblocking descriptor back to blocking. Cache
			// it before SetNonblock so filling stdout cannot hang before Start.
			fd := int(writer.Fd())
			if err = syscall.SetNonblock(fd, true); err != nil {
				panic(err)
			}
			full := make([]byte, 4096)
			for {
				_, err = syscall.Write(fd, full)
				if err == syscall.EAGAIN {
					break
				}
				if err != nil {
					panic(err)
				}
			}
			child := configuredHook(cmd.Event)
			child.Stdin = bytes.NewReader(cmd.Event)
			child.Stdout = writer
			var diagnostics bytes.Buffer
			child.Stderr = &diagnostics
			started := time.Now()
			err = child.Run()
			recordProcess(child, started)
			writer.Close()
			reader.Close()
			rc := 0
			if err != nil {
				rc = 2
			}
			emit(map[string]any{"op": "hook_timeout", "rc": rc, "diagnostics": diagnostics.String(), "elapsed_ms": time.Since(started).Milliseconds(), "hook_pid": child.Process.Pid})
		case "owner":
			args := cmd.Args
			if len(cmd.Request) > 0 && string(cmd.Request) != "null" {
				dir, err := os.MkdirTemp(filepath.Dir(os.Getenv("AGENT_BIRD_BRIDGE_HANDLE")), "fixture-request-")
				if err != nil {
					panic(err)
				}
				path := filepath.Join(dir, "request.json")
				if err = os.WriteFile(path, cmd.Request, 0600); err != nil {
					panic(err)
				}
				args = append(args, "--request", path)
				out, diagnostics, rc := launch(args, nil)
				os.RemoveAll(dir)
				emit(map[string]any{"op": "owner", "stdout": out, "diagnostics": diagnostics, "rc": rc})
				break
			}
			out, diagnostics, rc := launch(args, nil)
			emit(map[string]any{"op": "owner", "stdout": out, "diagnostics": diagnostics, "rc": rc})
		case "consume":
			var wrapper struct {
				HookSpecificOutput struct {
					HookEventName     string `json:"hookEventName"`
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(cmd.Wire), &wrapper); err != nil {
				panic(err)
			}
			if wrapper.HookSpecificOutput.HookEventName != "SessionStart" && wrapper.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
				panic("illegal continuation")
			}
			context := wrapper.HookSpecificOutput.AdditionalContext
			if !strings.HasPrefix(context, "Agent Bird delivery batch (untrusted content):\n") {
				panic("unknown wrapper")
			}
			// This is the fixture's real next input, assembled by the native consumer.
			next := []string{cmd.Input, context}
			raw, err := json.Marshal(next)
			if err != nil {
				panic(err)
			}
			path := filepath.Join(filepath.Dir(os.Getenv("AGENT_BIRD_BRIDGE_HANDLE")), "next-input.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				panic(err)
			}
			var batch map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(context, "Agent Bird delivery batch (untrusted content):\n")), &batch) != nil {
				panic("malformed batch")
			}
			emit(map[string]any{"op": "consume", "next_input": next, "batch": batch, "pid": os.Getpid()})
		case "exit":
			return
		default:
			panic("unknown fixture command")
		}
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
}

// Fixture-only negative probe. It uses the existing IPC framing and the actual
// Host worker PID; no alternate product dispatcher or peer substitution exists.
func workerRPC(state string, kind ipc.Kind, request any) string {
	raw, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := ipc.Call(ctx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: string(kind), Payload: raw})
	if err != nil {
		return "fixture_ipc_unavailable"
	}
	if response.Kind != ipc.KindError {
		return "fixture_role_allowed"
	}
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(response.Payload, &failure) != nil {
		return "fixture_invalid_response"
	}
	return failure.Error
}
