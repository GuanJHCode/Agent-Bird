package codexrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func readTool(handler func(map[string]any) (string, error)) DynamicTool {
	return DynamicTool{Name: "bird_read_file", Description: "Read workspace context", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}, "additionalProperties": false}, Call: handler}
}

func TestTurnRegistersOnlyBoundDynamicDefinitions(t *testing.T) {
	in := &memoryInput{}
	d := NewTurn(TurnConfig{Home: "/task/home", Directory: "/task/work", VerifyConfig: func(map[string]any) error { return nil }, DynamicTools: []DynamicTool{readTool(func(map[string]any) (string, error) { return "", nil })}}, in)
	d.Output(&bytes.Buffer{})
	d.Write([]byte("{\"id\":1,\"result\":{\"codexHome\":\"/task/home\"}}\n{\"id\":2,\"result\":{\"config\":{}}}\n"))
	var request map[string]any
	lines := strings.Split(strings.TrimSpace(in.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
		t.Fatal(err)
	}
	params, _ := request["params"].(map[string]any)
	defs, _ := params["dynamicTools"].([]any)
	if request["method"] != "thread/start" || len(defs) != 1 {
		t.Fatal("dynamic tool not registered")
	}
	definition, _ := defs[0].(map[string]any)
	if definition["type"] != "function" || definition["name"] != "bird_read_file" || definition["deferLoading"] != false {
		t.Fatal("wrong native definition", definition)
	}
}

func TestTurnRepliesToBoundReadCallAndPreservesFailure(t *testing.T) {
	for _, denied := range []bool{false, true} {
		in := &memoryInput{}
		d := NewTurn(TurnConfig{DynamicTools: []DynamicTool{readTool(func(args map[string]any) (string, error) {
			if args["path"] != "calc.py" {
				t.Fatal("arguments changed")
			}
			if denied {
				return "", errors.New("workspace_path_rejected")
			}
			return "line 1: def add(a,b):", nil
		})}}, in)
		d.thread, d.turn, d.stage = "thread-1", "turn-1", 5
		d.Write([]byte(`{"id":91,"method":"item/tool/call","params":{"threadId":"thread-1","turnId":"turn-1","callId":"call-1","namespace":null,"tool":"bird_read_file","arguments":{"path":"calc.py"}}}` + "\n"))
		if d.failure != nil || in.closed {
			t.Fatal("read call broke turn", d.failure)
		}
		var response map[string]any
		if err := json.Unmarshal(in.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		result, _ := response["result"].(map[string]any)
		items, _ := result["contentItems"].([]any)
		if response["id"] != float64(91) || result["success"] != !denied || len(items) != 1 {
			t.Fatal("wrong response", response)
		}
		item, _ := items[0].(map[string]any)
		want := "line 1: def add(a,b):"
		if denied {
			want = "workspace_path_rejected"
		}
		if item["type"] != "inputText" || item["text"] != want {
			t.Fatal("wrong content item", item)
		}
	}
}

func TestTurnRejectsDynamicReplayCrossTurnAndUnregisteredRequests(t *testing.T) {
	for _, scenario := range []string{"early", "other-thread", "other-turn", "unknown-tool", "namespace", "missing-namespace", "unknown-field", "replay", "oversize-output", "approval"} {
		t.Run(scenario, func(t *testing.T) {
			in := &memoryInput{}
			d := NewTurn(TurnConfig{DynamicTools: []DynamicTool{readTool(func(map[string]any) (string, error) {
				if scenario == "oversize-output" {
					return strings.Repeat("x", 65537), nil
				}
				return "context", nil
			})}}, in)
			d.thread, d.turn, d.stage = "thread-1", "turn-1", 5
			params := map[string]any{"threadId": "thread-1", "turnId": "turn-1", "callId": "call-1", "namespace": nil, "tool": "bird_read_file", "arguments": map[string]any{"path": "calc.py"}}
			method := "item/tool/call"
			switch scenario {
			case "early":
				d.turn = ""
				d.stage = 4
			case "other-thread":
				params["threadId"] = "other"
			case "other-turn":
				params["turnId"] = "other"
			case "unknown-tool":
				params["tool"] = "execute_command"
			case "namespace":
				params["namespace"] = "terminal"
			case "missing-namespace":
				delete(params, "namespace")
			case "unknown-field":
				params["approval"] = "allow"
			case "approval":
				method = "item/fileChange/requestApproval"
			}
			raw, _ := json.Marshal(map[string]any{"id": 91, "method": method, "params": params})
			d.Write(append(raw, '\n'))
			if scenario == "replay" {
				d.Write(append(raw, '\n'))
			}
			if d.failure == nil || !in.closed {
				t.Fatal("unbound dynamic request accepted")
			}
		})
	}
}
