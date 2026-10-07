package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func acpCompletionFixture(t *testing.T) (string, contract.ACPReceiveRequest) {
	t.Helper()
	state := privateTaskTemp(t)
	raw, err := os.ReadFile("../../internal/contract/testdata/acp-v1/requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	req, err := contract.DecodeACPReceiveRequest(all["receive"])
	if err != nil {
		t.Fatal(err)
	}
	return state, req
}

func TestACPCompletionPersistsPrivateExactImmutableReceipt(t *testing.T) {
	state, req := acpCompletionFixture(t)
	path, err := persistACPCompletion(state, req)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(state, "acp-origin-completions") {
		t.Fatal("completion escaped protected state")
	}
	for p, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, e := os.Lstat(p)
		if e != nil || info.Mode().Perm() != mode {
			t.Fatalf("private receipt mode: %v", e)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := contract.DecodeACPReceiveRequest(before)
	if err != nil || decoded != req {
		t.Fatalf("complete original tuple missing: %v", err)
	}
	info, _ := os.Stat(path)
	again, err := persistACPCompletion(state, req)
	if err != nil || again != path {
		t.Fatalf("exact replay=%v", err)
	}
	afterInfo, _ := os.Stat(path)
	if !os.SameFile(info, afterInfo) {
		t.Fatal("exact replay replaced original receipt")
	}
	other := req
	other.ResponseSHA256 = strings.Repeat("d", 64)
	other.RequestID = contract.ACPReceiveRequestID(other)
	if _, err = persistACPCompletion(state, other); err == nil {
		t.Fatal("different response overwrote the same attempt")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("immutable receipt changed")
	}
}

func TestACPCompletionRejectsUnsafeDirectoryAndFile(t *testing.T) {
	for _, kind := range []string{"state_link", "state_mode", "parent_link", "parent_mode", "file_link", "file_hardlink", "file_mode", "file_corrupt"} {
		t.Run(kind, func(t *testing.T) {
			state, req := acpCompletionFixture(t)
			outside := privateTaskTemp(t)
			foreign := filepath.Join(outside, "foreign")
			if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "state_link":
				alias := filepath.Join(outside, "state")
				if err := os.Symlink(state, alias); err != nil {
					t.Fatal(err)
				}
				state = alias
			case "state_mode":
				if err := os.Chmod(state, 0755); err != nil {
					t.Fatal(err)
				}
			case "parent_link":
				if err := os.Symlink(outside, filepath.Join(state, "acp-origin-completions")); err != nil {
					t.Fatal(err)
				}
			case "parent_mode":
				if err := os.Mkdir(filepath.Join(state, "acp-origin-completions"), 0755); err != nil {
					t.Fatal(err)
				}
			default:
				path, err := persistACPCompletion(state, req)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "file_mode" {
					err = os.Chmod(path, 0644)
				} else if kind == "file_corrupt" {
					err = os.WriteFile(path, []byte("{}"), 0600)
				} else {
					if err = os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if kind == "file_link" {
						err = os.Symlink(foreign, path)
					} else {
						err = os.Link(foreign, path)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := persistACPCompletion(state, req); err == nil {
				t.Fatal("unsafe receipt storage admitted")
			}
			if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "preserve" {
				t.Fatal("foreign file modified")
			}
		})
	}
}

func TestACPCompletionRejectsInvalidTupleBeforeCreatingStorage(t *testing.T) {
	state, req := acpCompletionFixture(t)
	req.RequestID = "wrong"
	if _, err := persistACPCompletion(state, req); err == nil {
		t.Fatal("invalid completion tuple accepted")
	}
	if _, err := os.Stat(filepath.Join(state, "acp-origin-completions")); !os.IsNotExist(err) {
		t.Fatal("invalid receipt created storage")
	}
}

func TestACPCompletionConcurrentConflictingWritesNeverReplaceWinner(t *testing.T) {
	state, req := acpCompletionFixture(t)
	other := req
	other.ResponseSHA256 = strings.Repeat("d", 64)
	other.RequestID = contract.ACPReceiveRequestID(other)
	var wg sync.WaitGroup
	paths := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := req
			if i%2 == 1 {
				r = other
			}
			if p, e := persistACPCompletion(state, r); e == nil {
				paths <- p
			}
		}(i)
	}
	wg.Wait()
	close(paths)
	path := ""
	for p := range paths {
		if path != "" && path != p {
			t.Fatal("same attempt created multiple completion files")
		}
		path = p
	}
	if path == "" {
		t.Fatal("no writer retained a valid completion")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := contract.DecodeACPReceiveRequest(raw)
	if err != nil || (stored != req && stored != other) {
		t.Fatal("concurrent write corrupted receipt")
	}
}
