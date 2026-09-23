package execbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeWorkspacePolicyBindsEveryFileOperation(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "code.py"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(root), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	policy, err := newWorkspacePolicy(context.Background(), root, true, []string{filepath.Join(root, "AGENTS.md")})
	if err != nil {
		t.Fatal(err)
	}
	defer policy.close()
	uri := func(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }
	for _, method := range []string{"fs/getMetadata", "fs/readFile", "fs/writeFile", "fs/createDirectory", "fs/remove"} {
		for _, path := range []string{filepath.Join(root, "..", "outside"), filepath.Join(root, "link", "outside"), filepath.Join(root, ".GIT", "config")} {
			params := map[string]any{"path": uri(path), "sandbox": nil, "dataBase64": "YWZ0ZXIK"}
			if method != "fs/writeFile" {
				delete(params, "dataBase64")
			}
			if method == "fs/createDirectory" {
				params["recursive"] = true
			}
			if method == "fs/remove" {
				params["recursive"], params["force"] = false, false
			}
			if err := policy.check(method, params); err == nil {
				t.Errorf("unsafe %s accepted %s", method, path)
			}
		}
	}
	for _, method := range []string{"fs/getMetadata", "fs/readFile"} {
		if err := policy.check(method, map[string]any{"path": uri(filepath.Join(root, "code.py")), "sandbox": nil}); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"fs/writeFile", "fs/remove"} {
		params := map[string]any{"path": uri(filepath.Join(root, "AGENTS.md")), "sandbox": nil}
		if method == "fs/writeFile" {
			params["dataBase64"] = "YWZ0ZXIK"
		}
		if method == "fs/remove" {
			params["recursive"], params["force"] = false, false
		}
		if err := policy.check(method, params); err == nil {
			t.Fatal("native patch could rewrite instructions")
		}
	}
	params := map[string]any{"path": uri(filepath.Join(root, "code.py")), "sandbox": nil, "dataBase64": "YWZ0ZXIK"}
	if err := policy.check("fs/writeFile", params); err != nil {
		t.Fatal(err)
	}
	policy.writable = false
	if err := policy.check("fs/writeFile", params); err == nil {
		t.Fatal("review profile gained writes")
	}
	if err := policy.check("process/start", map[string]any{}); err == nil {
		t.Fatal("workspace binding enabled process execution")
	}
}

func TestBridgeForcesNativeNoFollowAndRejectsWiderFileCapabilities(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "code.py"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := newWorkspacePolicy(context.Background(), root, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer policy.close()
	path := (&url.URL{Scheme: "file", Path: filepath.Join(root, "code.py")}).String()
	for _, method := range []string{"fs/getMetadata", "fs/readFile", "fs/writeFile", "fs/createDirectory", "fs/remove"} {
		params := map[string]any{"path": path, "sandbox": nil}
		if method == "fs/writeFile" {
			params["dataBase64"] = "YWZ0ZXIK"
		}
		if method == "fs/createDirectory" {
			params["recursive"] = true
			params["path"] = (&url.URL{Scheme: "file", Path: filepath.Join(root, "new")}).String()
		}
		if method == "fs/remove" {
			params["recursive"], params["force"] = false, false
		}
		raw, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": params})
		b := &Broker{tools: true, ready: true, workspace: policy, pending: map[string]requestInfo{}, seen: map[string]bool{}}
		var forwarded bytes.Buffer
		if err := b.copyFrames(strings.NewReader(string(raw)+"\n"), &forwarded, true); err != nil {
			t.Fatalf("native %s: %v", method, err)
		}
		var message map[string]any
		if json.Unmarshal(forwarded.Bytes(), &message) != nil || message["params"].(map[string]any)["followSymlinks"] != false {
			t.Fatal("forwarded native link-following default")
		}
	}
	for _, test := range []struct {
		method string
		extra  map[string]any
	}{
		{"fs/canonicalize", nil}, {"fs/copy", nil}, {"fs/open", nil}, {"fs/readDirectory", nil}, {"fs/walk", nil},
		{"fs/remove", nil}, {"fs/remove", map[string]any{"recursive": false}}, {"fs/remove", map[string]any{"force": false}}, {"fs/remove", map[string]any{"recursive": true, "force": false}},
		{"fs/createDirectory", nil}, {"fs/createDirectory", map[string]any{"recursive": false}},
		{"fs/writeFile", map[string]any{"dataBase64": "not base64"}}, {"fs/readFile", map[string]any{"followSymlinks": true}}, {"fs/readFile", map[string]any{"command": "forbidden"}},
		{"fs/readFile", map[string]any{"sandbox": map[string]any{"permissions": map[string]any{"type": "external", "network": "enabled"}}}},
	} {
		params := map[string]any{"path": path, "sandbox": nil}
		for key, value := range test.extra {
			params[key] = value
		}
		if err := policy.check(test.method, params); err == nil {
			t.Errorf("wider capability accepted: %s %+v", test.method, test.extra)
		}
	}
}
