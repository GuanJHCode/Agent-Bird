package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

// This exercises sequential request JSON -> providerControl -> private lock.
// It does not establish atomic compare-and-swap across concurrent processes.
func TestProviderLockPreviousDigestRequestJSONContract(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "provider")
	requestPath := filepath.Join(root, "request.json")
	lockPath := filepath.Join(root, "lock.json")
	writeRevision := func(revision string) string {
		t.Helper()
		source := []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0; else echo --output-format --input-format --permission-mode --disallowed-tools --model --effort --resume --json-schema; fi\n# " + revision + "\n")
		if err := os.WriteFile(binary, source, 0700); err != nil {
			t.Fatal(err)
		}
		// Every new executable content gets its own bounded preparation.
		// The subsequent product discovery still has its original 2s budget.
		if err := prepareFixtureFirstExec(binary); err != nil {
			t.Fatalf("fixture_revision_exec_not_ready (%s): %v", revision, err)
		}
		sum := sha256.Sum256(source)
		return hex.EncodeToString(sum[:])
	}
	requestJSON := func(confirm, previous string) []byte {
		t.Helper()
		// Use public JSON names directly, rather than marshaling the product DTO.
		request := map[string]string{"provider": "claude-code", "binary_path": binary, "lock_file": lockPath}
		if confirm != "" {
			request["confirm_sha256"] = confirm
		}
		if previous != "" {
			request["previous_sha256"] = previous
		}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	call := func(t *testing.T, operation string, body []byte) ([]byte, error) {
		t.Helper()
		if err := os.WriteFile(requestPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err := providerControl(context.Background(), operation, []string{"--request", requestPath}, &output)
		return output.Bytes(), err
	}
	refuse := func(t *testing.T, body []byte, wantCode string) {
		t.Helper()
		before, beforeErr := os.ReadFile(lockPath)
		if beforeErr != nil && !os.IsNotExist(beforeErr) {
			t.Fatal(beforeErr)
		}
		_, callErr := call(t, "provider-lock", body)
		if gotCode := errorCode(callErr); gotCode != wantCode {
			t.Fatalf("lock refusal code = %q, want %q (err=%v)", gotCode, wantCode, callErr)
		}
		after, afterErr := os.ReadFile(lockPath)
		if os.IsNotExist(beforeErr) {
			if !os.IsNotExist(afterErr) {
				t.Fatalf("refused first lock created a file: %v", afterErr)
			}
		} else if afterErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("refused lock changed full disk bytes: %v", afterErr)
		}
	}
	accept := func(t *testing.T, body []byte, digest string) {
		t.Helper()
		response, callErr := call(t, "provider-lock", body)
		if callErr != nil {
			t.Fatal(callErr)
		}
		info, err := os.Stat(lockPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("accepted lock must have mode 0600: info=%v err=%v", info, err)
		}
		data, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		var actual adapter.ProviderLock
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatal(err)
		}
		want := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: "claude-stream-json-v1", Binary: adapter.BinaryPin{Path: binary, Version: "1.0", SHA256: digest}}
		if actual != want {
			t.Fatalf("accepted full lock = %+v, want %+v", actual, want)
		}
		var result struct {
			Lock             adapter.ProviderLock `json:"lock"`
			ProfileSupported bool                 `json:"profile_supported"`
		}
		if err := json.Unmarshal(response, &result); err != nil || result.Lock != want || !result.ProfileSupported {
			t.Fatalf("accepted response disagrees with lock/capability: %s err=%v", response, err)
		}
	}
	checkRefusals := func(cases []struct{ name, confirm, previous, code string }) bool {
		t.Helper()
		for _, tc := range cases {
			if !t.Run(tc.name, func(t *testing.T) { refuse(t, requestJSON(tc.confirm, tc.previous), tc.code) }) {
				return false
			}
		}
		return true
	}
	first := writeRevision("first")
	wrong := strings.Repeat("0", 64)
	if !t.Run("probe-does-not-create-lock", func(t *testing.T) {
		if _, err := call(t, "provider-probe", requestJSON("", "")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("probe created a disk lock: %v", err)
		}
	}) {
		return
	}
	if !checkRefusals([]struct{ name, confirm, previous, code string }{
		{"no-lock-nonempty-previous", first, wrong, "provider_lock_changed"},
		{"first-lock-missing-confirmation", "", "", "provider_lock_confirmation_required"},
		{"first-lock-wrong-confirmation", wrong, "", "provider_lock_confirmation_required"},
	}) {
		return
	}
	if !t.Run("first-lock", func(t *testing.T) { accept(t, requestJSON(first, ""), first) }) {
		return
	}
	if !checkRefusals([]struct{ name, confirm, previous, code string }{
		{"existing-lock-missing-previous", first, "", "provider_lock_changed"},
		{"existing-lock-wrong-previous", first, wrong, "provider_lock_changed"},
	}) {
		return
	}
	second := writeRevision("second")
	if !checkRefusals([]struct{ name, confirm, previous, code string }{
		{"upgrade-missing-confirmation", "", first, "provider_lock_confirmation_required"},
		{"upgrade-wrong-confirmation", first, first, "provider_lock_confirmation_required"},
		{"upgrade-missing-previous", second, "", "provider_lock_changed"},
		{"upgrade-wrong-previous", second, wrong, "provider_lock_changed"},
	}) {
		return
	}
	upgradeJSON := requestJSON(second, first)
	if !t.Run("confirmed-upgrade", func(t *testing.T) { accept(t, upgradeJSON, second) }) {
		return
	}
	if !t.Run("repeat-exact-upgrade-request", func(t *testing.T) {
		refuse(t, upgradeJSON, "provider_lock_changed")
	}) {
		return
	}
	third := writeRevision("third")
	t.Run("stale-previous-for-new-revision", func(t *testing.T) {
		refuse(t, requestJSON(third, first), "provider_lock_changed")
	})
}
