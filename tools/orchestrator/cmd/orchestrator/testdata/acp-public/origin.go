// Owned offline ACP actor. It does not call any provider, account or network.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("acp-public-origin-v1")
		return
	}
	if len(os.Args) != 4 || strings.Join(os.Args[1:], " ") != "agent --no-leader stdio" {
		os.Exit(20)
	}
	if len(os.Environ()) != 8 || os.Getenv("XAI_API_KEY") != "" || os.Getenv("AGENT_BIRD_BRIDGE_BINDING") != "" || os.Getenv("AGENT_BIRD_COMMAND") != "" {
		os.Exit(21)
	}
	modeRaw, _ := os.ReadFile("actor-mode")
	mode := string(modeRaw)
	if mode == "timed_exit" {
		time.AfterFunc(3*time.Second, func() { os.Exit(0) })
	}
	trace, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "acp-trace.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(22)
	}
	defer trace.Close()
	r := bufio.NewScanner(os.Stdin)
	r.Buffer(make([]byte, 4096), 64<<10)
	for r.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      uint64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(r.Bytes(), &req) != nil {
			os.Exit(23)
		}
		if _, err = trace.Write(append(append([]byte(nil), r.Bytes()...), '\n')); err != nil || trace.Sync() != nil {
			os.Exit(24)
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case "session/new":
			result = map[string]any{"sessionId": "owned-public-session"}
		case "session/prompt":
			if req.ID >= 4 && mode == "lost_response" {
				time.Sleep(time.Minute)
				os.Exit(25)
			}
			if req.ID >= 4 && mode == "partial_response" {
				fmt.Print(`{"jsonrpc":"2.0","id":`)
				time.Sleep(time.Minute)
				os.Exit(26)
			}
			if req.ID >= 4 && mode == "exit_before_response" {
				return
			}
			for i := 0; i < 80; i++ {
				if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "owned-public-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "owned fixture chunk"}}}}); err != nil {
					os.Exit(27)
				}
				time.Sleep(time.Millisecond)
			}
			result = map[string]any{"stopReason": "end_turn"}
		default:
			os.Exit(28)
		}
		if err = json.NewEncoder(os.Stdout).Encode(struct {
			JSONRPC string `json:"jsonrpc"`
			ID      uint64 `json:"id"`
			Result  any    `json:"result"`
		}{"2.0", req.ID, result}); err != nil {
			os.Exit(29)
		}
	}
	if r.Err() != nil {
		os.Exit(30)
	}
}
