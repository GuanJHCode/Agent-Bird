package coordinator

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOriginalOwnerServerPersistsPrivateSigningKeys(t *testing.T) {
	state := ownerKeyTestState(t)
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "owner-runtime", "keys.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("coordinator has no durable owner signing key: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private key permissions", err)
	}
	s, err = NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	next, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, next) {
		t.Fatal("coordinator restart changed owner authority", err)
	}
}

func TestOriginalOwnerServerRejectsUnsafeKeyDirectory(t *testing.T) {
	state := ownerKeyTestState(t)
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(state, "owner-runtime")); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(state)
	if err == nil {
		s.Close()
		t.Fatal("coordinator ignored unsafe owner key directory")
	}
}

func ownerKeyTestState(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/tmp", "owner-keys-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	return path
}

func ownerKeyTestScope() contract.OriginalOwnerScope {
	return contract.OriginalOwnerScope{Version: 1, RunID: "run", LaunchID: "launch", ProducerID: "launch", OwnerID: "owner", Incarnation: 1, OriginalOriginContextID: "origin", OriginalOriginPID: 123, OriginalOriginBirth: "origin-birth", HostGeneration: "gen", Executable: "/pinned/orchestrator", ExecutableSHA256: strings.Repeat("a", 64)}
}

func TestOriginalOwnerPrivateKeyAndCredentialGuards(t *testing.T) {
	for _, name := range []string{"missing", "key_drift", "wrapping_drift", "public_mode", "symlink", "hardlink", "unknown_field", "tampered_escrow", "foreign_scope"} {
		t.Run(name, func(t *testing.T) {
			state := ownerKeyTestState(t)
			keys, err := loadOriginalOwnerKeys(state, nil)
			if err != nil {
				t.Fatal(err)
			}
			scope := ownerKeyTestScope()
			token := "launch-only-private-token"
			if err = keys.saveLaunchCredential(state, scope, token); err != nil {
				t.Fatal(err)
			}
			if err = keys.saveLaunchCredential(state, scope, token); err != nil {
				t.Fatal("exact replay", err)
			}
			if err = keys.saveLaunchCredential(state, scope, "other"); err == nil {
				t.Fatal("escrow overwritten")
			}
			sealed, err := os.ReadFile(ownerCredentialPath(state, scope))
			if err != nil || bytes.Contains(sealed, []byte(token)) {
				t.Fatal("plaintext launch token on disk", err)
			}
			required := []store.OriginalOwnerState{{Intent: contract.OriginalOwnerIntent{Scope: scope, CoordinatorKeyID: keys.id}, IsCurrent: true}}
			if _, err = loadOriginalOwnerKeys(state, required); err != nil {
				t.Fatal("unchanged key/credential rejected", err)
			}
			path := filepath.Join(state, "owner-runtime", "keys.json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing":
				err = os.Remove(path)
			case "key_drift", "wrapping_drift":
				var value originalOwnerKeyFile
				if err = json.Unmarshal(original, &value); err != nil {
					t.Fatal(err)
				}
				if name == "key_drift" {
					value.SignSeed = strings.Repeat("bb", 32)
				} else {
					value.WrappingKey = strings.Repeat("cc", 32)
				}
				raw, _ := json.Marshal(value)
				err = os.WriteFile(path, append(raw, '\n'), 0600)
			case "public_mode":
				err = os.Chmod(path, 0644)
			case "symlink":
				if err = os.Rename(path, path+".kept"); err == nil {
					err = os.Symlink(path+".kept", path)
				}
			case "hardlink":
				err = os.Link(path, path+".alias")
			case "unknown_field":
				err = os.WriteFile(path, bytes.Replace(original, []byte(`"version":1`), []byte(`"version":1,"extra":1`), 1), 0600)
			case "tampered_escrow":
				var value originalOwnerCredential
				if err = json.Unmarshal(sealed, &value); err != nil {
					t.Fatal(err)
				}
				value.Ciphertext = strings.Repeat("0", len(value.Ciphertext))
				raw, _ := json.Marshal(value)
				err = os.WriteFile(ownerCredentialPath(state, scope), append(raw, '\n'), 0600)
			case "foreign_scope":
				required[0].Intent.Scope.OwnerID = "other-owner"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = loadOriginalOwnerKeys(state, required); err == nil {
				t.Fatal("lost/untrusted/drifted owner credential accepted")
			}
			if name == "missing" {
				if _, err = os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("lost current-owner key regenerated", err)
				}
			}
		})
	}
}

func TestOriginalOwnerKeyPublicationCrashHelper(t *testing.T) {
	state := os.Getenv("ORIGINAL_OWNER_KEY_CRASH_STATE")
	if state == "" {
		return
	}
	dir, err := ownerPrivateDirectory(state)
	if err != nil {
		t.Fatal(err)
	}
	value := originalOwnerKeyFile{Version: 1, SignSeed: strings.Repeat("ab", 32), WrappingKey: strings.Repeat("cd", 32)}
	err = createOwnerPrivateFileWithHooks(filepath.Join(dir, "keys.json"), value, ownerPublicationHooks{after: func() error { os.Exit(73); return nil }})
	t.Fatal("crash boundary not reached", err)
}

func TestOriginalOwnerKeyPublicationCrashPreservesReadableIdentity(t *testing.T) {
	state := ownerKeyTestState(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestOriginalOwnerKeyPublicationCrashHelper$")
	cmd.Env = append(os.Environ(), "ORIGINAL_OWNER_KEY_CRASH_STATE="+state)
	err = cmd.Run()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("fixture original Wait did not observe crash boundary: %v / %v", err, ctx.Err())
	}
	keys, err := loadOriginalOwnerKeys(state, nil)
	if err != nil {
		t.Fatalf("post-publication process crash destroyed key availability: %v", err)
	}
	expected := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xab}, 32))
	if !bytes.Equal(keys.sign, expected) {
		t.Fatal("crash recovery regenerated identity")
	}
	t.Log("original helper Wait exit=73; no timeout; published identity preserved")
}

func TestOriginalOwnerKeyPublicationRejectsReplacedStagingInode(t *testing.T) {
	state := ownerKeyTestState(t)
	dir, err := ownerPrivateDirectory(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keys.json")
	value := originalOwnerKeyFile{Version: 1, SignSeed: strings.Repeat("ab", 32), WrappingKey: strings.Repeat("cd", 32)}
	err = createOwnerPrivateFileWithHooks(path, value, ownerPublicationHooks{before: func(stage string) error {
		if err := os.Rename(stage, stage+".original"); err != nil {
			return err
		}
		replacement := value
		replacement.SignSeed = strings.Repeat("ef", 32)
		body, _ := json.Marshal(replacement)
		return os.WriteFile(stage, append(body, '\n'), 0600)
	}})
	if err == nil {
		t.Fatal("foreign staging inode became signing authority")
	}
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("foreign staging inode published", err)
	}
}
