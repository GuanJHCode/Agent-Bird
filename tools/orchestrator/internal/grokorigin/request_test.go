package grokorigin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func validRequest() Request {
	return Request{SchemaVersion: 1, Binary: "/private/actor", BinarySHA256: strings.Repeat("a", 64), BinaryVersion: "fixture 1", Workspace: "/private/workspace", InitialPrompt: "  中文\nkeep whitespace  ", OperationTimeoutMS: 100, TotalTimeoutMS: 1000, OriginProfile: "readonly", AuthMode: "none"}
}
func requestFile(t *testing.T, raw []byte) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "request.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func requestJSON(t *testing.T, r Request) []byte {
	t.Helper()
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestRequestPreservesPromptAndExplicitAuth(t *testing.T) {
	for _, auth := range []string{"none", "xai_api_key_env"} {
		r := validRequest()
		r.AuthMode = auth
		got, err := ReadRequest(requestFile(t, requestJSON(t, r)))
		if err != nil || got != r {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
}
func TestRequestStrictJSON(t *testing.T) {
	raw := requestJSON(t, validRequest())
	cases := map[string][]byte{
		"duplicate":       append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"schema_version":1}`)...),
		"case_alias":      bytes.Replace(raw, []byte(`"auth_mode"`), []byte(`"Auth_Mode"`), 1),
		"unknown":         append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"extra":true}`)...),
		"trailing":        append(append([]byte{}, raw...), []byte(` {}`)...),
		"invalid_utf8":    bytes.Replace(raw, []byte(`fixture 1`), []byte{255}, 1),
		"unpaired_utf16":  bytes.Replace(raw, []byte(`fixture 1`), []byte(`\ud800`), 1),
		"unpaired_prompt": bytes.Replace(raw, []byte(`中文`), []byte(`\udc00`), 1),
		"oversized":       append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), 65537)...),
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for field := range fields {
		for _, mode := range []string{"null", "missing"} {
			copy := map[string]json.RawMessage{}
			for k, v := range fields {
				copy[k] = v
			}
			if mode == "null" {
				copy[field] = json.RawMessage("null")
			} else {
				delete(copy, field)
			}
			b, e := json.Marshal(copy)
			if e != nil {
				t.Fatal(e)
			}
			cases[field+"_"+mode] = b
		}
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ReadRequest(requestFile(t, body))
			if err == nil || got != (Request{}) || err.Error() != "grokorigin_invalid_request" {
				t.Fatalf("accepted invalid request or leaked data: request=%+v err=%v", got, err)
			}
		})
	}
}
func TestRequestRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "hardlink", "fifo", "directory", "parent_symlink", "relative"} {
		t.Run(kind, func(t *testing.T) {
			path := requestFile(t, requestJSON(t, validRequest()))
			switch kind {
			case "mode":
				if e := os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				link := path + "-link"
				if e := os.Symlink(path, link); e != nil {
					t.Fatal(e)
				}
				path = link
			case "hardlink":
				if e := os.Link(path, path+"-link"); e != nil {
					t.Fatal(e)
				}
			case "fifo":
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				if e := syscall.Mkfifo(path, 0600); e != nil {
					t.Fatal(e)
				}
			case "directory":
				path = filepath.Dir(path)
			case "parent_symlink":
				dir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(dir, "parent")
				if e := os.Symlink(filepath.Dir(path), link); e != nil {
					t.Fatal(e)
				}
				path = filepath.Join(link, filepath.Base(path))
			case "relative":
				path = "request.json"
			}
			started := time.Now()
			got, err := ReadRequest(path)
			if err == nil || got != (Request{}) || err.Error() != "grokorigin_invalid_request" {
				t.Fatalf("unsafe input accepted: %+v %v", got, err)
			}
			if time.Since(started) > time.Second {
				t.Fatal("unsafe input blocked")
			}
		})
	}
}
func TestRequestValidationBounds(t *testing.T) {
	cases := map[string]func(*Request){"relative_binary": func(r *Request) { r.Binary = "actor" }, "unclean_binary": func(r *Request) { r.Binary = "/private/../actor" }, "nul_path": func(r *Request) { r.Workspace = "/private/\x00bad" }, "path_too_long": func(r *Request) { r.Binary = "/" + strings.Repeat("x", 4096) }, "bad_hash": func(r *Request) { r.BinarySHA256 = strings.Repeat("A", 64) }, "empty_prompt": func(r *Request) { r.InitialPrompt = "" }, "prompt_too_long": func(r *Request) { r.InitialPrompt = strings.Repeat("界", 2731) }, "version_control": func(r *Request) { r.BinaryVersion = "a\nb" }, "operation_low": func(r *Request) { r.OperationTimeoutMS = 99 }, "operation_high": func(r *Request) { r.OperationTimeoutMS = 300001 }, "total_short": func(r *Request) { r.TotalTimeoutMS = 99 }, "total_long": func(r *Request) { r.TotalTimeoutMS = 3600001 }, "writable": func(r *Request) { r.OriginProfile = "workspace-write" }, "implicit_auth": func(r *Request) { r.AuthMode = "oauth" }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := validRequest()
			change(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("invalid field accepted")
			}
		})
	}
}

func TestRequestDetectsSwapsAndMutation(t *testing.T) {
	for _, kind := range []string{"before_open_replace", "before_open_fifo", "before_read_replace", "after_read_replace", "after_read_mode", "after_read_size", "after_read_mtime"} {
		t.Run(kind, func(t *testing.T) {
			path := requestFile(t, requestJSON(t, validRequest()))
			changed := false
			got, err := readRequest(path, func(stage string) error {
				if !strings.HasPrefix(kind, stage+"_") {
					return nil
				}
				changed = true
				switch strings.TrimPrefix(kind, stage+"_") {
				case "replace":
					if e := os.Rename(path, path+".original"); e != nil {
						return e
					}
					return os.WriteFile(path, requestJSON(t, validRequest()), 0600)
				case "fifo":
					if e := os.Remove(path); e != nil {
						return e
					}
					return syscall.Mkfifo(path, 0600)
				case "mode":
					return os.Chmod(path, 0644)
				case "size":
					return os.WriteFile(path, []byte("{}"), 0600)
				case "mtime":
					stamp := time.Unix(100, 0)
					return os.Chtimes(path, stamp, stamp)
				}
				return nil
			})
			if !changed || err == nil || got != (Request{}) {
				t.Fatalf("mutation undetected changed=%t got=%+v err=%v", changed, got, err)
			}
		})
	}
}
func TestRequestValidUnicodeEscapes(t *testing.T) {
	for _, text := range []string{`\ud83d\ude00`, `\ufffd`, `\\ud800`, `quote\" and slash\\`} {
		raw := bytes.Replace(requestJSON(t, validRequest()), []byte(`fixture 1`), []byte(text), 1)
		if _, err := ReadRequest(requestFile(t, raw)); err != nil {
			t.Fatalf("valid Unicode %s rejected: %v", text, err)
		}
	}
}
