package coordinator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func legacyOwnerCredentialFixture(t *testing.T, keys *originalOwnerKeys, state string, scope contract.OriginalOwnerScope, token string) (string, []byte) {
	t.Helper()
	hash, err := ownerCredentialScopeHash(scope)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := keys.aead()
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{0x13}, aead.NonceSize())
	value := originalOwnerCredential{Version: 1, ScopeSHA256: hash, Nonce: hex.EncodeToString(nonce), Ciphertext: hex.EncodeToString(aead.Seal(nil, nonce, []byte(token), keys.credentialAAD(hash)))}
	launch := sha256.Sum256([]byte(scope.LaunchID))
	path := filepath.Join(state, "owner-runtime", "launch-"+hex.EncodeToString(launch[:])+".json")
	if err = createOwnerPrivateFile(path, value); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func TestOriginalOwnerCredentialSupportsAdjacentScopes(t *testing.T) {
	state := ownerKeyTestState(t)
	keys, err := loadOriginalOwnerKeys(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := ownerKeyTestScope()
	next := old
	next.OwnerID = "successor"
	next.Incarnation++
	next.OriginalOriginPID++
	next.OriginalOriginBirth = "successor-origin"
	next.HostGeneration = "next-generation"
	if err = keys.saveLaunchCredential(state, old, "old-token"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ownerCredentialPath(state, old))
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.saveLaunchCredential(state, next, "new-token"); err != nil {
		t.Fatalf("successor scope collided with predecessor credential: %v", err)
	}
	if ownerCredentialPath(state, old) == ownerCredentialPath(state, next) {
		t.Fatal("scope filename collision")
	}
	for _, tc := range []struct {
		scope contract.OriginalOwnerScope
		token string
	}{{old, "old-token"}, {next, "new-token"}} {
		if got, e := keys.readLaunchCredential(state, tc.scope); e != nil || got != tc.token {
			t.Fatalf("independent scope credential: %q %v", got, e)
		}
	}
	after, err := os.ReadFile(ownerCredentialPath(state, old))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("successor replaced predecessor ciphertext")
	}
	normalized := old
	normalized.OwnerID = "another-owner"
	normalized.Incarnation = 9
	if err = keys.saveLaunchCredential(state, normalized, "old-token"); err != nil {
		t.Fatal("same launch scope could not replay", err)
	}
	if ownerCredentialPath(state, old) != ownerCredentialPath(state, normalized) {
		t.Fatal("owner-only incarnation incorrectly changed launch credential")
	}
	if err = keys.saveLaunchCredential(state, normalized, "different-token"); err == nil {
		t.Fatal("same scope accepted different credential")
	}
}

func TestOriginalOwnerCredentialLegacyMigrationPreservesPredecessor(t *testing.T) {
	state := ownerKeyTestState(t)
	keys, err := loadOriginalOwnerKeys(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := ownerKeyTestScope()
	legacy, before := legacyOwnerCredentialFixture(t, keys, state, old, "legacy-token")
	if got, e := keys.readLaunchCredential(state, old); e != nil || got != "legacy-token" {
		t.Fatal("legacy fallback unavailable", e)
	}
	if _, err = loadOriginalOwnerKeys(state, []store.OriginalOwnerState{{Intent: contract.OriginalOwnerIntent{Scope: old, CoordinatorKeyID: keys.id}, IsCurrent: true}}); err != nil {
		t.Fatal("legacy-only coordinator restart failed", err)
	}
	if err = keys.saveLaunchCredential(state, old, "legacy-token"); err != nil {
		t.Fatal(err)
	}
	current := ownerCredentialPath(state, old)
	if current == legacy {
		t.Fatal("save did not publish scoped credential path")
	}
	raw, err := os.ReadFile(current)
	if err != nil || !bytes.Equal(before, raw) {
		t.Fatal("migration changed original ciphertext/AAD")
	}
	next := old
	next.HostGeneration = "next"
	next.OriginalOriginBirth = "next-origin"
	if err = keys.saveLaunchCredential(state, next, "successor-token"); err != nil {
		t.Fatal("foreign legacy scope blocked independently authorized successor", err)
	}
	loaded, err := loadOriginalOwnerKeys(state, []store.OriginalOwnerState{{Intent: contract.OriginalOwnerIntent{Scope: next, CoordinatorKeyID: keys.id}, IsCurrent: true}})
	if err != nil {
		t.Fatal("successor scoped restart failed", err)
	}
	if got, e := loaded.readLaunchCredential(state, next); e != nil || got != "successor-token" {
		t.Fatal("successor adopted legacy token", e)
	}
	after, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy file overwritten")
	}
	if err = os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if got, e := keys.readLaunchCredential(state, old); e != nil || got != "legacy-token" {
		t.Fatal("preserved legacy fallback failed", e)
	}
}

func TestOriginalOwnerCredentialCorruptLegacyCannotPublishSuccessor(t *testing.T) {
	state := ownerKeyTestState(t)
	keys, err := loadOriginalOwnerKeys(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := ownerKeyTestScope()
	path, body := legacyOwnerCredentialFixture(t, keys, state, old, "legacy-token")
	var value originalOwnerCredential
	if json.Unmarshal(body, &value) != nil {
		t.Fatal("fixture")
	}
	value.Ciphertext = "00000000000000000000000000000000"
	raw, _ := json.Marshal(value)
	raw = append(raw, '\n')
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	next := old
	next.HostGeneration = "next"
	if err = keys.saveLaunchCredential(state, next, "current-token"); err == nil {
		t.Fatal("corrupt predecessor silently bypassed")
	}
	if _, err = os.Lstat(ownerCredentialPath(state, next)); !os.IsNotExist(err) {
		t.Fatal("failed legacy authentication published successor")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("legacy corruption was overwritten")
	}
}

func TestOriginalOwnerCredentialRejectsDriftWithoutLegacyFallback(t *testing.T) {
	for _, mode := range []string{"aad_swap", "ciphertext", "scope", "mode", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			state := ownerKeyTestState(t)
			keys, err := loadOriginalOwnerKeys(state, nil)
			if err != nil {
				t.Fatal(err)
			}
			scope := ownerKeyTestScope()
			_, legacy := legacyOwnerCredentialFixture(t, keys, state, scope, "token")
			if err = keys.saveLaunchCredential(state, scope, "token"); err != nil {
				t.Fatal(err)
			}
			path := ownerCredentialPath(state, scope)
			var value originalOwnerCredential
			if json.Unmarshal(legacy, &value) != nil {
				t.Fatal("fixture")
			}
			switch mode {
			case "aad_swap":
				other := scope
				other.HostGeneration = "foreign"
				if err = keys.saveLaunchCredential(state, other, "other"); err != nil {
					t.Fatal(err)
				}
				raw, e := os.ReadFile(ownerCredentialPath(state, other))
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(path, raw, 0600)
			case "ciphertext":
				value.Ciphertext = "00000000000000000000000000000000"
			case "scope":
				value.ScopeSHA256 = "foreign"
			case "mode":
				err = os.Chmod(path, 0644)
			case "symlink":
				err = os.Rename(path, path+".kept")
				if err == nil {
					err = os.Symlink(path+".kept", path)
				}
			case "hardlink":
				err = os.Link(path, path+".alias")
			}
			if mode == "ciphertext" || mode == "scope" {
				raw, _ := json.Marshal(value)
				err = os.WriteFile(path, append(raw, '\n'), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = keys.readLaunchCredential(state, scope); err == nil {
				t.Fatal("drifted scoped credential fell back to legacy")
			}
			if err = keys.saveLaunchCredential(state, scope, "token"); err == nil {
				t.Fatal("save repaired or accepted drifted current credential")
			}
		})
	}
}
