package grokacp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestClientPromptReturnsExactCompletionReceipts(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	prompt := "继续：\u4f60\u597d"
	rawResponses := []string{
		`{"jsonrpc":"2.0", "id":3,"result":{"stopReason":"end_turn"}}`,
		`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`,
	}
	actorDone := make(chan error, 1)
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		for _, step := range []struct {
			id     int
			method string
			result string
		}{
			{1, "initialize", `{"protocolVersion":1,"agentCapabilities":{}}`},
			{2, "session/new", `{"sessionId":"session-receipt"}`},
			{3, "session/prompt", rawResponses[0]},
			{4, "session/prompt", rawResponses[1]},
		} {
			line, err := read.ReadBytes('\n')
			if err != nil {
				actorDone <- err
				return
			}
			var request struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(line, &request); err != nil || request.ID != step.id || request.Method != step.method {
				actorDone <- &wireError{"unexpected request"}
				return
			}
			if step.method == "session/prompt" {
				var params struct {
					SessionID string `json:"sessionId"`
					Prompt    []struct {
						Text string `json:"text"`
					} `json:"prompt"`
				}
				if json.Unmarshal(request.Params, &params) != nil || params.SessionID != "session-receipt" || len(params.Prompt) != 1 || params.Prompt[0].Text != prompt {
					actorDone <- &wireError{"prompt params"}
					return
				}
			}
			body := `{"jsonrpc":"2.0","id":` + strconv.Itoa(step.id) + `,"result":` + step.result + `}`
			if step.method == "session/prompt" {
				body = step.result
			}
			if _, err := write.WriteString(body + "\n"); err != nil {
				actorDone <- err
				return
			}
			if err := write.Flush(); err != nil {
				actorDone <- err
				return
			}
		}
		actorDone <- nil
	}()

	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	sessionID, err := client.NewSession(ctx, "/private/work")
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Prompt(ctx, sessionID, prompt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Prompt(ctx, sessionID, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-actorDone; err != nil {
		t.Fatal(err)
	}

	promptSum := sha256.Sum256([]byte(prompt))
	if first.SessionID != sessionID || second.SessionID != sessionID || first.RPCRequestID != 3 || second.RPCRequestID != 4 || first.RPCRequestID == second.RPCRequestID || first.PromptSHA256 != hex.EncodeToString(promptSum[:]) || second.PromptSHA256 != hex.EncodeToString(promptSum[:]) || first.StopReason != "end_turn" || second.StopReason != "end_turn" {
		t.Fatalf("receipts = %#v, %#v", first, second)
	}
	firstResponseSum := sha256.Sum256([]byte(rawResponses[0]))
	secondResponseSum := sha256.Sum256([]byte(rawResponses[1]))
	if first.ResponseSHA256 != hex.EncodeToString(firstResponseSum[:]) || second.ResponseSHA256 != hex.EncodeToString(secondResponseSum[:]) || first.ResponseSHA256 == second.ResponseSHA256 {
		t.Fatalf("response hashes = %q, %q", first.ResponseSHA256, second.ResponseSHA256)
	}
}
