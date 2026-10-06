package coordinator

import (
	"bytes"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
