package codexrpc

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type memoryInput struct {
	bytes.Buffer
	closed bool
}

func (m *memoryInput) Close() error { m.closed = true; return nil }
func TestTurnCompletionRequiresBoundNativeTurnAndUsage(t *testing.T) {
	input := &memoryInput{}
	var output bytes.Buffer
	final := ""
	enabled := false
	d := NewTurn(TurnConfig{Home: "/task/home", Directory: "/task/work", Prompt: "fix calc", Approval: "never", Reviewer: "user", VerifyConfig: func(map[string]any) error { return nil }, EnableTools: func() error { enabled = true; return nil }, SaveFinal: func(s string) error { final = s; return nil }}, input)
	d.Output(&output)
	for _, line := range []string{
		`{"id":1,"result":{"codexHome":"/task/home"}}`,
		`{"id":2,"result":{"config":{}}}`,
		`{"id":3,"result":{"thread":{"id":"thread-1"},"cwd":"/task/work","approvalPolicy":"never","approvalsReviewer":"user"}}`,
		`{"id":4,"result":{"turn":{"id":"turn-1"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"i","type":"agentMessage","text":"done"}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":10,"cachedInputTokens":2,"cacheWriteInputTokens":0,"outputTokens":3,"reasoningOutputTokens":1}}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","error":null}}}`,
	} {
		d.Write([]byte(line + "\n"))
	}
	if err := d.Err(); err != nil {
		t.Fatal(err)
	}
	if !enabled || !input.closed || final != "done" {
		t.Fatalf("native completion not delivered: enabled=%t closed=%t final=%q", enabled, input.closed, final)
	}
	var last map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			t.Fatal(err)
		}
	}
	if last["type"] != "turn.completed" {
		t.Fatal("missing terminal result")
	}
	if !strings.Contains(input.String(), `"networkAccess":"restricted"`) || !strings.Contains(input.String(), `"ephemeral":true`) {
		t.Fatal("missing native isolation contract")
	}
}
func TestTurnNeverApprovesServerRequest(t *testing.T) {
	in := &memoryInput{}
	d := NewTurn(TurnConfig{}, in)
	d.Output(&bytes.Buffer{})
	d.Write([]byte(`{"id":99,"method":"item/commandExecution/requestApproval","params":{}}` + "\n"))
	if d.Err() == nil || !in.closed {
		t.Fatal("server approval request not failed closed")
	}
	if strings.Contains(in.String(), "approve") {
		t.Fatal("sent owner approval")
	}
}

func TestTurnRejectsMalformedEarlyNotification(t *testing.T) {
	for _, params := range []string{`null`, `[]`, `"invalid"`} {
		t.Run(params, func(t *testing.T) {
			in := &memoryInput{}
			d := NewTurn(TurnConfig{}, in)
			d.Output(&bytes.Buffer{})
			d.Write([]byte(`{"method":"item/completed","params":` + params + "}\n"))
			if !in.closed || d.failure == nil {
				t.Fatal("malformed notification retained for replay")
			}
		})
	}
}

func TestTurnBoundsEarlyNotificationBytes(t *testing.T) {
	in := &memoryInput{}
	d := NewTurn(TurnConfig{}, in)
	d.Output(&bytes.Buffer{})
	frame, err := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"text": strings.Repeat("x", maxAppServerLine/2)}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		d.Write(append(frame, '\n'))
	}
	if !in.closed || d.failure == nil {
		t.Fatal("early notification queue exceeded total frame budget")
	}
}

func TestTurnRejectsWrongIdentityAndIncompleteResult(t *testing.T) {
	for _, line := range []string{
		`{"method":"item/completed","params":{"threadId":"other","turnId":"turn-1"}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"other"}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`,
	} {
		in := &memoryInput{}
		d := NewTurn(TurnConfig{}, in)
		d.output = &bytes.Buffer{}
		d.thread, d.turn, d.stage = "thread-1", "turn-1", 5
		d.Write([]byte(line + "\n"))
		if !in.closed || d.failure == nil || d.complete {
			t.Fatal("invalid completion accepted")
		}
	}
}
