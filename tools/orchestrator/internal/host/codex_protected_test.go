package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func codexProtectedFixture(t *testing.T) (codexStateRoot, []byte, codexOwnedReferences) {
	t.Helper()
	_, _, _, refs := newCodexSealableFixture(t)
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := codexAnchorStateRoot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	return root, body, refs
}

func TestCodexProtectedCodecRequiresLosslessExactClosedMetadata(t *testing.T) {
	_, body, want := codexProtectedFixture(t)
	var got codexOwnedReferences
	if err := codexDecodeProtectedRecord(body, 16<<10, &got); err != nil || got != want {
		t.Fatal("valid fixed metadata rejected", err)
	}
	for _, kind := range []string{"unknown", "alias", "duplicate", "nested_alias", "missing", "null", "float", "exponent", "overflow", "invalid_utf8", "surrogate", "trailing", "too_large", "deep", "array"} {
		t.Run(kind, func(t *testing.T) {
			raw := append([]byte(nil), body...)
			text := string(raw)
			switch kind {
			case "unknown":
				text = strings.Replace(text, "{", "{\"unknown\":false,", 1)
			case "alias":
				text = strings.Replace(text, "\"auth_target\"", "\"AUTH_TARGET\"", 1)
			case "duplicate":
				text = strings.Replace(text, "{", "{\"auth_target\":\"duplicate\",", 1)
			case "nested_alias":
				text = strings.Replace(text, "\"present\"", "\"PRESENT\"", 1)
			case "missing":
				var object map[string]any
				if json.Unmarshal(raw, &object) != nil {
					t.Fatal("fixture")
				}
				delete(object, "auth_target")
				raw, _ = json.Marshal(object)
				text = string(raw)
			case "null":
				var object map[string]any
				if json.Unmarshal(raw, &object) != nil {
					t.Fatal("fixture")
				}
				object["auth_target"] = nil
				raw, _ = json.Marshal(object)
				text = string(raw)
			case "float":
				text = strings.Replace(text, "\"nlink\":1", "\"nlink\":1.5", 1)
			case "exponent":
				text = strings.Replace(text, "\"nlink\":1", "\"nlink\":1e0", 1)
			case "overflow":
				text = strings.Replace(text, "\"nlink\":1", "\"nlink\":18446744073709551616", 1)
			case "invalid_utf8":
				text = strings.Replace(text, "auth_target", string([]byte{0xff}), 1)
			case "surrogate":
				text = strings.Replace(text, "auth_target", `auth_target\ud800`, 1)
			case "trailing":
				text += "{}"
			case "too_large":
				text = strings.Repeat(" ", 16<<10) + text
			case "deep":
				text = `{"auth_target":` + strings.Repeat("[", 17) + `0` + strings.Repeat("]", 17) + `}`
			case "array":
				text = "[" + text + "]"
			}
			var out codexOwnedReferences
			if err := codexDecodeProtectedRecord([]byte(text), 16<<10, &out); err == nil {
				t.Fatal("non-exact/ambiguous metadata accepted")
			}
		})
	}
}

func TestCodexProtectedPublicationIsExclusiveAndDurable(t *testing.T) {
	root, body, want := codexProtectedFixture(t)
	file, err := codexWriteProtectedRecord(context.Background(), root, "codex-origin.json", body)
	if err != nil {
		t.Fatal(err)
	}
	if !file.Present || file.Identity.Mode != 0600 || file.Identity.Nlink != 1 || file.SHA256 != codexDigest(body) {
		t.Fatal("publication identity/hash invalid")
	}
	var out codexOwnedReferences
	read, err := codexReadProtectedRecord(context.Background(), root, "codex-origin.json", &out)
	if err != nil || read != file || out != want {
		t.Fatal("actual durable protected record not recovered", err)
	}
	for _, candidate := range [][]byte{body, []byte(`{"foreign":true}`)} {
		if _, err := codexWriteProtectedRecord(context.Background(), root, "codex-origin.json", candidate); err == nil {
			t.Fatal("immutable record was overwritten/adopted")
		}
	}
	current, err := os.ReadFile(filepath.Join(root.Path, "codex-origin.json"))
	if err != nil || !reflect.DeepEqual(current, body) {
		t.Fatal("conflicting publication destroyed original record")
	}
	if _, err := codexWriteProtectedRecord(context.Background(), root, "caller-chosen.json", body); err == nil {
		t.Fatal("arbitrary protected name accepted")
	}
	var missing codexOwnedReferences
	if _, err := codexReadProtectedRecord(context.Background(), root, "codex-thread.json", &missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("initial true absence not preserved", err)
	}
}

func TestCodexProtectedRecordsRejectPartialUnsafeAndReplacedParents(t *testing.T) {
	for _, kind := range []string{"partial", "oversize", "mode", "symlink", "hardlink", "fifo", "root_replace", "root_mode", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			root, body, _ := codexProtectedFixture(t)
			path := filepath.Join(root.Path, "codex-thread.json")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "partial":
				body = []byte("{")
			case "oversize":
				body = append([]byte(strings.Repeat(" ", 8<<10)), body...)
			}
			if kind != "symlink" && kind != "fifo" {
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("foreign", path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(root.Path, "alias")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "root_replace":
				if err := os.Rename(root.Path, root.Path+"-original"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(root.Path + "-original") })
				if err := os.Mkdir(root.Path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root.Path, "codex-thread.json"), body, 0600); err != nil {
					t.Fatal(err)
				}
			case "root_mode":
				if err := os.Chmod(root.Path, 0755); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(root.Path, 0700)
			case "cancel":
				cancel()
			}
			var out codexOwnedReferences
			if _, err := codexReadProtectedRecord(ctx, root, "codex-thread.json", &out); err == nil {
				t.Fatal("partial/unsafe/foreign record admitted")
			}
			if kind == "partial" {
				if _, err := codexWriteProtectedRecord(ctx, root, "codex-thread.json", []byte(`{"foreign":true}`)); err == nil {
					t.Fatal("partial record adopted")
				}
				retained, err := os.ReadFile(path)
				if err != nil || string(retained) != "{" {
					t.Fatal("partial evidence changed")
				}
			}
		})
	}
}
