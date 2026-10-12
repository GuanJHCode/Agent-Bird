package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codextools"
)

// These regressions catch whole-file responses, split UTF-8 cursors, stale
// pages, and unrestricted or ambiguous replacements at the real Host boundary.
func TestCodexPartialReadAndSearch(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	body := "abc中文\nneedle here\n" + strings.Repeat("x", 40000)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	result := callCodexReadTool(t, tools, "read_file", `{"path":"large.txt","offset":3,"limit_bytes":4}`)
	var page struct {
		Content, SHA256 string
		NextOffset      int `json:"next_offset"`
		FileBytes       int `json:"file_bytes"`
		Truncated       bool
	}
	if !result.Success || json.Unmarshal([]byte(result.Text), &page) != nil || page.Content != "中" || page.NextOffset != 6 || page.FileBytes != len(body) || !page.Truncated {
		t.Fatalf("page: %s", result.Text)
	}
	args, _ := json.Marshal(map[string]any{"path": "large.txt", "offset": 6, "limit_bytes": 4, "expectedSHA256": page.SHA256})
	result = callCodexReadTool(t, tools, "read_file", string(args))
	if !result.Success || json.Unmarshal([]byte(result.Text), &page) != nil || page.Content != "文\n" {
		t.Fatalf("next page: %s", result.Text)
	}
	result = callCodexReadTool(t, tools, "search_file", `{"path":"large.txt","query":"needle"}`)
	var search struct {
		SHA256    string
		Matches   []struct{ Offset, Line int }
		Truncated bool
	}
	if !result.Success || json.Unmarshal([]byte(result.Text), &search) != nil || search.SHA256 != page.SHA256 || len(search.Matches) != 1 || search.Matches[0].Offset != 10 || search.Matches[0].Line != 2 || search.Truncated {
		t.Fatalf("search: %s", result.Text)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if result = callCodexReadTool(t, tools, "read_file", string(args)); result.Success {
		t.Fatal("stale cursor accepted")
	}
}

func TestCodexReplacementRejectsOverlappingAndMalformedEdits(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("aaa"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, err := codextools.Open(root, codextools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	_, hash, err := broker.Read(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"path":"a","expectedSHA256":"` + hash + `","old_text":"aa","new_text":"b"}`,
		`{"path":"a","expectedSHA256":"` + hash + `","old_text":"aaa","new_text":null}`,
		`{"path":"a","expectedSHA256":"` + hash + `","old_text":"aaa","new_text":"b","new_text":"c"}`,
		`{"path":"a","expectedSHA256":"` + hash + `","old_text":"aaa","new_text":"b","extra":true}`,
	} {
		if _, failure := codexReplacement(context.Background(), broker, []byte(raw)); failure == "" {
			t.Fatal("accepted", raw)
		}
	}
}

func TestCodexPartialArgumentsFailClosed(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("中文"), 0600); err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	for _, args := range []string{
		`{"path":"a","offset":1,"limit_bytes":4}`, `{"path":"a","offset":7}`, `{"path":"a","offset":-1}`,
		`{"path":"a","limit_bytes":3}`, `{"path":"a","limit_bytes":16385}`, `{"path":"a","offset":0.5}`,
		`{"path":"a","offset":null}`, `{"path":"a","path":"a"}`, `{"path":null}`, `{"path":"a","extra":1}`,
		`{"path":"a","expectedSHA256":""}`, `{"path":"../a","offset":0}`,
	} {
		if got := callCodexReadTool(t, tools, "read_file", args); got.Success {
			t.Fatal(args, got)
		}
	}
	for _, args := range []string{`{"path":"a","query":""}`, `{"path":"a","query":null}`, `{"path":"a","query":"中","extra":1}`, `{"path":".git/config","query":"x"}`} {
		if got := callCodexReadTool(t, tools, "search_file", args); got.Success {
			t.Fatal(args, got)
		}
	}
	if got := callCodexReadTool(t, tools, "replace_text", `{"path":"a","expectedSHA256":"x","old_text":"中","new_text":"x"}`); got.Success {
		t.Fatal("read-only replacement")
	}
}

func TestCodexSearchResultsBounded(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte(strings.Repeat("hit\n", 10000)), 0600); err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	result := callCodexReadTool(t, tools, "search_file", `{"path":"a","query":"hit"}`)
	var got struct {
		Matches   []any
		Truncated bool
	}
	if !result.Success || json.Unmarshal([]byte(result.Text), &got) != nil || len(got.Matches) != 20 || !got.Truncated || len(result.Text) > 4096 {
		t.Fatal(result)
	}
}

func TestCodexSearchIncludesBoundedUTF8Context(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("前缀\nneedle=before\n后缀"), 0600); err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	result := callCodexReadTool(t, tools, "search_file", `{"path":"a","query":"needle","context_bytes":64}`)
	var got struct {
		SHA256  string
		Matches []struct {
			Content       string
			Offset        int
			ContentOffset int `json:"content_offset"`
		}
	}
	if !result.Success || json.Unmarshal([]byte(result.Text), &got) != nil || len(got.Matches) != 1 || got.Matches[0].Content != "前缀\nneedle=before\n后缀" || got.Matches[0].Offset != 7 || got.Matches[0].ContentOffset != 0 {
		t.Fatal(result)
	}
	for _, raw := range []string{`{"path":"a","query":"needle","context_bytes":null}`, `{"path":"a","query":"needle","context_bytes":257}`, `{"path":"a","query":"needle","context_bytes":-1}`} {
		if result := callCodexReadTool(t, tools, "search_file", raw); result.Success {
			t.Fatal(raw)
		}
	}
}

func TestCodexToolFailuresExplainRejectedInput(t *testing.T) {
	root := canonicalCodexToolRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	for _, tc := range []struct{ args, code string }{
		{`{"path":"a","unknown":true}`, "invalid_arguments"},
		{`{"path":"a","offset":-1}`, "invalid_range"},
		{`{"path":"a","offset":0,"expectedSHA256":"stale"}`, "file_changed"},
	} {
		result := callCodexReadTool(t, tools, "read_file", tc.args)
		var failure struct {
			Code string `json:"failure_reason"`
		}
		if result.Success || json.Unmarshal([]byte(result.Text), &failure) != nil || failure.Code != tc.code {
			t.Fatal(result)
		}
	}
}

func TestCodexContextWindowsStayBoundedAndReadbackDetectsDrift(t *testing.T) {
	body := []byte(strings.Repeat("中文abcd", 32))
	for offset := range body {
		if !utf8.RuneStart(body[offset]) {
			continue
		}
		for _, limit := range []int{4, 7, 16, 256} {
			lo, hi := codexContextWindow(body, offset, limit)
			if lo > offset || hi-lo > limit || !utf8.Valid(body[lo:hi]) {
				t.Fatalf("invalid window %d:%d at %d", lo, hi, offset)
			}
		}
	}
	root := canonicalCodexToolRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("changed after write"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, err := codextools.Open(root, codextools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	got, err := codexVerifiedReplacement(context.Background(), broker, codexReplacementPlan{Path: "a", Content: "submitted", VerifyBytes: 64}, codextools.WriteReceipt{Path: "a", SHA256: strings.Repeat("0", 64)})
	if !errors.Is(err, codextools.ErrUnknown) || got.Success {
		t.Fatal("fabricated readback", got, err)
	}
}
