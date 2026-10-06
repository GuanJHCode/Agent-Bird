package coordinator

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The two independent keys have separate purposes. The owner receives only
// the signing public key; neither private key is an owner/worker credential.
type originalOwnerKeys struct {
	sign ed25519.PrivateKey
	wrap []byte
	id   string
}
type originalOwnerKeyFile struct {
	Version     int    `json:"version"`
	SignSeed    string `json:"sign_seed"`
	WrappingKey string `json:"wrapping_key"`
}

func ownerPrivateDirectory(state string) (string, error) {
	dir := filepath.Join(state, "owner-runtime")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", store.CodeError("owner_runtime_directory_unsafe")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return "", store.CodeError("owner_runtime_directory_unsafe")
	}
	return dir, nil
}

func readOwnerPrivateFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Size() < 1 || info.Size() > limit {
		return nil, store.CodeError("owner_runtime_file_unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) || !os.SameFile(info, after) || info.Size() != int64(len(body)) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, store.CodeError("owner_runtime_file_changed")
	}
	return body, nil
}

// Publication never replaces an existing identity. The coordinator instance
// lock serializes legitimate writers; link also refuses a racing destination.
func createOwnerPrivateFile(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > 4096 {
		return store.CodeError("owner_runtime_file_too_large")
	}
	stage, err := os.CreateTemp(filepath.Dir(path), ".owner-runtime-*.new")
	if err != nil {
		return err
	}
	name := stage.Name()
	defer os.Remove(name)
	if _, err = stage.Write(body); err == nil {
		err = stage.Sync()
	}
	if e := stage.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Link(name, path); err != nil {
		return err
	}
	if err = os.Remove(name); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func loadOriginalOwnerKeys(state string, required []store.OriginalOwnerState) (*originalOwnerKeys, error) {
	dir, err := ownerPrivateDirectory(state)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "keys.json")
	body, err := readOwnerPrivateFile(path, 1024)
	if errors.Is(err, os.ErrNotExist) {
		if len(required) != 0 {
			return nil, store.CodeError("original_owner_key_lost")
		}
		seed, wrap := make([]byte, 32), make([]byte, 32)
		if _, err = rand.Read(seed); err != nil {
			return nil, err
		}
		if _, err = rand.Read(wrap); err != nil {
			return nil, err
		}
		if err = createOwnerPrivateFile(path, originalOwnerKeyFile{Version: 1, SignSeed: hex.EncodeToString(seed), WrappingKey: hex.EncodeToString(wrap)}); err != nil {
			return nil, err
		}
		body, err = readOwnerPrivateFile(path, 1024)
	}
	if err != nil {
		return nil, err
	}
	var value originalOwnerKeyFile
	if contract.DecodeBridgeStrict(body, &value) != nil || value.Version != 1 {
		return nil, store.CodeError("original_owner_key_invalid")
	}
	canonical, _ := json.Marshal(value)
	if !bytes.Equal(append(canonical, '\n'), body) {
		return nil, store.CodeError("original_owner_key_invalid")
	}
	seed, e1 := hex.DecodeString(value.SignSeed)
	wrap, e2 := hex.DecodeString(value.WrappingKey)
	if e1 != nil || e2 != nil || len(seed) != 32 || len(wrap) != 32 || hex.EncodeToString(seed) != value.SignSeed || hex.EncodeToString(wrap) != value.WrappingKey {
		return nil, store.CodeError("original_owner_key_invalid")
	}
	key := ed25519.NewKeyFromSeed(seed)
	id, _ := contract.OriginalOwnerKeyID(key.Public().(ed25519.PublicKey))
	keys := &originalOwnerKeys{sign: key, wrap: wrap, id: id}
	for _, owner := range required {
		if owner.Intent.CoordinatorKeyID != id {
			return nil, store.CodeError("original_owner_key_changed")
		}
		// Also prove the independent wrapping key still opens this owner's exact
		// credential. A matching signing seed cannot hide wrapping-key drift.
		if _, err = keys.readLaunchCredential(state, owner.Intent.Scope); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

type originalOwnerCredential struct {
	Version     int    `json:"version"`
	ScopeSHA256 string `json:"scope_sha256"`
	Nonce       string `json:"nonce"`
	Ciphertext  string `json:"ciphertext"`
}

func ownerCredentialPath(state string, scope contract.OriginalOwnerScope) string {
	sum := sha256.Sum256([]byte(scope.LaunchID))
	return filepath.Join(state, "owner-runtime", "launch-"+hex.EncodeToString(sum[:])+".json")
}
func (k *originalOwnerKeys) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.wrap)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (k *originalOwnerKeys) credentialAAD(scopeHash string) []byte {
	return []byte("original-owner-launch-credential-v1\x00" + k.id + "\x00" + scopeHash)
}
func (k *originalOwnerKeys) saveLaunchCredential(state string, scope contract.OriginalOwnerScope, token string) error {
	if token == "" || len(token) > 1024 {
		return store.CodeError("original_owner_credential_invalid")
	}
	path := ownerCredentialPath(state, scope)
	if _, err := os.Lstat(path); err == nil {
		old, err := k.readLaunchCredential(state, scope)
		if err != nil {
			return err
		}
		if old != token {
			return store.ErrConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	scopeHash, err := contract.OriginalOwnerScopeSHA256(scope)
	if err != nil {
		return err
	}
	aead, err := k.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nil, nonce, []byte(token), k.credentialAAD(scopeHash))
	return createOwnerPrivateFile(path, originalOwnerCredential{Version: 1, ScopeSHA256: scopeHash, Nonce: hex.EncodeToString(nonce), Ciphertext: hex.EncodeToString(sealed)})
}
func (k *originalOwnerKeys) readLaunchCredential(state string, scope contract.OriginalOwnerScope) (string, error) {
	body, err := readOwnerPrivateFile(ownerCredentialPath(state, scope), 4096)
	if err != nil {
		return "", store.CodeError("original_owner_credential_unavailable")
	}
	var value originalOwnerCredential
	if contract.DecodeBridgeStrict(body, &value) != nil || value.Version != 1 {
		return "", store.CodeError("original_owner_credential_invalid")
	}
	canonical, _ := json.Marshal(value)
	hash, err := contract.OriginalOwnerScopeSHA256(scope)
	if err != nil || hash != value.ScopeSHA256 || !bytes.Equal(append(canonical, '\n'), body) {
		return "", store.CodeError("original_owner_credential_invalid")
	}
	nonce, e1 := hex.DecodeString(value.Nonce)
	sealed, e2 := hex.DecodeString(value.Ciphertext)
	aead, err := k.aead()
	if e1 != nil || e2 != nil || err != nil || len(nonce) != aead.NonceSize() || len(sealed) > 1040 {
		return "", store.CodeError("original_owner_credential_invalid")
	}
	plain, err := aead.Open(nil, nonce, sealed, k.credentialAAD(hash))
	if err != nil || len(plain) == 0 || len(plain) > 1024 {
		return "", store.CodeError("original_owner_credential_invalid")
	}
	return string(plain), nil
}
