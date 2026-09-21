package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const maxAppServerLine = 1024 * 1024

type CodeError string

func (e CodeError) Error() string { return string(e) }

type Client struct {
	stdin      io.WriteCloser
	lines      *bufio.Scanner
	mu         sync.Mutex
	nextID     int
	userConfig string
	cmd        *exec.Cmd
	stdout     io.ReadCloser
	pid        int
	birth      string
	done       chan error
	stopOnce   sync.Once
	stopErr    error
}

// StartCommand owns a short-lived stdio process group. Callers construct and
// verify the command; the client never starts a model or attaches to a daemon.
func StartCommand(ctx context.Context, cmd *exec.Cmd) (*Client, func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, CodeError("app_server_start_failed")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, CodeError("app_server_start_failed")
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, CodeError("app_server_start_failed")
	}
	birth, err := process.Birth(cmd.Process.Pid)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, CodeError("app_server_start_failed")
	}
	rpc := &Client{stdin: stdin, stdout: stdout, cmd: cmd, pid: cmd.Process.Pid, birth: birth, done: make(chan error, 1), lines: bufio.NewScanner(stdout), nextID: 1}
	go func() { rpc.done <- cmd.Wait() }()
	rpc.lines.Buffer(make([]byte, 4096), maxAppServerLine)
	closeRPC := rpc.stop
	go func() { <-ctx.Done(); _ = rpc.stop() }()
	initialized, err := rpc.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "agent-bird", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}})
	if err != nil {
		stopErr := closeRPC()
		if stopErr != nil {
			err = errors.Join(err, process.ErrProcessTreeUnknown, stopErr)
		}
		return nil, func() error { return nil }, err
	}
	codexHome, ok := initialized["codexHome"].(string)
	if !ok || !filepath.IsAbs(codexHome) || filepath.Clean(codexHome) != codexHome {
		stopErr := closeRPC()
		if stopErr != nil {
			return nil, nil, errors.Join(process.ErrProcessTreeUnknown, stopErr)
		}
		return nil, func() error { return nil }, CodeError("app_server_protocol_failed")
	}
	rpc.userConfig = filepath.Join(codexHome, "config.toml")
	if err := rpc.notify("initialized", map[string]any{}); err != nil {
		stopErr := closeRPC()
		if stopErr != nil {
			err = errors.Join(err, process.ErrProcessTreeUnknown, stopErr)
		}
		return nil, func() error { return nil }, err
	}
	return rpc, closeRPC, nil
}

func (r *Client) stop() error {
	r.stopOnce.Do(func() {
		_ = r.stdin.Close()
		_ = r.stdout.Close()
		select {
		case <-r.done:
		case <-time.After(time.Second):
			if now, err := process.Birth(r.pid); err != nil || now != r.birth {
				r.stopErr = CodeError("app_server_process_tree_unknown")
				return
			}
			if err := syscall.Kill(-r.pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
				r.stopErr = CodeError("app_server_process_tree_unknown")
				return
			}
			select {
			case <-r.done:
			case <-time.After(time.Second):
				_ = syscall.Kill(-r.pid, syscall.SIGKILL)
				select {
				case <-r.done:
				case <-time.After(time.Second):
					r.stopErr = CodeError("app_server_process_tree_unknown")
					return
				}
			}
		}
		if r.stopErr == nil && process.ConfirmGroupExited(r.pid) != nil {
			if err := syscall.Kill(-r.pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
				r.stopErr = CodeError("app_server_process_tree_unknown")
				return
			}
			deadline := time.Now().Add(time.Second)
			for process.ConfirmGroupExited(r.pid) != nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if process.ConfirmGroupExited(r.pid) != nil {
				_ = syscall.Kill(-r.pid, syscall.SIGKILL)
				deadline = time.Now().Add(time.Second)
				for process.ConfirmGroupExited(r.pid) != nil && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if process.ConfirmGroupExited(r.pid) != nil {
					r.stopErr = CodeError("app_server_process_tree_unknown")
				}
			}
		}
	})
	return r.stopErr
}

func (r *Client) notify(method string, params map[string]any) error {
	return r.write(map[string]any{"method": method, "params": params})
}

func (r *Client) Call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	if err := r.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, CodeError("app_server_timeout")
		}
		if !r.lines.Scan() {
			return nil, CodeError("app_server_protocol_failed")
		}
		var message map[string]any
		if json.Unmarshal(r.lines.Bytes(), &message) != nil {
			return nil, CodeError("app_server_protocol_failed")
		}
		if _, requested := message["method"]; requested {
			if _, hasID := message["id"]; hasID {
				return nil, CodeError("app_server_server_request")
			}
			continue
		}
		messageID, hasID := message["id"].(float64)
		if !hasID || int(messageID) != id {
			continue
		}
		if _, hasError := message["error"]; hasError {
			return nil, CodeError("app_server_rejected")
		}
		result, ok := message["result"].(map[string]any)
		if !ok {
			return nil, CodeError("app_server_protocol_failed")
		}
		return result, nil
	}
}

func (r *Client) write(message map[string]any) error {
	b, err := json.Marshal(message)
	if err != nil || len(b) > maxAppServerLine {
		return CodeError("app_server_protocol_failed")
	}
	b = append(b, '\n')
	if _, err := r.stdin.Write(b); err != nil {
		return CodeError("app_server_protocol_failed")
	}
	return nil
}

func (r *Client) UserConfig() string { return r.userConfig }
