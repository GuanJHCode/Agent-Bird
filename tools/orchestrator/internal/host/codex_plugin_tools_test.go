package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
)

func TestOpenCodexReadToolsReadAndList(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	body := []byte("hello\n")
	if err := os.WriteFile(filepath.Join(root, "note.txt"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeTools() })
	if tools.Writable {
		t.Fatal("read tools became writable")
	}
	read := callCodexReadTool(t, tools, "read_file", `{"path":"note.txt"}`)
	if !read.Success {
		t.Fatal(read.Text)
	}
	var got struct {
		Path    string `json:"path"`
		SHA256  string `json:"sha256"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(read.Text), &got); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	if got.Path != "note.txt" || got.Content != string(body) || got.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("read result = %#v", got)
	}
	list := callCodexReadTool(t, tools, "list_files", `{"path":""}`)
	if !list.Success {
		t.Fatal(list.Text)
	}
	var listed struct {
		Entries []string `json:"entries"`
	}
	if err := json.Unmarshal([]byte(list.Text), &listed); err != nil {
		t.Fatal(err)
	}
	if strings.Join(listed.Entries, ",") != "dir,note.txt" {
		t.Fatalf("entries = %#v", listed.Entries)
	}
}

func TestOpenCodexReadToolsRejectsWriteAndInvalidArguments(t *testing.T) {
	tools, closeTools, err := openCodexReadTools(canonicalCodexToolRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeTools() })
	for _, tc := range []struct{ tool, args string }{
		{"write_file", `{"path":"new.txt","expectedSHA256":"","content":"no"}`},
		{"read_file", `{"path":"a.txt","extra":true}`},
		{"list_files", `{"path":1}`},
		{"read_file", `{"path":"a","path":"b"}`},
	} {
		if result := callCodexReadTool(t, tools, tc.tool, tc.args); result.Success || len(result.Text) == 0 || len(result.Text) > 256<<10 {
			t.Fatalf("%s %s = %#v", tc.tool, tc.args, result)
		}
	}
}

func TestOpenCodexReadToolsBoundsUTF8AndContext(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	if err := os.WriteFile(filepath.Join(root, "invalid.txt"), []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "at-limit.txt"), []byte(strings.Repeat("x", 32<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "too-large.txt"), []byte(strings.Repeat("x", (32<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeTools() })
	if result := callCodexReadTool(t, tools, "read_file", `{"path":"at-limit.txt"}`); !result.Success || len(result.Text) > 256<<10 {
		t.Fatalf("limit result = %#v", result)
	}
	for _, path := range []string{"invalid.txt", "too-large.txt"} {
		if result := callCodexReadTool(t, tools, "read_file", `{"path":"`+path+`"}`); result.Success || len(result.Text) == 0 || len(result.Text) > 256<<10 {
			t.Fatalf("%s = %#v", path, result)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := tools.OnTool(ctx, codexsession.ToolCall{Tool: "read_file", Arguments: json.RawMessage(`{"path":"at-limit.txt"}`)})
	if err != nil || result.Success || len(result.Text) == 0 || len(result.Text) > 256<<10 {
		t.Fatalf("cancelled = %#v, %v", result, err)
	}
}

func callCodexReadTool(t *testing.T, tools *codexsession.HostTools, tool, args string) codexsession.ToolResult {
	t.Helper()
	result, err := tools.OnTool(context.Background(), codexsession.ToolCall{Tool: tool, Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func canonicalCodexToolRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
